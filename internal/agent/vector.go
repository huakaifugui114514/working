package agent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/embedding"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/qdrant"
)

// VectorOverFetch 是向量检索的放大倍数。
//
// Qdrant 返回的是 chunk id；回表时还要过滤软删/collection，可能丢一些候选，
// 所以先多取，再截断到 topK。
const VectorOverFetch = 4

// VectorSearcher 是 VectorRetriever 依赖的向量检索接口。
type VectorSearcher interface {
	Search(ctx context.Context, vector []float32, topK int) ([]qdrant.SearchHit, error)
}

// VectorRetriever 用 embedding + Qdrant + MySQL 回表实现 Retriever。
type VectorRetriever struct {
	embedder embedding.Embedder
	searcher VectorSearcher
	chunks   knowledge.ChunkLookup
}

// NewVectorRetriever 注入 embedding、Qdrant 和 MySQL 回表。依赖不能为 nil。
func NewVectorRetriever(
	embedder embedding.Embedder,
	searcher VectorSearcher,
	chunks knowledge.ChunkLookup,
) *VectorRetriever {
	if embedder == nil {
		panic("agent.NewVectorRetriever: embedder is nil")
	}
	if searcher == nil {
		panic("agent.NewVectorRetriever: searcher is nil")
	}
	if chunks == nil {
		panic("agent.NewVectorRetriever: chunks is nil")
	}
	return &VectorRetriever{embedder: embedder, searcher: searcher, chunks: chunks}
}

// Retrieve 执行 query embedding → Qdrant → MySQL 回表。
//
// Qdrant 的 score 不一定是 [0,1]，原样放到 RetrievedChunk.Score；
// 排序以 Qdrant 返回顺序为准，回表只负责补正文/标题和过滤。
func (r *VectorRetriever) Retrieve(
	ctx context.Context,
	query string,
	collectionID *uuid.UUID,
	topK int,
) ([]RetrievedChunk, error) {
	if topK < 1 {
		return nil, fmt.Errorf("agent: topK 必须为正数")
	}

	vectors, err := r.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("agent: query embedding 失败: %w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, fmt.Errorf("agent: query embedding 返回 %d 个向量", len(vectors))
	}

	limit := topK * VectorOverFetch
	if limit > 200 {
		limit = 200
	}
	hits, err := r.searcher.Search(ctx, vectors[0], limit)
	if err != nil {
		return nil, fmt.Errorf("agent: Qdrant 检索失败: %w", err)
	}
	if len(hits) == 0 {
		return []RetrievedChunk{}, nil
	}

	ids := make([]uuid.UUID, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.ChunkID)
	}
	views, err := r.chunks.GetChunksByIDs(ctx, ids, collectionID)
	if err != nil {
		return nil, fmt.Errorf("agent: 向量结果回表失败: %w", err)
	}
	byID := make(map[uuid.UUID]knowledge.ChunkView, len(views))
	for _, view := range views {
		byID[view.ChunkID] = view
	}

	chunks := make([]RetrievedChunk, 0, topK)
	seen := make(map[uuid.UUID]bool, len(hits))
	for _, hit := range hits {
		if seen[hit.ChunkID] {
			continue
		}
		view, ok := byID[hit.ChunkID]
		if !ok {
			continue
		}
		seen[hit.ChunkID] = true
		chunks = append(chunks, RetrievedChunk{
			ChunkID:       view.ChunkID,
			DocumentID:    view.DocumentID,
			DocumentTitle: view.DocumentTitle,
			ChunkIndex:    view.ChunkIndex,
			Content:       view.Content,
			Score:         hit.Score,
		})
		if len(chunks) == topK {
			break
		}
	}
	return chunks, nil
}
