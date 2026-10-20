package knowledge

// 本文件是 DocumentRepository 的 MySQL 实现。
// 契约见 document_repository.go，模型见 model.go。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

var _ DocumentRepository = (*MySQLRepository)(nil)

const (
	// mysqlDuplicateEntry 是 MySQL 的 ER_DUP_ENTRY。
	// 关联表用联合主键防重复，重复添加会命中这个错误；不能把 1062 当成 500。
	mysqlDuplicateEntry = 1062

	// documentColumns 统一 documents 的 SELECT 列顺序，scanDocument 依赖这个顺序。
	documentColumns = `id, title, source_uri, mime_type, size_bytes, created_at, updated_at, deleted_at`

	// documentColumnsForJoin 给 JOIN 用：d.created_at 与 cd.created_at 同名，
	// 不限定表名会让 MySQL 报 "Column 'created_at' in field list is ambiguous"。
	documentColumnsForJoin = `d.id, d.title, d.source_uri, d.mime_type, d.size_bytes, d.created_at, d.updated_at, d.deleted_at`

	createDocumentStatement = `INSERT INTO documents
								(id, title, source_uri, mime_type, size_bytes, created_at, updated_at, deleted_at)
								VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	// 软删的文档对读路径不可见，所以 Get / Update 都必须带 deleted_at IS NULL。
	getDocumentStatement = `SELECT ` + documentColumns + `
								FROM documents
								WHERE id = ? AND deleted_at IS NULL`

	// FOR UPDATE 锁住目标行，保证"存在性判断 → 更新"之间不会被并发软删。
	getDocumentForUpdateStatement = getDocumentStatement + `
								FOR UPDATE`

	// 添加关联只读 collection、不修改它，所以用 FOR SHARE（共享锁）而不是 FOR UPDATE：
	// 同一知识库的多个 add 可以并发；软删 collection 的 UPDATE 需要排他锁，仍会等待
	// 这些共享锁释放，避免"确认存在 → 插入关联"之间 collection 被删掉的窗口。
	// 注意：collection_documents 没有外键，MySQL 不会自动替我们锁父行，这个共享锁必须显式加。
	lockCollectionStatement = `SELECT 1
								FROM collections
								WHERE id = ? AND deleted_at IS NULL
								FOR SHARE`

	collectionExistsStatement = `SELECT 1
								FROM collections
								WHERE id = ? AND deleted_at IS NULL`

	insertCollectionDocumentStatement = `INSERT INTO collection_documents (collection_id, document_id, created_at)
								VALUES (?, ?, ?)`

	removeCollectionDocumentStatement = `DELETE FROM collection_documents
								WHERE collection_id = ? AND document_id = ?`

	softDeleteDocumentStatement = `UPDATE documents
								SET updated_at = ?, deleted_at = ?
								WHERE id = ? AND deleted_at IS NULL`

	listDocumentsByCollectionWithoutCursor = `SELECT ` + documentColumnsForJoin + `, cd.created_at
								FROM collection_documents cd
								JOIN documents d ON d.id = cd.document_id
								WHERE cd.collection_id = ? AND d.deleted_at IS NULL
								ORDER BY cd.created_at DESC, cd.document_id DESC
								LIMIT ?`

	// 展开写 (created_at, document_id) 的 keyset 条件：
	// 行构造器 (a,b) < (?,?) 在 MySQL 8 会退化成全索引扫描（collections 已实测）。
	listDocumentsByCollectionWithCursor = `SELECT ` + documentColumnsForJoin + `, cd.created_at
								FROM collection_documents cd
								JOIN documents d ON d.id = cd.document_id
								WHERE cd.collection_id = ?
								  AND d.deleted_at IS NULL
								  AND (cd.created_at < ? OR (cd.created_at = ? AND cd.document_id < ?))
								ORDER BY cd.created_at DESC, cd.document_id DESC
								LIMIT ?`
)

// documentScanner 是 *sql.Row 与 *sql.Rows 的公共子集。
// 抽出来是为了让 Get / Update / Add / List 共用同一套 NULL 与 ErrNoRows 处理。
type documentScanner interface {
	Scan(dest ...any) error
}

// scanDocument 按 documentColumns 的顺序扫描一行。
//
// source_uri / mime_type 用 *string、size_bytes 用 *int64 接收：database/sql
// 对 NULL 会写入 nil，对非 NULL 会分配新值，正好对应领域模型的可空字段。
func scanDocument(row documentScanner) (*Document, error) {
	d := &Document{}
	if err := row.Scan(
		&d.ID,
		&d.Title,
		&d.SourceURI,
		&d.MimeType,
		&d.SizeBytes,
		&d.CreatedAt,
		&d.UpdatedAt,
		&d.DeletedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan document: %w", err)
	}
	return d, nil
}

// nullableString / nullableInt64 把可空指针显式转成 SQL 参数。
//
// 不直接传 *string 给驱动：虽然 database/sql 会替我们解引用，但显式转换让
// "nil = SQL NULL"这件事在 SQL 参数处就能看懂，也不依赖驱动的隐式行为。
func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// CreateDocument 实现 DocumentRepository。
func (r *MySQLRepository) CreateDocument(
	ctx context.Context,
	title string,
	sourceURI, mimeType *string,
	sizeBytes *int64,
) (*Document, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("new document uuid: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := r.db.ExecContext(ctx, createDocumentStatement,
		id[:], title, nullableString(sourceURI), nullableString(mimeType), nullableInt64(sizeBytes), now, now, nil); err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}

	return &Document{
		ID:        id,
		Title:     title,
		SourceURI: sourceURI,
		MimeType:  mimeType,
		SizeBytes: sizeBytes,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// GetDocument 实现 DocumentRepository。
func (r *MySQLRepository) GetDocument(ctx context.Context, id uuid.UUID) (*Document, error) {
	return scanDocument(r.db.QueryRowContext(ctx, getDocumentStatement, id[:]))
}

// UpdateDocument 实现 DocumentRepository。
//
// 动态拼 SET 而不是固定写四列：PATCH 的"没出现"不能被当成"清空"。
// 用事务 + SELECT ... FOR UPDATE 锁行，保证存在性判断、并发软删和更新之间没有窗口。
func (r *MySQLRepository) UpdateDocument(ctx context.Context, id uuid.UUID, patch DocumentUpdate) (*Document, error) {
	if patch.Title == nil && !patch.SourceURI.Set && !patch.MimeType.Set && !patch.SizeBytes.Set {
		// 空 PATCH 是 no-op：不写库、不刷新 updated_at。
		return r.GetDocument(ctx, id)
	}

	// 默认隔离级别（MySQL 是 REPEATABLE READ）足够：目标行已经被 FOR UPDATE 锁住，
	// 不需要 Serializable——那会把范围锁扩大到更多行，徒增死锁概率。
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update document: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	d, err := scanDocument(tx.QueryRowContext(ctx, getDocumentForUpdateStatement, id[:]))
	if err != nil {
		return nil, err
	}

	sets := make([]string, 0, 5)
	args := make([]any, 0, 6)

	if patch.Title != nil {
		d.Title = *patch.Title
		sets = append(sets, "title = ?")
		args = append(args, d.Title)
	}
	if patch.SourceURI.Set {
		d.SourceURI = patch.SourceURI.Value
		sets = append(sets, "source_uri = ?")
		args = append(args, nullableString(d.SourceURI))
	}
	if patch.MimeType.Set {
		d.MimeType = patch.MimeType.Value
		sets = append(sets, "mime_type = ?")
		args = append(args, nullableString(d.MimeType))
	}
	if patch.SizeBytes.Set {
		d.SizeBytes = patch.SizeBytes.Value
		sets = append(sets, "size_bytes = ?")
		args = append(args, nullableInt64(d.SizeBytes))
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	d.UpdatedAt = now
	sets = append(sets, "updated_at = ?")
	args = append(args, now)
	args = append(args, id[:])

	stmt := "UPDATE documents SET " + strings.Join(sets, ", ") + " WHERE id = ? AND deleted_at IS NULL"
	if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
		return nil, fmt.Errorf("update document id(%x): %w", id[:], err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update document: %w", err)
	}
	return d, nil
}

// SoftDeleteDocument 实现 DocumentRepository。
//
// 一条带 deleted_at IS NULL 的 UPDATE 同时完成"存在性判断 + 软删"；
// RowsAffected==0 表示不存在或已经软删，两者都映射成 ErrNotFound。
func (r *MySQLRepository) SoftDeleteDocument(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	res, err := r.db.ExecContext(ctx, softDeleteDocumentStatement, now, now, id[:])
	if err != nil {
		return fmt.Errorf("soft delete document id(%x): %w", id[:], err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("soft delete document rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("document id(%x) not found: %w", id[:], ErrNotFound)
	}
	return nil
}

// AddDocumentToCollection 实现 DocumentRepository。
//
// 加锁顺序固定为 collection → document：同一对 (collection, document) 的并发
// 添加会串行化，重复插入由联合主键兜底并转成 ErrConflict。
func (r *MySQLRepository) AddDocumentToCollection(
	ctx context.Context,
	collectionID, documentID uuid.UUID,
) (*CollectionDocument, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin add document to collection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var marker int
	if err := tx.QueryRowContext(ctx, lockCollectionStatement, collectionID[:]).Scan(&marker); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("collection id(%x) not found: %w", collectionID[:], ErrNotFound)
		}
		return nil, fmt.Errorf("lock collection id(%x): %w", collectionID[:], err)
	}

	d, err := scanDocument(tx.QueryRowContext(ctx, getDocumentForUpdateStatement, documentID[:]))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("document id(%x) not found: %w", documentID[:], ErrNotFound)
		}
		return nil, err
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := tx.ExecContext(ctx, insertCollectionDocumentStatement,
		collectionID[:], documentID[:], now); err != nil {
		var mysqlErr *mysqlDriver.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry {
			return nil, fmt.Errorf("document id(%x) already in collection id(%x): %w",
				documentID[:], collectionID[:], ErrConflict)
		}
		return nil, fmt.Errorf("insert collection document: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit add document to collection: %w", err)
	}
	return &CollectionDocument{Document: d, AddedAt: now}, nil
}

// RemoveDocumentFromCollection 实现 DocumentRepository。
//
// 只删关联行，不碰 documents：文档是全局资源，可能还属于其他 collection。
func (r *MySQLRepository) RemoveDocumentFromCollection(
	ctx context.Context,
	collectionID, documentID uuid.UUID,
) error {
	res, err := r.db.ExecContext(ctx, removeCollectionDocumentStatement, collectionID[:], documentID[:])
	if err != nil {
		return fmt.Errorf("remove document from collection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove document rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("association collection(%x) document(%x) not found: %w",
			collectionID[:], documentID[:], ErrNotFound)
	}
	return nil
}

// ListDocumentsByCollection 实现 DocumentRepository。
//
// 先单独确认 collection 存在：否则"知识库不存在"和"知识库为空"都会返回空页，
// 调用方无法区分 404 和 200。随后多取一条判断 next，最后截断到 limit。
func (r *MySQLRepository) ListDocumentsByCollection(
	ctx context.Context,
	collectionID uuid.UUID,
	cursor *Cursor,
	limit int,
) ([]*CollectionDocument, *Cursor, error) {
	if limit <= 0 {
		return nil, nil, fmt.Errorf("limit must be positive: %d", limit)
	}

	var marker int
	if err := r.db.QueryRowContext(ctx, collectionExistsStatement, collectionID[:]).Scan(&marker); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, fmt.Errorf("collection id(%x) not found: %w", collectionID[:], ErrNotFound)
		}
		return nil, nil, fmt.Errorf("check collection id(%x): %w", collectionID[:], err)
	}

	fetch := limit + 1
	var (
		rows *sql.Rows
		err  error
	)
	if cursor == nil {
		rows, err = r.db.QueryContext(ctx, listDocumentsByCollectionWithoutCursor, collectionID[:], fetch)
	} else {
		// cursor.CreatedAt 是 collection_documents.created_at（加入时间），
		// cursor.ID 是 document_id；传 ID[:] 而不是 uuid.UUID，避免被驱动转成 36 字符串。
		rows, err = r.db.QueryContext(ctx, listDocumentsByCollectionWithCursor,
			collectionID[:], cursor.CreatedAt, cursor.CreatedAt, cursor.ID[:], fetch)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list documents by collection: %w", err)
	}
	defer rows.Close()

	items := make([]*CollectionDocument, 0, fetch)
	for rows.Next() {
		d := &Document{}
		var addedAt time.Time
		if err := rows.Scan(
			&d.ID,
			&d.Title,
			&d.SourceURI,
			&d.MimeType,
			&d.SizeBytes,
			&d.CreatedAt,
			&d.UpdatedAt,
			&d.DeletedAt,
			&addedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan document in collection: %w", err)
		}
		items = append(items, &CollectionDocument{Document: d, AddedAt: addedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate documents by collection: %w", err)
	}

	if len(items) <= limit {
		return items, nil, nil
	}

	items = items[:limit]
	last := items[limit-1]
	return items, &Cursor{CreatedAt: last.AddedAt, ID: last.Document.ID}, nil
}
