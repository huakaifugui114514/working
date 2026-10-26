package rest

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// maxContentRequestBytes 是正文接口的请求体上限。
//
// content 本身允许 1 MiB；JSON 包装（字段名、引号、转义）会再加一层，
// 所以这里给到 2 MiB，而不是复用其他接口的 1 MiB。
const maxContentRequestBytes = 2 << 20

// ContentService 是 HTTP 层依赖的正文业务接口。
// *knowledge.ContentService 实现了它；测试可以注入 fake。
type ContentService interface {
	ReplaceDocumentContent(ctx context.Context, documentID uuid.UUID, content string) error
}

// ReplaceContentRequest 是 PUT /api/v1/documents/{id}/content 的请求体。
//
// 契约：content 必填；空串/纯空白 → 400；UTF-8 字节数 > 1 MiB → 400。
// 请求体上限 2 MiB（JSON 包装预留空间）。
type ReplaceContentRequest struct {
	Content string `json:"content"`
}

// handleReplaceContent 处理 PUT /api/v1/documents/{id}/content。
func (s *Server) handleReplaceContent(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req ReplaceContentRequest
	if err := decodeJSONBodyWithLimit(w, r, &req, maxContentRequestBytes); err != nil {
		s.writeInvalidJSON(w, r, err)
		return
	}

	if err := s.contents.ReplaceDocumentContent(r.Context(), id, req.Content); err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
