package knowledge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
)

// fakeContentRepository 用函数字段注入行为；没设置的函数被调用时直接 panic，
// 避免测试悄悄走到没预期的分支。
type fakeContentRepository struct {
	replace func(context.Context, uuid.UUID, string, [32]byte, []ContentChunk) error
	get     func(context.Context, uuid.UUID) (*DocumentContent, error)
}

var _ ContentRepository = (*fakeContentRepository)(nil)

func (f *fakeContentRepository) ReplaceDocumentContent(
	ctx context.Context,
	documentID uuid.UUID,
	content string,
	hash [32]byte,
	chunks []ContentChunk,
) error {
	if f.replace == nil {
		panic("unexpected ReplaceDocumentContent call")
	}
	return f.replace(ctx, documentID, content, hash, chunks)
}

func (f *fakeContentRepository) ListDocumentChunks(ctx context.Context, documentID uuid.UUID) ([]StoredChunk, error) {
	panic("unexpected ListDocumentChunks call")
}

func (f *fakeContentRepository) GetDocumentContent(ctx context.Context, documentID uuid.UUID) (*DocumentContent, error) {
	if f.get == nil {
		panic("unexpected GetDocumentContent call")
	}
	return f.get(ctx, documentID)
}

func TestNewContentServiceRejectsNilRepository(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewContentService(nil) 应当 panic")
		}
	}()

	NewContentService(nil)
}

func TestContentServiceReplaceDocumentContent(t *testing.T) {
	repoInternalErr := errors.New("db down")
	contentAtLimit := strings.Repeat("a", MaxDocumentContentBytes)
	contentOverLimit := contentAtLimit + "a"

	tests := []struct {
		name       string
		id         uuid.UUID
		content    string
		repoErr    error
		wantCode   string
		wantCalled bool
		wantCause  error
	}{
		{
			name:     "uuid.Nil",
			id:       uuid.Nil,
			content:  "正文",
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:      "空正文",
			id:        uuid.New(),
			content:   "",
			wantCode:  apperr.CodeInvalidArgument,
			wantCause: ErrContentEmpty,
		},
		{
			name:      "纯空白",
			id:        uuid.New(),
			content:   " \t\r\n\u3000",
			wantCode:  apperr.CodeInvalidArgument,
			wantCause: ErrContentEmpty,
		},
		{
			name:       "正文正好 1 MiB",
			id:         uuid.New(),
			content:    contentAtLimit,
			wantCalled: true,
		},
		{
			name:      "正文超过 1 MiB 一字节",
			id:        uuid.New(),
			content:   contentOverLimit,
			wantCode:  apperr.CodeInvalidArgument,
			wantCause: ErrContentTooLarge,
		},
		{
			name:       "repo ErrNotFound",
			id:         uuid.New(),
			content:    "正文",
			repoErr:    ErrNotFound,
			wantCode:   apperr.CodeNotFound,
			wantCalled: true,
			wantCause:  ErrNotFound,
		},
		{
			name:       "repo 内部错误",
			id:         uuid.New(),
			content:    "正文",
			repoErr:    repoInternalErr,
			wantCode:   apperr.CodeInternal,
			wantCalled: true,
			wantCause:  repoInternalErr,
		},
		{
			name:       "成功",
			id:         uuid.New(),
			content:    "第一段。第二段。",
			wantCalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			repo := &fakeContentRepository{
				replace: func(context.Context, uuid.UUID, string, [32]byte, []ContentChunk) error {
					called = true
					return tt.repoErr
				},
			}
			svc := NewContentService(repo)

			err := svc.ReplaceDocumentContent(context.Background(), tt.id, tt.content)
			assertErrorCode(t, err, tt.wantCode)

			if called != tt.wantCalled {
				t.Fatalf("repository 调用 = %v, want %v", called, tt.wantCalled)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Fatalf("错误原因链应保留 %v, got %v", tt.wantCause, err)
			}
		})
	}
}

