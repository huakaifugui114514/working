package knowledge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
)

// ContentService 是文档正文入库的业务层。
//
// 职责边界：参数校验、分块、SHA-256、调用 ContentRepository、翻译错误；
// 不写 SQL、不处理 HTTP/JSON。
type ContentService struct {
	repo ContentRepository
}

// NewContentService 注入 ContentRepository。repo 不能为 nil。
func NewContentService(repo ContentRepository) *ContentService {
	if repo == nil {
		panic("knowledge.NewContentService: repo is nil")
	}
	return &ContentService{repo: repo}
}

// ReplaceDocumentContent 校验并整体替换一篇文档的正文与 chunks。
//
// 契约：
//   - uuid.Nil → invalid_argument
//   - 正文为空/纯空白 → invalid_argument
//   - 正文超过 MaxDocumentContentBytes → invalid_argument
//   - 分块超过 MaxDocumentChunks → invalid_argument
//   - 文档不存在/已软删 → not_found
//   - 其他仓库错误 → internal_error
//   - 成功返回 nil（HTTP 204）
func (s *ContentService) ReplaceDocumentContent(
	ctx context.Context,
	documentID uuid.UUID,
	content string,
) error {
	if err := validateDocumentID(documentID); err != nil {
		return err
	}

	// 只把“全空白”判为无效，不 trim 后再存：
	// 正文里的缩进、换行和首尾空白都可能是内容的一部分。
	if strings.TrimSpace(content) == "" {
		return apperr.Wrap(ErrContentEmpty, apperr.CodeInvalidArgument, "正文不能为空")
	}

	// len(string) 是 UTF-8 字节数，正好对应 MaxDocumentContentBytes 的定义。
	// 用 > 而不是 >=：上限本身是允许的，只有“超过”才拒绝。
	if len(content) > MaxDocumentContentBytes {
		return apperr.Wrap(
			ErrContentTooLarge,
			apperr.CodeInvalidArgument,
			fmt.Sprintf("正文不能超过 %d 字节", MaxDocumentContentBytes),
		)
	}

	chunks, err := ChunkContent(content)
	if err != nil {
		return wrapContentChunkError(err)
	}

	// hash 必须和最终存进去的 content 完全一致：
	// 这里先算整篇正文的 SHA-256，chunks 只是它的派生数据。
	hash := sha256.Sum256([]byte(content))

	if err := s.repo.ReplaceDocumentContent(ctx, documentID, content, hash, chunks); err != nil {
		return wrapContentRepoError("替换文档正文失败", err)
	}
	return nil
}

// wrapContentChunkError 把分块错误翻译成业务错误。
//
// ErrContentEmpty / ErrTooManyChunks 是用户输入问题，映射 400；
// 其他错误说明分块实现出了故障，映射 500，真实原因保留在 Cause 里。
func wrapContentChunkError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, ErrContentEmpty):
		return apperr.Wrap(err, apperr.CodeInvalidArgument, "正文不能为空")
	case errors.Is(err, ErrTooManyChunks):
		return apperr.Wrap(
			err,
			apperr.CodeInvalidArgument,
			fmt.Sprintf("正文分块不能超过 %d 块", MaxDocumentChunks),
		)
	default:
		return apperr.Wrap(err, apperr.CodeInternal, "正文分块失败")
	}
}

// wrapContentRepoError 把 repository 错误翻译成业务错误。
//
// ErrNotFound 映射 404；其他错误映射 500。
// action 是面向用户的泛化说明，真实原因保留在 Cause 里，只进日志不出接口。
func wrapContentRepoError(action string, err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, ErrNotFound) {
		return apperr.Wrap(err, apperr.CodeNotFound, "文档不存在")
	}
	return apperr.Wrap(err, apperr.CodeInternal, action)
}
