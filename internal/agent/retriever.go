package agent

import (
	"context"

	"github.com/google/uuid"
)

// RetrievedChunk 是一条检索结果。
//
// 它是 Agent 与检索实现之间的值对象：
// 当前由 KeywordRetriever 从 knowledge.SearchHit 映射而来；
// 以后 VectorRetriever / HybridRetriever 也返回同样的结构。
type RetrievedChunk struct {
	ChunkID       uuid.UUID
	DocumentID    uuid.UUID
	DocumentTitle string
	ChunkIndex    int
	Content       string
	Score         float64
}

// Retriever 是 Agent 依赖的检索接口。
//
// 契约：
//   - query 由 AgentService trim 并校验后传入；
//   - collectionID == nil 表示全局检索；
//   - topK > 0，返回最多 topK 条；
//   - 没有结果返回空切片，不返回 ErrNotFound；
//   - collection 不存在/已软删的错误由实现返回，AgentService 负责翻译。
type Retriever interface {
	Retrieve(
		ctx context.Context,
		query string,
		collectionID *uuid.UUID,
		topK int,
	) ([]RetrievedChunk, error)
}
