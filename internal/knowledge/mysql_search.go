package knowledge

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// MySQLRepository 的 chunk 检索实现。
//
// 不把 SQL 放到 service：service 只负责校验、collection 存在性和分页策略；
// 这里只负责把固定查询映射成 []SearchHit。

var _ SearchRepository = (*MySQLRepository)(nil)

const (
	searchChunksStatement = `
		SELECT c.id,
		       c.document_id,
		       d.title,
		       c.chunk_index,
		       c.content,
		       MATCH(c.content) AGAINST(? IN NATURAL LANGUAGE MODE) AS score
		FROM document_chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE d.deleted_at IS NULL
		  AND MATCH(c.content) AGAINST(? IN NATURAL LANGUAGE MODE)
		ORDER BY score DESC, c.id DESC
		LIMIT ? OFFSET ?`

	// 指定 collection 时额外 JOIN collection_documents。
	// 再 JOIN collections 过滤软删，是为了堵住
	// “service 查完 collection → collection 被软删 → 搜索仍返回它”的窗口。
	searchChunksInCollectionStatement = `
		SELECT c.id,
		       c.document_id,
		       d.title,
		       c.chunk_index,
		       c.content,
		       MATCH(c.content) AGAINST(? IN NATURAL LANGUAGE MODE) AS score
		FROM document_chunks c
		JOIN documents d ON d.id = c.document_id
		JOIN collection_documents cd
		  ON cd.document_id = d.id AND cd.collection_id = ?
		JOIN collections col
		  ON col.id = cd.collection_id AND col.deleted_at IS NULL
		WHERE d.deleted_at IS NULL
		  AND MATCH(c.content) AGAINST(? IN NATURAL LANGUAGE MODE)
		ORDER BY score DESC, c.id DESC
		LIMIT ? OFFSET ?`
)

// Search 实现 SearchRepository。
//
// 取 limit+1 行：多出来的一行只用于判断 hasMore，然后截掉。
// 这样不需要额外 COUNT(*)，也不会把 count 的全表扫描带进搜索路径。
func (r *MySQLRepository) Search(
	ctx context.Context,
	query string,
	collectionID *uuid.UUID,
	limit int,
	offset int,
) ([]SearchHit, bool, error) {
	if limit <= 0 || offset < 0 {
		return nil, false, fmt.Errorf("search: invalid limit/offset: limit=%d offset=%d", limit, offset)
	}

	stmt := searchChunksStatement
	args := []any{query, query, limit + 1, offset}
	if collectionID != nil {
		stmt = searchChunksInCollectionStatement
		args = []any{query, collectionID[:], query, limit + 1, offset}
	}

	rows, err := r.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, false, fmt.Errorf("query search chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hits := make([]SearchHit, 0, limit)
	for rows.Next() {
		var hit SearchHit
		if err := rows.Scan(
			&hit.ChunkID,
			&hit.DocumentID,
			&hit.DocumentTitle,
			&hit.ChunkIndex,
			&hit.Content,
			&hit.Score,
		); err != nil {
			return nil, false, fmt.Errorf("scan search hit: %w", err)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate search hits: %w", err)
	}

	hasMore := len(hits) > limit
	if hasMore {
		hits = hits[:limit]
	}
	return hits, hasMore, nil
}
