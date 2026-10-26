package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/knowledge"
)

type fakeRetriever struct {
	retrieve func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error)
}

var _ Retriever = (*fakeRetriever)(nil)

func (f *fakeRetriever) Retrieve(
	ctx context.Context,
	query string,
	collectionID *uuid.UUID,
	topK int,
) ([]RetrievedChunk, error) {
	if f.retrieve == nil {
		panic("unexpected Retrieve call")
	}
	return f.retrieve(ctx, query, collectionID, topK)
}

type fakeLLM struct {
	chat   func(context.Context, ChatRequest) (*ChatResponse, error)
	stream func(context.Context, ChatRequest) (ChatStream, error)
}

var _ LLMClient = (*fakeLLM)(nil)

func (f *fakeLLM) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if f.chat == nil {
		panic("unexpected Chat call")
	}
	return f.chat(ctx, req)
}

func (f *fakeLLM) ChatStream(ctx context.Context, req ChatRequest) (ChatStream, error) {
	if f.stream == nil {
		panic("unexpected ChatStream call")
	}
	return f.stream(ctx, req)
}

type fakeCollectionLookup struct {
	get func(context.Context, uuid.UUID) (*knowledge.Collection, error)
}

var _ CollectionLookup = (*fakeCollectionLookup)(nil)

func (f *fakeCollectionLookup) GetCollection(ctx context.Context, id uuid.UUID) (*knowledge.Collection, error) {
	if f.get == nil {
		panic("unexpected GetCollection call")
	}
	return f.get(ctx, id)
}

func TestNewServiceRejectsInvalidDependencies(t *testing.T) {
	tests := []struct {
		name        string
		retriever   Retriever
		llm         LLMClient
		collections CollectionLookup
		topK        int
		maxTokens   int
	}{
		{name: "retriever nil", retriever: nil, llm: &fakeLLM{}, collections: &fakeCollectionLookup{}, topK: 5, maxTokens: 1024},
		{name: "llm nil", retriever: &fakeRetriever{}, llm: nil, collections: &fakeCollectionLookup{}, topK: 5, maxTokens: 1024},
		{name: "collections nil", retriever: &fakeRetriever{}, llm: &fakeLLM{}, collections: nil, topK: 5, maxTokens: 1024},
		{name: "topK 为 0", retriever: &fakeRetriever{}, llm: &fakeLLM{}, collections: &fakeCollectionLookup{}, topK: 0, maxTokens: 1024},
		{name: "topK 超过上限", retriever: &fakeRetriever{}, llm: &fakeLLM{}, collections: &fakeCollectionLookup{}, topK: MaxTopK + 1, maxTokens: 1024},
		{name: "maxTokens 为 0", retriever: &fakeRetriever{}, llm: &fakeLLM{}, collections: &fakeCollectionLookup{}, topK: 5, maxTokens: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewService 应当 panic")
				}
			}()
			NewService(tt.retriever, tt.llm, tt.collections, tt.topK, tt.maxTokens)
		})
	}
}

func TestServiceChatValidation(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{name: "空消息", message: ""},
		{name: "纯空白", message: " \t\n"},
		{name: "只有 1 个 rune", message: "问"},
		{name: "超过 4000 rune", message: strings.Repeat("字", MaxMessageRunes+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(&fakeRetriever{}, &fakeLLM{}, &fakeCollectionLookup{}, 5, 1024)
			_, err := svc.Chat(context.Background(), tt.message, nil)
			assertAgentErrorCode(t, err, apperr.CodeInvalidArgument)
		})
	}

	t.Run("collection 为 uuid.Nil", func(t *testing.T) {
		nilID := uuid.Nil
		svc := NewService(&fakeRetriever{}, &fakeLLM{}, &fakeCollectionLookup{}, 5, 1024)
		_, err := svc.Chat(context.Background(), "知识库", &nilID)
		assertAgentErrorCode(t, err, apperr.CodeInvalidArgument)
	})
}

