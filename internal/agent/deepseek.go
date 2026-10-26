package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeepSeekOptions 是 DeepSeek / OpenAI-compatible 客户端的配置。
type DeepSeekOptions struct {
	BaseURL   string
	APIKey    string
	Model     string
	Timeout   time.Duration
	MaxTokens int
}

// DeepSeekClient 是 OpenAI-compatible chat/completions 客户端。
//
// 只用标准库 net/http：这个接口很简单，不需要为它引入 SDK。
type DeepSeekClient struct {
	endpoint  string
	apiKey    string
	model     string
	maxTokens int
	http      *http.Client
}

// NewDeepSeekClient 校验基础配置并构造客户端。
//
// APIKey 允许为空：本地没有 key 时服务仍可启动，Chat 会返回 ErrLLMNotConfigured。
func NewDeepSeekClient(opts DeepSeekOptions) (*DeepSeekClient, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("deepseek: base_url 不能为空")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("deepseek: base_url=%q 不是合法 http(s) URL", opts.BaseURL)
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("deepseek: model 不能为空")
	}
	if opts.Timeout <= 0 {
		return nil, fmt.Errorf("deepseek: timeout 必须为正数")
	}
	if opts.MaxTokens < 1 {
		return nil, fmt.Errorf("deepseek: max_tokens 必须 ≥ 1")
	}

	return &DeepSeekClient{
		endpoint:  baseURL + "/chat/completions",
		apiKey:    opts.APIKey,
		model:     opts.Model,
		maxTokens: opts.MaxTokens,
		http:      &http.Client{Timeout: opts.Timeout},
	}, nil
}

type deepSeekChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	Stream      bool          `json:"stream"`
}

type deepSeekChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat 调用 {baseURL}/chat/completions。
//
// 非 2xx、超时、空 choices、空 content 都返回 error。
func (c *DeepSeekClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("%w: 缺少 KF_LLM_API_KEY", ErrLLMNotConfigured)
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("deepseek: messages 不能为空")
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.maxTokens
	}

	payload := deepSeekChatRequest{
		Model:       c.model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   maxTokens,
		Stream:      false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("deepseek: 编码请求失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("deepseek: 创建请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("deepseek: 请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("deepseek: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out deepSeekChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("deepseek: 解析响应失败: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("deepseek: 响应没有 choices")
	}

	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return nil, fmt.Errorf("deepseek: 响应 content 为空")
	}

	return &ChatResponse{
		Content: content,
		Usage: TokenUsage{
			PromptTokens:     out.Usage.PromptTokens,
			CompletionTokens: out.Usage.CompletionTokens,
		},
	}, nil
}
