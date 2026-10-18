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

// fakeRepository 用函数字段按用例注入行为；没设置的函数被调用时直接 panic，
// 避免测试悄悄走到没预期的分支。
type fakeRepository struct {
	createCollection func(context.Context, string, string) (*Collection, error)
	getCollection    func(context.Context, uuid.UUID) (*Collection, error)
	listCollections  func(context.Context, *Cursor, int) ([]*Collection, *Cursor, error)
	updateCollection func(context.Context, uuid.UUID, CollectionUpdate) (*Collection, error)
	softDelete       func(context.Context, uuid.UUID) error
}

var _ Repository = (*fakeRepository)(nil)

func (f *fakeRepository) CreateCollection(ctx context.Context, name, description string) (*Collection, error) {
	if f.createCollection == nil {
		panic("unexpected CreateCollection call")
	}
	return f.createCollection(ctx, name, description)
}

func (f *fakeRepository) GetCollection(ctx context.Context, id uuid.UUID) (*Collection, error) {
	if f.getCollection == nil {
		panic("unexpected GetCollection call")
	}
	return f.getCollection(ctx, id)
}

func (f *fakeRepository) ListCollections(ctx context.Context, cursor *Cursor, limit int) ([]*Collection, *Cursor, error) {
	if f.listCollections == nil {
		panic("unexpected ListCollections call")
	}
	return f.listCollections(ctx, cursor, limit)
}

func (f *fakeRepository) UpdateCollection(ctx context.Context, id uuid.UUID, patch CollectionUpdate) (*Collection, error) {
	if f.updateCollection == nil {
		panic("unexpected UpdateCollection call")
	}
	return f.updateCollection(ctx, id, patch)
}

func (f *fakeRepository) SoftDeleteCollection(ctx context.Context, id uuid.UUID) error {
	if f.softDelete == nil {
		panic("unexpected SoftDeleteCollection call")
	}
	return f.softDelete(ctx, id)
}

// TestServiceCreateCollection 覆盖创建校验与错误映射；
// Get/List/Update/SoftDelete 各有独立的表驱动测试。
func TestServiceCreateCollection(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		desc     string
		repoErr  error
		wantName string // 空表示期望失败
		wantCode string
	}{
		{name: "空名字", input: "   ", wantCode: apperr.CodeInvalidArgument},
		{name: "正常并 trim", input: "  产品文档  ", wantName: "产品文档"},
		{
			name:     "255 rune 名字合法",
			input:    strings.Repeat("字", MaxCollectionNameRunes),
			wantName: strings.Repeat("字", MaxCollectionNameRunes),
		},
		{
			name:     "256 rune 名字超长",
			input:    strings.Repeat("字", MaxCollectionNameRunes+1),
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "description 1024 rune 合法",
			input:    "产品文档",
			desc:     strings.Repeat("字", MaxCollectionDescriptionRunes),
			wantName: "产品文档",
		},
		{
			name:     "description 1025 rune 超长",
			input:    "产品文档",
			desc:     strings.Repeat("字", MaxCollectionDescriptionRunes+1),
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "repo ErrNotFound",
			input:    "产品文档",
			repoErr:  ErrNotFound,
			wantCode: apperr.CodeNotFound,
		},
		{name: "repo 内部错误", input: "产品文档", repoErr: errors.New("db down"), wantCode: apperr.CodeInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotName string
			repo := &fakeRepository{
				createCollection: func(_ context.Context, name, _ string) (*Collection, error) {
					gotName = name
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return &Collection{ID: uuid.New(), Name: name}, nil
				},
			}
			svc := NewService(repo)

			got, err := svc.CreateCollection(context.Background(), tt.input, tt.desc)
			if tt.wantCode != "" {
				if err == nil {
					t.Fatalf("期望错误码 %q，实际 err=nil", tt.wantCode)
				}
				if code := apperr.Code(err); code != tt.wantCode {
					t.Fatalf("错误码 = %q, want %q (err=%v)", code, tt.wantCode, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不期望错误: %v", err)
			}
			if got.Name != tt.wantName || gotName != tt.wantName {
				t.Fatalf("name = %q/%q, want %q", got.Name, gotName, tt.wantName)
			}
		})
	}
}

