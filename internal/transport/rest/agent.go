package rest

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/agent"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

const (
	agentChatPath = "/api/v1/agent/chat"

	// agentWriteDeadlineMargin 是 LLM 超时之外给响应写入留的余量。
	// 默认 server.write_timeout 只有 10s，而 LLM 可能跑几十秒；
	// 这里只对 Agent 路由单独延长，不影响其他接口。
	agentWriteDeadlineMargin = 30 * time.Second
)

// AgentService 是 HTTP 层依赖的单轮 RAG 业务接口。
type AgentService interface {
	Chat(
		ctx context.Context,
		message string,
		collectionID *uuid.UUID,
	) (*agent.ChatResult, error)
}

type agentChatRequest struct {
	CollectionID *string `json:"collection_id"`
	Message      string  `json:"message"`
}

type agentCitationResponse struct {
	Index         int     `json:"index"`
	ChunkID       string  `json:"chunk_id"`
	DocumentID    string  `json:"document_id"`
	DocumentTitle string  `json:"document_title"`
	ChunkIndex    int     `json:"chunk_index"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
}

type agentChatResponse struct {
	Answer    string                  `json:"answer"`
	Citations []agentCitationResponse `json:"citations"`
}

// handleAgentChat 处理 POST /api/v1/agent/chat。
func (s *Server) handleAgentChat(w http.ResponseWriter, r *http.Request) {
	// 只对 Agent 路由延长写截止时间：LLM 可能超过全局 write_timeout。
	// httptest.ResponseRecorder 不支持 SetWriteDeadline，测试里会返回 ErrNotSupported；
	// 这不会影响响应本身，所以只记 debug 日志，不中断请求。
	if err := http.NewResponseController(w).SetWriteDeadline(
		time.Now().Add(s.cfg.LLM.Timeout + agentWriteDeadlineMargin),
	); err != nil {
		s.logger.DebugContext(r.Context(), "延长 Agent 写超时失败", "error", err)
	}

	var req agentChatRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		s.writeInvalidJSON(w, r, err)
		return
	}

	var collectionID *uuid.UUID
	if req.CollectionID != nil {
		raw := strings.TrimSpace(*req.CollectionID)
		if raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				respond.Error(w, s.logger, http.StatusBadRequest,
					apperr.CodeInvalidArgument, "collection_id 不是合法的 UUID")
				return
			}
			collectionID = &id
		}
	}

	result, err := s.agents.Chat(r.Context(), req.Message, collectionID)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	citations := make([]agentCitationResponse, 0, len(result.Citations))
	for i, citation := range result.Citations {
		citations = append(citations, agentCitationResponse{
			Index:         i + 1,
			ChunkID:       citation.ChunkID.String(),
			DocumentID:    citation.DocumentID.String(),
			DocumentTitle: citation.DocumentTitle,
			ChunkIndex:    citation.ChunkIndex,
			Content:       citation.Content,
			Score:         citation.Score,
		})
	}

	respond.JSON(w, s.logger, http.StatusOK, agentChatResponse{
		Answer:    result.Answer,
		Citations: citations,
	})
}
