package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
)

// fakeDocumentRepository 用函数字段注入行为；没设置的函数被调用时直接 panic，
// 避免测试悄悄走到没预期的分支。
type fakeDocumentRepository struct {
	createDocument  func(context.Context, string, *string, *string, *int64) (*Document, error)
	getDocument     func(context.Context, uuid.UUID) (*Document, error)
	updateDocument  func(context.Context, uuid.UUID, DocumentUpdate) (*Document, error)
	softDelete      func(context.Context, uuid.UUID) error
	restoreDocument func(context.Context, uuid.UUID) (*Document, error)
	add             func(context.Context, uuid.UUID, uuid.UUID) (*CollectionDocument, error)
	remove          func(context.Context, uuid.UUID, uuid.UUID) error
	list            func(context.Context, uuid.UUID, *Cursor, int) ([]*CollectionDocument, *Cursor, error)
}

var _ DocumentRepository = (*fakeDocumentRepository)(nil)

func (f *fakeDocumentRepository) CreateDocument(ctx context.Context, title string, sourceURI, mimeType *string, sizeBytes *int64) (*Document, error) {
	if f.createDocument == nil {
		panic("unexpected CreateDocument call")
	}
	return f.createDocument(ctx, title, sourceURI, mimeType, sizeBytes)
}

func (f *fakeDocumentRepository) GetDocument(ctx context.Context, id uuid.UUID) (*Document, error) {
	if f.getDocument == nil {
		panic("unexpected GetDocument call")
	}
	return f.getDocument(ctx, id)
}

func (f *fakeDocumentRepository) UpdateDocument(ctx context.Context, id uuid.UUID, patch DocumentUpdate) (*Document, error) {
	if f.updateDocument == nil {
		panic("unexpected UpdateDocument call")
	}
	return f.updateDocument(ctx, id, patch)
}

func (f *fakeDocumentRepository) SoftDeleteDocument(ctx context.Context, id uuid.UUID) error {
	if f.softDelete == nil {
		panic("unexpected SoftDeleteDocument call")
	}
	return f.softDelete(ctx, id)
}

func (f *fakeDocumentRepository) RestoreDocument(ctx context.Context, id uuid.UUID) (*Document, error) {
	if f.restoreDocument == nil {
		panic("unexpected RestoreDocument call")
	}
	return f.restoreDocument(ctx, id)
}

func (f *fakeDocumentRepository) AddDocumentToCollection(ctx context.Context, collectionID, documentID uuid.UUID) (*CollectionDocument, error) {
	if f.add == nil {
		panic("unexpected AddDocumentToCollection call")
	}
	return f.add(ctx, collectionID, documentID)
}

func (f *fakeDocumentRepository) RemoveDocumentFromCollection(ctx context.Context, collectionID, documentID uuid.UUID) error {
	if f.remove == nil {
		panic("unexpected RemoveDocumentFromCollection call")
	}
	return f.remove(ctx, collectionID, documentID)
}

func (f *fakeDocumentRepository) ListDocumentsByCollection(ctx context.Context, collectionID uuid.UUID, cursor *Cursor, limit int) ([]*CollectionDocument, *Cursor, error) {
	if f.list == nil {
		panic("unexpected ListDocumentsByCollection call")
	}
	return f.list(ctx, collectionID, cursor, limit)
}

func TestNewDocumentServiceRejectsNilRepository(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewDocumentService(nil) 应当 panic")
		}
	}()

	NewDocumentService(nil)
}

