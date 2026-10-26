package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

const (
	defaultHybridCandidates = 20
	defaultRRFK             = 60
)

// HybridRetriever 融合 FTS 和向量两路结果。
//
// RRF（Reciprocal Rank Fusion）只看排名，不依赖两路分数量纲：
//
//	score(d) = Σ 1 / (k + rank_i(d))
//
// 两路都命中会累加，天然偏向“关键词和语义都认为相关”的 chunk。
type HybridRetriever struct {
	fts        Retriever
	vector     Retriever
	candidates int
	rrfK       int
}

// NewHybridRetriever 注入两路检索器。依赖不能为 nil。
func NewHybridRetriever(fts, vector Retriever, candidates, rrfK int) *HybridRetriever {
	if fts == nil {
		panic("agent.NewHybridRetriever: fts is nil")
	}
	if vector == nil {
		panic("agent.NewHybridRetriever: vector is nil")
	}
	if candidates <= 0 {
		candidates = defaultHybridCandidates
	}
	if rrfK <= 0 {
		rrfK = defaultRRFK
	}
	return &HybridRetriever{fts: fts, vector: vector, candidates: candidates, rrfK: rrfK}
}

// Retrieve 顺序调用两路检索，RRF 融合后返回 topK。
//
// 单路失败时降级到另一路；两路都失败才返回 error。
func (h *HybridRetriever) Retrieve(
	ctx context.Context,
	query string,
	collectionID *uuid.UUID,
	topK int,
) ([]RetrievedChunk, error) {
	if topK < 1 {
		return nil, fmt.Errorf("agent: topK 必须为正数")
	}
	n := h.candidates
	if n < topK {
		n = topK
	}

	ftsChunks, ftsErr := h.fts.Retrieve(ctx, query, collectionID, n)
	vectorChunks, vectorErr := h.vector.Retrieve(ctx, query, collectionID, n)

	if ftsErr != nil && vectorErr != nil {
		return nil, fmt.Errorf("agent: hybrid 两路检索都失败: %w", errors.Join(ftsErr, vectorErr))
	}
	if ftsErr != nil {
		ftsChunks = nil
	}
	if vectorErr != nil {
		vectorChunks = nil
	}
	if len(ftsChunks) == 0 && len(vectorChunks) == 0 {
		return []RetrievedChunk{}, nil
	}

	type fused struct {
		chunk RetrievedChunk
		score float64
	}
	byID := make(map[uuid.UUID]*fused, len(ftsChunks)+len(vectorChunks))
	add := func(chunks []RetrievedChunk) {
		for rank, chunk := range chunks {
			f, ok := byID[chunk.ChunkID]
			if !ok {
				f = &fused{chunk: chunk}
				byID[chunk.ChunkID] = f
			}
			f.score += 1.0 / float64(h.rrfK+rank+1)
		}
	}
	add(ftsChunks)
	add(vectorChunks)

	out := make([]fused, 0, len(byID))
	for _, item := range byID {
		item.chunk.Score = item.score
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		// 分数相同时按 UUID 固定顺序，避免 map 遍历导致 citations 抖动。
		return out[i].chunk.ChunkID.String() < out[j].chunk.ChunkID.String()
	})

	if len(out) > topK {
		out = out[:topK]
	}
	chunks := make([]RetrievedChunk, 0, len(out))
	for _, item := range out {
		chunks = append(chunks, item.chunk)
	}
	return chunks, nil
}
