package rest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/knowledge"
)

type fakeDocumentService struct {
	create func(context.Context, string, *string, *string, *int64) (*knowledge.Document, error)
	get    func(context.Context, uuid.UUID) (*knowledge.Document, error)
	update func(context.Context, uuid.UUID, knowledge.DocumentUpdate) (*knowledge.Document, error)
	delete func(context.Context, uuid.UUID) error
	add    func(context.Context, uuid.UUID, uuid.UUID) (*knowledge.CollectionDocument, error)
	remove func(context.Context, uuid.UUID, uuid.UUID) error
	list   func(context.Context, uuid.UUID, *knowledge.Cursor, int) ([]*knowledge.CollectionDocument, *knowledge.Cursor, error)
}

var _ DocumentService = (*fakeDocumentService)(nil)

func (f *fakeDocumentService) CreateDocument(ctx context.Context, title string, sourceURI, mimeType *string, sizeBytes *int64) (*knowledge.Document, error) {
	if f.create == nil {
		panic("unexpected CreateDocument call")
	}
	return f.create(ctx, title, sourceURI, mimeType, sizeBytes)
}

func (f *fakeDocumentService) GetDocument(ctx context.Context, id uuid.UUID) (*knowledge.Document, error) {
	if f.get == nil {
		panic("unexpected GetDocument call")
	}
	return f.get(ctx, id)
}

func (f *fakeDocumentService) UpdateDocument(ctx context.Context, id uuid.UUID, patch knowledge.DocumentUpdate) (*knowledge.Document, error) {
	if f.update == nil {
		panic("unexpected UpdateDocument call")
	}
	return f.update(ctx, id, patch)
}

func (f *fakeDocumentService) SoftDeleteDocument(ctx context.Context, id uuid.UUID) error {
	if f.delete == nil {
		panic("unexpected SoftDeleteDocument call")
	}
	return f.delete(ctx, id)
}

func (f *fakeDocumentService) AddDocumentToCollection(ctx context.Context, collectionID, documentID uuid.UUID) (*knowledge.CollectionDocument, error) {
	if f.add == nil {
		panic("unexpected AddDocumentToCollection call")
	}
	return f.add(ctx, collectionID, documentID)
}

func (f *fakeDocumentService) RemoveDocumentFromCollection(ctx context.Context, collectionID, documentID uuid.UUID) error {
	if f.remove == nil {
		panic("unexpected RemoveDocumentFromCollection call")
	}
	return f.remove(ctx, collectionID, documentID)
}

func (f *fakeDocumentService) ListDocumentsByCollection(ctx context.Context, collectionID uuid.UUID, cursor *knowledge.Cursor, limit int) ([]*knowledge.CollectionDocument, *knowledge.Cursor, error) {
	if f.list == nil {
		panic("unexpected ListDocumentsByCollection call")
	}
	return f.list(ctx, collectionID, cursor, limit)
}

func ptr[T any](v T) *T {
	return &v
}

func testDocument(id uuid.UUID) *knowledge.Document {
	return &knowledge.Document{
		ID:        id,
		Title:     "设计文档",
		SourceURI: ptr("s3://knowflow/docs/design.pdf"),
		MimeType:  ptr("application/pdf"),
		SizeBytes: ptr(int64(1024)),
		CreatedAt: time.Date(2026, 10, 18, 3, 4, 5, 600*int(time.Millisecond), time.UTC),
		UpdatedAt: time.Date(2026, 10, 18, 3, 5, 6, 700*int(time.Millisecond), time.UTC),
	}
}

