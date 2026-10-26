package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/knowledge"
)

// KeywordRetriever 用 MySQL FULLTEXT 实现 Retriever。
//
// 它是当前 MVP 的检索器；以后换 VectorRetriever / HybridRetriever 时，
// AgentService 不需要改。
type KeywordRetriever struct {
	search *knowledge.SearchService
}

// NewKeywordRetriever 注入 SearchService。search 不能为 nil。
func NewKeywordRetriever(search *knowledge.SearchService) *KeywordRetriever {
	if search == nil {
		panic("agent.NewKeywordRetriever: search is nil")
	}
	return &KeywordRetriever{search: search}
}

// Retrieve 调 SearchService 并映射成 RetrievedChunk。
//
// 不在这里重复校验 q/topK：q 由 SearchService 校验，topK 由 AgentService 校验。
func (r *KeywordRetriever) Retrieve(
	ctx context.Context,
	query string,
	collectionID *uuid.UUID,
	topK int,
) ([]RetrievedChunk, error) {
	result, err := r.search.Search(ctx, query, collectionID, topK, 0)
	if err != nil {
		return nil, err
	}

	chunks := make([]RetrievedChunk, 0, len(result.Items))
	for _, hit := range result.Items {
		chunks = append(chunks, RetrievedChunk{
			ChunkID:       hit.ChunkID,
			DocumentID:    hit.DocumentID,
			DocumentTitle: hit.DocumentTitle,
			ChunkIndex:    hit.ChunkIndex,
			Content:       hit.Content,
			Score:         hit.Score,
		})
	}
	return chunks, nil
}
