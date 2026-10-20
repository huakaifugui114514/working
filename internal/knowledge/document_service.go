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
	// DefaultDocumentListLimit 是文档列表的默认每页条数；handler 在缺省时使用。
	DefaultDocumentListLimit = DefaultCollectionListLimit

	// MaxDocumentListLimit 是文档列表允许的最大每页条数。
	MaxDocumentListLimit = MaxCollectionListLimit

	// MaxDocumentTitleRunes 是文档标题的最大字符数（按 rune 计，不是字节）。
	MaxDocumentTitleRunes = 512

	// MaxDocumentSourceURIRunes 是 source_uri 的最大字符数。
	MaxDocumentSourceURIRunes = 2048

	// MaxDocumentMimeTypeRunes 是 mime_type 的最大字符数。
	MaxDocumentMimeTypeRunes = 128
)

// DocumentService 是文档与知识库关联的业务层。
//
// 职责与 Service 对齐：只做参数校验、调用 DocumentRepository、翻译领域错误；
// 不处理 HTTP、JSON、cursor 编解码，也不写 SQL。拆成独立类型是因为 documents
// 和 collections 是两个聚合，测试时可以分别注入 fake。
type DocumentService struct {
	repo DocumentRepository
}

// NewDocumentService 注入 DocumentRepository。repo 不能为 nil。
func NewDocumentService(repo DocumentRepository) *DocumentService {
	if repo == nil {
		panic("knowledge.NewDocumentService: repo is nil")
	}
	return &DocumentService{repo: repo}
}

// CreateDocument 校验元数据后创建文档。
//
// 契约：
//   - title 去首尾空白后不能为空，rune 数 ≤ MaxDocumentTitleRunes
//   - source_uri / mime_type 为 nil、空串或纯空白都表示没有（写 SQL NULL）；
//     非空时校验 rune 长度
//   - size_bytes 为 nil 表示未知；非 nil 时必须 ≥ 0（数据库列是 UNSIGNED）
func (s *DocumentService) CreateDocument(
	ctx context.Context,
	title string,
	sourceURI, mimeType *string,
	sizeBytes *int64,
) (*Document, error) {
	normalizedTitle, err := normalizeDocumentTitle(title)
	if err != nil {
		return nil, err
	}
	sourceURI = normalizeOptionalString(sourceURI)
	mimeType = normalizeOptionalString(mimeType)
	if err := validateOptionalString("source_uri", sourceURI, MaxDocumentSourceURIRunes); err != nil {
		return nil, err
	}
	if err := validateOptionalString("mime_type", mimeType, MaxDocumentMimeTypeRunes); err != nil {
		return nil, err
	}
	if err := validateDocumentSize(sizeBytes); err != nil {
		return nil, err
	}

	d, err := s.repo.CreateDocument(ctx, normalizedTitle, sourceURI, mimeType, sizeBytes)
	if err != nil {
		return nil, wrapDocumentRepoError("创建文档失败", "文档不存在", err)
	}
	return d, nil
}

// GetDocument 按 id 取未软删的文档。
//
// 契约：uuid.Nil → invalid_argument；repo ErrNotFound → not_found。
func (s *DocumentService) GetDocument(ctx context.Context, id uuid.UUID) (*Document, error) {
	if err := validateDocumentID(id); err != nil {
		return nil, err
	}

	d, err := s.repo.GetDocument(ctx, id)
	if err != nil {
		return nil, wrapDocumentRepoError("查询文档失败", "文档不存在", err)
	}
	return d, nil
}