func TestDocumentCreate(t *testing.T) {
	id := uuid.New()

	t.Run("成功", func(t *testing.T) {
		var gotTitle string
		var gotSource, gotMime *string
		var gotSize *int64
		svc := &fakeDocumentService{
			create: func(_ context.Context, title string, sourceURI, mimeType *string, sizeBytes *int64) (*knowledge.Document, error) {
				gotTitle, gotSource, gotMime, gotSize = title, sourceURI, mimeType, sizeBytes
				return testDocument(id), nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)

		rec := doJSON(t, s, http.MethodPost, documentsPath,
			`{"title":"设计文档","source_uri":"s3://knowflow/docs/design.pdf","mime_type":"application/pdf","size_bytes":1024}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("状态码 = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}
		if loc := rec.Header().Get("Location"); loc != documentsPath+"/"+id.String() {
			t.Fatalf("Location = %q, want %q", loc, documentsPath+"/"+id.String())
		}
		if gotTitle != "设计文档" || gotSource == nil || *gotSource != "s3://knowflow/docs/design.pdf" ||
			gotMime == nil || *gotMime != "application/pdf" || gotSize == nil || *gotSize != 1024 {
			t.Fatalf("service 收到参数 = (%q,%v,%v,%v)", gotTitle, gotSource, gotMime, gotSize)
		}

		body := decodeBody[documentResponse](t, rec)
		if body.ID != id.String() || body.Title != "设计文档" || body.SizeBytes == nil || *body.SizeBytes != 1024 {
			t.Fatalf("响应体 = %+v", body)
		}
	})

	t.Run("非法 JSON", func(t *testing.T) {
		s := newTestServerWithServices(t, nil, nil, &fakeDocumentService{})
		rec := doJSON(t, s, http.MethodPost, documentsPath, `{"title":`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("未知字段", func(t *testing.T) {
		s := newTestServerWithServices(t, nil, nil, &fakeDocumentService{})
		rec := doJSON(t, s, http.MethodPost, documentsPath, `{"title":"x","unknown":1}`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("service 校验失败", func(t *testing.T) {
		svc := &fakeDocumentService{
			create: func(context.Context, string, *string, *string, *int64) (*knowledge.Document, error) {
				return nil, apperr.New(apperr.CodeInvalidArgument, "title 不能为空")
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := doJSON(t, s, http.MethodPost, documentsPath, `{"title":""}`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})
}

func TestDocumentGet(t *testing.T) {
	id := uuid.New()

	t.Run("成功", func(t *testing.T) {
		svc := &fakeDocumentService{
			get: func(_ context.Context, gotID uuid.UUID) (*knowledge.Document, error) {
				if gotID != id {
					t.Fatalf("repo 收到 id=%v, want %v", gotID, id)
				}
				return testDocument(id), nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := do(t, s, http.MethodGet, documentsPath+"/"+id.String())
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		body := decodeBody[documentResponse](t, rec)
		if body.ID != id.String() || body.SourceURI == nil || *body.SourceURI != "s3://knowflow/docs/design.pdf" {
			t.Fatalf("响应体 = %+v", body)
		}
	})

	t.Run("非法 UUID", func(t *testing.T) {
		s := newTestServerWithServices(t, nil, nil, &fakeDocumentService{})
		rec := do(t, s, http.MethodGet, documentsPath+"/not-a-uuid")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("不存在", func(t *testing.T) {
		svc := &fakeDocumentService{
			get: func(context.Context, uuid.UUID) (*knowledge.Document, error) {
				return nil, apperr.New(apperr.CodeNotFound, "文档不存在")
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := do(t, s, http.MethodGet, documentsPath+"/"+id.String())
		assertHTTPError(t, rec, http.StatusNotFound, apperr.CodeNotFound)
	})
}

func TestDocumentUpdate(t *testing.T) {
	id := uuid.New()

	t.Run("三态映射", func(t *testing.T) {
		var got knowledge.DocumentUpdate
		svc := &fakeDocumentService{
			update: func(_ context.Context, gotID uuid.UUID, patch knowledge.DocumentUpdate) (*knowledge.Document, error) {
				if gotID != id {
					t.Fatalf("repo 收到 id=%v, want %v", gotID, id)
				}
				got = patch
				return testDocument(id), nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)

		rec := doJSON(t, s, http.MethodPatch, documentsPath+"/"+id.String(),
			`{"title":"新标题","source_uri":null,"mime_type":"text/plain","size_bytes":0}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got.Title == nil || *got.Title != "新标题" {
			t.Fatalf("title = %v, want 新标题", got.Title)
		}
		if !got.SourceURI.Set || got.SourceURI.Value != nil {
			t.Fatalf("source_uri = %+v, want Set=true Value=nil", got.SourceURI)
		}
		if !got.MimeType.Set || got.MimeType.Value == nil || *got.MimeType.Value != "text/plain" {
			t.Fatalf("mime_type = %+v", got.MimeType)
		}
		if !got.SizeBytes.Set || got.SizeBytes.Value == nil || *got.SizeBytes.Value != 0 {
			t.Fatalf("size_bytes = %+v", got.SizeBytes)
		}
	})

	t.Run("缺省字段不修改", func(t *testing.T) {
		var got knowledge.DocumentUpdate
		svc := &fakeDocumentService{
			update: func(_ context.Context, _ uuid.UUID, patch knowledge.DocumentUpdate) (*knowledge.Document, error) {
				got = patch
				return testDocument(id), nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := doJSON(t, s, http.MethodPatch, documentsPath+"/"+id.String(), `{}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got.Title != nil || got.SourceURI.Set || got.MimeType.Set || got.SizeBytes.Set {
			t.Fatalf("空 PATCH 映射错误: %+v", got)
		}
	})

	t.Run("title 为 null 等于不改", func(t *testing.T) {
		var got knowledge.DocumentUpdate
		svc := &fakeDocumentService{
			update: func(_ context.Context, _ uuid.UUID, patch knowledge.DocumentUpdate) (*knowledge.Document, error) {
				got = patch
				return testDocument(id), nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := doJSON(t, s, http.MethodPatch, documentsPath+"/"+id.String(), `{"title":null}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got.Title != nil {
			t.Fatalf("title:null 应映射为 nil（不改），got %v", got.Title)
		}
	})

	t.Run("非法 JSON", func(t *testing.T) {
		s := newTestServerWithServices(t, nil, nil, &fakeDocumentService{})
		rec := doJSON(t, s, http.MethodPatch, documentsPath+"/"+id.String(), `[]`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("service 校验失败", func(t *testing.T) {
		svc := &fakeDocumentService{
			update: func(context.Context, uuid.UUID, knowledge.DocumentUpdate) (*knowledge.Document, error) {
				return nil, apperr.New(apperr.CodeInvalidArgument, "title 不能为空")
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := doJSON(t, s, http.MethodPatch, documentsPath+"/"+id.String(), `{"title":""}`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})
}

func TestDocumentDelete(t *testing.T) {
	id := uuid.New()

	t.Run("成功", func(t *testing.T) {
		var got uuid.UUID
		svc := &fakeDocumentService{
			delete: func(_ context.Context, gotID uuid.UUID) error {
				got = gotID
				return nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := do(t, s, http.MethodDelete, documentsPath+"/"+id.String())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("状态码 = %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
		if got != id {
			t.Fatalf("service 收到 id=%v, want %v", got, id)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("204 不应有响应体, got %q", rec.Body.String())
		}
	})

	t.Run("非法 UUID", func(t *testing.T) {
		s := newTestServerWithServices(t, nil, nil, &fakeDocumentService{})
		rec := do(t, s, http.MethodDelete, documentsPath+"/bad")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("不存在", func(t *testing.T) {
		svc := &fakeDocumentService{
			delete: func(context.Context, uuid.UUID) error {
				return apperr.New(apperr.CodeNotFound, "文档不存在")
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := do(t, s, http.MethodDelete, documentsPath+"/"+id.String())
		assertHTTPError(t, rec, http.StatusNotFound, apperr.CodeNotFound)
	})
}

func TestDocumentAddToCollection(t *testing.T) {
	collectionID, documentID := uuid.New(), uuid.New()

	t.Run("成功", func(t *testing.T) {
		var gotCollection, gotDocument uuid.UUID
		svc := &fakeDocumentService{
			add: func(_ context.Context, cid, did uuid.UUID) (*knowledge.CollectionDocument, error) {
				gotCollection, gotDocument = cid, did
				return &knowledge.CollectionDocument{
					Document: testDocument(did),
					AddedAt:  time.Date(2026, 10, 18, 4, 0, 0, 0, time.UTC),
				}, nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := doJSON(t, s, http.MethodPost, collectionsPath+"/"+collectionID.String()+"/documents",
			`{"document_id":"`+documentID.String()+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("状态码 = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}
		wantLocation := collectionsPath + "/" + collectionID.String() + "/documents/" + documentID.String()
		if loc := rec.Header().Get("Location"); loc != wantLocation {
			t.Fatalf("Location = %q, want %q", loc, wantLocation)
		}
		if gotCollection != collectionID || gotDocument != documentID {
			t.Fatalf("service 收到 (%v,%v), want (%v,%v)", gotCollection, gotDocument, collectionID, documentID)
		}
		body := decodeBody[collectionDocumentResponse](t, rec)
		if body.Document.ID != documentID.String() || !body.AddedAt.Equal(time.Date(2026, 10, 18, 4, 0, 0, 0, time.UTC)) {
			t.Fatalf("响应体 = %+v", body)
		}
	})

	t.Run("document_id 非法", func(t *testing.T) {
		s := newTestServerWithServices(t, nil, nil, &fakeDocumentService{})
		rec := doJSON(t, s, http.MethodPost, collectionsPath+"/"+collectionID.String()+"/documents",
			`{"document_id":"not-a-uuid"}`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("重复关联 409", func(t *testing.T) {
		svc := &fakeDocumentService{
			add: func(context.Context, uuid.UUID, uuid.UUID) (*knowledge.CollectionDocument, error) {
				return nil, apperr.New(apperr.CodeConflict, "文档已在该知识库中")
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := doJSON(t, s, http.MethodPost, collectionsPath+"/"+collectionID.String()+"/documents",
			`{"document_id":"`+documentID.String()+`"}`)
		assertHTTPError(t, rec, http.StatusConflict, apperr.CodeConflict)
	})

	t.Run("知识库或文档不存在 404", func(t *testing.T) {
		svc := &fakeDocumentService{
			add: func(context.Context, uuid.UUID, uuid.UUID) (*knowledge.CollectionDocument, error) {
				return nil, apperr.New(apperr.CodeNotFound, "知识库或文档不存在")
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := doJSON(t, s, http.MethodPost, collectionsPath+"/"+collectionID.String()+"/documents",
			`{"document_id":"`+documentID.String()+`"}`)
		assertHTTPError(t, rec, http.StatusNotFound, apperr.CodeNotFound)
	})
}

func TestDocumentListByCollection(t *testing.T) {
	collectionID := uuid.New()
	first, second := testDocument(uuid.New()), testDocument(uuid.New())
	next := &knowledge.Cursor{CreatedAt: time.Date(2026, 10, 18, 4, 1, 2, 300*int(time.Millisecond), time.UTC), ID: uuid.New()}

	t.Run("成功且返回 next_cursor", func(t *testing.T) {
		var gotCursor *knowledge.Cursor
		svc := &fakeDocumentService{
			list: func(_ context.Context, gotCollection uuid.UUID, cursor *knowledge.Cursor, limit int) ([]*knowledge.CollectionDocument, *knowledge.Cursor, error) {
				if gotCollection != collectionID || cursor != nil || limit != knowledge.DefaultDocumentListLimit {
					t.Fatalf("service 收到 (%v,%+v,%d)", gotCollection, cursor, limit)
				}
				gotCursor = cursor
				return []*knowledge.CollectionDocument{
					{Document: first, AddedAt: next.CreatedAt},
					{Document: second, AddedAt: next.CreatedAt},
				}, next, nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := do(t, s, http.MethodGet, collectionsPath+"/"+collectionID.String()+"/documents")
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if gotCursor != nil {
			t.Fatal("第一页 cursor 应为 nil")
		}
		body := decodeBody[collectionDocumentListResponse](t, rec)
		if len(body.Items) != 2 || body.Items[0].Document.ID != first.ID.String() ||
			body.Items[1].Document.ID != second.ID.String() {
			t.Fatalf("items = %+v", body.Items)
		}
		if body.NextCursor == nil {
			t.Fatal("next_cursor 不应为 nil")
		}
		decoded, err := decodeListCursor(*body.NextCursor)
		if err != nil {
			t.Fatalf("next_cursor 解码失败: %v", err)
		}
		if decoded.ID != next.ID || !decoded.CreatedAt.Equal(next.CreatedAt) {
			t.Fatalf("next_cursor = %+v, want %+v", decoded, next)
		}
	})

	t.Run("第二页空列表", func(t *testing.T) {
		encoded, err := encodeListCursor(next)
		if err != nil {
			t.Fatalf("编码 cursor: %v", err)
		}
		svc := &fakeDocumentService{
			list: func(_ context.Context, _ uuid.UUID, cursor *knowledge.Cursor, limit int) ([]*knowledge.CollectionDocument, *knowledge.Cursor, error) {
				if cursor == nil || cursor.ID != next.ID || limit != 5 {
					t.Fatalf("service 收到 cursor=%+v limit=%d", cursor, limit)
				}
				return nil, nil, nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := do(t, s, http.MethodGet,
			collectionsPath+"/"+collectionID.String()+"/documents?limit=5&cursor="+encoded)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		body := decodeBody[collectionDocumentListResponse](t, rec)
		if body.Items == nil || len(body.Items) != 0 || body.NextCursor != nil {
			t.Fatalf("空页 = %+v", body)
		}
	})

	t.Run("limit 非法", func(t *testing.T) {
		s := newTestServerWithServices(t, nil, nil, &fakeDocumentService{})
		rec := do(t, s, http.MethodGet, collectionsPath+"/"+collectionID.String()+"/documents?limit=101")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("cursor 非法", func(t *testing.T) {
		s := newTestServerWithServices(t, nil, nil, &fakeDocumentService{})
		rec := do(t, s, http.MethodGet, collectionsPath+"/"+collectionID.String()+"/documents?cursor=bad")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("知识库不存在", func(t *testing.T) {
		svc := &fakeDocumentService{
			list: func(context.Context, uuid.UUID, *knowledge.Cursor, int) ([]*knowledge.CollectionDocument, *knowledge.Cursor, error) {
				return nil, nil, apperr.New(apperr.CodeNotFound, "知识库不存在")
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := do(t, s, http.MethodGet, collectionsPath+"/"+collectionID.String()+"/documents")
		assertHTTPError(t, rec, http.StatusNotFound, apperr.CodeNotFound)
	})
}

func TestDocumentRemoveFromCollection(t *testing.T) {
	collectionID, documentID := uuid.New(), uuid.New()

	t.Run("成功", func(t *testing.T) {
		var gotCollection, gotDocument uuid.UUID
		svc := &fakeDocumentService{
			remove: func(_ context.Context, cid, did uuid.UUID) error {
				gotCollection, gotDocument = cid, did
				return nil
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := do(t, s, http.MethodDelete,
			collectionsPath+"/"+collectionID.String()+"/documents/"+documentID.String())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("状态码 = %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
		if gotCollection != collectionID || gotDocument != documentID {
			t.Fatalf("service 收到 (%v,%v), want (%v,%v)", gotCollection, gotDocument, collectionID, documentID)
		}
	})

	t.Run("关联不存在", func(t *testing.T) {
		svc := &fakeDocumentService{
			remove: func(context.Context, uuid.UUID, uuid.UUID) error {
				return apperr.New(apperr.CodeNotFound, "关联不存在")
			},
		}
		s := newTestServerWithServices(t, nil, nil, svc)
		rec := do(t, s, http.MethodDelete,
			collectionsPath+"/"+collectionID.String()+"/documents/"+documentID.String())
		assertHTTPError(t, rec, http.StatusNotFound, apperr.CodeNotFound)
	})

	t.Run("document_id 非法", func(t *testing.T) {
		s := newTestServerWithServices(t, nil, nil, &fakeDocumentService{})
		rec := do(t, s, http.MethodDelete,
			collectionsPath+"/"+collectionID.String()+"/documents/not-a-uuid")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})
}
