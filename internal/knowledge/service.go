package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
)

const (
	// DefaultCollectionListLimit 是 API 的默认每页条数；handler 在缺省时使用。
	DefaultCollectionListLimit = 20

	// MaxCollectionListLimit 是 service 允许的最大每页条数。
	MaxCollectionListLimit = 100

	// MaxCollectionNameRunes 是知识库名称的最大字符数（按 rune 计，不是字节）。
	MaxCollectionNameRunes = 255

	// MaxCollectionDescriptionRunes 是知识库描述的最大字符数（按 rune 计）。
	MaxCollectionDescriptionRunes = 1024
)

// Service 是知识库的业务层。
//
// 职责边界：
//   - 参数校验与规范化（trim、长度、limit 范围）
//   - 调用 Repository 并翻译领域错误
//   - 不处理 HTTP 状态码、JSON、cursor 编解码、SQL
type Service struct {
	repo Repository
}

// NewService 注入 Repository。repo 不能为 nil。
func NewService(repo Repository) *Service {
	if repo == nil {
		panic("knowledge.NewService: repo is nil")
	}
	return &Service{repo: repo}
}

// CreateCollection 校验 name/description 后创建。
//
// 契约：
//   - name trim 后不能为空，rune 数 ≤ MaxCollectionNameRunes
//   - description rune 数 ≤ MaxCollectionDescriptionRunes；空串合法
//   - 失败：invalid_argument / internal_error
func (s *Service) CreateCollection(ctx context.Context, name, description string) (*Collection, error) {
	normalizedName, err := normalizeCollectionName(name)
	if err != nil {
		return nil, err
	}
	if err := validateCollectionDescription(description); err != nil {
		return nil, err
	}

	c, err := s.repo.CreateCollection(ctx, normalizedName, description)
	if err != nil {
		return nil, wrapCollectionRepoError("创建知识库失败", err)
	}
	return c, nil
}

// GetCollection 按 id 取未软删的知识库。
//
// 契约：uuid.Nil → invalid_argument；repo ErrNotFound → not_found。
func (s *Service) GetCollection(ctx context.Context, id uuid.UUID) (*Collection, error) {
	if err := validateCollectionID(id); err != nil {
		return nil, err
	}

	c, err := s.repo.GetCollection(ctx, id)
	if err != nil {
		return nil, wrapCollectionRepoError("查询知识库失败", err)
	}
	return c, nil
}

// ListCollections 校验 limit/cursor 后分页。
//
// 契约：
//   - limit 必须在 1..MaxCollectionListLimit
//   - cursor 为 nil 表示第一页；非 nil 时 CreatedAt 不能是零值、ID 不能是 uuid.Nil
//   - 返回的 next 由 repository 决定，service 不重新计算
func (s *Service) ListCollections(ctx context.Context, cursor *Cursor, limit int) ([]*Collection, *Cursor, error) {
	if err := validateListLimit(limit); err != nil {
		return nil, nil, err
	}
	if err := validateCursor(cursor); err != nil {
		return nil, nil, err
	}

	collections, next, err := s.repo.ListCollections(ctx, cursor, limit)
	if err != nil {
		return nil, nil, wrapCollectionRepoError("查询知识库列表失败", err)
	}
	// Repository 正常会返回非 nil 空切片；这里兜底，保证 JSON 的 items 始终是数组。
	if collections == nil {
		collections = []*Collection{}
	}
	return collections, next, nil
}

// UpdateCollection 按 PATCH 语义更新。
//
// 契约：
//   - uuid.Nil → invalid_argument
//   - patch.Name != nil 时按 name 规则校验并 trim
//   - patch.Description != nil 时按 description 规则校验
//   - 两个字段都 nil 是合法 no-op，直接交给 repository
func (s *Service) UpdateCollection(ctx context.Context, id uuid.UUID, patch CollectionUpdate) (*Collection, error) {
	if err := validateCollectionID(id); err != nil {
		return nil, err
	}
	if patch.Name != nil {
		normalizedName, err := normalizeCollectionName(*patch.Name)
		if err != nil {
			return nil, err
		}
		patch.Name = &normalizedName
	}
	if patch.Description != nil {
		if err := validateCollectionDescription(*patch.Description); err != nil {
			return nil, err
		}
	}

	c, err := s.repo.UpdateCollection(ctx, id, patch)
	if err != nil {
		return nil, wrapCollectionRepoError("更新知识库失败", err)
	}
	return c, nil
}