func TestNormalizeDocumentTitle(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     string
		wantCode string
	}{
		{name: "trim", input: "  设计文档  ", want: "设计文档"},
		{name: "空串", input: "", wantCode: apperr.CodeInvalidArgument},
		{name: "全空白", input: " \t\n", wantCode: apperr.CodeInvalidArgument},
		{
			name:  "512 rune 合法",
			input: strings.Repeat("字", MaxDocumentTitleRunes),
			want:  strings.Repeat("字", MaxDocumentTitleRunes),
		},
		{
			name:     "513 rune 超长",
			input:    strings.Repeat("字", MaxDocumentTitleRunes+1),
			wantCode: apperr.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeDocumentTitle(tt.input)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode == "" && got != tt.want {
				t.Fatalf("normalizeDocumentTitle = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateOptionalString(t *testing.T) {
	tests := []struct {
		name     string
		value    *string
		max      int
		wantCode string
	}{
		{name: "nil 合法", value: nil, max: 8},
		{name: "等长合法", value: stringPtr("12345678"), max: 8},
		{name: "超长", value: stringPtr("123456789"), max: 8, wantCode: apperr.CodeInvalidArgument},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorCode(t, validateOptionalString("field", tt.value, tt.max), tt.wantCode)
		})
	}
}

func TestValidateDocumentSize(t *testing.T) {
	assertErrorCode(t, validateDocumentSize(nil), "")
	assertErrorCode(t, validateDocumentSize(int64Ptr(0)), "")
	assertErrorCode(t, validateDocumentSize(int64Ptr(-1)), apperr.CodeInvalidArgument)
}

func TestServiceCreateDocument(t *testing.T) {
	validSource := "s3://knowflow/docs/design.pdf"
	validMime := "application/pdf"
	validSize := int64(1024)
	longSource := strings.Repeat("a", MaxDocumentSourceURIRunes)
	longMime := strings.Repeat("m", MaxDocumentMimeTypeRunes)

	tests := []struct {
		name      string
		title     string
		source    *string
		mime      *string
		size      *int64
		repoErr   error
		wantCode  string
		wantTitle string
	}{
		{name: "空 title", title: "   ", wantCode: apperr.CodeInvalidArgument},
		{name: "正常并 trim", title: "  设计文档  ", source: &validSource, mime: &validMime, size: &validSize, wantTitle: "设计文档"},
		{
			name:      "512 rune title 合法",
			title:     strings.Repeat("字", MaxDocumentTitleRunes),
			wantTitle: strings.Repeat("字", MaxDocumentTitleRunes),
		},
		{
			name:     "513 rune title 超长",
			title:    strings.Repeat("字", MaxDocumentTitleRunes+1),
			wantCode: apperr.CodeInvalidArgument,
		},
		{name: "source_uri 2048 合法", title: "文档", source: &longSource, wantTitle: "文档"},
		{
			name:     "source_uri 2049 超长",
			title:    "文档",
			source:   stringPtr(longSource + "a"),
			wantCode: apperr.CodeInvalidArgument,
		},
		{name: "mime_type 128 合法", title: "文档", mime: &longMime, wantTitle: "文档"},
		{
			name:     "mime_type 129 超长",
			title:    "文档",
			mime:     stringPtr(longMime + "m"),
			wantCode: apperr.CodeInvalidArgument,
		},
		{name: "size 0 合法", title: "文档", size: int64Ptr(0), wantTitle: "文档"},
		{name: "size 负数", title: "文档", size: int64Ptr(-1), wantCode: apperr.CodeInvalidArgument},
		{name: "repo ErrNotFound", title: "文档", repoErr: ErrNotFound, wantCode: apperr.CodeNotFound},
		{name: "repo ErrConflict", title: "文档", repoErr: ErrConflict, wantCode: apperr.CodeConflict},
		{name: "repo 内部错误", title: "文档", repoErr: errors.New("db down"), wantCode: apperr.CodeInternal},
		{name: "成功且可空字段为 nil", title: "文档", wantTitle: "文档"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotTitle string
			var gotSource, gotMime *string
			var gotSize *int64
			repo := &fakeDocumentRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.createDocument = func(_ context.Context, title string, sourceURI, mimeType *string, sizeBytes *int64) (*Document, error) {
					gotTitle, gotSource, gotMime, gotSize = title, sourceURI, mimeType, sizeBytes
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return &Document{ID: uuid.New(), Title: title, SourceURI: sourceURI, MimeType: mimeType, SizeBytes: sizeBytes}, nil
				}
			}

			got, err := NewDocumentService(repo).CreateDocument(context.Background(), tt.title, tt.source, tt.mime, tt.size)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode != "" {
				return
			}
			if got == nil || gotTitle != tt.wantTitle {
				t.Fatalf("CreateDocument = %+v, title=%q want %q", got, gotTitle, tt.wantTitle)
			}
			if !sameStringPtr(gotSource, tt.source) || !sameStringPtr(gotMime, tt.mime) || !sameInt64Ptr(gotSize, tt.size) {
				t.Fatalf("可空字段被改写: got=(%v,%v,%v) want=(%v,%v,%v)",
					gotSource, gotMime, gotSize, tt.source, tt.mime, tt.size)
			}
		})
	}
}