func TestNormalizeCollectionName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     string
		wantCode string
	}{
		{name: "trim", input: "  产品文档  ", want: "产品文档"},
		{name: "空串", input: "", wantCode: apperr.CodeInvalidArgument},
		{name: "全空白", input: " \t\n", wantCode: apperr.CodeInvalidArgument},
		{
			name:  "255 rune 合法",
			input: strings.Repeat("字", MaxCollectionNameRunes),
			want:  strings.Repeat("字", MaxCollectionNameRunes),
		},
		{
			name:     "256 rune 超长",
			input:    strings.Repeat("字", MaxCollectionNameRunes+1),
			wantCode: apperr.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeCollectionName(tt.input)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode == "" && got != tt.want {
				t.Fatalf("normalizeCollectionName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateCollectionDescription(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantCode string
	}{
		{name: "空串合法", input: ""},
		{
			name:  "1024 rune 合法",
			input: strings.Repeat("字", MaxCollectionDescriptionRunes),
		},
		{
			name:     "1025 rune 超长",
			input:    strings.Repeat("字", MaxCollectionDescriptionRunes+1),
			wantCode: apperr.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorCode(t, validateCollectionDescription(tt.input), tt.wantCode)
		})
	}
}

func TestValidateCollectionID(t *testing.T) {
	assertErrorCode(t, validateCollectionID(uuid.Nil), apperr.CodeInvalidArgument)
	assertErrorCode(t, validateCollectionID(uuid.New()), "")
}

func TestValidateListLimit(t *testing.T) {
	tests := []struct {
		name     string
		limit    int
		wantCode string
	}{
		{name: "负数", limit: -1, wantCode: apperr.CodeInvalidArgument},
		{name: "零", limit: 0, wantCode: apperr.CodeInvalidArgument},
		{name: "下界", limit: 1},
		{name: "上界", limit: MaxCollectionListLimit},
		{name: "超过上界", limit: MaxCollectionListLimit + 1, wantCode: apperr.CodeInvalidArgument},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorCode(t, validateListLimit(tt.limit), tt.wantCode)
		})
	}
}

func TestValidateCursor(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		cursor   *Cursor
		wantCode string
	}{
		{name: "nil 是第一页", cursor: nil},
		{name: "零值时间", cursor: &Cursor{ID: uuid.New()}, wantCode: apperr.CodeInvalidArgument},
		{name: "零值 ID", cursor: &Cursor{CreatedAt: now}, wantCode: apperr.CodeInvalidArgument},
		{name: "合法", cursor: &Cursor{CreatedAt: now, ID: uuid.New()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorCode(t, validateCursor(tt.cursor), tt.wantCode)
		})
	}
}

func TestWrapCollectionRepoError(t *testing.T) {
	if err := wrapCollectionRepoError("查询知识库失败", nil); err != nil {
		t.Fatalf("nil error 应返回 nil, got %v", err)
	}

	notFound := wrapCollectionRepoError("查询知识库失败", ErrNotFound)
	assertErrorCode(t, notFound, apperr.CodeNotFound)
	if !errors.Is(notFound, ErrNotFound) {
		t.Fatalf("not_found 错误应保留 ErrNotFound 原因链, got %v", notFound)
	}

	internal := wrapCollectionRepoError("查询知识库失败", errors.New("db down"))
	assertErrorCode(t, internal, apperr.CodeInternal)

	var appErr *apperr.Error
	if !errors.As(internal, &appErr) {
		t.Fatalf("internal 错误应是 *apperr.Error, got %T", internal)
	}
	if appErr.Message != "查询知识库失败" {
		t.Fatalf("message = %q, want %q", appErr.Message, "查询知识库失败")
	}
}

// assertErrorCode 断言 err 的错误码；wantCode 为空表示期望 err == nil。
func assertErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if wantCode == "" {
		if err != nil {
			t.Fatalf("不期望错误, got %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("期望错误码 %q, got nil", wantCode)
	}
	if got := apperr.Code(err); got != wantCode {
		t.Fatalf("错误码 = %q, want %q (err=%v)", got, wantCode, err)
	}
}

func TestServiceGetCollection(t *testing.T) {
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
			repo := &fakeRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.getCollection = func(_ context.Context, id uuid.UUID) (*Collection, error) {
					if id != tt.id {
						t.Fatalf("repo 收到 id=%v, want %v", id, tt.id)
					}
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return &Collection{ID: id, Name: "ok"}, nil
				}
			}

			got, err := NewService(repo).GetCollection(context.Background(), tt.id)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode == "" && (got == nil || got.ID != tt.id) {
				t.Fatalf("GetCollection = %+v, want id=%v", got, tt.id)
			}
		})
	}
}

