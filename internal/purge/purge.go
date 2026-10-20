// Package purge 负责软删数据的物理清理。
//
// 设计约束（见 docs/adr/004-documents-api.md）：
//   - 只清理 deleted_at 早于保留期的行；读路径完全不受影响
//   - 按父行（document / collection）开短事务，先锁父行再删子行
//   - 先删关联，再删 document / collection；没有关联的过期行才允许物理删除
//   - 不依赖外键（schema 故意没有 FK），一致性由事务里的条件判断兜底
//
// restore 与 purge 的并发协议：双方都对同一父行执行 SELECT ... FOR UPDATE。
// 谁先拿到锁谁赢；后拿到锁的一方重新读取 deleted_at 再决定恢复或删除。
package purge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultRetention 是 ADR 004 定的默认保留期：软删后 30 天再物理清理。
	DefaultRetention = 30 * 24 * time.Hour

	// DefaultBatchSize 是每批处理的父行数。README 建议 500–2000，默认取中间值。
	DefaultBatchSize = 1000

	// MaxBatchSize 防止误传超大值导致单条 SQL 的 IN 列表过长。
	MaxBatchSize = 10000

	// DefaultInterval 是批间等待时间，避免清理任务持续打满数据库。
	DefaultInterval = time.Second
)

// DB 是 *sql.DB 和 *sql.Conn 的公共子集。
//
// 集成测试传固定连接，在会话内建 TEMPORARY TABLE，不碰真实表。
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// Options 是一次 purge 运行的参数。
type Options struct {
	// Retention 是软删后保留多久才允许物理删除；必须 > 0。
	Retention time.Duration

	// BatchSize 是每批最多取多少个 document / collection 父行；必须 1..MaxBatchSize。
	BatchSize int

	// Interval 是批间等待；0 表示不等待（测试用），负数非法。
	Interval time.Duration

	// DryRun 只统计会删除多少行，不执行 DELETE。
	DryRun bool
}

// Validate 校验参数范围。默认值由调用方（cmd/purge 的 flag）提供，
// 这里不静默补默认值：批处理任务里"没传参数"应该显式看见，而不是猜。
func (o Options) Validate() error {
	if o.Retention <= 0 {
		return fmt.Errorf("retention 必须大于 0，当前 %s", o.Retention)
	}
	if o.BatchSize < 1 || o.BatchSize > MaxBatchSize {
		return fmt.Errorf("batch 必须在 1~%d 之间，当前 %d", MaxBatchSize, o.BatchSize)
	}
	if o.Interval < 0 {
		return fmt.Errorf("interval 不能为负数，当前 %s", o.Interval)
	}
	return nil
}

// Stats 是一次运行的统计。
//
// 非 DryRun 时字段是实际删除行数；DryRun 时是预计会删除的行数。
// Associations 统计的是 collection_documents 行数，不是 document 数。
type Stats struct {
	Associations int64
	Documents    int64
	Collections  int64
	Batches      int
	DryRun       bool
}

// Runner 执行软删清理。
type Runner struct {
	db     DB
	logger *slog.Logger
}

// NewRunner 注入数据库与 logger。两者都不能为 nil。
func NewRunner(db DB, logger *slog.Logger) *Runner {
	if db == nil {
		panic("purge.NewRunner: db is nil")
	}
	if logger == nil {
		panic("purge.NewRunner: logger is nil")
	}
	return &Runner{db: db, logger: logger}
}

