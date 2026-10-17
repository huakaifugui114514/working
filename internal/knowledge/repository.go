package knowledge

import (
	"context"

	"github.com/google/uuid"
)

// Repository 是知识库的持久化接口。
//
// 为什么定义在这里、而不是躺在 mysql.go 里：接口应当由**使用方**（service）定义，
// 这样 service 的单测可以注入 fake，完全不需要连数据库（AGENTS.md §1.2.3）。
//
// 全局约定：
//   - 每个方法都带 ctx，并在内部使用 QueryContext / ExecContext / QueryRowContext
//   - 只返回**未软删**的数据：deleted_at IS NULL 的过滤在 SQL 里做，不推给调用方
//   - 时间一律 UTC（DATETIME(3) 存的是 UTC，读出来也应是 UTC）
//   - 分页是 keyset：cursor 为 nil 表示第一页；返回的 next 为 nil 表示没有更多
//   - 目标不存在时返回 ErrNotFound（不要泄漏 sql.ErrNoRows）
type Repository interface {
	// CreateCollection 新建知识库。
	//
	// 契约：
	//   - ID 由实现生成（uuid.NewV7()）；写入 BINARY(16) 时传 id[:]
	//   - CreatedAt / UpdatedAt 由实现填（UTC，毫秒精度）
	//   - 返回的 Collection 必须带上生成好的字段
	CreateCollection(ctx context.Context, name, description string) (*Collection, error)

	// GetCollection 按 id 取一个未软删的知识库；不存在返回 ErrNotFound。
	GetCollection(ctx context.Context, id uuid.UUID) (*Collection, error)

	// ListCollections 返回按 (created_at DESC, id DESC) 排序的一页。
	//
	// 契约：
	//   - cursor == nil：第一页（不加游标条件）
	//   - cursor != nil：SQL 必须写**展开形式**
	//     created_at < ? OR (created_at = ? AND id < ?)
	//     —— 行构造器 (a,b) < (?,?) 会被优化器退化成全索引扫描（已实测）
	//   - limit：实现假定调用方已校验范围（1..100）
	//   - next：还有数据时返回"最后一条的 (created_at, id)"；否则 nil
	ListCollections(ctx context.Context, cursor *Cursor, limit int) ([]*Collection, *Cursor, error)

	// UpdateCollection 按 PATCH 语义更新；字段为 nil 表示不改。
	//
	// 契约：
	//   - 不存在 / 已软删 → ErrNotFound
	//   - 更新成功后 UpdatedAt 刷新为当前时间
	//   - 返回更新后的完整记录
	UpdateCollection(ctx context.Context, id uuid.UUID, patch CollectionUpdate) (*Collection, error)

	// SoftDeleteCollection 软删：置 deleted_at = 当前时间（UTC）。
	//
	// 契约：
	//   - 不存在 / 已软删 → ErrNotFound（软删两次应当报错，而不是静默成功）
	//   - 不物理删除；关联表 collection_documents 的行保留（软删不触发级联，
	//     列表查询会从 collections 出发过滤，所以残留关联不影响正确性）
	SoftDeleteCollection(ctx context.Context, id uuid.UUID) error
}
