package knowledge

import (
	"context"

	"github.com/google/uuid"
)

// DocumentRepository 是文档与 collection_documents 关联的持久化接口。
//
// 与 Repository 分开：collections 和 documents 是两个聚合，service 可以分别注入 fake。
// 全局约定同 Repository：带 ctx、只返回未软删数据、时间 UTC、不存在返回 ErrNotFound。
type DocumentRepository interface {
	// CreateDocument 新建文档元数据。
	//
	// 契约：
	//   - ID 由实现生成（uuid.NewV7()），写入 BINARY(16) 时传 id[:]
	//   - CreatedAt / UpdatedAt 由实现填（UTC，毫秒精度）
	//   - SourceURI / MimeType / SizeBytes 为 nil 时写 NULL
	CreateDocument(ctx context.Context, title string, sourceURI, mimeType *string, sizeBytes *int64) (*Document, error)

	// GetDocument 按 id 取一个未软删的文档；不存在返回 ErrNotFound。
	GetDocument(ctx context.Context, id uuid.UUID) (*Document, error)

	// UpdateDocument 按 PATCH 语义更新文档元数据。
	//
	// 契约：
	//   - 不存在 / 已软删 → ErrNotFound
	//   - Title 为 nil 表示不改；其余字段按 Optional 的三态处理
	//   - 更新成功后 UpdatedAt 刷新为当前时间，返回完整记录
	UpdateDocument(ctx context.Context, id uuid.UUID, patch DocumentUpdate) (*Document, error)

	// SoftDeleteDocument 软删文档：置 deleted_at = 当前时间。
	//
	// 契约：
	//   - 不存在 / 已软删 → ErrNotFound
	//   - 不物理删除；collection_documents 关联行保留，列表查询通过 JOIN 过滤
	SoftDeleteDocument(ctx context.Context, id uuid.UUID) error

	// AddDocumentToCollection 创建文档与知识库的关联。
	//
	// 契约：
	//   - collection / document 不存在或已软删 → ErrNotFound
	//   - 关联已存在（PRIMARY KEY 冲突）→ ErrConflict
	//   - 返回的 CollectionDocument 带上 Document 和 AddedAt
	AddDocumentToCollection(ctx context.Context, collectionID, documentID uuid.UUID) (*CollectionDocument, error)

	// RemoveDocumentFromCollection 移除关联（硬删 collection_documents 行）。
	//
	// 契约：关联不存在 → ErrNotFound；不删除 document 本身。
	RemoveDocumentFromCollection(ctx context.Context, collectionID, documentID uuid.UUID) error

	// ListDocumentsByCollection 返回按 (added_at DESC, document_id DESC) 排序的一页。
	//
	// 契约：
	//   - collection 不存在或已软删 → ErrNotFound
	//   - 只返回未软删的 document
	//   - cursor.CreatedAt 是 added_at，cursor.ID 是 document_id
	//   - limit 由调用方校验（1..100）；next 为 nil 表示没有更多
	ListDocumentsByCollection(ctx context.Context, collectionID uuid.UUID, cursor *Cursor, limit int) ([]*CollectionDocument, *Cursor, error)

	// RestoreDocument 恢复一个已软删的文档：deleted_at 置 NULL，updated_at 刷新。
	//
	// 契约：
	//   - 行不存在（含已被物理删除）→ ErrNotFound
	//   - 行存在但 deleted_at 已经是 NULL → ErrConflict（本来就没删）
	//   - 必须用事务 + SELECT ... FOR UPDATE 锁行；restore 与 purge 通过同一把行锁串行化
	//   - 关联行、正文（M4.1 之后）都不在这里改；软删时保留，恢复后原样可见
	RestoreDocument(ctx context.Context, id uuid.UUID) (*Document, error)
}