// UpdateDocument 按 PATCH 语义更新文档元数据。
//
// 契约：
//   - uuid.Nil → invalid_argument
//   - patch.Title 为 nil 表示不改；非 nil 时按 title 规则校验并 trim
//   - source_uri / mime_type / size_bytes 按 Optional 三态处理；
//     source_uri / mime_type 的 Set=true 且值为空串/纯空白时，按“清空”写 NULL
//   - 空 patch 是合法 no-op，直接交给 repository
func (s *DocumentService) UpdateDocument(ctx context.Context, id uuid.UUID, patch DocumentUpdate) (*Document, error) {
	if err := validateDocumentID(id); err != nil {
		return nil, err
	}
	if patch.Title != nil {
		normalizedTitle, err := normalizeDocumentTitle(*patch.Title)
		if err != nil {
			return nil, err
		}
		patch.Title = &normalizedTitle
	}
	if patch.SourceURI.Set {
		patch.SourceURI.Value = normalizeOptionalString(patch.SourceURI.Value)
	}
	if patch.MimeType.Set {
		patch.MimeType.Value = normalizeOptionalString(patch.MimeType.Value)
	}
	if err := validateOptionalString("source_uri", patch.SourceURI.Value, MaxDocumentSourceURIRunes); err != nil {
		return nil, err
	}
	if err := validateOptionalString("mime_type", patch.MimeType.Value, MaxDocumentMimeTypeRunes); err != nil {
		return nil, err
	}
	if err := validateDocumentSize(patch.SizeBytes.Value); err != nil {
		return nil, err
	}

	d, err := s.repo.UpdateDocument(ctx, id, patch)
	if err != nil {
		return nil, wrapDocumentRepoError("更新文档失败", "文档不存在", err)
	}
	return d, nil
}

// SoftDeleteDocument 软删文档。
//
// 契约：uuid.Nil → invalid_argument；repo ErrNotFound → not_found。
func (s *DocumentService) SoftDeleteDocument(ctx context.Context, id uuid.UUID) error {
	if err := validateDocumentID(id); err != nil {
		return err
	}

	if err := s.repo.SoftDeleteDocument(ctx, id); err != nil {
		return wrapDocumentRepoError("删除文档失败", "文档不存在", err)
	}
	return nil
}

// RestoreDocument 恢复一个已软删的文档。
//
// 契约：uuid.Nil → invalid_argument；不存在/物理删除 → not_found；
// 本来就是 active → conflict。
func (s *DocumentService) RestoreDocument(ctx context.Context, id uuid.UUID) (*Document, error) {
	if err := validateDocumentID(id); err != nil {
		return nil, err
	}

	d, err := s.repo.RestoreDocument(ctx, id)
	if err != nil {
		return nil, wrapDocumentRepoErrorWithConflict("恢复文档失败", "文档不存在", "文档未被软删", err)
	}
	return d, nil
}

// AddDocumentToCollection 把已有文档加入知识库。
//
// 契约：collection / document 不存在或已软删 → not_found；重复添加 → conflict。
func (s *DocumentService) AddDocumentToCollection(
	ctx context.Context,
	collectionID, documentID uuid.UUID,
) (*CollectionDocument, error) {
	if err := validateCollectionID(collectionID); err != nil {
		return nil, err
	}
	if err := validateUUIDField("document_id", documentID); err != nil {
		return nil, err
	}

	cd, err := s.repo.AddDocumentToCollection(ctx, collectionID, documentID)
	if err != nil {
		return nil, wrapDocumentRepoError("添加文档到知识库失败", "知识库或文档不存在", err)
	}
	return cd, nil
}

// RemoveDocumentFromCollection 移除文档与知识库的关联。
//
// 契约：关联不存在 → not_found；不删除文档本身。
func (s *DocumentService) RemoveDocumentFromCollection(
	ctx context.Context,
	collectionID, documentID uuid.UUID,
) error {
	if err := validateCollectionID(collectionID); err != nil {
		return err
	}
	if err := validateUUIDField("document_id", documentID); err != nil {
		return err
	}

	if err := s.repo.RemoveDocumentFromCollection(ctx, collectionID, documentID); err != nil {
		return wrapDocumentRepoError("移除文档关联失败", "关联不存在", err)
	}
	return nil
}

