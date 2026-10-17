package knowledge

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
)

// MySQLRepository 是 Repository 的 MySQL 实现。
//
// 实现要点（契约见 repository.go，这里只补充"怎么落库"的约束）：
//   - id 存 BINARY(16)：写入传 id[:]；读出用 uuid.FromBytes(raw)
//   - 时间存 DATETIME(3)，全部 UTC；写入时用 time.Now().UTC()
//   - 软删过滤 deleted_at IS NULL 每条查询都要带（建议封成常量）
//   - 分页 SQL 用展开形式，不用 (a,b) < (?,?) 行构造器
type MySQLRepository struct {
	db *sql.DB
}

// NewMySQLRepository 用已有的连接池构造仓储。
// 连接池的生命周期由调用方管理（main 里 defer db.Close()），仓储不负责关闭它。
func NewMySQLRepository(db *sql.DB) *MySQLRepository {
	return &MySQLRepository{db: db}
}

// CreateCollection 实现 Repository。
//
// 实现提示：
//   - id, err := uuid.NewV7()
//   - now := time.Now().UTC()
//   - INSERT INTO collections (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
//   - 不需要 RETURNING（MySQL 没有），直接用生成好的值构造返回结构
func (r *MySQLRepository) CreateCollection(ctx context.Context, name, description string) (*Collection, error) {
	panic("TODO(day4): 实现 CreateCollection")
}

// GetCollection 实现 Repository。
//
// 实现提示：
//   - QueryRowContext + Scan；用 errors.Is(err, sql.ErrNoRows) 判断"不存在"
//   - Scan 的目标与列顺序必须逐字对应（这是最容易错的地方：改了 SELECT 忘了改 Scan）
func (r *MySQLRepository) GetCollection(ctx context.Context, id uuid.UUID) (*Collection, error) {
	panic("TODO(day4): 实现 GetCollection")
}

// ListCollections 实现 Repository。
//
// 实现要点：
//   - 没有游标：WHERE deleted_at IS NULL ORDER BY created_at DESC, id DESC LIMIT ?
//   - 有游标：追加 AND (created_at < ? OR (created_at = ? AND id < ?))
//   - ⚠️ 必须检查 rows.Err()：遍历中途出错时 rows.Next() 只会返回 false，
//     不查 rows.Err() 会把"查询失败"当成"没有数据"
//   - 取 limit+1 条来判断"还有没有下一页"，可以少一次 count 查询
func (r *MySQLRepository) ListCollections(ctx context.Context, cursor *Cursor, limit int) ([]*Collection, *Cursor, error) {
	panic("TODO(day4): 实现 ListCollections")
}

// UpdateCollection 实现 Repository。
//
// 实现要点：
//   - PATCH 语义：只更新非 nil 的字段（动态拼 SQL 时注意参数顺序与占位符个数）
//   - 建议先 GetCollection，再 UPDATE，最后返回更新后的记录（两步之间用同一事务）
//   - 用 UPDATE ... WHERE id = ? AND deleted_at IS NULL，检查 RowsAffected 判断是否存在
func (r *MySQLRepository) UpdateCollection(ctx context.Context, id uuid.UUID, patch CollectionUpdate) (*Collection, error) {
	panic("TODO(day4): 实现 UpdateCollection")
}

// SoftDeleteCollection 实现 Repository。
//
// 实现要点：
//   - UPDATE collections SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL
//   - 用 RowsAffected 判断：0 行 = 不存在或已删 → ErrNotFound
//     （把 deleted_at IS NULL 放进 WHERE，既防重复删，也避免"读到旧值再改"的竞态）
func (r *MySQLRepository) SoftDeleteCollection(ctx context.Context, id uuid.UUID) error {
	panic("TODO(day4): 实现 SoftDeleteCollection")
}