func TestServiceChatCollection(t *testing.T) {
	collectionID := uuid.New()

	t.Run("collection 不存在", func(t *testing.T) {
		lookup := &fakeCollectionLookup{
			get: func(context.Context, uuid.UUID) (*knowledge.Collection, error) {
				return nil, knowledge.ErrNotFound
			},
		}
		svc := NewService(&fakeRetriever{}, &fakeLLM{}, lookup, 5, 1024)
		_, err := svc.Chat(context.Background(), "知识库", &collectionID)
		assertAgentErrorCode(t, err, apperr.CodeNotFound)
	})

	t.Run("collection 查询内部错误", func(t *testing.T) {
		baseErr := errors.New("db down")
		lookup := &fakeCollectionLookup{
			get: func(context.Context, uuid.UUID) (*knowledge.Collection, error) {
				return nil, baseErr
			},
		}
		svc := NewService(&fakeRetriever{}, &fakeLLM{}, lookup, 5, 1024)
		_, err := svc.Chat(context.Background(), "知识库", &collectionID)
		assertAgentErrorCode(t, err, apperr.CodeInternal)
		if !errors.Is(err, baseErr) {
			t.Fatalf("错误原因链应保留底层错误, got %v", err)
		}
	})

	t.Run("collection 存在时传给 retriever", func(t *testing.T) {
		lookup := &fakeCollectionLookup{
			get: func(_ context.Context, id uuid.UUID) (*knowledge.Collection, error) {
				if id != collectionID {
					t.Fatalf("collection id = %v, want %v", id, collectionID)
				}
				return &knowledge.Collection{ID: id}, nil
			},
		}
		var gotCollection *uuid.UUID
		retriever := &fakeRetriever{
			retrieve: func(_ context.Context, _ string, cid *uuid.UUID, topK int) ([]RetrievedChunk, error) {
				gotCollection = cid
				return []RetrievedChunk{}, nil
			},
		}
		svc := NewService(retriever, &fakeLLM{}, lookup, 5, 1024)
		if _, err := svc.Chat(context.Background(), "知识库", &collectionID); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if gotCollection == nil || *gotCollection != collectionID {
			t.Fatalf("retriever collection = %v, want %v", gotCollection, collectionID)
		}
	})
}

func TestServiceChatNoContext(t *testing.T) {
	retriever := &fakeRetriever{
		retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
			return nil, nil
		},
	}
	// llm.chat 留 nil：0 条结果时不能调用 LLM。
	svc := NewService(retriever, &fakeLLM{}, &fakeCollectionLookup{}, 5, 1024)

	result, err := svc.Chat(context.Background(), "没有资料的问题", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result.Answer != NoContextAnswer {
		t.Fatalf("answer = %q, want %q", result.Answer, NoContextAnswer)
	}
	if result.Citations == nil || len(result.Citations) != 0 {
		t.Fatalf("citations = %#v, want 非 nil 空切片", result.Citations)
	}
}

func TestServiceChatSuccess(t *testing.T) {
	collectionID := uuid.New()
	chunks := []RetrievedChunk{
		{ChunkID: uuid.New(), DocumentID: uuid.New(), DocumentTitle: "文档一", Content: "第一段"},
		{ChunkID: uuid.New(), DocumentID: uuid.New(), DocumentTitle: "文档二", Content: "第二段"},
	}

	var gotRequest ChatRequest
	retriever := &fakeRetriever{
		retrieve: func(_ context.Context, query string, cid *uuid.UUID, topK int) ([]RetrievedChunk, error) {
			if query != "知识库怎么检索" {
				t.Fatalf("query = %q", query)
			}
			if cid == nil || *cid != collectionID {
				t.Fatalf("collection = %v, want %v", cid, collectionID)
			}
			if topK != 5 {
				t.Fatalf("topK = %d, want 5", topK)
			}
			return chunks, nil
		},
	}
	llm := &fakeLLM{
		chat: func(_ context.Context, req ChatRequest) (*ChatResponse, error) {
			gotRequest = req
			return &ChatResponse{
				Content: "答案是 [1]",
				Usage:   TokenUsage{PromptTokens: 10, CompletionTokens: 5},
			}, nil
		},
	}
	lookup := &fakeCollectionLookup{
		get: func(context.Context, uuid.UUID) (*knowledge.Collection, error) {
			return &knowledge.Collection{ID: collectionID}, nil
		},
	}
	svc := NewService(retriever, llm, lookup, 5, 1024)

	result, err := svc.Chat(context.Background(), "  知识库怎么检索  ", &collectionID)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result.Answer != "答案是 [1]" {
		t.Fatalf("answer = %q", result.Answer)
	}
	if len(result.Citations) != 2 || result.Citations[0].ChunkID != chunks[0].ChunkID {
		t.Fatalf("citations = %+v", result.Citations)
	}
	if gotRequest.MaxTokens != 1024 || gotRequest.Temperature != agentTemperature {
		t.Fatalf("LLM request = %+v", gotRequest)
	}
	if len(gotRequest.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(gotRequest.Messages))
	}
	if !strings.Contains(gotRequest.Messages[1].Content, "[1] 文档标题：文档一") ||
		!strings.Contains(gotRequest.Messages[1].Content, "[2] 文档标题：文档二") {
		t.Fatalf("user prompt 没有编号资料: %s", gotRequest.Messages[1].Content)
	}
}

