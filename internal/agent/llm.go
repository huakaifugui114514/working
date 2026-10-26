package agent

import (
	"context"
	"errors"
)

// ErrLLMNotConfigured 表示外部 LLM 没有配置（通常是缺少 API key）。
var ErrLLMNotConfigured = errors.New("agent: LLM 未配置")

// ChatMessage 是 LLM 的一条消息（system / user / assistant）。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 是一次生成请求。
type ChatRequest struct {
	Messages    []ChatMessage
	Temperature float64
	MaxTokens   int
}

// TokenUsage 记录 token 消耗；日志可以记，响应体不暴露。
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
}

// ChatResponse 是一次生成的返回。
type ChatResponse struct {
	Content string
	Usage   TokenUsage
}

// LLMClient 是 Agent 依赖的生成接口。
//
// M4.3a 用 fake；M4.3b 再用 DeepSeek 的 OpenAI-compatible 客户端。
type LLMClient interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}
