package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
)

type fakeSearchRepository struct {
	search func(context.Context, string, *uuid.UUID, int, int) ([]SearchHit, bool, error)
}

var _ SearchRepository = (*fakeSearchRepository)(nil)

func (f *fakeSearchRepository) Search(
	ctx context.Context,
	query string,
	collectionID *uuid.UUID,
	limit int,
	offset int,
) ([]SearchHit, bool, error) {
	if f.search == nil {
		panic("unexpected Search call")
	}
	return f.search(ctx, query, collectionID, limit, offset)
}

type fakeCollectionLookup struct {
	get func(context.Context, uuid.UUID) (*Collection, error)
}

var _ CollectionLookup = (*fakeCollectionLookup)(nil)

func (f *fakeCollectionLookup) GetCollection(ctx context.Context, id uuid.UUID) (*Collection, error) {
	if f.get == nil {
		panic("unexpected GetCollection call")
	}
	return f.get(ctx, id)
}

func TestNewSearchServiceRejectsNil(t *testing.T) {
	tests := []struct {
		name        string
		search      SearchRepository
		collections CollectionLookup
	}{
		{name: "search nil", search: nil, collections: &fakeCollectionLookup{}},
		{name: "collections nil", search: &fakeSearchRepository{}, collections: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewSearchService 应当 panic")
				}
			}()
			NewSearchService(tt.search, tt.collections)
		})
	}
}

func TestSearchServiceValidation(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		limit    int
		offset   int
		wantCode string
	}{
		{name: "q 为空", query: "", limit: 20, offset: 0, wantCode: apperr.CodeInvalidArgument},
		{name: "q 纯空白", query: " \t\n", limit: 20, offset: 0, wantCode: apperr.CodeInvalidArgument},
		{name: "q 只有 1 rune", query: "检", limit: 20, offset: 0, wantCode: apperr.CodeInvalidArgument},
		{
			name:     "q 超过 256 rune",
			query:    strings.Repeat("字", MaxSearchQueryRunes+1),
			limit:    20,
			offset:   0,
			wantCode: apperr.CodeInvalidArgument,
		},
		{name: "limit 为 0", query: "知识", limit: 0, offset: 0, wantCode: apperr.CodeInvalidArgument},
		{name: "limit 超过 50", query: "知识", limit: MaxSearchLimit + 1, offset: 0, wantCode: apperr.CodeInvalidArgument},
		{name: "offset 为负", query: "知识", limit: 20, offset: -1, wantCode: apperr.CodeInvalidArgument},
		{
			name:     "offset+limit 超过窗口",
			query:    "知识",
			limit:    20,
			offset:   MaxSearchWindow - 19,
			wantCode: apperr.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 两个 fake 的函数字段都留 nil：校验失败时不能碰 repository / lookup。
			svc := NewSearchService(&fakeSearchRepository{}, &fakeCollectionLookup{})
			_, err := svc.Search(context.Background(), tt.query, nil, tt.limit, tt.offset)
			assertErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestSearchServiceCollection(t *testing.T) {
	collectionID := uuid.New()
	query := "知识"

	t.Run("不传 collection 不查 collection", func(t *testing.T) {
		lookupCalls := 0
		lookup := &fakeCollectionLookup{
			get: func(context.Context, uuid.UUID) (*Collection, error) {
				lookupCalls++
				return nil, nil
			},
		}
		repo := &fakeSearchRepository{
			search: func(_ context.Context, gotQuery string, gotCollection *uuid.UUID, limit, offset int) ([]SearchHit, bool, error) {
				if gotCollection != nil {
					t.Fatalf("collectionID = %v, want nil", gotCollection)
				}
				return []SearchHit{}, false, nil
			},
		}
		svc := NewSearchService(repo, lookup)

		if _, err := svc.Search(context.Background(), query, nil, 20, 0); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if lookupCalls != 0 {
			t.Fatalf("GetCollection 调用次数 = %d, want 0", lookupCalls)
		}
	})

	t.Run("collection 存在", func(t *testing.T) {
		lookupCalls := 0
		lookup := &fakeCollectionLookup{
			get: func(_ context.Context, id uuid.UUID) (*Collection, error) {
				lookupCalls++
				if id != collectionID {
					t.Fatalf("GetCollection id = %v, want %v", id, collectionID)
				}
				return &Collection{ID: id}, nil
			},
		}
		repo := &fakeSearchRepository{
			search: func(_ context.Context, _ string, gotCollection *uuid.UUID, limit, offset int) ([]SearchHit, bool, error) {
				if gotCollection == nil || *gotCollection != collectionID {
					t.Fatalf("collectionID = %v, want %v", gotCollection, collectionID)
				}
				return []SearchHit{{ChunkID: uuid.New(), DocumentID: uuid.New()}}, false, nil
			},
		}
		svc := NewSearchService(repo, lookup)

		result, err := svc.Search(context.Background(), query, &collectionID, 20, 0)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if lookupCalls != 1 {
			t.Fatalf("GetCollection 调用次数 = %d, want 1", lookupCalls)
		}
		if len(result.Items) != 1 {
			t.Fatalf("items = %d, want 1", len(result.Items))
		}
	})

	t.Run("collection 不存在", func(t *testing.T) {
		lookup := &fakeCollectionLookup{
			get: func(context.Context, uuid.UUID) (*Collection, error) {
				return nil, ErrNotFound
			},
		}
		// repo.search 为 nil：collection 404 时不应该继续搜索。
		svc := NewSearchService(&fakeSearchRepository{}, lookup)

		_, err := svc.Search(context.Background(), query, &collectionID, 20, 0)
		assertErrorCode(t, err, apperr.CodeNotFound)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("错误原因链应保留 ErrNotFound, got %v", err)
		}
	})

	t.Run("collection 查询内部错误", func(t *testing.T) {
		baseErr := errors.New("db down")
		lookup := &fakeCollectionLookup{
			get: func(context.Context, uuid.UUID) (*Collection, error) {
				return nil, baseErr
			},
		}
		svc := NewSearchService(&fakeSearchRepository{}, lookup)

		_, err := svc.Search(context.Background(), query, &collectionID, 20, 0)
		assertErrorCode(t, err, apperr.CodeInternal)
		if !errors.Is(err, baseErr) {
			t.Fatalf("错误原因链应保留底层错误, got %v", err)
		}
	})

	t.Run("collection 为 uuid.Nil", func(t *testing.T) {
		nilID := uuid.Nil
		svc := NewSearchService(&fakeSearchRepository{}, &fakeCollectionLookup{})

		_, err := svc.Search(context.Background(), query, &nilID, 20, 0)
		assertErrorCode(t, err, apperr.CodeInvalidArgument)
	})
}

func TestSearchServicePagination(t *testing.T) {
	tests := []struct {
		name     string
		limit    int
		offset   int
		hasMore  bool
		wantNext *int
	}{
		{name: "没有下一页", limit: 20, offset: 0, hasMore: false, wantNext: nil},
		{name: "有下一页", limit: 20, offset: 0, hasMore: true, wantNext: intPtr(20)},
		{name: "窗口中间", limit: 20, offset: 900, hasMore: true, wantNext: intPtr(920)},
		{name: "正好到窗口上限", limit: 20, offset: 980, hasMore: true, wantNext: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeSearchRepository{
				search: func(context.Context, string, *uuid.UUID, int, int) ([]SearchHit, bool, error) {
					return []SearchHit{{ChunkID: uuid.New(), DocumentID: uuid.New()}}, tt.hasMore, nil
				},
			}
			svc := NewSearchService(repo, &fakeCollectionLookup{})
			result, err := svc.Search(context.Background(), "知识", nil, tt.limit, tt.offset)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}

			if tt.wantNext == nil {
				if result.NextOffset != nil {
					t.Fatalf("NextOffset = %d, want nil", *result.NextOffset)
				}
				return
			}
			if result.NextOffset == nil || *result.NextOffset != *tt.wantNext {
				t.Fatalf("NextOffset = %v, want %d", result.NextOffset, *tt.wantNext)
			}
		})
	}
}