func TestServiceChatRetrieverError(t *testing.T) {
	baseErr := errors.New("search failed")
	retriever := &fakeRetriever{
		retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
			return nil, baseErr
		},
	}
	svc := NewService(retriever, &fakeLLM{}, &fakeCollectionLookup{}, 5, 1024)

	_, err := svc.Chat(context.Background(), "知识库", nil)
	assertAgentErrorCode(t, err, apperr.CodeInternal)
	if !errors.Is(err, baseErr) {
		t.Fatalf("错误原因链应保留底层错误, got %v", err)
	}
}

func TestServiceChatLLMError(t *testing.T) {
	baseErr := errors.New("llm timeout")
	retriever := &fakeRetriever{
		retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
			return []RetrievedChunk{{ChunkID: uuid.New(), DocumentID: uuid.New()}}, nil
		},
	}
	llm := &fakeLLM{
		chat: func(context.Context, ChatRequest) (*ChatResponse, error) {
			return nil, baseErr
		},
	}
	svc := NewService(retriever, llm, &fakeCollectionLookup{}, 5, 1024)

	_, err := svc.Chat(context.Background(), "知识库", nil)
	assertAgentErrorCode(t, err, apperr.CodeInternal)
	if !errors.Is(err, baseErr) {
		t.Fatalf("错误原因链应保留底层错误, got %v", err)
	}
}

func TestServiceChatEmptyLLMAnswer(t *testing.T) {
	retriever := &fakeRetriever{
		retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
			return []RetrievedChunk{{ChunkID: uuid.New(), DocumentID: uuid.New()}}, nil
		},
	}
	llm := &fakeLLM{
		chat: func(context.Context, ChatRequest) (*ChatResponse, error) {
			return &ChatResponse{Content: "   "}, nil
		},
	}
	svc := NewService(retriever, llm, &fakeCollectionLookup{}, 5, 1024)

	_, err := svc.Chat(context.Background(), "知识库", nil)
	assertAgentErrorCode(t, err, apperr.CodeInternal)
}

func TestBuildMessages(t *testing.T) {
	messages := BuildMessages("问题", []RetrievedChunk{
		{DocumentTitle: "文档一", Content: "第一段"},
		{DocumentTitle: "文档二", Content: "第二段"},
	})
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	if messages[0].Role != "system" || !strings.Contains(messages[0].Content, "只能根据") {
		t.Fatalf("system message = %+v", messages[0])
	}
	if !strings.Contains(messages[1].Content, "[1] 文档标题：文档一") ||
		!strings.Contains(messages[1].Content, "[2] 文档标题：文档二") {
		t.Fatalf("user message 没有编号资料: %s", messages[1].Content)
	}
}

func assertAgentErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %q, got nil", wantCode)
	}
	if got := apperr.Code(err); got != wantCode {
		t.Fatalf("错误码 = %q, want %q (err=%v)", got, wantCode, err)
	}
}

func TestServiceChatRetrieverAppErrors(t *testing.T) {
	tests := []struct {
		name     string
		repoErr  error
		wantCode string
	}{
		{
			name:     "retriever not_found",
			repoErr:  apperr.New(apperr.CodeNotFound, "知识库不存在"),
			wantCode: apperr.CodeNotFound,
		},
		{
			name:     "retriever invalid_argument",
			repoErr:  apperr.New(apperr.CodeInvalidArgument, "参数错误"),
			wantCode: apperr.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retriever := &fakeRetriever{
				retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
					return nil, tt.repoErr
				},
			}
			svc := NewService(retriever, &fakeLLM{}, &fakeCollectionLookup{}, 5, 1024)
			_, err := svc.Chat(context.Background(), "知识库", nil)
			assertAgentErrorCode(t, err, tt.wantCode)
		})
	}
}
