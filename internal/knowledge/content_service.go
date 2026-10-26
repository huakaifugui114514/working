package knowledge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/workflow"
)

// ContentService 是文档正文入库的业务层。
//
// 正文入库本身也走 YAML 同款 DAG 引擎：chunk → persist。
// 这样 PDF/纯文本/Workflow 入口共用同一条流水线，而不是各写一套。
type ContentService struct {
	repo       ContentRepository
	versioner  CacheVersioner
	definition *workflow.Definition
}

// NewContentService 注入 ContentRepository。repo 不能为 nil。
func NewContentService(repo ContentRepository) *ContentService {
	if repo == nil {
		panic("knowledge.NewContentService: repo is nil")
	}
	def := &workflow.Definition{
		Name: "document-content",
		Nodes: []workflow.Node{
			{ID: "chunk", Action: "chunk", Timeout: "5s"},
			{ID: "persist", Action: "persist", DependsOn: []string{"chunk"}, Timeout: "30s"},
		},
	}
	if err := def.Validate(); err != nil {
		panic("knowledge.NewContentService: " + err.Error())
	}
	return &ContentService{repo: repo, definition: def}
}

// WithCacheVersioner 让正文替换递增搜索缓存版本。
func (s *ContentService) WithCacheVersioner(v CacheVersioner) *ContentService {
	s.versioner = v
	return s
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
	if len(content) > MaxDocumentContentBytes {
		return apperr.Wrap(
			ErrContentTooLarge,
			apperr.CodeInvalidArgument,
			fmt.Sprintf("正文不能超过 %d 字节", MaxDocumentContentBytes),
		)
	}

	hash := sha256.Sum256([]byte(content))
	actions := map[string]workflow.Action{
		"chunk": func(ctx context.Context, _ map[string]any, _ map[string]any) (map[string]any, error) {
			chunks, err := ChunkContent(content)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", errContentChunkStage, err)
			}
			return map[string]any{"chunks": chunks}, nil
		},
		"persist": func(ctx context.Context, vars map[string]any, _ map[string]any) (map[string]any, error) {
			chunks, ok := vars["chunk"].(map[string]any)["chunks"].([]ContentChunk)
			if !ok {
				return nil, fmt.Errorf("%w: chunks 类型错误", errContentPersistStage)
			}
			if err := s.repo.ReplaceDocumentContent(ctx, documentID, content, hash, chunks); err != nil {
				return nil, fmt.Errorf("%w: %w", errContentPersistStage, err)
			}
			bumpSearchCache(ctx, s.versioner)
			return map[string]any{"chunks": len(chunks)}, nil
		},
	}

	engine, err := workflow.NewEngine(s.definition, actions, workflow.NewMemoryStore(), nil)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "初始化正文流水线失败")
	}
	runID, err := uuid.NewV7()
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "生成正文流水线 id 失败")
	}
	if _, err := engine.Execute(ctx, runID, map[string]any{
		"document_id":  documentID,
		"content":      content,
		"content_hash": hash,
	}); err != nil {
		switch {
		case errors.Is(err, errContentChunkStage):
			return wrapContentChunkError(err)
		case errors.Is(err, errContentPersistStage):
			return wrapContentRepoError("替换文档正文失败", err)
		default:
			return apperr.Wrap(err, apperr.CodeInternal, "正文流水线失败")
		}
	}
	return nil
}

var (
	errContentChunkStage   = errors.New("content: chunk stage")
	errContentPersistStage = errors.New("content: persist stage")
)

// wrapContentChunkError 把分块错误翻译成业务错误。
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
func wrapContentRepoError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return apperr.Wrap(err, apperr.CodeNotFound, "文档不存在")
	}
	return apperr.Wrap(err, apperr.CodeInternal, action)
}