func TestServiceCreateDocumentNormalizesEmptyOptionalStrings(t *testing.T) {
	var gotSource, gotMime *string
	repo := &fakeDocumentRepository{}
	repo.createDocument = func(_ context.Context, title string, sourceURI, mimeType *string, sizeBytes *int64) (*Document, error) {
		gotSource, gotMime = sourceURI, mimeType
		return &Document{ID: uuid.New(), Title: title}, nil
	}

	_, err := NewDocumentService(repo).CreateDocument(
		context.Background(), "文档", stringPtr("   "), stringPtr("\t"), nil)
	assertErrorCode(t, err, "")
	if gotSource != nil || gotMime != nil {
		t.Fatalf("空串/纯空白应折叠为 nil, got source=%v mime=%v", gotSource, gotMime)
	}
}

func TestServiceUpdateDocumentNormalizesEmptyOptionalStrings(t *testing.T) {
	var got DocumentUpdate
	repo := &fakeDocumentRepository{}
	repo.updateDocument = func(_ context.Context, id uuid.UUID, patch DocumentUpdate) (*Document, error) {
		got = patch
		return &Document{ID: id}, nil
	}

	_, err := NewDocumentService(repo).UpdateDocument(context.Background(), uuid.New(), DocumentUpdate{
		SourceURI: Optional[string]{Set: true, Value: stringPtr("   ")},
		MimeType:  Optional[string]{Set: true, Value: stringPtr("")},
	})
	assertErrorCode(t, err, "")
	if !got.SourceURI.Set || got.SourceURI.Value != nil {
		t.Fatalf("source_uri 空串应折叠为 NULL, got %+v", got.SourceURI)
	}
	if !got.MimeType.Set || got.MimeType.Value != nil {
		t.Fatalf("mime_type 空串应折叠为 NULL, got %+v", got.MimeType)
	}
}

func TestServiceGetDocument(t *testing.T) {
	tests := []struct {
		name     string
		id       uuid.UUID
		repoErr  error
		wantCode string
	}{
		{name: "uuid.Nil", id: uuid.Nil, wantCode: apperr.CodeInvalidArgument},
		{name: "repo ErrNotFound", id: uuid.New(), repoErr: ErrNotFound, wantCode: apperr.CodeNotFound},
		{name: "repo 内部错误", id: uuid.New(), repoErr: errors.New("db down"), wantCode: apperr.CodeInternal},
		{name: "成功", id: uuid.New()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeDocumentRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.getDocument = func(_ context.Context, id uuid.UUID) (*Document, error) {
					if id != tt.id {
						t.Fatalf("repo 收到 id=%v, want %v", id, tt.id)
					}
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return &Document{ID: id, Title: "文档"}, nil
				}
			}

			got, err := NewDocumentService(repo).GetDocument(context.Background(), tt.id)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode == "" && (got == nil || got.ID != tt.id) {
				t.Fatalf("GetDocument = %+v, want id=%v", got, tt.id)
			}
		})
	}
}

