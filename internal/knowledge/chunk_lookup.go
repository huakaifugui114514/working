package knowledge

import (
	"context"

	"github.com/google/uuid"
)

// ChunkView 是向量检索回表后需要的 chunk 元数据。
//
// Qdrant 只返回 chunk id + score；正文/标题仍以 MySQL 为准，避免向量库成为第二事实源。
type ChunkView struct {
	ChunkID       uuid.UUID
	DocumentID    uuid.UUID
	DocumentTitle string
	ChunkIndex    int
	Content       string
}

// ChunkLookup 按 chunk id 批量回表，并过滤软删 / collection。
//
// 返回顺序不保证与 ids 一致；调用方自己按命中顺序重排。
type ChunkLookup interface {
	GetChunksByIDs(
		ctx context.Context,
		ids []uuid.UUID,
		collectionID *uuid.UUID,
	) ([]ChunkView, error)
}
