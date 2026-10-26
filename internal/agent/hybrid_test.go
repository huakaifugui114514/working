package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestHybridRetrieverRRF(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	fts := &fakeRetriever{retrieve: func(_ context.Context, _ string, _ *uuid.UUID, topK int) ([]RetrievedChunk, error) {
		if topK != 20 {
			t.Fatalf("fts topK = %d, want 20", topK)
		}
		return []RetrievedChunk{
			{ChunkID: a, Content: "a"},
			{ChunkID: b, Content: "b"},
		}, nil
	}}
	vector := &fakeRetriever{retrieve: func(_ context.Context, _ string, _ *uuid.UUID, topK int) ([]RetrievedChunk, error) {
		if topK != 20 {
			t.Fatalf("vector topK = %d, want 20", topK)
		}
		return []RetrievedChunk{
			{ChunkID: b, Content: "b2"},
			{ChunkID: c, Content: "c"},
		}, nil
	}}
	h := NewHybridRetriever(fts, vector, 20, 60)

	chunks, err := h.Retrieve(context.Background(), "q", nil, 2)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	// b 两路都命中，排第一；a 在 FTS 第 1，c 在 vector 第 2。
	if chunks[0].ChunkID != b || chunks[1].ChunkID != a {
		t.Fatalf("order = %v, %v; want b, a", chunks[0].ChunkID, chunks[1].ChunkID)
	}
	if chunks[0].Content != "b" {
		t.Fatalf("重复 chunk 应保留第一次出现的元数据, got %q", chunks[0].Content)
	}
	if chunks[0].Score <= chunks[1].Score {
		t.Fatalf("RRF score 应降序: %+v", chunks)
	}
}

func TestHybridRetrieverFallback(t *testing.T) {
	chunk := uuid.New()
	ok := &fakeRetriever{retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
		return []RetrievedChunk{{ChunkID: chunk, Content: "ok"}}, nil
	}}
	baseErr := errors.New("down")
	fail := &fakeRetriever{retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
		return nil, baseErr
	}}

	t.Run("vector 失败用 FTS", func(t *testing.T) {
		h := NewHybridRetriever(ok, fail, 20, 60)
		chunks, err := h.Retrieve(context.Background(), "q", nil, 5)
		if err != nil || len(chunks) != 1 || chunks[0].ChunkID != chunk {
			t.Fatalf("chunks=%v err=%v", chunks, err)
		}
	})

	t.Run("FTS 失败用 vector", func(t *testing.T) {
		h := NewHybridRetriever(fail, ok, 20, 60)
		chunks, err := h.Retrieve(context.Background(), "q", nil, 5)
		if err != nil || len(chunks) != 1 || chunks[0].ChunkID != chunk {
			t.Fatalf("chunks=%v err=%v", chunks, err)
		}
	})

	t.Run("两路都失败", func(t *testing.T) {
		h := NewHybridRetriever(fail, fail, 20, 60)
		_, err := h.Retrieve(context.Background(), "q", nil, 5)
		if !errors.Is(err, baseErr) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNewHybridRetrieverRejectsNil(t *testing.T) {
	retriever := &fakeRetriever{}
	tests := []struct {
		name   string
		fts    Retriever
		vector Retriever
	}{
		{name: "fts nil", vector: retriever},
		{name: "vector nil", fts: retriever},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewHybridRetriever 应当 panic")
				}
			}()
			NewHybridRetriever(tt.fts, tt.vector, 20, 60)
		})
	}
}