func TestServiceUpdateDocument(t *testing.T) {
	id := uuid.New()
	longSource := strings.Repeat("a", MaxDocumentSourceURIRunes+1)
	longMime := strings.Repeat("m", MaxDocumentMimeTypeRunes+1)

	tests := []struct {
		name       string
		id         uuid.UUID
		patch      DocumentUpdate
		repoErr    error
		wantCode   string
		wantTitle  *string
		wantSource Optional[string]
		wantMime   Optional[string]
		wantSize   Optional[int64]
	}{
		{name: "uuid.Nil", id: uuid.Nil, wantCode: apperr.CodeInvalidArgument},
		{
			name:     "title 全空白",
			id:       id,
			patch:    DocumentUpdate{Title: stringPtr("   ")},
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "source_uri 超长",
			id:       id,
			patch:    DocumentUpdate{SourceURI: Optional[string]{Set: true, Value: &longSource}},
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "mime_type 超长",
			id:       id,
			patch:    DocumentUpdate{MimeType: Optional[string]{Set: true, Value: &longMime}},
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "size_bytes 负数",
			id:       id,
			patch:    DocumentUpdate{SizeBytes: Optional[int64]{Set: true, Value: int64Ptr(-1)}},
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "repo ErrNotFound",
			id:       id,
			patch:    DocumentUpdate{Title: stringPtr("新标题")},
			repoErr:  ErrNotFound,
			wantCode: apperr.CodeNotFound,
		},
		{
			name:     "repo ErrConflict",
			id:       id,
			patch:    DocumentUpdate{Title: stringPtr("新标题")},
			repoErr:  ErrConflict,
			wantCode: apperr.CodeConflict,
		},
		{
			name:     "repo 内部错误",
			id:       id,
			patch:    DocumentUpdate{Title: stringPtr("新标题")},
			repoErr:  errors.New("db down"),
			wantCode: apperr.CodeInternal,
		},
		{name: "空 patch", id: id},
		{
			name:      "title 被 trim",
			id:        id,
			patch:     DocumentUpdate{Title: stringPtr("  新标题  ")},
			wantTitle: stringPtr("新标题"),
		},
		{
			name:       "清空 source_uri",
			id:         id,
			patch:      DocumentUpdate{SourceURI: Optional[string]{Set: true}},
			wantSource: Optional[string]{Set: true},
		},
		{
			name:       "设置 size_bytes 0",
			id:         id,
			patch:      DocumentUpdate{SizeBytes: Optional[int64]{Set: true, Value: int64Ptr(0)}},
			wantSize:   Optional[int64]{Set: true, Value: int64Ptr(0)},
			wantSource: Optional[string]{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPatch DocumentUpdate
			repo := &fakeDocumentRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.updateDocument = func(_ context.Context, gotID uuid.UUID, patch DocumentUpdate) (*Document, error) {
					if gotID != tt.id {
						t.Fatalf("repo 收到 id=%v, want %v", gotID, tt.id)
					}
					gotPatch = patch
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return &Document{ID: gotID, Title: "文档"}, nil
				}
			}

			got, err := NewDocumentService(repo).UpdateDocument(context.Background(), tt.id, tt.patch)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode != "" {
				return
			}
			if got == nil {
				t.Fatal("UpdateDocument 返回了 nil Document")
			}
			if !sameStringPtr(gotPatch.Title, tt.wantTitle) {
				t.Fatalf("repo 收到 title=%v, want %v", gotPatch.Title, tt.wantTitle)
			}
			if !sameOptionalString(gotPatch.SourceURI, tt.wantSource) {
				t.Fatalf("repo 收到 source_uri=%+v, want %+v", gotPatch.SourceURI, tt.wantSource)
			}
			if !sameOptionalString(gotPatch.MimeType, tt.wantMime) {
				t.Fatalf("repo 收到 mime_type=%+v, want %+v", gotPatch.MimeType, tt.wantMime)
			}
			if !sameOptionalInt64(gotPatch.SizeBytes, tt.wantSize) {
				t.Fatalf("repo 收到 size_bytes=%+v, want %+v", gotPatch.SizeBytes, tt.wantSize)
			}
		})
	}
}

func TestServiceRestoreDocument(t *testing.T) {
	tests := []struct {
		name     string
		id       uuid.UUID
		repoErr  error
		wantCode string
	}{
		{name: "uuid.Nil", id: uuid.Nil, wantCode: apperr.CodeInvalidArgument},
		{name: "repo ErrNotFound", id: uuid.New(), repoErr: ErrNotFound, wantCode: apperr.CodeNotFound},
		{name: "repo ErrConflict", id: uuid.New(), repoErr: ErrConflict, wantCode: apperr.CodeConflict},
		{name: "repo 内部错误", id: uuid.New(), repoErr: errors.New("db down"), wantCode: apperr.CodeInternal},
		{name: "成功", id: uuid.New()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeDocumentRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.restoreDocument = func(_ context.Context, id uuid.UUID) (*Document, error) {
					if id != tt.id {
						t.Fatalf("repo 收到 id=%v, want %v", id, tt.id)
					}
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return &Document{ID: id, Title: "恢复"}, nil
				}
			}

			got, err := NewDocumentService(repo).RestoreDocument(context.Background(), tt.id)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode == "" && (got == nil || got.ID != tt.id) {
				t.Fatalf("RestoreDocument = %+v, want id=%v", got, tt.id)
			}
		})
	}
}

