package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// collectionColumns 统一 SELECT 的列顺序；description 允许 NULL，
	// 但领域模型是 string，所以读出来统一折叠成空串。
	collectionColumns = `id, name, COALESCE(description, ''), created_at, updated_at`

	createCollectionStatement = `INSERT INTO collections (id, name, description, created_at, updated_at)
									VALUES (?, ?, ?, ?, ?)`

	getCollectionStatement = `SELECT ` + collectionColumns + `
								FROM collections
								WHERE id = ? AND deleted_at IS NULL`

	// getCollectionForUpdateStatement 与 getCollectionStatement 只差 FOR UPDATE：
	// UpdateCollection 用它锁住目标行，避免"读到旧值 → 写回"之间被并发修改或软删。
	getCollectionForUpdateStatement = `SELECT ` + collectionColumns + `
								FROM collections
								WHERE id = ? AND deleted_at IS NULL
								FOR UPDATE`

	listCollectionsStatementsWithoutCursor = `SELECT ` + collectionColumns + `
								FROM collections
								WHERE deleted_at IS NULL
								ORDER BY created_at DESC, id DESC
								LIMIT ?`

	listCollectionsStatementsWithCursor = `SELECT ` + collectionColumns + `
								FROM collections
								WHERE deleted_at IS NULL AND (created_at < ? OR (created_at = ? AND id < ?))
								ORDER BY created_at DESC, id DESC
								LIMIT ?`

	softDeleteCollectionStatement = `UPDATE collections
								SET deleted_at = ?, updated_at = ?
								WHERE id = ? AND deleted_at IS NULL`

	// restoreCollectionColumns 比 collectionColumns 多 deleted_at：
	// restore 必须看到软删行，否则无法判断 ErrNotFound / ErrConflict。
	restoreCollectionColumns = collectionColumns + `, deleted_at`

	restoreCollectionForUpdateStatement = `SELECT ` + restoreCollectionColumns + `
								FROM collections
								WHERE id = ?
								FOR UPDATE`

	restoreCollectionStatement = `UPDATE collections
								SET deleted_at = NULL, updated_at = ?
								WHERE id = ?`
)

// dbExecutor 是 *sql.DB 和 *sql.Conn 的公共子集。
//
// 生产环境传 *sql.DB；集成测试传一条固定的 *sql.Conn，在会话内建同名
// TEMPORARY TABLE 隔离数据（临时表只对当前连接可见，不会碰真实表）。
type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// MySQLRepository 是 Repository 的 MySQL 实现。
//
// 实现要点（契约见 repository.go，这里只补充"怎么落库"的约束）：
//   - id 存 BINARY(16)：写入传 id[:]；读出用 uuid.UUID 的 sql.Scanner
//   - 时间存 DATETIME(3)，全部 UTC；写入时用 time.Now().UTC()
//   - 软删过滤 deleted_at IS NULL 每条查询都要带
//   - 分页 SQL 用展开形式，不用 (a,b) < (?,?) 行构造器
type MySQLRepository struct {
	db dbExecutor
}

// NewMySQLRepository 用已有的连接池构造仓储。
// 连接池的生命周期由调用方管理（main 里 defer db.Close()），仓储不负责关闭它。
func NewMySQLRepository(db *sql.DB) *MySQLRepository {
	return &MySQLRepository{db: db}
}

