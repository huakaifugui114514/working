package rest

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/knowledge"
)

type fakeSearchService struct {
	search func(context.Context, string, *uuid.UUID, int, int) (*knowledge.SearchResult, error)
}

var _ SearchService = (*fakeSearchService)(nil)

func (f *fakeSearchService) Search(
	ctx context.Context,
	q string,
	collectionID *uuid.UUID,
	limit int,
	offset int,
) (*knowledge.SearchResult, error) {
	if f.search == nil {
		panic("unexpected Search call")
	}
	return f.search(ctx, q, collectionID, limit, offset)
}

func TestSearch(t *testing.T) {
	collectionID := uuid.New()
	chunkID := uuid.New()
	documentID := uuid.New()
	nextOffset := 20

	var (
		gotQuery      string
		gotCollection *uuid.UUID
		gotLimit      int
		gotOffset     int
	)
	svc := &fakeSearchService{
		search: func(_ context.Context, q string, cid *uuid.UUID, limit, offset int) (*knowledge.SearchResult, error) {
			gotQuery = q
			gotCollection = cid
			gotLimit = limit
			gotOffset = offset
			return &knowledge.SearchResult{
				Items: []knowledge.SearchHit{
					{
						ChunkID:       chunkID,
						DocumentID:    documentID,
						DocumentTitle: "产品文档",
						ChunkIndex:    3,
						Content:       "企业知识库支持全文检索",
						Score:         1.25,
					},
				},
				NextOffset: &nextOffset,
			}, nil
		},
	}
	s := newTestServerWithSearch(t, nil, svc)

	target := searchPath + "?q=" + url.QueryEscape("知识") +
		"&collection_id=" + collectionID.String() +
		"&limit=1&offset=0"
	rec := do(t, s, http.MethodGet, target)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotQuery != "知识" {
		t.Fatalf("q = %q, want 知识", gotQuery)
	}
	if gotCollection == nil || *gotCollection != collectionID {
		t.Fatalf("collection_id = %v, want %v", gotCollection, collectionID)
	}
	if gotLimit != 1 || gotOffset != 0 {
		t.Fatalf("limit/offset = %d/%d, want 1/0", gotLimit, gotOffset)
	}

	got := decodeBody[searchResponse](t, rec)
	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(got.Items))
	}
	item := got.Items[0]
	if item.ChunkID != chunkID.String() || item.DocumentID != documentID.String() {
		t.Fatalf("chunk/document id = %s/%s, want %s/%s",
			item.ChunkID, item.DocumentID, chunkID, documentID)
	}
	if item.DocumentTitle != "产品文档" || item.ChunkIndex != 3 ||
		item.Content != "企业知识库支持全文检索" || item.Score != 1.25 {
		t.Fatalf("item = %+v", item)
	}
	if got.NextOffset == nil || *got.NextOffset != nextOffset {
		t.Fatalf("next_offset = %v, want %d", got.NextOffset, nextOffset)
	}
}

func TestSearchDefaultsAndNullNextOffset(t *testing.T) {
	var (
		gotLimit  int
		gotOffset int
	)
	svc := &fakeSearchService{
		search: func(_ context.Context, _ string, _ *uuid.UUID, limit, offset int) (*knowledge.SearchResult, error) {
			gotLimit = limit
			gotOffset = offset
			return &knowledge.SearchResult{Items: []knowledge.SearchHit{}, NextOffset: nil}, nil
		},
	}
	s := newTestServerWithSearch(t, nil, svc)

	rec := do(t, s, http.MethodGet, searchPath+"?q="+url.QueryEscape("知识"))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotLimit != knowledge.DefaultSearchLimit || gotOffset != 0 {
		t.Fatalf("limit/offset = %d/%d, want %d/0", gotLimit, gotOffset, knowledge.DefaultSearchLimit)
	}
	if !strings.Contains(rec.Body.String(), `"next_offset":null`) {
		t.Fatalf("响应应显式包含 next_offset:null, body=%s", rec.Body.String())
	}
	got := decodeBody[searchResponse](t, rec)
	if got.NextOffset != nil {
		t.Fatalf("next_offset = %v, want nil", got.NextOffset)
	}
	if len(got.Items) != 0 {
		t.Fatalf("items = %d, want 0", len(got.Items))
	}
}

func TestSearchRejectsInvalidQueryParams(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "limit 不是整数", query: "limit=x"},
		{name: "limit 小于 1", query: "limit=0"},
		{name: "limit 大于 50", query: "limit=51"},
		{name: "offset 为负", query: "offset=-1"},
		{name: "offset 超过 1000", query: "offset=1001"},
		{name: "collection_id 不是 UUID", query: "collection_id=not-a-uuid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// fake 的 search 为 nil：参数非法时不能调用 service。
			s := newTestServerWithSearch(t, nil, &fakeSearchService{})
			rec := do(t, s, http.MethodGet, searchPath+"?q="+url.QueryEscape("知识")+"&"+tt.query)
			assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
		})
	}
}

func TestSearchServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "参数错误",
			serviceErr: apperr.New(apperr.CodeInvalidArgument, "q 不能为空"),
			wantStatus: http.StatusBadRequest,
			wantCode:   apperr.CodeInvalidArgument,
		},
		{
			name:       "知识库不存在",
			serviceErr: apperr.New(apperr.CodeNotFound, "知识库不存在"),
			wantStatus: http.StatusNotFound,
			wantCode:   apperr.CodeNotFound,
		},
		{
			name:       "内部错误",
			serviceErr: apperr.New(apperr.CodeInternal, "boom"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   apperr.CodeInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeSearchService{
				search: func(context.Context, string, *uuid.UUID, int, int) (*knowledge.SearchResult, error) {
					return nil, tt.serviceErr
				},
			}
			s := newTestServerWithSearch(t, nil, svc)

			rec := do(t, s, http.MethodGet, searchPath+"?q="+url.QueryEscape("知识"))
			assertHTTPError(t, rec, tt.wantStatus, tt.wantCode)
		})
	}
}