func TestServiceListCollections(t *testing.T) {
	validCursor := &Cursor{CreatedAt: time.Now().UTC(), ID: uuid.New()}
	next := &Cursor{CreatedAt: time.Now().UTC(), ID: uuid.New()}
	tests := []struct {
		name     string
		cursor   *Cursor
		limit    int
		repoErr  error
		wantCode string
		wantNext *Cursor
	}{
		{name: "limit 0", limit: 0, wantCode: apperr.CodeInvalidArgument},
		{name: "limit 101", limit: MaxCollectionListLimit + 1, wantCode: apperr.CodeInvalidArgument},
		{name: "limit 100", limit: MaxCollectionListLimit},
		{
			name:     "cursor 零值时间",
			cursor:   &Cursor{ID: uuid.New()},
			limit:    20,
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "cursor 零值 ID",
			cursor:   &Cursor{CreatedAt: time.Now().UTC()},
			limit:    20,
			wantCode: apperr.CodeInvalidArgument,
		},
		{name: "repo ErrNotFound", limit: 20, repoErr: ErrNotFound, wantCode: apperr.CodeNotFound},
		{name: "repo 内部错误", limit: 20, repoErr: errors.New("db down"), wantCode: apperr.CodeInternal},
		{name: "空页", limit: 20},
		{name: "有下一页", cursor: validCursor, limit: 20, wantNext: next},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.listCollections = func(_ context.Context, cursor *Cursor, limit int) ([]*Collection, *Cursor, error) {
					if cursor != tt.cursor {
						t.Fatalf("repo 收到 cursor=%+v, want %+v", cursor, tt.cursor)
					}
					if limit != tt.limit {
						t.Fatalf("repo 收到 limit=%d, want %d", limit, tt.limit)
					}
					if tt.repoErr != nil {
						return nil, nil, tt.repoErr
					}
					return nil, tt.wantNext, nil
				}
			}

			got, gotNext, err := NewService(repo).ListCollections(context.Background(), tt.cursor, tt.limit)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode != "" {
				return
			}
			if got == nil {
				t.Fatal("ListCollections 应把 nil 切片规范成空切片")
			}
			if len(got) != 0 {
				t.Fatalf("ListCollections = %+v, want empty", got)
			}
			if gotNext != tt.wantNext {
				t.Fatalf("next = %+v, want %+v", gotNext, tt.wantNext)
			}
		})
	}
}

func TestServiceUpdateCollection(t *testing.T) {
	id := uuid.New()
	tests := []struct {
		name     string
		id       uuid.UUID
		patch    CollectionUpdate
		repoErr  error
		wantCode string
		wantName *string
		wantDesc *string
	}{
		{name: "uuid.Nil", id: uuid.Nil, wantCode: apperr.CodeInvalidArgument},
		{
			name:     "name 全空白",
			id:       id,
			patch:    CollectionUpdate{Name: stringPtr("   ")},
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "description 超长",
			id:       id,
			patch:    CollectionUpdate{Description: stringPtr(strings.Repeat("字", MaxCollectionDescriptionRunes+1))},
			wantCode: apperr.CodeInvalidArgument,
		},
		{
			name:     "repo ErrNotFound",
			id:       id,
			patch:    CollectionUpdate{Name: stringPtr("新名字")},
			repoErr:  ErrNotFound,
			wantCode: apperr.CodeNotFound,
		},
		{
			name:     "repo 内部错误",
			id:       id,
			patch:    CollectionUpdate{Name: stringPtr("新名字")},
			repoErr:  errors.New("db down"),
			wantCode: apperr.CodeInternal,
		},
		{name: "空 patch", id: id},
		{
			name:     "name 被 trim",
			id:       id,
			patch:    CollectionUpdate{Name: stringPtr("  新名字  ")},
			wantName: stringPtr("新名字"),
		},
		{
			name:     "只改 description",
			id:       id,
			patch:    CollectionUpdate{Description: stringPtr("新描述")},
			wantDesc: stringPtr("新描述"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPatch CollectionUpdate
			repo := &fakeRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.updateCollection = func(_ context.Context, gotID uuid.UUID, patch CollectionUpdate) (*Collection, error) {
					if gotID != tt.id {
						t.Fatalf("repo 收到 id=%v, want %v", gotID, tt.id)
					}
					gotPatch = patch
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return &Collection{ID: gotID}, nil
				}
			}

			got, err := NewService(repo).UpdateCollection(context.Background(), tt.id, tt.patch)
			assertErrorCode(t, err, tt.wantCode)
			if tt.wantCode != "" {
				return
			}
			if got == nil {
				t.Fatal("UpdateCollection 返回了 nil Collection")
			}
			if tt.wantName != nil {
				if gotPatch.Name == nil || *gotPatch.Name != *tt.wantName {
					t.Fatalf("repo 收到 name=%v, want %q", gotPatch.Name, *tt.wantName)
				}
				return
			}
			if tt.wantDesc != nil {
				if gotPatch.Description == nil || *gotPatch.Description != *tt.wantDesc {
					t.Fatalf("repo 收到 description=%v, want %q", gotPatch.Description, *tt.wantDesc)
				}
				return
			}
			if gotPatch.Name != nil || gotPatch.Description != nil {
				t.Fatalf("空 patch 被改成了 %+v", gotPatch)
			}
		})
	}
}

func TestServiceSoftDeleteCollection(t *testing.T) {
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
			repo := &fakeRepository{}
			if tt.wantCode == "" || tt.repoErr != nil {
				repo.softDelete = func(_ context.Context, id uuid.UUID) error {
					if id != tt.id {
						t.Fatalf("repo 收到 id=%v, want %v", id, tt.id)
					}
					return tt.repoErr
				}
			}

			err := NewService(repo).SoftDeleteCollection(context.Background(), tt.id)
			assertErrorCode(t, err, tt.wantCode)
		})
	}
}

func stringPtr(s string) *string {
	return &s
}

func TestNewServiceRejectsNilRepository(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewService(nil) 应当 panic")
		}
	}()

	NewService(nil)
}
