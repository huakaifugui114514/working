package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

type fakeCollectionService struct {
	create  func(context.Context, string, string) (*knowledge.Collection, error)
	get     func(context.Context, uuid.UUID) (*knowledge.Collection, error)
	list    func(context.Context, *knowledge.Cursor, int) ([]*knowledge.Collection, *knowledge.Cursor, error)
	update  func(context.Context, uuid.UUID, knowledge.CollectionUpdate) (*knowledge.Collection, error)
	delete  func(context.Context, uuid.UUID) error
	restore func(context.Context, uuid.UUID) (*knowledge.Collection, error)
}

var _ CollectionService = (*fakeCollectionService)(nil)

func (f *fakeCollectionService) CreateCollection(ctx context.Context, name, description string) (*knowledge.Collection, error) {
	if f.create == nil {
		panic("unexpected CreateCollection call")
	}
	return f.create(ctx, name, description)
}

func (f *fakeCollectionService) GetCollection(ctx context.Context, id uuid.UUID) (*knowledge.Collection, error) {
	if f.get == nil {
		panic("unexpected GetCollection call")
	}
	return f.get(ctx, id)
}

func (f *fakeCollectionService) ListCollections(ctx context.Context, cursor *knowledge.Cursor, limit int) ([]*knowledge.Collection, *knowledge.Cursor, error) {
	if f.list == nil {
		panic("unexpected ListCollections call")
	}
	return f.list(ctx, cursor, limit)
}

func (f *fakeCollectionService) UpdateCollection(ctx context.Context, id uuid.UUID, patch knowledge.CollectionUpdate) (*knowledge.Collection, error) {
	if f.update == nil {
		panic("unexpected UpdateCollection call")
	}
	return f.update(ctx, id, patch)
}

func (f *fakeCollectionService) SoftDeleteCollection(ctx context.Context, id uuid.UUID) error {
	if f.delete == nil {
		panic("unexpected SoftDeleteCollection call")
	}
	return f.delete(ctx, id)
}

func (f *fakeCollectionService) RestoreCollection(ctx context.Context, id uuid.UUID) (*knowledge.Collection, error) {
	if f.restore == nil {
		panic("unexpected RestoreCollection call")
	}
	return f.restore(ctx, id)
}

func testCollection(id uuid.UUID) *knowledge.Collection {
	return &knowledge.Collection{
		ID:          id,
		Name:        "产品文档",
		Description: "描述",
		CreatedAt:   time.Date(2026, 10, 18, 2, 25, 39, 123*int(time.Millisecond), time.UTC),
		UpdatedAt:   time.Date(2026, 10, 18, 2, 26, 0, 0, time.UTC),
	}
}

func doJSON(t *testing.T, s *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("解析响应失败: %v; body=%s", err, rec.Body.String())
	}
	return v
}

func assertHTTPError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("状态码 = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
	}
	body := decodeBody[respond.ErrorBody](t, rec)
	if body.Error.Code != code {
		t.Fatalf("错误码 = %q, want %q", body.Error.Code, code)
	}
}

