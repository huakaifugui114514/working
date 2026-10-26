package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/knowledge"
)

type fakeKnowledgeSearchRepository struct {
	search func(context.Context, string, *uuid.UUID, int, int) ([]knowledge.SearchHit, bool, error)
}

var _ knowledge.SearchRepository = (*fakeKnowledgeSearchRepository)(nil)

func (f *fakeKnowledgeSearchRepository) Search(
	ctx context.Context,
	query string,
	collectionID *uuid.UUID,
	limit int,
	offset int,
) ([]knowledge.SearchHit, bool, error) {
	if f.search == nil {
		panic("unexpected Search call")
	}
	return f.search(ctx, query, collectionID, limit, offset)
}

func TestNewKeywordRetrieverRejectsNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewKeywordRetriever(nil) 应当 panic")
		}
	}()
	NewKeywordRetriever(nil)
}

func TestKeywordRetrieverRetrieve(t *testing.T) {
	chunkID := uuid.New()
	documentID := uuid.New()
	collectionID := uuid.New()

	repo := &fakeKnowledgeSearchRepository{
		search: func(_ context.Context, query string, cid *uuid.UUID, limit, offset int) ([]knowledge.SearchHit, bool, error) {
			if query != "知识库" {
				t.Fatalf("query = %q", query)
			}
			if cid == nil || *cid != collectionID {
				t.Fatalf("collection = %v, want %v", cid, collectionID)
			}
			if limit != 5 || offset != 0 {
				t.Fatalf("limit/offset = %d/%d, want 5/0", limit, offset)
			}
			return []knowledge.SearchHit{{
				ChunkID:       chunkID,
				DocumentID:    documentID,
				DocumentTitle: "文档",
				ChunkIndex:    2,
				Content:       "正文",
				Score:         0.5,
			}}, true, nil
		},
	}
	lookup := &fakeCollectionLookup{
		get: func(context.Context, uuid.UUID) (*knowledge.Collection, error) {
			return &knowledge.Collection{ID: collectionID}, nil
		},
	}
	searchService := knowledge.NewSearchService(repo, lookup)
	retriever := NewKeywordRetriever(searchService)

	chunks, err := retriever.Retrieve(context.Background(), "知识库", &collectionID, 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	got := chunks[0]
	if got.ChunkID != chunkID || got.DocumentID != documentID ||
		got.DocumentTitle != "文档" || got.ChunkIndex != 2 ||
		got.Content != "正文" || got.Score != 0.5 {
		t.Fatalf("chunk = %+v", got)
	}
}

func TestKeywordRetrieverPassesError(t *testing.T) {
	baseErr := errors.New("search failed")
	repo := &fakeKnowledgeSearchRepository{
		search: func(context.Context, string, *uuid.UUID, int, int) ([]knowledge.SearchHit, bool, error) {
			return nil, false, baseErr
		},
	}
	searchService := knowledge.NewSearchService(repo, &fakeCollectionLookup{})
	retriever := NewKeywordRetriever(searchService)

	_, err := retriever.Retrieve(context.Background(), "知识库", nil, 5)
	if !errors.Is(err, baseErr) {
		t.Fatalf("error = %v, want %v", err, baseErr)
	}
}
