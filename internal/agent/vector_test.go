package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/embedding"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/qdrant"
)

type fakeEmbedder struct {
	embed func(context.Context, []string) ([][]float32, error)
}

var _ embedding.Embedder = (*fakeEmbedder)(nil)

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if f.embed == nil {
		panic("unexpected Embed call")
	}
	return f.embed(ctx, texts)
}

type fakeVectorSearcher struct {
	search func(context.Context, []float32, int) ([]qdrant.SearchHit, error)
}

var _ VectorSearcher = (*fakeVectorSearcher)(nil)

func (f *fakeVectorSearcher) Search(ctx context.Context, vector []float32, topK int) ([]qdrant.SearchHit, error) {
	if f.search == nil {
		panic("unexpected Search call")
	}
	return f.search(ctx, vector, topK)
}

type fakeChunkLookup struct {
	get func(context.Context, []uuid.UUID, *uuid.UUID) ([]knowledge.ChunkView, error)
}

var _ knowledge.ChunkLookup = (*fakeChunkLookup)(nil)

func (f *fakeChunkLookup) GetChunksByIDs(ctx context.Context, ids []uuid.UUID, collectionID *uuid.UUID) ([]knowledge.ChunkView, error) {
	if f.get == nil {
		panic("unexpected GetChunksByIDs call")
	}
	return f.get(ctx, ids, collectionID)
}

func TestVectorRetrieverSuccess(t *testing.T) {
	collectionID := uuid.New()
	chunk1, chunk2, filtered := uuid.New(), uuid.New(), uuid.New()
	doc1, doc2 := uuid.New(), uuid.New()

	embedder := &fakeEmbedder{embed: func(_ context.Context, texts []string) ([][]float32, error) {
		if len(texts) != 1 || texts[0] != "问题" {
			t.Fatalf("texts = %v", texts)
		}
		return [][]float32{{0.1, 0.2, 0.3}}, nil
	}}
	searcher := &fakeVectorSearcher{search: func(_ context.Context, vector []float32, topK int) ([]qdrant.SearchHit, error) {
		if len(vector) != 3 || topK != 20 {
			t.Fatalf("vector/limit = %v/%d", vector, topK)
		}
		return []qdrant.SearchHit{
			{ChunkID: chunk1, DocumentID: doc1, Score: 0.9},
			{ChunkID: filtered, DocumentID: uuid.New(), Score: 0.8},
			{ChunkID: chunk2, DocumentID: doc2, Score: 0.7},
		}, nil
	}}
	lookup := &fakeChunkLookup{get: func(_ context.Context, ids []uuid.UUID, cid *uuid.UUID) ([]knowledge.ChunkView, error) {
		if len(ids) != 3 || cid == nil || *cid != collectionID {
			t.Fatalf("ids/collection = %v/%v", ids, cid)
		}
		return []knowledge.ChunkView{
			{ChunkID: chunk1, DocumentID: doc1, DocumentTitle: "文档一", ChunkIndex: 0, Content: "第一段"},
			{ChunkID: chunk2, DocumentID: doc2, DocumentTitle: "文档二", ChunkIndex: 1, Content: "第二段"},
		}, nil
	}}

	retriever := NewVectorRetriever(embedder, searcher, lookup)
	chunks, err := retriever.Retrieve(context.Background(), "问题", &collectionID, 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if chunks[0].ChunkID != chunk1 || chunks[0].Score != 0.9 || chunks[0].Content != "第一段" {
		t.Fatalf("chunk[0] = %+v", chunks[0])
	}
	if chunks[1].ChunkID != chunk2 || chunks[1].ChunkIndex != 1 {
		t.Fatalf("chunk[1] = %+v", chunks[1])
	}
}

func TestVectorRetrieverEmptyAndErrors(t *testing.T) {
	embedder := &fakeEmbedder{embed: func(context.Context, []string) ([][]float32, error) {
		return [][]float32{{1}}, nil
	}}
	searcherEmpty := &fakeVectorSearcher{search: func(context.Context, []float32, int) ([]qdrant.SearchHit, error) {
		return nil, nil
	}}
	lookup := &fakeChunkLookup{get: func(context.Context, []uuid.UUID, *uuid.UUID) ([]knowledge.ChunkView, error) {
		t.Fatal("空 hits 不应回表")
		return nil, nil
	}}
	retriever := NewVectorRetriever(embedder, searcherEmpty, lookup)
	chunks, err := retriever.Retrieve(context.Background(), "q", nil, 5)
	if err != nil || len(chunks) != 0 {
		t.Fatalf("empty = %v, %v", chunks, err)
	}

	t.Run("embed error", func(t *testing.T) {
		baseErr := errors.New("ollama down")
		r := NewVectorRetriever(&fakeEmbedder{embed: func(context.Context, []string) ([][]float32, error) {
			return nil, baseErr
		}}, searcherEmpty, lookup)
		if _, err := r.Retrieve(context.Background(), "q", nil, 5); !errors.Is(err, baseErr) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("search error", func(t *testing.T) {
		baseErr := errors.New("qdrant down")
		r := NewVectorRetriever(embedder, &fakeVectorSearcher{search: func(context.Context, []float32, int) ([]qdrant.SearchHit, error) {
			return nil, baseErr
		}}, lookup)
		if _, err := r.Retrieve(context.Background(), "q", nil, 5); !errors.Is(err, baseErr) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("lookup error", func(t *testing.T) {
		baseErr := errors.New("db down")
		searcher := &fakeVectorSearcher{search: func(context.Context, []float32, int) ([]qdrant.SearchHit, error) {
			return []qdrant.SearchHit{{ChunkID: uuid.New()}}, nil
		}}
		r := NewVectorRetriever(embedder, searcher, &fakeChunkLookup{get: func(context.Context, []uuid.UUID, *uuid.UUID) ([]knowledge.ChunkView, error) {
			return nil, baseErr
		}})
		if _, err := r.Retrieve(context.Background(), "q", nil, 5); !errors.Is(err, baseErr) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestVectorRetrieverRejectsNilDependencies(t *testing.T) {
	embedder := &fakeEmbedder{}
	searcher := &fakeVectorSearcher{}
	lookup := &fakeChunkLookup{}
	tests := []struct {
		name     string
		embedder embedding.Embedder
		searcher VectorSearcher
		lookup   knowledge.ChunkLookup
	}{
		{name: "embedder nil", searcher: searcher, lookup: lookup},
		{name: "searcher nil", embedder: embedder, lookup: lookup},
		{name: "lookup nil", embedder: embedder, searcher: searcher},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewVectorRetriever 应当 panic")
				}
			}()
			NewVectorRetriever(tt.embedder, tt.searcher, tt.lookup)
		})
	}
}