// Run 清理所有超过保留期的软删数据，直到没有可清理的父行。
//
// 每一轮先取一批候选 document / collection id，然后逐个父行开事务：
//  1. SELECT ... FOR UPDATE 锁父行
//  2. 重新确认 deleted_at 仍然过期（restore 可能已经抢先把它改回 NULL）
//  3. 删除关联
//  4. 删除父行
//  5. 提交
//
// 不在事务里锁住父行就开始删关联，会出现"restore 成功但关联被删"的窗口。
func (r *Runner) Run(ctx context.Context, opts Options) (Stats, error) {
	if err := opts.Validate(); err != nil {
		return Stats{}, err
	}
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}

	cutoff := time.Now().UTC().Add(-opts.Retention).Truncate(time.Millisecond)
	stats := Stats{DryRun: opts.DryRun}
	r.logger.InfoContext(ctx, "purge 开始",
		"retention", opts.Retention.String(),
		"cutoff", cutoff,
		"batch_size", opts.BatchSize,
		"interval", opts.Interval.String(),
		"dry_run", opts.DryRun)

	if opts.DryRun {
		if err := r.countCandidates(ctx, cutoff, &stats); err != nil {
			return stats, err
		}
		r.logger.InfoContext(ctx, "purge dry-run 完成",
			"associations", stats.Associations,
			"documents", stats.Documents,
			"collections", stats.Collections)
		return stats, nil
	}

	for {
		documentIDs, err := r.selectExpiredDocumentIDs(ctx, cutoff, opts.BatchSize)
		if err != nil {
			return stats, err
		}
		collectionIDs, err := r.selectExpiredCollectionIDs(ctx, cutoff, opts.BatchSize)
		if err != nil {
			return stats, err
		}
		if len(documentIDs) == 0 && len(collectionIDs) == 0 {
			break
		}

		stats.Batches++
		var progressed int64

		for _, id := range documentIDs {
			associations, documents, err := r.purgeDocument(ctx, id, cutoff)
			if err != nil {
				return stats, err
			}
			stats.Associations += associations
			stats.Documents += documents
			progressed += associations + documents
		}
		for _, id := range collectionIDs {
			associations, collections, err := r.purgeCollection(ctx, id, cutoff)
			if err != nil {
				return stats, err
			}
			stats.Associations += associations
			stats.Collections += collections
			progressed += associations + collections
		}

		r.logger.DebugContext(ctx, "purge 批次完成",
			"batch", stats.Batches,
			"associations", stats.Associations,
			"documents", stats.Documents,
			"collections", stats.Collections)

		// 候选行可能全部被 restore/其他 worker 抢先处理；没有进展就停，
		// 避免同一批候选永远选出来、又永远删不掉。
		if progressed == 0 {
			break
		}
		if err := sleep(ctx, opts.Interval); err != nil {
			return stats, err
		}
	}

	r.logger.InfoContext(ctx, "purge 完成",
		"associations", stats.Associations,
		"documents", stats.Documents,
		"collections", stats.Collections,
		"batches", stats.Batches)
	return stats, nil
}

// countCandidates 统计 dry-run 会删除的行数。
//
// 关联用 OR 统计：同一个关联同时指向过期 document 和过期 collection 时只算一次。
// document / collection 直接数过期行：过期父行的关联会被先删掉，最终都会被物理删除。
func (r *Runner) countCandidates(ctx context.Context, cutoff time.Time, stats *Stats) error {
	queries := []struct {
		name string
		stmt string
		args []any
		dst  *int64
	}{
		{
			name: "associations",
			stmt: `SELECT COUNT(*)
				   FROM collection_documents cd
				   LEFT JOIN documents d ON d.id = cd.document_id
				   LEFT JOIN collections c ON c.id = cd.collection_id
				   WHERE (d.deleted_at IS NOT NULL AND d.deleted_at < ?)
				      OR (c.deleted_at IS NOT NULL AND c.deleted_at < ?)`,
			args: []any{cutoff, cutoff},
			dst:  &stats.Associations,
		},
		{
			name: "documents",
			stmt: `SELECT COUNT(*)
				   FROM documents d
				   WHERE d.deleted_at IS NOT NULL AND d.deleted_at < ?`,
			args: []any{cutoff},
			dst:  &stats.Documents,
		},
		{
			name: "collections",
			stmt: `SELECT COUNT(*)
				   FROM collections c
				   WHERE c.deleted_at IS NOT NULL AND c.deleted_at < ?`,
			args: []any{cutoff},
			dst:  &stats.Collections,
		},
	}

	for _, q := range queries {
		if err := r.db.QueryRowContext(ctx, q.stmt, q.args...).Scan(q.dst); err != nil {
			return fmt.Errorf("统计待清理 %s: %w", q.name, err)
		}
	}
	return nil
}

// purgeDocument 在一个事务里处理一个过期 document。
//
// 先锁父行再删关联，是 restore 与 purge 并发正确性的核心：
// restore 也会对同一行 FOR UPDATE，二者只能有一个先拿到锁。
func (r *Runner) purgeDocument(ctx context.Context, id uuid.UUID, cutoff time.Time) (associations, documents int64, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin purge document id(%x): %w", id[:], err)
	}
	defer func() { _ = tx.Rollback() }()

	eligible, err := lockExpiredDocument(ctx, tx, id, cutoff)
	if err != nil {
		return 0, 0, err
	}
	if !eligible {
		return 0, 0, nil
	}

	associations, err = execDelete(ctx, tx,
		`DELETE FROM collection_documents WHERE document_id = ?`, id[:])
	if err != nil {
		return 0, 0, fmt.Errorf("delete document associations id(%x): %w", id[:], err)
	}

	// M4.1 增加 document_chunks / document_contents 后，这里必须在删 documents
	// 之前先按 document_id 删除正文和 chunk，否则会留下孤儿数据。

	documents, err = execDelete(ctx, tx, `DELETE FROM documents WHERE id = ?`, id[:])
	if err != nil {
		return 0, 0, fmt.Errorf("delete document id(%x): %w", id[:], err)
	}
	if documents != 1 {
		return 0, 0, fmt.Errorf("delete document id(%x) affected %d rows, want 1", id[:], documents)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit purge document id(%x): %w", id[:], err)
	}
	return associations, documents, nil
}