// ListDocumentsByCollection 分页列出知识库下的文档。
//
// 契约：
//   - limit 必须在 1..MaxDocumentListLimit
//   - cursor 为 nil 表示第一页；非 nil 时两个字段都不能是零值
//   - 知识库不存在 / 已软删 → not_found
//   - 返回的 next 由 repository 决定，service 不重新计算
func (s *DocumentService) ListDocumentsByCollection(
	ctx context.Context,
	collectionID uuid.UUID,
	cursor *Cursor,
	limit int,
) ([]*CollectionDocument, *Cursor, error) {
	if err := validateCollectionID(collectionID); err != nil {
		return nil, nil, err
	}
	if err := validateListLimitMax(limit, MaxDocumentListLimit); err != nil {
		return nil, nil, err
	}
	if err := validateCursor(cursor); err != nil {
		return nil, nil, err
	}

	items, next, err := s.repo.ListDocumentsByCollection(ctx, collectionID, cursor, limit)
	if err != nil {
		return nil, nil, wrapDocumentRepoError("查询知识库文档列表失败", "知识库不存在", err)
	}
	// Repository 正常会返回非 nil 空切片；这里兜底，保证 JSON 的 items 始终是数组。
	if items == nil {
		items = []*CollectionDocument{}
	}
	return items, next, nil
}

// normalizeDocumentTitle 去首尾空白并校验 rune 长度。
// 返回值已经 trim，调用方应使用返回值写库，而不是原始入参。
func normalizeDocumentTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "title 不能为空")
	}
	if utf8.RuneCountInString(title) > MaxDocumentTitleRunes {
		return "", apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("title 不能超过 %d 个字符", MaxDocumentTitleRunes))
	}
	return title, nil
}

// normalizeOptionalString 把 nil、空串和纯空白统一折叠成 nil（SQL NULL）。
//
// 为什么 trim：source_uri / mime_type 的前后空白没有业务含义，留着会让
// `IS NULL` 查询漏掉这些"看起来没有、实际有空串"的行。
func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// validateOptionalString 校验可空字符串的 rune 长度；nil 合法，不清洗原文。
func validateOptionalString(field string, value *string, maxRunes int) error {
	if value == nil {
		return nil
	}
	if utf8.RuneCountInString(*value) > maxRunes {
		return apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("%s 不能超过 %d 个字符", field, maxRunes))
	}
	return nil
}

// validateDocumentSize 拒绝负数。数据库列是 BIGINT UNSIGNED，依赖数据库报错
// 会变成 500，且错误信息里可能带 SQL 细节，所以在 service 先挡住。
func validateDocumentSize(sizeBytes *int64) error {
	if sizeBytes != nil && *sizeBytes < 0 {
		return apperr.New(apperr.CodeInvalidArgument, "size_bytes 不能为负数")
	}
	return nil
}

// validateDocumentID 拒绝 uuid.Nil。
func validateDocumentID(id uuid.UUID) error {
	return validateUUIDField("id", id)
}

// validateUUIDField 拒绝 uuid.Nil，field 用于错误消息里的字段名。
func validateUUIDField(field string, id uuid.UUID) error {
	if id == uuid.Nil {
		return apperr.New(apperr.CodeInvalidArgument, field+" 不能为空")
	}
	return nil
}

// wrapDocumentRepoError 把 repository 错误翻译成 apperr。
//
// ErrNotFound / ErrConflict 是有明确业务含义的冲突，分别映射 404 / 409；
// 其他错误映射为 internal_error，真实原因保留在 Cause 里，只进日志不出接口。
func wrapDocumentRepoError(action, notFoundMessage string, err error) error {
	return wrapDocumentRepoErrorWithConflict(action, notFoundMessage, "文档已在该知识库中", err)
}

// wrapDocumentRepoErrorWithConflict 允许调用方指定 409 的提示语：
// 添加关联说“文档已在该知识库中”，恢复文档说“文档未被软删”。
func wrapDocumentRepoErrorWithConflict(action, notFoundMessage, conflictMessage string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return apperr.Wrap(err, apperr.CodeNotFound, notFoundMessage)
	case errors.Is(err, ErrConflict):
		return apperr.Wrap(err, apperr.CodeConflict, conflictMessage)
	default:
		return apperr.Wrap(err, apperr.CodeInternal, action)
	}
}
