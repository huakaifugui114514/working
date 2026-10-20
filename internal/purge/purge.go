// Package purge 负责软删数据的物理清理。
//
// 设计约束（见 docs/adr/004-documents-api.md）：
//   - 只清理 deleted_at 早于保留期的行；读路径完全不受影响
//   - 分批执行，批间 sleep，避免长事务、锁等待和主从延迟
//   - 先删关联，再删 document / collection；没有关联的过期行才允许物理删除
//   - 不依赖外键（schema 故意没有 FK），一致性由本包的 NOT EXISTS 条件兜底
//
// 本包不挂 HTTP，也不在请求路径上运行：cmd/purge 由 cron / 定时任务调用。
package purge

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
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
}

// Options 是一次 purge 运行的参数。
type Options struct {
	// Retention 是软删后保留多久才允许物理删除；必须 > 0。
	Retention time.Duration

	// BatchSize 是每批最多处理多少个 document / collection；必须 1..MaxBatchSize。
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

// Run 清理所有超过保留期的软删数据，直到没有可清理的行。
//
// 每一轮按固定顺序执行：
//  1. 删除"过期 document"的关联
//  2. 删除"过期 collection"的关联
//  3. 删除已无关联的过期 document
//  4. 删除已无关联的过期 collection
//
// 顺序不能反：先删父行会留下孤儿关联；先删关联才能让第 3/4 步的 NOT EXISTS 成立。
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
		var progressed int64
		stats.Batches++

		n, err := r.purgeDocumentAssociations(ctx, cutoff, opts.BatchSize)
		if err != nil {
			return stats, err
		}
		stats.Associations += n
		progressed += n

		n, err = r.purgeCollectionAssociations(ctx, cutoff, opts.BatchSize)
		if err != nil {
			return stats, err
		}
		stats.Associations += n
		progressed += n

		n, err = r.purgeDocuments(ctx, cutoff, opts.BatchSize)
		if err != nil {
			return stats, err
		}
		stats.Documents += n
		progressed += n

		n, err = r.purgeCollections(ctx, cutoff, opts.BatchSize)
		if err != nil {
			return stats, err
		}
		stats.Collections += n
		progressed += n

		r.logger.DebugContext(ctx, "purge 批次完成",
			"batch", stats.Batches,
			"associations", stats.Associations,
			"documents", stats.Documents,
			"collections", stats.Collections)

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
// 关联用 OR 统计：同一个关联同时指向过期 document 和过期 collection 时只算一次，
// 实际删除时也只会删一次。
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
			// 过期 document 的关联会先被清掉，所以"过期 document"最终都会被物理删除；
			// dry-run 直接数过期行即可，不需要再算当前有没有关联。
			name: "documents",
			stmt: `SELECT COUNT(*)
				   FROM documents d
				   WHERE d.deleted_at IS NOT NULL AND d.deleted_at < ?`,
			args: []any{cutoff},
			dst:  &stats.Documents,
		},
		{
			// 同上：过期 collection 的关联会先被清掉，最终都会被删除。
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

// purgeDocumentAssociations 删除指向过期 document 的关联。
//
// 按关联行（而不是按 document）分批：一个 document 可能属于很多 collection，
// 按父行分批会让单条 DELETE 一次删掉巨量关联，回到长事务问题。
func (r *Runner) purgeDocumentAssociations(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	const selectStmt = `SELECT cd.collection_id, cd.document_id
						FROM collection_documents cd
						JOIN documents d ON d.id = cd.document_id
						WHERE d.deleted_at IS NOT NULL AND d.deleted_at < ?
						ORDER BY cd.document_id, cd.collection_id
						LIMIT ?`
	pairs, err := queryAssociationPairs(ctx, r.db, selectStmt, cutoff, batch)
	if err != nil {
		return 0, err
	}
	if len(pairs) == 0 {
		return 0, nil
	}

	stmt := `DELETE FROM collection_documents
			 WHERE (collection_id, document_id) IN (` + pairPlaceholders(len(pairs)) + `)`
	res, err := r.db.ExecContext(ctx, stmt, pairArgs(pairs)...)
	if err != nil {
		return 0, fmt.Errorf("删除过期 document 的关联: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取删除关联行数: %w", err)
	}
	return n, nil
}

// purgeCollectionAssociations 删除指向过期 collection 的关联。
// 同样按关联行分批，避免一个超大知识库让单条 DELETE 删掉几十万行。
func (r *Runner) purgeCollectionAssociations(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	const selectStmt = `SELECT cd.collection_id, cd.document_id
						FROM collection_documents cd
						JOIN collections c ON c.id = cd.collection_id
						WHERE c.deleted_at IS NOT NULL AND c.deleted_at < ?
						ORDER BY cd.collection_id, cd.document_id
						LIMIT ?`
	pairs, err := queryAssociationPairs(ctx, r.db, selectStmt, cutoff, batch)
	if err != nil {
		return 0, err
	}
	if len(pairs) == 0 {
		return 0, nil
	}

	stmt := `DELETE FROM collection_documents
			 WHERE (collection_id, document_id) IN (` + pairPlaceholders(len(pairs)) + `)`
	res, err := r.db.ExecContext(ctx, stmt, pairArgs(pairs)...)
	if err != nil {
		return 0, fmt.Errorf("删除过期 collection 的关联: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取删除关联行数: %w", err)
	}
	return n, nil
}

// purgeDocuments 删除已无关联的过期 document。
//
// 删除语句再次带 NOT EXISTS：从 SELECT 到 DELETE 之间如果有人插入了关联，
// 这一行必须留下，下一轮再处理，不能制造孤儿关联。
func (r *Runner) purgeDocuments(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	ids, err := r.selectExpiredDocumentIDsToDelete(ctx, cutoff, batch)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	stmt := `DELETE d FROM documents d
			 WHERE d.id IN (` + placeholders(len(ids)) + `)
			   AND d.deleted_at IS NOT NULL
			   AND d.deleted_at < ?
			   AND NOT EXISTS (SELECT 1 FROM collection_documents cd WHERE cd.document_id = d.id)`
	args := append(idArgs(ids), cutoff)
	res, err := r.db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return 0, fmt.Errorf("删除过期 document: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取删除 document 行数: %w", err)
	}
	return n, nil
}