func TestCollectionCreate(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		id := uuid.New()
		var gotName, gotDesc string
		svc := &fakeCollectionService{
			create: func(_ context.Context, name, description string) (*knowledge.Collection, error) {
				gotName, gotDesc = name, description
				return testCollection(id), nil
			},
		}
		s := newTestServerWithCollections(t, nil, svc)

		rec := doJSON(t, s, http.MethodPost, collectionsPath,
			`{"name":" 产品文档 ","description":"描述"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("状态码 = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}
		if gotName != " 产品文档 " || gotDesc != "描述" {
			t.Fatalf("service 收到 name=%q desc=%q", gotName, gotDesc)
		}
		if loc := rec.Header().Get("Location"); loc != collectionsPath+"/"+id.String() {
			t.Fatalf("Location = %q, want %q", loc, collectionsPath+"/"+id.String())
		}
		got := decodeBody[collectionResponse](t, rec)
		if got.ID != id.String() || got.Name != "产品文档" {
			t.Fatalf("响应 = %+v", got)
		}
	})

	t.Run("非法 JSON", func(t *testing.T) {
		s := newTestServerWithCollections(t, nil, &fakeCollectionService{})
		rec := doJSON(t, s, http.MethodPost, collectionsPath, `{"name":`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("未知字段", func(t *testing.T) {
		s := newTestServerWithCollections(t, nil, &fakeCollectionService{})
		rec := doJSON(t, s, http.MethodPost, collectionsPath, `{"name":"x","unknown":1}`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("service 业务错误", func(t *testing.T) {
		svc := &fakeCollectionService{
			create: func(context.Context, string, string) (*knowledge.Collection, error) {
				return nil, apperr.New(apperr.CodeInvalidArgument, "name 不能为空")
			},
		}
		s := newTestServerWithCollections(t, nil, svc)
		rec := doJSON(t, s, http.MethodPost, collectionsPath, `{"name":"x"}`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})
}

func TestCollectionGet(t *testing.T) {
	id := uuid.New()
	svc := &fakeCollectionService{
		get: func(_ context.Context, gotID uuid.UUID) (*knowledge.Collection, error) {
			if gotID != id {
				t.Fatalf("service 收到 id=%v, want %v", gotID, id)
			}
			return testCollection(id), nil
		},
	}
	s := newTestServerWithCollections(t, nil, svc)

	rec := do(t, s, http.MethodGet, collectionsPath+"/"+id.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeBody[collectionResponse](t, rec)
	if got.ID != id.String() {
		t.Fatalf("响应 = %+v", got)
	}
}

func TestCollectionGetErrors(t *testing.T) {
	t.Run("非法 UUID", func(t *testing.T) {
		s := newTestServerWithCollections(t, nil, &fakeCollectionService{})
		rec := do(t, s, http.MethodGet, collectionsPath+"/not-a-uuid")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("不存在", func(t *testing.T) {
		svc := &fakeCollectionService{
			get: func(context.Context, uuid.UUID) (*knowledge.Collection, error) {
				return nil, apperr.New(apperr.CodeNotFound, "知识库不存在")
			},
		}
		s := newTestServerWithCollections(t, nil, svc)
		rec := do(t, s, http.MethodGet, collectionsPath+"/"+uuid.New().String())
		assertHTTPError(t, rec, http.StatusNotFound, apperr.CodeNotFound)
	})
}

func TestCollectionList(t *testing.T) {
	next := &knowledge.Cursor{CreatedAt: time.Now().UTC().Truncate(time.Millisecond), ID: uuid.New()}

	t.Run("默认 limit 与 next_cursor", func(t *testing.T) {
		var gotLimit int
		var gotCursor *knowledge.Cursor
		svc := &fakeCollectionService{
			list: func(_ context.Context, cursor *knowledge.Cursor, limit int) ([]*knowledge.Collection, *knowledge.Cursor, error) {
				gotCursor, gotLimit = cursor, limit
				return []*knowledge.Collection{testCollection(uuid.New())}, next, nil
			},
		}
		s := newTestServerWithCollections(t, nil, svc)

		rec := do(t, s, http.MethodGet, collectionsPath)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if gotLimit != knowledge.DefaultCollectionListLimit || gotCursor != nil {
			t.Fatalf("service 收到 limit=%d cursor=%+v", gotLimit, gotCursor)
		}
		body := decodeBody[collectionListResponse](t, rec)
		if len(body.Items) != 1 || body.NextCursor == nil {
			t.Fatalf("响应 = %+v", body)
		}
		decoded, err := decodeListCursor(*body.NextCursor)
		if err != nil {
			t.Fatalf("解码 next_cursor: %v", err)
		}
		if decoded.ID != next.ID {
			t.Fatalf("next cursor id = %v, want %v", decoded.ID, next.ID)
		}
	})

	t.Run("显式 limit 与 cursor", func(t *testing.T) {
		var gotLimit int
		var gotCursor *knowledge.Cursor
		svc := &fakeCollectionService{
			list: func(_ context.Context, cursor *knowledge.Cursor, limit int) ([]*knowledge.Collection, *knowledge.Cursor, error) {
				gotCursor, gotLimit = cursor, limit
				return []*knowledge.Collection{}, nil, nil
			},
		}
		s := newTestServerWithCollections(t, nil, svc)

		encoded, err := encodeListCursor(next)
		if err != nil {
			t.Fatalf("编码 cursor: %v", err)
		}
		rec := do(t, s, http.MethodGet, collectionsPath+"?limit=1&cursor="+encoded)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if gotLimit != 1 || gotCursor == nil || gotCursor.ID != next.ID {
			t.Fatalf("service 收到 limit=%d cursor=%+v", gotLimit, gotCursor)
		}
		body := decodeBody[collectionListResponse](t, rec)
		if body.NextCursor != nil || body.Items == nil {
			t.Fatalf("响应 = %+v，items 应为空数组、next_cursor 应为 null", body)
		}
	})

	t.Run("非法 cursor", func(t *testing.T) {
		s := newTestServerWithCollections(t, nil, &fakeCollectionService{})
		rec := do(t, s, http.MethodGet, collectionsPath+"?cursor=bad")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("非法 limit", func(t *testing.T) {
		s := newTestServerWithCollections(t, nil, &fakeCollectionService{})
		rec := do(t, s, http.MethodGet, collectionsPath+"?limit=abc")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})
}

func TestCollectionUpdate(t *testing.T) {
	t.Run("只改 name", func(t *testing.T) {
		id := uuid.New()
		var gotPatch knowledge.CollectionUpdate
		svc := &fakeCollectionService{
			update: func(_ context.Context, gotID uuid.UUID, patch knowledge.CollectionUpdate) (*knowledge.Collection, error) {
				if gotID != id {
					t.Fatalf("service 收到 id=%v, want %v", gotID, id)
				}
				gotPatch = patch
				return testCollection(id), nil
			},
		}
		s := newTestServerWithCollections(t, nil, svc)

		rec := doJSON(t, s, http.MethodPatch, collectionsPath+"/"+id.String(), `{"name":" 新名字 "}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if gotPatch.Name == nil || *gotPatch.Name != " 新名字 " || gotPatch.Description != nil {
			t.Fatalf("patch = %+v", gotPatch)
		}
	})

	t.Run("空对象", func(t *testing.T) {
		id := uuid.New()
		var gotPatch knowledge.CollectionUpdate
		svc := &fakeCollectionService{
			update: func(_ context.Context, _ uuid.UUID, patch knowledge.CollectionUpdate) (*knowledge.Collection, error) {
				gotPatch = patch
				return testCollection(id), nil
			},
		}
		s := newTestServerWithCollections(t, nil, svc)

		rec := doJSON(t, s, http.MethodPatch, collectionsPath+"/"+id.String(), `{}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if gotPatch.Name != nil || gotPatch.Description != nil {
			t.Fatalf("空 patch = %+v", gotPatch)
		}
	})

	t.Run("未知字段", func(t *testing.T) {
		s := newTestServerWithCollections(t, nil, &fakeCollectionService{})
		rec := doJSON(t, s, http.MethodPatch, collectionsPath+"/"+uuid.New().String(), `{"unknown":1}`)
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("不存在", func(t *testing.T) {
		svc := &fakeCollectionService{
			update: func(context.Context, uuid.UUID, knowledge.CollectionUpdate) (*knowledge.Collection, error) {
				return nil, apperr.New(apperr.CodeNotFound, "知识库不存在")
			},
		}
		s := newTestServerWithCollections(t, nil, svc)
		rec := doJSON(t, s, http.MethodPatch, collectionsPath+"/"+uuid.New().String(), `{"name":"x"}`)
		assertHTTPError(t, rec, http.StatusNotFound, apperr.CodeNotFound)
	})
}

func TestCollectionDelete(t *testing.T) {
	id := uuid.New()
	var gotID uuid.UUID
	svc := &fakeCollectionService{
		delete: func(_ context.Context, got uuid.UUID) error {
			gotID = got
			return nil
		},
	}
	s := newTestServerWithCollections(t, nil, svc)

	rec := do(t, s, http.MethodDelete, collectionsPath+"/"+id.String())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if gotID != id {
		t.Fatalf("service 收到 id=%v, want %v", gotID, id)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 不应有响应体: %s", rec.Body.String())
	}
}

func TestCollectionDeleteErrors(t *testing.T) {
	t.Run("非法 UUID", func(t *testing.T) {
		s := newTestServerWithCollections(t, nil, &fakeCollectionService{})
		rec := do(t, s, http.MethodDelete, collectionsPath+"/not-a-uuid")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("不存在", func(t *testing.T) {
		svc := &fakeCollectionService{
			delete: func(context.Context, uuid.UUID) error {
				return apperr.New(apperr.CodeNotFound, "知识库不存在")
			},
		}
		s := newTestServerWithCollections(t, nil, svc)
		rec := do(t, s, http.MethodDelete, collectionsPath+"/"+uuid.New().String())
		assertHTTPError(t, rec, http.StatusNotFound, apperr.CodeNotFound)
	})
}

func TestCollectionClientCanceled(t *testing.T) {
	svc := &fakeCollectionService{
		get: func(context.Context, uuid.UUID) (*knowledge.Collection, error) {
			return nil, fmt.Errorf("query: %w", context.Canceled)
		},
	}
	s := newTestServerWithCollections(t, nil, svc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, collectionsPath+"/"+uuid.New().String(), nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Body.Len() != 0 {
		t.Fatalf("客户端取消时不应写响应体，got %q", rec.Body.String())
	}
}

func TestCollectionCursorCodec(t *testing.T) {
	cursor := &knowledge.Cursor{
		CreatedAt: time.Date(2026, 10, 18, 2, 25, 39, 123*int(time.Millisecond), time.UTC),
		ID:        uuid.MustParse("01a14b3e-1b29-759b-b7c6-a9094bab7487"),
	}

	encoded, err := encodeListCursor(cursor)
	if err != nil {
		t.Fatalf("encodeListCursor: %v", err)
	}
	if len(encoded) != 32 {
		t.Fatalf("cursor 长度 = %d, want 32（24 字节 base64url）", len(encoded))
	}

	decoded, err := decodeListCursor(encoded)
	if err != nil {
		t.Fatalf("decodeListCursor: %v", err)
	}
	if decoded.ID != cursor.ID || !decoded.CreatedAt.Equal(cursor.CreatedAt) {
		t.Fatalf("cursor 往返 = %+v, want %+v", decoded, cursor)
	}

	if _, err := decodeListCursor("!!!"); err == nil || apperr.Code(err) != apperr.CodeInvalidArgument {
		t.Fatalf("非法 cursor 应返回 invalid_argument, got %v", err)
	}
	if _, err := encodeListCursor(nil); err != nil {
		t.Fatalf("nil cursor 不应报错: %v", err)
	}
}

func TestCollectionRestore(t *testing.T) {
	id := uuid.New()

	t.Run("成功", func(t *testing.T) {
		var gotID uuid.UUID
		svc := &fakeCollectionService{
			restore: func(_ context.Context, got uuid.UUID) (*knowledge.Collection, error) {
				gotID = got
				return testCollection(id), nil
			},
		}
		s := newTestServerWithCollections(t, nil, svc)
		rec := do(t, s, http.MethodPost, collectionsPath+"/"+id.String()+"/restore")
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if gotID != id {
			t.Fatalf("service 收到 id=%v, want %v", gotID, id)
		}
		body := decodeBody[collectionResponse](t, rec)
		if body.ID != id.String() {
			t.Fatalf("响应体 = %+v", body)
		}
	})

	t.Run("非法 UUID", func(t *testing.T) {
		s := newTestServerWithCollections(t, nil, &fakeCollectionService{})
		rec := do(t, s, http.MethodPost, collectionsPath+"/bad/restore")
		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})

	t.Run("本来就没删 409", func(t *testing.T) {
		svc := &fakeCollectionService{
			restore: func(context.Context, uuid.UUID) (*knowledge.Collection, error) {
				return nil, apperr.New(apperr.CodeConflict, "知识库未被软删")
			},
		}
		s := newTestServerWithCollections(t, nil, svc)
		rec := do(t, s, http.MethodPost, collectionsPath+"/"+id.String()+"/restore")
		assertHTTPError(t, rec, http.StatusConflict, apperr.CodeConflict)
	})

	t.Run("不存在 404", func(t *testing.T) {
		svc := &fakeCollectionService{
			restore: func(context.Context, uuid.UUID) (*knowledge.Collection, error) {
				return nil, apperr.New(apperr.CodeNotFound, "知识库不存在")
			},
		}
		s := newTestServerWithCollections(t, nil, svc)
		rec := do(t, s, http.MethodPost, collectionsPath+"/"+id.String()+"/restore")
		assertHTTPError(t, rec, http.StatusNotFound, apperr.CodeNotFound)
	})
}