func TestContentServiceReplaceDocumentContentPassesComputedValues(t *testing.T) {
	type ctxKey struct{}

	id := uuid.New()
	content := "  第一段\r\n第二段。  "
	ctx := context.WithValue(context.Background(), ctxKey{}, "trace-1")

	var (
		gotID      uuid.UUID
		gotContent string
		gotHash    [32]byte
		gotChunks  []ContentChunk
		gotTrace   any
	)
	repo := &fakeContentRepository{
		replace: func(c context.Context, documentID uuid.UUID, got string, hash [32]byte, chunks []ContentChunk) error {
			gotID = documentID
			gotContent = got
			gotHash = hash
			gotChunks = chunks
			gotTrace = c.Value(ctxKey{})
			return nil
		},
	}
	svc := NewContentService(repo)

	if err := svc.ReplaceDocumentContent(ctx, id, content); err != nil {
		t.Fatalf("ReplaceDocumentContent: %v", err)
	}

	if gotID != id {
		t.Fatalf("documentID = %v, want %v", gotID, id)
	}
	// service 不应该 trim 正文；缩进/换行是内容的一部分。
	if gotContent != content {
		t.Fatalf("content 被改了: %q, want %q", gotContent, content)
	}
	if want := sha256.Sum256([]byte(content)); gotHash != want {
		t.Fatalf("hash = %x, want %x", gotHash, want)
	}
	if gotTrace != "trace-1" {
		t.Fatalf("ctx 没有传给 repository: %v", gotTrace)
	}

	wantChunks, err := ChunkContent(content)
	if err != nil {
		t.Fatalf("ChunkContent: %v", err)
	}
	if !reflect.DeepEqual(gotChunks, wantChunks) {
		t.Fatalf("chunks 不一致:\n got=%+v\nwant=%+v", gotChunks, wantChunks)
	}
	if len(gotChunks) == 0 {
		t.Fatal("非空正文应至少产生一个 chunk")
	}
	if gotChunks[0].Index != 0 {
		t.Fatalf("第一个 chunk Index = %d, want 0", gotChunks[0].Index)
	}
}

func TestWrapContentChunkError(t *testing.T) {
	unknownErr := errors.New("chunker bug")
	tests := []struct {
		name      string
		err       error
		wantCode  string
		wantCause error
	}{
		{name: "nil", err: nil},
		{
			name:      "正文为空",
			err:       ErrContentEmpty,
			wantCode:  apperr.CodeInvalidArgument,
			wantCause: ErrContentEmpty,
		},
		{
			name:      "分块过多",
			err:       ErrTooManyChunks,
			wantCode:  apperr.CodeInvalidArgument,
			wantCause: ErrTooManyChunks,
		},
		{
			name:      "包装后的分块过多",
			err:       fmt.Errorf("chunker: %w", ErrTooManyChunks),
			wantCode:  apperr.CodeInvalidArgument,
			wantCause: ErrTooManyChunks,
		},
		{
			name:      "未知分块错误",
			err:       unknownErr,
			wantCode:  apperr.CodeInternal,
			wantCause: unknownErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapContentChunkError(tt.err)
			assertErrorCode(t, got, tt.wantCode)
			if tt.wantCause != nil && !errors.Is(got, tt.wantCause) {
				t.Fatalf("错误原因链应保留 %v, got %v", tt.wantCause, got)
			}
		})
	}
}

func TestWrapContentRepoError(t *testing.T) {
	unknownErr := errors.New("db down")
	tests := []struct {
		name      string
		err       error
		wantCode  string
		wantCause error
	}{
		{name: "nil", err: nil},
		{
			name:      "ErrNotFound",
			err:       ErrNotFound,
			wantCode:  apperr.CodeNotFound,
			wantCause: ErrNotFound,
		},
		{
			name:      "包装后的 ErrNotFound",
			err:       fmt.Errorf("repo: %w", ErrNotFound),
			wantCode:  apperr.CodeNotFound,
			wantCause: ErrNotFound,
		},
		{
			name:      "其他错误",
			err:       unknownErr,
			wantCode:  apperr.CodeInternal,
			wantCause: unknownErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapContentRepoError("替换文档正文失败", tt.err)
			assertErrorCode(t, got, tt.wantCode)
			if tt.wantCause != nil && !errors.Is(got, tt.wantCause) {
				t.Fatalf("错误原因链应保留 %v, got %v", tt.wantCause, got)
			}
		})
	}
}