func TestSearchServicePassesTrimmedQueryAndValues(t *testing.T) {
	var (
		gotQuery      string
		gotLimit      int
		gotOffset     int
		gotCollection *uuid.UUID
	)
	repo := &fakeSearchRepository{
		search: func(_ context.Context, query string, collectionID *uuid.UUID, limit, offset int) ([]SearchHit, bool, error) {
			gotQuery = query
			gotCollection = collectionID
			gotLimit = limit
			gotOffset = offset
			return nil, false, nil
		},
	}
	svc := NewSearchService(repo, &fakeCollectionLookup{})

	result, err := svc.Search(context.Background(), "  知识  ", nil, 20, 40)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotQuery != "知识" {
		t.Fatalf("query = %q, want 知识（trim 后）", gotQuery)
	}
	if gotCollection != nil {
		t.Fatalf("collectionID = %v, want nil", gotCollection)
	}
	if gotLimit != 20 || gotOffset != 40 {
		t.Fatalf("limit/offset = %d/%d, want 20/40", gotLimit, gotOffset)
	}
	if result.Items == nil {
		t.Fatal("nil hits 应归一成空切片，避免 JSON 输出 null")
	}
	if len(result.Items) != 0 {
		t.Fatalf("items = %d, want 0", len(result.Items))
	}
	if result.NextOffset != nil {
		t.Fatalf("NextOffset = %v, want nil", result.NextOffset)
	}
}

func TestSearchServiceRepoError(t *testing.T) {
	baseErr := errors.New("query failed")
	repo := &fakeSearchRepository{
		search: func(context.Context, string, *uuid.UUID, int, int) ([]SearchHit, bool, error) {
			return nil, false, baseErr
		},
	}
	svc := NewSearchService(repo, &fakeCollectionLookup{})

	_, err := svc.Search(context.Background(), "知识", nil, 20, 0)
	assertErrorCode(t, err, apperr.CodeInternal)
	if !errors.Is(err, baseErr) {
		t.Fatalf("错误原因链应保留底层错误, got %v", err)
	}
}

func intPtr(v int) *int {
	return &v
}
