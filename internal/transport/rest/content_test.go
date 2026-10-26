package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
)

type fakeContentService struct {
	replace func(context.Context, uuid.UUID, string) error
}

var _ ContentService = (*fakeContentService)(nil)

func (f *fakeContentService) ReplaceDocumentContent(ctx context.Context, documentID uuid.UUID, content string) error {
	if f.replace == nil {
		panic("unexpected ReplaceDocumentContent call")
	}
	return f.replace(ctx, documentID, content)
}

func TestReplaceContent(t *testing.T) {
	id := uuid.New()
	var (
		gotID      uuid.UUID
		gotContent string
	)
	svc := &fakeContentService{
		replace: func(_ context.Context, documentID uuid.UUID, content string) error {
			gotID = documentID
			gotContent = content
			return nil
		},
	}
	s := newTestServerWithContent(t, nil, svc)

	rec := doJSON(t, s, http.MethodPut,
		documentsPath+"/"+id.String()+"/content",
		`{"content":"第一段。第二段。"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 不应有响应体, got %q", rec.Body.String())
	}
	if gotID != id {
		t.Fatalf("documentID = %v, want %v", gotID, id)
	}
	if gotContent != "第一段。第二段。" {
		t.Fatalf("content = %q", gotContent)
	}
}

func TestReplaceContentServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "参数错误",
			serviceErr: apperr.New(apperr.CodeInvalidArgument, "正文不能为空"),
			wantStatus: http.StatusBadRequest,
			wantCode:   apperr.CodeInvalidArgument,
		},
		{
			name:       "文档不存在",
			serviceErr: apperr.New(apperr.CodeNotFound, "文档不存在"),
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
			id := uuid.New()
			svc := &fakeContentService{
				replace: func(context.Context, uuid.UUID, string) error {
					return tt.serviceErr
				},
			}
			s := newTestServerWithContent(t, nil, svc)

			rec := doJSON(t, s, http.MethodPut,
				documentsPath+"/"+id.String()+"/content",
				`{"content":"正文"}`)

			assertHTTPError(t, rec, tt.wantStatus, tt.wantCode)
		})
	}
}

func TestReplaceContentRejectsInvalidJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "空请求体", body: ""},
		{name: "非法 JSON", body: `{"content":`},
		{name: "未知字段", body: `{"content":"正文","extra":1}`},
		{name: "多个 JSON 对象", body: `{"content":"一"}{"content":"二"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := uuid.New()
			// fake 的 replace 为 nil：如果 handler 错误地调用 service，会 panic。
			s := newTestServerWithContent(t, nil, &fakeContentService{})

			rec := doJSON(t, s, http.MethodPut,
				documentsPath+"/"+id.String()+"/content",
				tt.body)

			assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
		})
	}
}

func TestReplaceContentRejectsInvalidDocumentID(t *testing.T) {
	s := newTestServerWithContent(t, nil, &fakeContentService{})

	rec := doJSON(t, s, http.MethodPut,
		documentsPath+"/not-a-uuid/content",
		`{"content":"正文"}`)

	assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
}

func TestReplaceContentBodyLimit(t *testing.T) {
	t.Run("1 MiB 正文可以通过（证明没有复用 1 MiB 的全局上限）", func(t *testing.T) {
		id := uuid.New()
		var got string
		svc := &fakeContentService{
			replace: func(_ context.Context, _ uuid.UUID, content string) error {
				got = content
				return nil
			},
		}
		s := newTestServerWithContent(t, nil, svc)

		content := strings.Repeat("a", 1<<20)
		rec := doJSON(t, s, http.MethodPut,
			documentsPath+"/"+id.String()+"/content",
			`{"content":"`+content+`"}`)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("状态码 = %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
		if got != content {
			t.Fatalf("content 长度 = %d, want %d", len(got), len(content))
		}
	})

	t.Run("超过 2 MiB 请求体被拒绝", func(t *testing.T) {
		id := uuid.New()
		// replace 为 nil：超限请求必须在进入 service 前被拒绝。
		s := newTestServerWithContent(t, nil, &fakeContentService{})

		content := strings.Repeat("a", maxContentRequestBytes)
		rec := doJSON(t, s, http.MethodPut,
			documentsPath+"/"+id.String()+"/content",
			`{"content":"`+content+`"}`)

		assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
	})
}
