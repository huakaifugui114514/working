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

// StreamDelta 是流式生成的一段内容。
// Done=true 表示模型流结束；Content 可以为空。
type StreamDelta struct {
	Content string
	Done    bool
}

// ChatStream 是一次流式生成。
type ChatStream interface {
	// Recv 返回下一段内容；流正常结束时返回 Done=true。
	Recv() (StreamDelta, error)
	Close() error
}

// StreamingLLMClient 是可选能力：实现它的 LLM 支持流式输出。
//
// AgentService 在可用时走 SSE；未实现时降级为一次性回答。
type StreamingLLMClient interface {
	ChatStream(ctx context.Context, req ChatRequest) (ChatStream, error)
}

// LLMClient 是 Agent 依赖的生成接口。
//
// M4.3a 用 fake；M4.3b 再用 DeepSeek 的 OpenAI-compatible 客户端。
type LLMClient interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}