// CreateCollection 实现 Repository。
//
// id 和 created_at 是两次独立的时钟读取：uuid.NewV7() 的时间戳只用于
// "id 大致按时间有序"，created_at 才是业务上的创建时间；两者可能差 1ms。
func (r *MySQLRepository) CreateCollection(ctx context.Context, name, description string) (*Collection, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid.NewV7: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := r.db.ExecContext(ctx, createCollectionStatement, id[:], name, description, now, now); err != nil {
		return nil, fmt.Errorf("create collection: %w", err)
	}

	return &Collection{
		ID:          id,
		Name:        name,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// scanCollection 把一行扫描成 Collection，并把 sql.ErrNoRows 映射成领域错误。
// id 只用于错误消息，方便定位是哪条记录。
func scanCollection(row *sql.Row, id uuid.UUID) (*Collection, error) {
	c := &Collection{}
	if err := row.Scan(
		&c.ID,
		&c.Name,
		&c.Description,
		&c.CreatedAt,
		&c.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("id(%x) not found: %w", id[:], ErrNotFound)
		}
		return nil, fmt.Errorf("scan collection: %w", err)
	}
	return c, nil
}

// GetCollection 实现 Repository。
func (r *MySQLRepository) GetCollection(ctx context.Context, id uuid.UUID) (*Collection, error) {
	return scanCollection(r.db.QueryRowContext(ctx, getCollectionStatement, id[:]), id)
}

// ListCollections 实现 Repository。
//
// 多取一条（limit+1）来判断"还有没有下一页"：如果只有 limit 条或更少，
// 说明已经到底，next 必须是 nil；否则截断到 limit，cursor 取本页最后一条。
func (r *MySQLRepository) ListCollections(ctx context.Context, cursor *Cursor, limit int) ([]*Collection, *Cursor, error) {
	if limit <= 0 {
		return nil, nil, fmt.Errorf("limit must be positive: %d", limit)
	}

	fetch := limit + 1
	var (
		rows *sql.Rows
		err  error
	)
	if cursor == nil {
		rows, err = r.db.QueryContext(ctx, listCollectionsStatementsWithoutCursor, fetch)
	} else {
		// cursor.ID[:] 是 BINARY(16)；直接传 uuid.UUID 会走 Value() 变成
		// 36 字符字符串，MySQL 的二进制比较会得到错误结果。
		rows, err = r.db.QueryContext(ctx, listCollectionsStatementsWithCursor,
			cursor.CreatedAt, cursor.CreatedAt, cursor.ID[:], fetch)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list collections: %w", err)
	}
	defer rows.Close()

	collections := make([]*Collection, 0, fetch)
	for rows.Next() {
		c := &Collection{}
		if err := rows.Scan(
			&c.ID,
			&c.Name,
			&c.Description,
			&c.CreatedAt,
			&c.UpdatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan collection: %w", err)
		}
		collections = append(collections, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate collections: %w", err)
	}

	if len(collections) <= limit {
		return collections, nil, nil
	}

	collections = collections[:limit]
	last := collections[limit-1]
	return collections, &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, nil
}

// UpdateCollection 实现 Repository。
//
// PATCH 语义：只把非 nil 的字段拼进 SET，避免"读旧值 → 写回"覆盖并发修改。
// SELECT ... FOR UPDATE 锁住目标行，保证存在性判断和更新在同一个事务里；
// 空 PATCH 是 no-op，不写库、不刷新 updated_at。
func (r *MySQLRepository) UpdateCollection(ctx context.Context, id uuid.UUID, patch CollectionUpdate) (*Collection, error) {
	if patch.Name == nil && patch.Description == nil {
		return r.GetCollection(ctx, id)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update collection: %w", err)
	}
	// Commit 成功后 Rollback 返回 sql.ErrTxDone；这里只为失败路径兜底，
	// 因此忽略返回值（真正需要报告的错误在 Commit 分支返回）。
	defer func() { _ = tx.Rollback() }()

	c, err := scanCollection(tx.QueryRowContext(ctx, getCollectionForUpdateStatement, id[:]), id)
	if err != nil {
		return nil, err
	}

	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if patch.Name != nil {
		c.Name = *patch.Name
		sets = append(sets, "name = ?")
		args = append(args, c.Name)
	}
	if patch.Description != nil {
		c.Description = *patch.Description
		sets = append(sets, "description = ?")
		args = append(args, c.Description)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	c.UpdatedAt = now
	sets = append(sets, "updated_at = ?")
	args = append(args, now)
	args = append(args, id[:])

	stmt := "UPDATE collections SET " + strings.Join(sets, ", ") + " WHERE id = ? AND deleted_at IS NULL"
	res, err := tx.ExecContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("update collection: %w", err)
	}
	// FOR UPDATE 已确认行存在；RowsAffected 为 0 只可能是同一毫秒内所有字段
	// （含 updated_at）都没变化，不是 not found，所以只检查 error。
	if _, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("update collection rows affected: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update collection: %w", err)
	}
	return c, nil
}

// SoftDeleteCollection 实现 Repository。
//
// 用一条带 deleted_at IS NULL 的 UPDATE 完成"存在性判断 + 软删"：
// RowsAffected==0 时，要么行不存在，要么已经被软删；契约要求两者都返回 ErrNotFound。
func (r *MySQLRepository) SoftDeleteCollection(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	res, err := r.db.ExecContext(ctx, softDeleteCollectionStatement, now, now, id[:])
	if err != nil {
		return fmt.Errorf("soft delete collection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("soft delete collection rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("id(%x) not found: %w", id[:], ErrNotFound)
	}
	return nil
}

// RestoreCollection 实现 Repository。
//
// 先 SELECT ... FOR UPDATE 锁住目标行，再根据 deleted_at 区分：
// 行不存在 → ErrNotFound；deleted_at 为 NULL → ErrConflict；否则恢复。
// 这把行锁与 purge 的父行锁是同一个，二者只能有一个先拿到锁。
func (r *MySQLRepository) RestoreCollection(ctx context.Context, id uuid.UUID) (*Collection, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin restore collection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	c := &Collection{}
	if err := tx.QueryRowContext(ctx, restoreCollectionForUpdateStatement, id[:]).Scan(
		&c.ID,
		&c.Name,
		&c.Description,
		&c.CreatedAt,
		&c.UpdatedAt,
		&c.DeletedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("id(%x) not found: %w", id[:], ErrNotFound)
		}
		return nil, fmt.Errorf("scan collection for restore: %w", err)
	}
	if c.DeletedAt == nil {
		return nil, fmt.Errorf("id(%x) is not deleted: %w", id[:], ErrConflict)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	res, err := tx.ExecContext(ctx, restoreCollectionStatement, now, id[:])
	if err != nil {
		return nil, fmt.Errorf("restore collection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("restore collection rows affected: %w", err)
	}
	if n != 1 {
		return nil, fmt.Errorf("restore collection affected %d rows, want 1", n)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit restore collection: %w", err)
	}

	c.DeletedAt = nil
	c.UpdatedAt = now
	return c, nil
}
