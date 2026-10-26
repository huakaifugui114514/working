package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/agent"
	"github.com/huakaifugui114514/working/internal/apperr"
)

type fakeAgentService struct {
	chat   func(context.Context, string, *uuid.UUID) (*agent.ChatResult, error)
	stream func(context.Context, string, *uuid.UUID) (<-chan agent.StreamEvent, error)
}

var _ AgentService = (*fakeAgentService)(nil)

func (f *fakeAgentService) Chat(
	ctx context.Context,
	message string,
	collectionID *uuid.UUID,
) (*agent.ChatResult, error) {
	if f.chat == nil {
		panic("unexpected Chat call")
	}
	return f.chat(ctx, message, collectionID)
}

func (f *fakeAgentService) ChatStream(
	ctx context.Context,
	message string,
	collectionID *uuid.UUID,
) (<-chan agent.StreamEvent, error) {
	if f.stream == nil {
		panic("unexpected ChatStream call")
	}
	return f.stream(ctx, message, collectionID)
}

func TestAgentChat(t *testing.T) {
	collectionID := uuid.New()
	chunk1 := uuid.New()
	chunk2 := uuid.New()
	doc1 := uuid.New()
	doc2 := uuid.New()

	var (
		gotMessage    string
		gotCollection *uuid.UUID
	)
	svc := &fakeAgentService{
		chat: func(_ context.Context, message string, cid *uuid.UUID) (*agent.ChatResult, error) {
			gotMessage = message
			gotCollection = cid
			return &agent.ChatResult{
				Answer: "答案 [1][2]",
				Citations: []agent.RetrievedChunk{
					{ChunkID: chunk1, DocumentID: doc1, DocumentTitle: "文档一", ChunkIndex: 0, Content: "第一段", Score: 0.9},
					{ChunkID: chunk2, DocumentID: doc2, DocumentTitle: "文档二", ChunkIndex: 1, Content: "第二段", Score: 0.8},
				},
			}, nil
		},
	}
	s := newTestServerWithAgent(t, nil, svc)

	rec := doJSON(t, s, http.MethodPost, agentChatPath,
		`{"collection_id":"`+collectionID.String()+`","message":"知识库怎么检索"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotMessage != "知识库怎么检索" {
		t.Fatalf("message = %q", gotMessage)
	}
	if gotCollection == nil || *gotCollection != collectionID {
		t.Fatalf("collection_id = %v, want %v", gotCollection, collectionID)
	}

	got := decodeBody[agentChatResponse](t, rec)
	if got.Answer != "答案 [1][2]" || len(got.Citations) != 2 {
		t.Fatalf("response = %+v", got)
	}
	if got.Citations[0].Index != 1 || got.Citations[1].Index != 2 {
		t.Fatalf("citation index = %d/%d, want 1/2", got.Citations[0].Index, got.Citations[1].Index)
	}
	if got.Citations[0].ChunkID != chunk1.String() || got.Citations[1].DocumentID != doc2.String() {
		t.Fatalf("citation ids = %+v", got.Citations)
	}
}

func TestAgentChatNoCollectionAndEmptyCitations(t *testing.T) {
	var gotCollection *uuid.UUID
	svc := &fakeAgentService{
		chat: func(_ context.Context, _ string, cid *uuid.UUID) (*agent.ChatResult, error) {
			gotCollection = cid
			return &agent.ChatResult{Answer: agent.NoContextAnswer, Citations: []agent.RetrievedChunk{}}, nil
		},
	}
	s := newTestServerWithAgent(t, nil, svc)

	rec := doJSON(t, s, http.MethodPost, agentChatPath, `{"message":"没有资料"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotCollection != nil {
		t.Fatalf("collection_id = %v, want nil", gotCollection)
	}
	if !strings.Contains(rec.Body.String(), `"citations":[]`) {
		t.Fatalf("空 citations 应输出 []，body=%s", rec.Body.String())
	}
}

func TestAgentChatRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "空请求体", body: ""},
		{name: "非法 JSON", body: `{"message":`},
		{name: "未知字段", body: `{"message":"问题","extra":1}`},
		{name: "collection_id 不是 UUID", body: `{"collection_id":"not-a-uuid","message":"问题"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// fake 的 chat 为 nil：请求非法时不能调用 service。
			s := newTestServerWithAgent(t, nil, &fakeAgentService{})
			rec := doJSON(t, s, http.MethodPost, agentChatPath, tt.body)
			assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
		})
	}
}

func TestAgentChatServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "参数错误",
			serviceErr: apperr.New(apperr.CodeInvalidArgument, "message 不能为空"),
			wantStatus: http.StatusBadRequest,
			wantCode:   apperr.CodeInvalidArgument,
		},
		{
			name:       "知识库不存在",
			serviceErr: apperr.New(apperr.CodeNotFound, "知识库不存在"),
			wantStatus: http.StatusNotFound,
			wantCode:   apperr.CodeNotFound,
		},
		{
			name:       "内部错误",
			serviceErr: apperr.New(apperr.CodeInternal, "生成回答失败"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   apperr.CodeInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAgentService{
				chat: func(context.Context, string, *uuid.UUID) (*agent.ChatResult, error) {
					return nil, tt.serviceErr
				},
			}
			s := newTestServerWithAgent(t, nil, svc)

			rec := doJSON(t, s, http.MethodPost, agentChatPath, `{"message":"问题"}`)
			assertHTTPError(t, rec, tt.wantStatus, tt.wantCode)
		})
	}
}

func TestAgentChatStream(t *testing.T) {
	chunk := uuid.New()
	doc := uuid.New()
	svc := &fakeAgentService{
		stream: func(context.Context, string, *uuid.UUID) (<-chan agent.StreamEvent, error) {
			ch := make(chan agent.StreamEvent, 3)
			ch <- agent.StreamEvent{Delta: "你"}
			ch <- agent.StreamEvent{Delta: "好"}
			ch <- agent.StreamEvent{Done: true, Citations: []agent.RetrievedChunk{{
				ChunkID: chunk, DocumentID: doc, DocumentTitle: "文档", Content: "正文", Score: 0.9,
			}}}
			close(ch)
			return ch, nil
		},
	}
	s := newTestServerWithAgent(t, nil, svc)

	rec := doJSON(t, s, http.MethodPost, agentChatStreamPath, `{"message":"问题"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: token", `"delta":"你"`, `"delta":"好"`,
		"event: done", `"chunk_id":"` + chunk.String() + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE 缺少 %q:\n%s", want, body)
		}
	}
}

func TestAgentChatStreamInvalidRequest(t *testing.T) {
	s := newTestServerWithAgent(t, nil, &fakeAgentService{})
	rec := doJSON(t, s, http.MethodPost, agentChatStreamPath, `{"message":`)
	assertHTTPError(t, rec, http.StatusBadRequest, apperr.CodeInvalidArgument)
}
