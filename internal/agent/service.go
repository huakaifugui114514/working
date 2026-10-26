package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/knowledge"
)

const (
	// DefaultTopK 是单轮 RAG 默认检索的 chunk 数。
	DefaultTopK = 5

	// MaxTopK 是 topK 上限，避免上下文过长。
	MaxTopK = 20

	// MinMessageRunes 是问题的最小长度。
	// 和 SearchService 的 q 下限对齐：1 个字符无法形成有效检索。
	MinMessageRunes = 2

	// MaxMessageRunes 是问题的最大长度。
	MaxMessageRunes = 4000

	// NoContextAnswer 是检索不到资料时的固定回答；此时不调用 LLM。
	NoContextAnswer = "知识库中没有找到相关内容。"

	agentTemperature = 0.2
)

// CollectionLookup 是 AgentService 需要的 collection 存在性检查。
//
// 只取一个方法；*knowledge.Service 已满足这个接口。
type CollectionLookup interface {
	GetCollection(ctx context.Context, id uuid.UUID) (*knowledge.Collection, error)
}

// ChatResult 是一次单轮 RAG 的结果。
//
// Citations 的顺序必须和 prompt 里的 [1]..[n] 一致；
// handler 会按切片顺序生成 index=1..n。
type ChatResult struct {
	Answer    string
	Citations []RetrievedChunk
}

// Service 是单轮 RAG 的业务层。
type Service struct {
	retriever   Retriever
	llm         LLMClient
	collections CollectionLookup
	topK        int
	maxTokens   int
}

// NewService 注入检索、LLM 和 collection 查询。依赖不能为 nil。
func NewService(
	retriever Retriever,
	llm LLMClient,
	collections CollectionLookup,
	topK int,
	maxTokens int,
) *Service {
	if retriever == nil {
		panic("agent.NewService: retriever is nil")
	}
	if llm == nil {
		panic("agent.NewService: llm is nil")
	}
	if collections == nil {
		panic("agent.NewService: collections is nil")
	}
	if topK < 1 || topK > MaxTopK {
		panic(fmt.Sprintf("agent.NewService: topK must be 1..%d", MaxTopK))
	}
	if maxTokens < 1 {
		panic("agent.NewService: maxTokens must be positive")
	}
	return &Service{
		retriever:   retriever,
		llm:         llm,
		collections: collections,
		topK:        topK,
		maxTokens:   maxTokens,
	}
}

// Chat 执行一次单轮 RAG。
//
// 顺序：
//  1. message trim；2..4000 rune
//  2. collectionID 非 nil 时做存在性检查
//  3. 检索 topK 个 chunk
//  4. 0 条结果：固定 answer + 空 citations，不调用 LLM
//  5. 拼 prompt，调 LLM
//  6. 返回 answer + citations
func (s *Service) Chat(
	ctx context.Context,
	message string,
	collectionID *uuid.UUID,
) (*ChatResult, error) {
	message = strings.TrimSpace(message)
	messageRunes := utf8.RuneCountInString(message)
	if messageRunes < MinMessageRunes || messageRunes > MaxMessageRunes {
		return nil, apperr.New(
			apperr.CodeInvalidArgument,
			fmt.Sprintf("message 长度必须在 %d~%d 个字符之间", MinMessageRunes, MaxMessageRunes),
		)
	}

	if collectionID != nil {
		if *collectionID == uuid.Nil {
			return nil, apperr.New(apperr.CodeInvalidArgument, "collection_id 不能为空")
		}
		if _, err := s.collections.GetCollection(ctx, *collectionID); err != nil {
			if errors.Is(err, knowledge.ErrNotFound) {
				return nil, apperr.Wrap(err, apperr.CodeNotFound, "知识库不存在")
			}
			return nil, apperr.Wrap(err, apperr.CodeInternal, "查询知识库失败")
		}
	}

	chunks, err := s.retriever.Retrieve(ctx, message, collectionID, s.topK)
	if err != nil {
		switch apperr.Code(err) {
		case apperr.CodeNotFound:
			return nil, apperr.Wrap(err, apperr.CodeNotFound, "知识库不存在")
		case apperr.CodeInvalidArgument:
			return nil, apperr.Wrap(err, apperr.CodeInvalidArgument, "检索参数不合法")
		default:
			return nil, apperr.Wrap(err, apperr.CodeInternal, "检索失败")
		}
	}

	if len(chunks) == 0 {
		return &ChatResult{
			Answer:    NoContextAnswer,
			Citations: []RetrievedChunk{},
		}, nil
	}

	resp, err := s.llm.Chat(ctx, ChatRequest{
		Messages:    BuildMessages(message, chunks),
		Temperature: agentTemperature,
		MaxTokens:   s.maxTokens,
	})
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "生成回答失败")
	}

	answer := strings.TrimSpace(resp.Content)
	if answer == "" {
		return nil, apperr.New(apperr.CodeInternal, "生成回答为空")
	}

	return &ChatResult{
		Answer:    answer,
		Citations: chunks,
	}, nil
}