// purgeCollections 删除已无关联的过期 collection。
func (r *Runner) purgeCollections(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	ids, err := r.selectExpiredCollectionIDsToDelete(ctx, cutoff, batch)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	stmt := `DELETE c FROM collections c
			 WHERE c.id IN (` + placeholders(len(ids)) + `)
			   AND c.deleted_at IS NOT NULL
			   AND c.deleted_at < ?
			   AND NOT EXISTS (SELECT 1 FROM collection_documents cd WHERE cd.collection_id = c.id)`
	args := append(idArgs(ids), cutoff)
	res, err := r.db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return 0, fmt.Errorf("删除过期 collection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取删除 collection 行数: %w", err)
	}
	return n, nil
}

func (r *Runner) selectExpiredDocumentIDsToDelete(ctx context.Context, cutoff time.Time, batch int) ([]uuid.UUID, error) {
	const stmt = `SELECT d.id
				  FROM documents d
				  WHERE d.deleted_at IS NOT NULL AND d.deleted_at < ?
				    AND NOT EXISTS (SELECT 1 FROM collection_documents cd WHERE cd.document_id = d.id)
				  ORDER BY d.deleted_at, d.id
				  LIMIT ?`
	return queryUUIDs(ctx, r.db, stmt, cutoff, batch)
}

func (r *Runner) selectExpiredCollectionIDsToDelete(ctx context.Context, cutoff time.Time, batch int) ([]uuid.UUID, error) {
	const stmt = `SELECT c.id
				  FROM collections c
				  WHERE c.deleted_at IS NOT NULL AND c.deleted_at < ?
				    AND NOT EXISTS (SELECT 1 FROM collection_documents cd WHERE cd.collection_id = c.id)
				  ORDER BY c.deleted_at, c.id
				  LIMIT ?`
	return queryUUIDs(ctx, r.db, stmt, cutoff, batch)
}

// associationPair 是 collection_documents 的联合主键，用来按行批量 DELETE。
type associationPair struct {
	collectionID uuid.UUID
	documentID   uuid.UUID
}

func queryAssociationPairs(ctx context.Context, db DB, query string, args ...any) ([]associationPair, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询待清理关联: %w", err)
	}
	defer rows.Close()

	pairs := make([]associationPair, 0)
	for rows.Next() {
		var pair associationPair
		if err := rows.Scan(&pair.collectionID, &pair.documentID); err != nil {
			return nil, fmt.Errorf("扫描待清理关联: %w", err)
		}
		pairs = append(pairs, pair)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历待清理关联: %w", err)
	}
	return pairs, nil
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

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func idArgs(ids []uuid.UUID) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id[:]
	}
	return args
}

func pairPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("(?,?),", n), ",")
}

func pairArgs(pairs []associationPair) []any {
	args := make([]any, 0, len(pairs)*2)
	for _, pair := range pairs {
		args = append(args, pair.collectionID[:], pair.documentID[:])
	}
	return args
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
