package knowledge

// 本文件是 DocumentRepository 的 MySQL 实现骨架。
// 具体 SQL 由你实现；契约见 document_repository.go，模型见 model.go。

import (
	"context"

	"github.com/google/uuid"
)

var _ DocumentRepository = (*MySQLRepository)(nil)

// CreateDocument 实现 DocumentRepository。
//
// 关键点：
//   - id 用 uuid.NewV7()；写入 BINARY(16) 用 id[:]
//   - source_uri / mime_type / size_bytes 为 nil 时写 NULL
//   - created_at / updated_at 用 UTC 毫秒
//   - MySQL 没有 RETURNING，用生成好的值构造返回结构
func (r *MySQLRepository) CreateDocument(
	ctx context.Context,
	title string,
	sourceURI, mimeType *string,
	sizeBytes *int64,
) (*Document, error) {
	panic("TODO(documents): CreateDocument")
}

// GetDocument 实现 DocumentRepository。
//
// 关键点：WHERE id = ? AND deleted_at IS NULL；sql.ErrNoRows → ErrNotFound。
func (r *MySQLRepository) GetDocument(ctx context.Context, id uuid.UUID) (*Document, error) {
	panic("TODO(documents): GetDocument")
}

// UpdateDocument 实现 DocumentRepository。
//
// 关键点：
//   - 空 patch 直接 GetDocument，不写库
//   - 事务 + SELECT ... FOR UPDATE 锁行
//   - Title 非 nil 才拼 title = ?
//   - Optional.Set=false 不拼；Set=true 且 Value=nil 拼 xxx = NULL；否则拼 xxx = ?
//   - 永远刷新 updated_at；返回锁内读到的完整记录
//   - RowsAffected 只检查 error（FOR UPDATE 已确认行存在）
func (r *MySQLRepository) UpdateDocument(ctx context.Context, id uuid.UUID, patch DocumentUpdate) (*Document, error) {
	panic("TODO(documents): UpdateDocument")
}

// SoftDeleteDocument 实现 DocumentRepository。
//
// 关键点：UPDATE ... SET deleted_at=?, updated_at=? WHERE id=? AND deleted_at IS NULL；
// RowsAffected==0 → ErrNotFound。不要删 collection_documents。
func (r *MySQLRepository) SoftDeleteDocument(ctx context.Context, id uuid.UUID) error {
	panic("TODO(documents): SoftDeleteDocument")
}

// AddDocumentToCollection 实现 DocumentRepository。
//
// 关键点：
//   - 事务；先锁 collection 行，再 SELECT document ... FOR UPDATE
//   - collection/document 不存在或已软删 → ErrNotFound
//   - INSERT collection_documents；主键冲突（MySQL 1062）→ ErrConflict
//   - 返回 CollectionDocument{Document, AddedAt}
func (r *MySQLRepository) AddDocumentToCollection(
	ctx context.Context,
	collectionID, documentID uuid.UUID,
) (*CollectionDocument, error) {
	panic("TODO(documents): AddDocumentToCollection")
}

// RemoveDocumentFromCollection 实现 DocumentRepository。
//
// 关键点：DELETE FROM collection_documents WHERE collection_id=? AND document_id=?；
// RowsAffected==0 → ErrNotFound。不要删 documents 行。
func (r *MySQLRepository) RemoveDocumentFromCollection(
	ctx context.Context,
	collectionID, documentID uuid.UUID,
) error {
	panic("TODO(documents): RemoveDocumentFromCollection")
}

// ListDocumentsByCollection 实现 DocumentRepository。
//
// 关键点：
//   - 先确认 collection 存在且未软删，否则 ErrNotFound
//   - JOIN documents d ON d.id = cd.document_id AND d.deleted_at IS NULL
//   - 排序 cd.created_at DESC, cd.document_id DESC
//   - 取 limit+1 判断 next；cursor.CreatedAt 是 added_at，cursor.ID 是 document_id
//   - 展开写法：cd.created_at < ? OR (cd.created_at = ? AND cd.document_id < ?)
//   - cursor.ID 传 cursor.ID[:]；不要传 uuid.UUID（会变成 36 字符字符串）
//   - defer rows.Close()，检查 rows.Err()
func (r *MySQLRepository) ListDocumentsByCollection(
	ctx context.Context,
	collectionID uuid.UUID,
	cursor *Cursor,
	limit int,
) ([]*CollectionDocument, *Cursor, error) {
	panic("TODO(documents): ListDocumentsByCollection")
}

// 实现提示：建议抽一个 scanDocument 辅助函数。
// source_uri / mime_type 用 sql.NullString，size_bytes 用 sql.NullInt64，
// 再转成 *string / *int64；sql.ErrNoRows 统一映射成 ErrNotFound。