func TestServiceSoftDeleteDocument(t *testing.T) {
	tests := []struct {
		name     string
		id       uuid.UUID
		repoErr  error
		wantCode string
	}{
		{name: "uuid.Nil", id: uuid.Nil, wantCode: apperr.CodeInvalidArgument},
		{name: "repo ErrNotFound", id: uuid.New(), repoErr: ErrNotFound, wantCode: apperr.CodeNotFound},
		{name: "repo 内部错误", id: uuid.New(), repoErr: errors.New("db down"), wantCode: apperr.CodeInternal},
		{name: "成功", id: uuid.New()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeDocumentRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.softDelete = func(_ context.Context, id uuid.UUID) error {
					if id != tt.id {
						t.Fatalf("repo 收到 id=%v, want %v", id, tt.id)
					}
					return tt.repoErr
				}
			}
			assertErrorCode(t, NewDocumentService(repo).SoftDeleteDocument(context.Background(), tt.id), tt.wantCode)
		})
	}
}

func TestServiceAddDocumentToCollection(t *testing.T) {
	collectionID, documentID := uuid.New(), uuid.New()
	tests := []struct {
		name         string
		collectionID uuid.UUID
		documentID   uuid.UUID
		repoErr      error
		wantCode     string
	}{
		{name: "collection uuid.Nil", collectionID: uuid.Nil, documentID: documentID, wantCode: apperr.CodeInvalidArgument},
		{name: "document uuid.Nil", collectionID: collectionID, documentID: uuid.Nil, wantCode: apperr.CodeInvalidArgument},
		{name: "repo ErrNotFound", collectionID: collectionID, documentID: documentID, repoErr: ErrNotFound, wantCode: apperr.CodeNotFound},
		{name: "repo ErrConflict", collectionID: collectionID, documentID: documentID, repoErr: ErrConflict, wantCode: apperr.CodeConflict},
		{name: "repo 内部错误", collectionID: collectionID, documentID: documentID, repoErr: errors.New("db down"), wantCode: apperr.CodeInternal},
		{name: "成功", collectionID: collectionID, documentID: documentID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeDocumentRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.add = func(_ context.Context, cid, did uuid.UUID) (*CollectionDocument, error) {
					if cid != tt.collectionID || did != tt.documentID {
						t.Fatalf("repo 收到 (%v,%v), want (%v,%v)", cid, did, tt.collectionID, tt.documentID)
					}
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return &CollectionDocument{Document: &Document{ID: did}, AddedAt: time.Now().UTC()}, nil
				}
			}
			got, err := NewDocumentService(repo).AddDocumentToCollection(context.Background(), tt.collectionID, tt.documentID)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode == "" && (got == nil || got.Document == nil || got.Document.ID != documentID) {
				t.Fatalf("AddDocumentToCollection = %+v", got)
			}
		})
	}
}

func TestServiceRemoveDocumentFromCollection(t *testing.T) {
	collectionID, documentID := uuid.New(), uuid.New()
	tests := []struct {
		name         string
		collectionID uuid.UUID
		documentID   uuid.UUID
		repoErr      error
		wantCode     string
	}{
		{name: "collection uuid.Nil", collectionID: uuid.Nil, documentID: documentID, wantCode: apperr.CodeInvalidArgument},
		{name: "document uuid.Nil", collectionID: collectionID, documentID: uuid.Nil, wantCode: apperr.CodeInvalidArgument},
		{name: "repo ErrNotFound", collectionID: collectionID, documentID: documentID, repoErr: ErrNotFound, wantCode: apperr.CodeNotFound},
		{name: "repo 内部错误", collectionID: collectionID, documentID: documentID, repoErr: errors.New("db down"), wantCode: apperr.CodeInternal},
		{name: "成功", collectionID: collectionID, documentID: documentID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeDocumentRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.remove = func(_ context.Context, cid, did uuid.UUID) error {
					if cid != tt.collectionID || did != tt.documentID {
						t.Fatalf("repo 收到 (%v,%v), want (%v,%v)", cid, did, tt.collectionID, tt.documentID)
					}
					return tt.repoErr
				}
			}
			assertErrorCode(t,
				NewDocumentService(repo).RemoveDocumentFromCollection(context.Background(), tt.collectionID, tt.documentID),
				tt.wantCode)
		})
	}
}