// purgeCollection 在一个事务里处理一个过期 collection。
func (r *Runner) purgeCollection(ctx context.Context, id uuid.UUID, cutoff time.Time) (associations, collections int64, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin purge collection id(%x): %w", id[:], err)
	}
	defer func() { _ = tx.Rollback() }()

	eligible, err := lockExpiredCollection(ctx, tx, id, cutoff)
	if err != nil {
		return 0, 0, err
	}
	if !eligible {
		return 0, 0, nil
	}

	associations, err = execDelete(ctx, tx,
		`DELETE FROM collection_documents WHERE collection_id = ?`, id[:])
	if err != nil {
		return 0, 0, fmt.Errorf("delete collection associations id(%x): %w", id[:], err)
	}

	collections, err = execDelete(ctx, tx, `DELETE FROM collections WHERE id = ?`, id[:])
	if err != nil {
		return 0, 0, fmt.Errorf("delete collection id(%x): %w", id[:], err)
	}
	if collections != 1 {
		return 0, 0, fmt.Errorf("delete collection id(%x) affected %d rows, want 1", id[:], collections)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit purge collection id(%x): %w", id[:], err)
	}
	return associations, collections, nil
}

// lockExpiredDocument 锁住 document 行并重新判断它是否仍可清理。
// 返回 false 表示不存在、已被 restore、或 deleted_at 已不在保留期。
func lockExpiredDocument(ctx context.Context, tx *sql.Tx, id uuid.UUID, cutoff time.Time) (bool, error) {
	var deletedAt sql.NullTime
	if err := tx.QueryRowContext(ctx,
		`SELECT deleted_at FROM documents WHERE id = ? FOR UPDATE`, id[:]).Scan(&deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lock document id(%x): %w", id[:], err)
	}
	if !deletedAt.Valid || !deletedAt.Time.Before(cutoff) {
		return false, nil
	}
	return true, nil
}

// lockExpiredCollection 锁住 collection 行并重新判断它是否仍可清理。
func lockExpiredCollection(ctx context.Context, tx *sql.Tx, id uuid.UUID, cutoff time.Time) (bool, error) {
	var deletedAt sql.NullTime
	if err := tx.QueryRowContext(ctx,
		`SELECT deleted_at FROM collections WHERE id = ? FOR UPDATE`, id[:]).Scan(&deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lock collection id(%x): %w", id[:], err)
	}
	if !deletedAt.Valid || !deletedAt.Time.Before(cutoff) {
		return false, nil
	}
	return true, nil
}

func execDelete(ctx context.Context, tx *sql.Tx, stmt string, args ...any) (int64, error) {
	res, err := tx.ExecContext(ctx, stmt, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (r *Runner) selectExpiredDocumentIDs(ctx context.Context, cutoff time.Time, batch int) ([]uuid.UUID, error) {
	const stmt = `SELECT id
				  FROM documents
				  WHERE deleted_at IS NOT NULL AND deleted_at < ?
				  ORDER BY deleted_at, id
				  LIMIT ?`
	return queryUUIDs(ctx, r.db, stmt, cutoff, batch)
}

func (r *Runner) selectExpiredCollectionIDs(ctx context.Context, cutoff time.Time, batch int) ([]uuid.UUID, error) {
	const stmt = `SELECT id
				  FROM collections
				  WHERE deleted_at IS NOT NULL AND deleted_at < ?
				  ORDER BY deleted_at, id
				  LIMIT ?`
	return queryUUIDs(ctx, r.db, stmt, cutoff, batch)
}

func queryUUIDs(ctx context.Context, db DB, query string, args ...any) ([]uuid.UUID, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询待清理 id: %w", err)
	}
	defer rows.Close()

	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("扫描待清理 id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历待清理 id: %w", err)
	}
	return ids, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
