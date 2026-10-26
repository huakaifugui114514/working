package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/agent"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

const (
	agentChatPath       = "/api/v1/agent/chat"
	agentChatStreamPath = "/api/v1/agent/chat/stream"

	// agentWriteDeadlineMargin 是 LLM 超时之外给响应写入留的余量。
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

// AgentStreamService 是可选流式能力；生产 *agent.Service 同时实现两者。
type AgentStreamService interface {
	ChatStream(
		ctx context.Context,
		message string,
		collectionID *uuid.UUID,
	) (<-chan agent.StreamEvent, error)
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

func (s *Server) handleAgentChat(w http.ResponseWriter, r *http.Request) {
	s.extendAgentWriteDeadline(w, r)
	req, collectionID, ok := s.decodeAgentChatRequest(w, r)
	if !ok {
		return
	}

	result, err := s.agents.Chat(r.Context(), req.Message, collectionID)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	respond.JSON(w, s.logger, http.StatusOK, agentChatResponse{
		Answer:    result.Answer,
		Citations: citationResponses(result.Citations),
	})
}

func (s *Server) handleAgentChatStream(w http.ResponseWriter, r *http.Request) {
	s.extendAgentWriteDeadline(w, r)
	streamSvc, ok := s.agents.(AgentStreamService)
	if !ok {
		respond.Error(w, s.logger, http.StatusNotImplemented, apperr.CodeInternal, "流式回答未启用")
		return
	}
	req, collectionID, ok := s.decodeAgentChatRequest(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		respond.Error(w, s.logger, http.StatusInternalServerError, apperr.CodeInternal, "当前 ResponseWriter 不支持流式输出")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, err := streamSvc.ChatStream(ctx, req.Message, collectionID)
	if err != nil {
		s.handleServiceError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for event := range events {
		if event.Err != nil {
			s.logger.ErrorContext(r.Context(), "流式回答失败", "error", event.Err)
			_ = writeSSE(w, flusher, "error", map[string]string{"message": "生成回答失败"})
			return
		}
		if event.Delta != "" {
			if err := writeSSE(w, flusher, "token", map[string]string{"delta": event.Delta}); err != nil {
				return
			}
		}
		if event.Done {
			_ = writeSSE(w, flusher, "done", map[string]any{
				"citations": citationResponses(event.Citations),
			})
			return
		}
	}
}

func (s *Server) extendAgentWriteDeadline(w http.ResponseWriter, r *http.Request) {
	// 只对 Agent 路由延长写截止时间：LLM 可能超过全局 write_timeout。
	// httptest.ResponseRecorder 不支持 SetWriteDeadline，测试里会返回 ErrNotSupported；
	// 这不会影响响应本身，所以只记 debug 日志，不中断请求。
	if err := http.NewResponseController(w).SetWriteDeadline(
		time.Now().Add(s.cfg.LLM.Timeout + agentWriteDeadlineMargin),
	); err != nil {
		s.logger.DebugContext(r.Context(), "延长 Agent 写超时失败", "error", err)
	}
}

func (s *Server) decodeAgentChatRequest(w http.ResponseWriter, r *http.Request) (*agentChatRequest, *uuid.UUID, bool) {
	var req agentChatRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		s.writeInvalidJSON(w, r, err)
		return nil, nil, false
	}

	var collectionID *uuid.UUID
	if req.CollectionID != nil {
		raw := strings.TrimSpace(*req.CollectionID)
		if raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				respond.Error(w, s.logger, http.StatusBadRequest,
					apperr.CodeInvalidArgument, "collection_id 不是合法的 UUID")
				return nil, nil, false
			}
			collectionID = &id
		}
	}
	return &req, collectionID, true
}

func citationResponses(citations []agent.RetrievedChunk) []agentCitationResponse {
	out := make([]agentCitationResponse, 0, len(citations))
	for i, citation := range citations {
		out = append(out, agentCitationResponse{
			Index:         i + 1,
			ChunkID:       citation.ChunkID.String(),
			DocumentID:    citation.DocumentID.String(),
			DocumentTitle: citation.DocumentTitle,
			ChunkIndex:    citation.ChunkIndex,
			Content:       citation.Content,
			Score:         citation.Score,
		})
	}
	return out
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal sse: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return fmt.Errorf("write sse: %w", err)
	}
	flusher.Flush()
	return nil
}