func TestServiceListDocumentsByCollection(t *testing.T) {
	collectionID := uuid.New()
	validCursor := &Cursor{CreatedAt: time.Now().UTC(), ID: uuid.New()}
	next := &Cursor{CreatedAt: time.Now().UTC(), ID: uuid.New()}
	item := &CollectionDocument{Document: &Document{ID: uuid.New()}, AddedAt: time.Now().UTC()}

	t.Run("collection uuid.Nil", func(t *testing.T) {
		_, _, err := NewDocumentService(&fakeDocumentRepository{}).ListDocumentsByCollection(
			context.Background(), uuid.Nil, nil, 20)
		assertErrorCode(t, err, apperr.CodeInvalidArgument)
	})

	tests := []struct {
		name     string
		limit    int
		cursor   *Cursor
		repoErr  error
		items    []*CollectionDocument
		next     *Cursor
		wantCode string
	}{
		{name: "limit 0", limit: 0, wantCode: apperr.CodeInvalidArgument},
		{name: "limit 101", limit: MaxDocumentListLimit + 1, wantCode: apperr.CodeInvalidArgument},
		{
			name:     "cursor 零值时间",
			limit:    20,
			cursor:   &Cursor{ID: uuid.New()},
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "cursor 零值 ID",
			limit:    20,
			cursor:   &Cursor{CreatedAt: time.Now().UTC()},
			wantCode: apperr.CodeInvalidArgument,
		},
		{name: "repo ErrNotFound", limit: 20, repoErr: ErrNotFound, wantCode: apperr.CodeNotFound},
		{name: "repo 内部错误", limit: 20, repoErr: errors.New("db down"), wantCode: apperr.CodeInternal},
		{name: "空页", limit: 20},
		{name: "有下一页", limit: 20, cursor: validCursor, items: []*CollectionDocument{item}, next: next},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeDocumentRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.list = func(_ context.Context, gotCollectionID uuid.UUID, cursor *Cursor, limit int) ([]*CollectionDocument, *Cursor, error) {
					if gotCollectionID != collectionID {
						t.Fatalf("repo 收到 collectionID=%v, want %v", gotCollectionID, collectionID)
					}
					if cursor != tt.cursor {
						t.Fatalf("repo 收到 cursor=%+v, want %+v", cursor, tt.cursor)
					}
					if limit != tt.limit {
						t.Fatalf("repo 收到 limit=%d, want %d", limit, tt.limit)
					}
					if tt.repoErr != nil {
						return nil, nil, tt.repoErr
					}
					return tt.items, tt.next, nil
				}
			}

			got, gotNext, err := NewDocumentService(repo).ListDocumentsByCollection(
				context.Background(), collectionID, tt.cursor, tt.limit)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode != "" {
				return
			}
			if got == nil {
				t.Fatal("ListDocumentsByCollection 应把 nil 切片规范成空切片")
			}
			if len(got) != len(tt.items) {
				t.Fatalf("items 长度 = %d, want %d", len(got), len(tt.items))
			}
			if gotNext != tt.next {
				t.Fatalf("next = %+v, want %+v", gotNext, tt.next)
			}
		})
	}
}

func TestWrapDocumentRepoError(t *testing.T) {
	if err := wrapDocumentRepoError("查询文档失败", "文档不存在", nil); err != nil {
		t.Fatalf("nil error 应返回 nil, got %v", err)
	}

	notFound := wrapDocumentRepoError("查询文档失败", "文档不存在", ErrNotFound)
	assertErrorCode(t, notFound, apperr.CodeNotFound)
	if !errors.Is(notFound, ErrNotFound) {
		t.Fatalf("not_found 错误应保留 ErrNotFound 原因链, got %v", notFound)
	}

	conflict := wrapDocumentRepoError("添加关联失败", "文档不存在", ErrConflict)
	assertErrorCode(t, conflict, apperr.CodeConflict)
	if !errors.Is(conflict, ErrConflict) {
		t.Fatalf("conflict 错误应保留 ErrConflict 原因链, got %v", conflict)
	}

	internal := wrapDocumentRepoError("查询文档失败", "文档不存在", errors.New("db down"))
	assertErrorCode(t, internal, apperr.CodeInternal)

	var appErr *apperr.Error
	if !errors.As(internal, &appErr) || appErr.Message != "查询文档失败" {
		t.Fatalf("internal 错误 = %+v", internal)
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameOptionalString(a, b Optional[string]) bool {
	return a.Set == b.Set && sameStringPtr(a.Value, b.Value)
}

func sameOptionalInt64(a, b Optional[int64]) bool {
	return a.Set == b.Set && sameInt64Ptr(a.Value, b.Value)
}