// SoftDeleteCollection 软删。
//
// 契约：uuid.Nil → invalid_argument；repo ErrNotFound → not_found。
func (s *Service) SoftDeleteCollection(ctx context.Context, id uuid.UUID) error {
	if err := validateCollectionID(id); err != nil {
		return err
	}

	if err := s.repo.SoftDeleteCollection(ctx, id); err != nil {
		return wrapCollectionRepoError("删除知识库失败", err)
	}
	return nil
}

// RestoreCollection 恢复一个已软删的知识库。
//
// 契约：uuid.Nil → invalid_argument；不存在/物理删除 → not_found；
// 本来就是 active → conflict。
func (s *Service) RestoreCollection(ctx context.Context, id uuid.UUID) (*Collection, error) {
	if err := validateCollectionID(id); err != nil {
		return nil, err
	}

	c, err := s.repo.RestoreCollection(ctx, id)
	if err != nil {
		return nil, wrapCollectionRestoreError("恢复知识库失败", err)
	}
	return c, nil
}

// normalizeCollectionName 去首尾空白并校验 rune 长度。
// 返回值已经 trim，调用方应使用返回值写库，而不是原始入参。
func normalizeCollectionName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "name 不能为空")
	}
	if utf8.RuneCountInString(name) > MaxCollectionNameRunes {
		return "", apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("name 不能超过 %d 个字符", MaxCollectionNameRunes))
	}
	return name, nil
}

// validateCollectionDescription 校验描述长度；不清洗原文。
func validateCollectionDescription(raw string) error {
	if utf8.RuneCountInString(raw) > MaxCollectionDescriptionRunes {
		return apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("description 不能超过 %d 个字符", MaxCollectionDescriptionRunes))
	}
	return nil
}

// validateCollectionID 拒绝 uuid.Nil。
func validateCollectionID(id uuid.UUID) error {
	if id == uuid.Nil {
		return apperr.New(apperr.CodeInvalidArgument, "id 不能为空")
	}
	return nil
}

// validateListLimit 校验 collections 的分页条数在 1..MaxCollectionListLimit。
func validateListLimit(limit int) error {
	return validateListLimitMax(limit, MaxCollectionListLimit)
}

// validateListLimitMax 是 collections / documents 共用的分页上界校验。
// 抽出来是为了让两个聚合各自使用自己的上限，而不是隐式共用同一个常量。
func validateListLimitMax(limit, maxLimit int) error {
	if limit < 1 || limit > maxLimit {
		return apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("limit 必须在 1~%d 之间", maxLimit))
	}
	return nil
}

// validateCursor 校验非 nil 游标的两个字段；nil 表示第一页，合法。
func validateCursor(cursor *Cursor) error {
	if cursor == nil {
		return nil
	}
	if cursor.CreatedAt.IsZero() || cursor.ID == uuid.Nil {
		return apperr.New(apperr.CodeInvalidArgument, "cursor 无效")
	}
	return nil
}

// wrapCollectionRepoError 把 repository 错误翻译成 apperr。
//
// ErrNotFound 映射为 not_found；其他映射为 internal_error。
// action 是面向用户的泛化说明，真实原因保留在 Cause 里，只进日志不出接口。
func wrapCollectionRepoError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return apperr.Wrap(err, apperr.CodeNotFound, "知识库不存在")
	}
	return apperr.Wrap(err, apperr.CodeInternal, action)
}

// wrapCollectionRestoreError 单独处理 restore 的 409：
// 行存在但 deleted_at 为 NULL 说明“本来就没删”，不是 not_found。
func wrapCollectionRestoreError(action string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return apperr.Wrap(err, apperr.CodeNotFound, "知识库不存在")
	case errors.Is(err, ErrConflict):
		return apperr.Wrap(err, apperr.CodeConflict, "知识库未被软删")
	default:
		return apperr.Wrap(err, apperr.CodeInternal, action)
	}
}
