package embedding

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

// OllamaOptions 是本地 Ollama embedding 的配置。
type OllamaOptions struct {
	BaseURL string
	Model   string
	Timeout time.Duration
}

// OllamaEmbedder 调 Ollama 的 POST /api/embed。
//
// 仅用于本地开发/评测；CPU 推理吞吐有限，不作为高并发生产方案。
type OllamaEmbedder struct {
	endpoint string
	model    string
	http     *http.Client
}

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// NewOllamaEmbedder 校验配置并构造客户端。
func NewOllamaEmbedder(opts OllamaOptions) (*OllamaEmbedder, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("embedding: base_url 不能为空")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("embedding: base_url=%q 不是合法 http(s) URL", opts.BaseURL)
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("embedding: model 不能为空")
	}
	if opts.Timeout <= 0 {
		return nil, fmt.Errorf("embedding: timeout 必须为正数")
	}

	return &OllamaEmbedder{
		endpoint: baseURL + "/api/embed",
		model:    opts.Model,
		http:     &http.Client{Timeout: opts.Timeout},
	}, nil
}

// Embed 调用 Ollama /api/embed。
func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, ErrEmptyInput
	}

	body, err := json.Marshal(ollamaEmbedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("embedding: 编码请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: 创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding: 请求 Ollama 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("embedding: Ollama status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embedding: 解析 Ollama 响应失败: %w", err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embedding: 向量数量=%d, want %d", len(out.Embeddings), len(texts))
	}
	for i, vec := range out.Embeddings {
		if len(vec) == 0 {
			return nil, fmt.Errorf("embedding: 第 %d 个向量为空", i)
		}
	}

	return out.Embeddings, nil
}
