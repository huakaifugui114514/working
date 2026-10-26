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

// MySQLRepository 的正文/分块实现放在这里，和 documents 的元数据实现分开。
// 两者共享同一个 dbExecutor，但接口分开，service 的单测可以只 fake ContentRepository。
var _ ContentRepository = (*MySQLRepository)(nil)

const (
	// chunkInsertBatchSize 是单条 INSERT 的行数上限。
	// 250 行 × 6 列 = 1500 个占位符，远低于 MySQL 的 65535 上限；
	// 单条 SQL 的体积也保持在几百 KB，避免 max_allowed_packet 和长事务。
	chunkInsertBatchSize = 250

	deleteDocumentChunksFromDocumentID = `DELETE FROM document_chunks WHERE document_id=?`

	upsertDocumentContent = `INSERT INTO document_contents
								(document_id, content, content_hash, chunker_version, created_at, updated_at)
								VALUES (?, ?, ?, ?, ?, ?) AS new
								ON DUPLICATE KEY UPDATE
								content         = new.content,
								content_hash    = new.content_hash,
								chunker_version = new.chunker_version,
								updated_at      = new.updated_at`

	// upsertEmbeddingJob 是正文事务里的 outbox 写入。
	// 连续替换会重置为 pending，并让旧 worker 的结果因 content_hash 不匹配而作废。
	upsertEmbeddingJob = `INSERT INTO embedding_jobs
							(document_id, content_hash, chunker_version, status, attempts,
							 available_at, created_at, updated_at)
							VALUES (?, ?, ?, 'pending', 0, ?, ?, ?) AS new
							ON DUPLICATE KEY UPDATE
							content_hash    = new.content_hash,
							chunker_version = new.chunker_version,
							status          = 'pending',
							attempts        = 0,
							available_at    = new.available_at,
							locked_at       = NULL,
							locked_by       = NULL,
							last_error      = NULL,
							updated_at      = new.updated_at`

	// insertDocumentEvent 把“正文已替换”写入 Kafka outbox，和正文/chunks/job 同一事务。
	insertDocumentEvent = `INSERT INTO document_events
							(id, event_type, document_id, payload, status, attempts, available_at, created_at)
							VALUES (?, 'document.content.replaced', ?, JSON_OBJECT('content_hash', ?), 'pending', 0, ?, ?)`

	// getDocumentContentStatement 必须 JOIN documents 过滤软删：
	// 文档软删后正文行还在（restore 要能原样恢复），但读路径不能返回它。
	getDocumentContentStatement = `SELECT c.content, c.content_hash, c.chunker_version, c.created_at, c.updated_at
								FROM document_contents c
								JOIN documents d ON d.id = c.document_id
								WHERE c.document_id = ? AND d.deleted_at IS NULL`
)

// ReplaceDocumentContent 实现 ContentRepository。
//
// 整个替换是一个事务：
//  1. SELECT ... FOR UPDATE 锁 documents 行，确认文档存在且未软删
//  2. DELETE 旧 chunks
//  3. upsert document_contents
//  4. 分批 INSERT 新 chunks
//  5. Commit
//
// 先锁 documents 是为了和软删、restore、purge 串行化：
// 不锁就删 chunks，会出现"检查时文档还在 → 并发软删 → 仍然写入"的窗口；
// 锁顺序统一为父行 → 子表，purge 也必须遵守，避免死锁。
func (r *MySQLRepository) ReplaceDocumentContent(
	ctx context.Context,
	documentID uuid.UUID,
	content string,
	hash [32]byte,
	chunks []ContentChunk,
) error {
	if len(chunks) == 0 {
		// service 的 ChunkContent 对非空正文至少返回一块；这里防编程错误，
		// 避免写出“有正文但没有 chunk”的不可检索状态。
		return fmt.Errorf("replace document content id(%x): chunks 为空", documentID[:])
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace document content id(%x): %w", documentID[:], err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := scanDocument(tx.QueryRowContext(ctx, getDocumentForUpdateStatement, documentID[:])); err != nil {
		return err
	}

	// 先删旧 chunk：uk_document_chunk(document_id, chunk_index) 不允许
	// 新旧两代同一个 index 同时存在。同一事务内 DELETE 对自己可见，
	// 所以后面的 INSERT 不会撞唯一键（不会 1062）。
	if _, err := tx.ExecContext(ctx, deleteDocumentChunksFromDocumentID, documentID[:]); err != nil {
		return fmt.Errorf("delete document_chunks id(%x): %w", documentID[:], err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)

	// document_contents 是一篇文档一行，用 upsert 而不是删了再插：
	// 保留 created_at，也避免把 1 MiB 正文写两遍。
	// hash[:] 显式转成 []byte，BINARY(32) 不依赖驱动的隐式类型转换。
	//
	// 不检查 RowsAffected：ODKU 的返回值是 1=插入、2=更新、0=值没变化，
	// 0 不是失败；真正的错误看 err。
	//
	// chunker_version 目前取包常量 ContentChunkerVersion：service 和 repository
	// 在同一个二进制里发布，切块算法和版本常量一起变更。以后要支持多版本/灰度，
	// 再把它显式放进参数或 ChunkSet。
	if _, err := tx.ExecContext(ctx, upsertDocumentContent,
		documentID[:], content, hash[:], ContentChunkerVersion, now, now); err != nil {
		return fmt.Errorf("upsert document_contents id(%x): %w", documentID[:], err)
	}

	for start := 0; start < len(chunks); start += chunkInsertBatchSize {
		end := start + chunkInsertBatchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		if err := insertDocumentChunkBatch(ctx, tx, documentID, chunks[start:end], now); err != nil {
			return err
		}
	}

	// 和正文/chunks 同一个事务：正文提交成功，任务一定存在；回滚则一起消失。
	if _, err := tx.ExecContext(ctx, upsertEmbeddingJob,
		documentID[:], hash[:], ContentChunkerVersion,
		now, now, now); err != nil {
		return fmt.Errorf("upsert embedding_jobs id(%x): %w", documentID[:], err)
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("生成 document_events id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, insertDocumentEvent,
		eventID[:], documentID[:], fmt.Sprintf("%x", hash), now, now); err != nil {
		return fmt.Errorf("insert document_events id(%x): %w", documentID[:], err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace document content id(%x): %w", documentID[:], err)
	}
	return nil
}

// insertDocumentChunkBatch 把一批 chunk 用一条 multi-row INSERT 写入。
//
// 不逐行 Exec：2000 个 chunk 会产生 2000 次网络往返，事务和锁会被拖得很长。
// 每条 SQL 的占位符数量由 chunkInsertBatchSize 控制，不会触发 MySQL 的
// 65535 个占位符上限。
func insertDocumentChunkBatch(
	ctx context.Context,
	tx *sql.Tx,
	documentID uuid.UUID,
	chunks []ContentChunk,
	now time.Time,
) error {
	if len(chunks) == 0 {
		return nil
	}

	var query strings.Builder
	query.Grow(len(chunks)*32 + 128)
	query.WriteString(`INSERT INTO document_chunks
		(id, document_id, chunk_index, content, char_count, created_at)
		VALUES `)

	args := make([]any, 0, len(chunks)*6)
	for i, chunk := range chunks {
		if i > 0 {
			query.WriteString(", ")
		}
		query.WriteString("(?, ?, ?, ?, ?, ?)")

		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid.NewV7 for document_chunks id(%x): %w", documentID[:], err)
		}

		// args 会一直持有到 Exec；把 id 复制成独立的 []byte，
		// 不依赖循环变量 id 的底层数组在下一次迭代仍然有效。
		idBytes := make([]byte, len(id))
		copy(idBytes, id[:])

		args = append(args,
			idBytes,
			documentID[:],
			chunk.Index,
			chunk.Content,
			chunk.CharCount,
			now,
		)
	}

	if _, err := tx.ExecContext(ctx, query.String(), args...); err != nil {
		return fmt.Errorf("insert document_chunks id(%x): %w", documentID[:], err)
	}
	return nil
}

const listDocumentChunksStatement = `SELECT c.id, c.chunk_index, c.content, c.char_count
	FROM document_chunks c
	JOIN documents d ON d.id = c.document_id
	WHERE c.document_id = ? AND d.deleted_at IS NULL
	ORDER BY c.chunk_index ASC`

// ListDocumentChunks 实现 ContentRepository。
func (r *MySQLRepository) ListDocumentChunks(
	ctx context.Context,
	documentID uuid.UUID,
) ([]StoredChunk, error) {
	rows, err := r.db.QueryContext(ctx, listDocumentChunksStatement, documentID[:])
	if err != nil {
		return nil, fmt.Errorf("list document_chunks id(%x): %w", documentID[:], err)
	}
	defer func() { _ = rows.Close() }()

	var chunks []StoredChunk
	for rows.Next() {
		var (
			id        []byte
			index     int
			content   string
			charCount int
		)
		if err := rows.Scan(&id, &index, &content, &charCount); err != nil {
			return nil, fmt.Errorf("scan document_chunks id(%x): %w", documentID[:], err)
		}
		chunkID, err := uuid.FromBytes(id)
		if err != nil {
			return nil, fmt.Errorf("parse chunk id(%x): %w", id, err)
		}
		chunks = append(chunks, StoredChunk{
			ID:        chunkID,
			Index:     index,
			Content:   content,
			CharCount: charCount,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate document_chunks id(%x): %w", documentID[:], err)
	}
	return chunks, nil
}

// GetDocumentContent 实现 ContentRepository。
//
// content_hash 是 BINARY(32)，先扫到 []byte 再校验长度、copy 到 [32]byte；
// 不直接扫 [32]byte，避免依赖 database/sql 对数组类型的隐式转换。
func (r *MySQLRepository) GetDocumentContent(ctx context.Context, documentID uuid.UUID) (*DocumentContent, error) {
	var (
		content   string
		hashBytes []byte
		version   int
		createdAt time.Time
		updatedAt time.Time
	)

	err := r.db.QueryRowContext(ctx, getDocumentContentStatement, documentID[:]).Scan(
		&content,
		&hashBytes,
		&version,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get document content id(%x): %w", documentID[:], err)
	}

	var hash [32]byte
	if len(hashBytes) != len(hash) {
		return nil, fmt.Errorf(
			"get document content id(%x): content_hash length = %d, want %d",
			documentID[:], len(hashBytes), len(hash),
		)
	}
	copy(hash[:], hashBytes)

	return &DocumentContent{
		DocumentID:     documentID,
		Content:        content,
		ContentHash:    hash,
		ChunkerVersion: version,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}, nil
}

// GetChunksByIDs 按 chunk id 批量回表。
//
// 用 EXISTS 而不是 JOIN collection_documents：同一文档可能有多条关联，
// JOIN 会返回重复 chunk；EXISTS 只判断“是否属于该 collection”。
func (r *MySQLRepository) GetChunksByIDs(
	ctx context.Context,
	ids []uuid.UUID,
	collectionID *uuid.UUID,
) ([]ChunkView, error) {
	if len(ids) == 0 {
		return []ChunkView{}, nil
	}

	var query strings.Builder
	query.WriteString(`SELECT c.id, c.document_id, d.title, c.chunk_index, c.content
		FROM document_chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE c.id IN (`)
	args := make([]any, 0, len(ids)+1)
	for i, id := range ids {
		if i > 0 {
			query.WriteString(", ")
		}
		query.WriteString("?")
		args = append(args, id[:])
	}
	query.WriteString(`) AND d.deleted_at IS NULL`)
	if collectionID != nil {
		query.WriteString(` AND EXISTS (
			SELECT 1 FROM collection_documents cd
			WHERE cd.document_id = d.id AND cd.collection_id = ?
		)`)
		args = append(args, collectionID[:])
	}

	rows, err := r.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("get chunks by ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	views := make([]ChunkView, 0, len(ids))
	for rows.Next() {
		var (
			chunkID  []byte
			document []byte
			title    string
			index    int
			content  string
		)
		if err := rows.Scan(&chunkID, &document, &title, &index, &content); err != nil {
			return nil, fmt.Errorf("scan chunk view: %w", err)
		}
		cid, err := uuid.FromBytes(chunkID)
		if err != nil {
			return nil, fmt.Errorf("parse chunk id(%x): %w", chunkID, err)
		}
		did, err := uuid.FromBytes(document)
		if err != nil {
			return nil, fmt.Errorf("parse document id(%x): %w", document, err)
		}
		views = append(views, ChunkView{
			ChunkID:       cid,
			DocumentID:    did,
			DocumentTitle: title,
			ChunkIndex:    index,
			Content:       content,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunk views: %w", err)
	}
	return views, nil
}
