package mcp

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

// APIClient 调 KnowFlow REST API；MCP Server 不直接访问 MySQL。
type APIClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewAPIClient 校验 baseURL 并构造客户端。
func NewAPIClient(baseURL, apiKey string, timeout time.Duration) (*APIClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("mcp: api_url=%q 不是合法 http(s) URL", baseURL)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("mcp: timeout 必须为正数")
	}
	return &APIClient{baseURL: baseURL, apiKey: apiKey, http: &http.Client{Timeout: timeout}}, nil
}

// Search 调 GET /api/v1/search。
func (c *APIClient) Search(ctx context.Context, query string, collectionID string, limit int) (any, error) {
	values := url.Values{}
	values.Set("q", query)
	values.Set("limit", fmt.Sprintf("%d", limit))
	if collectionID != "" {
		values.Set("collection_id", collectionID)
	}
	return c.do(ctx, http.MethodGet, "/api/v1/search?"+values.Encode(), nil)
}

// Document 调 GET /api/v1/documents/{id}。
func (c *APIClient) Document(ctx context.Context, documentID string) (any, error) {
	return c.do(ctx, http.MethodGet, "/api/v1/documents/"+url.PathEscape(documentID), nil)
}

// Ask 调 POST /api/v1/agent/chat。
func (c *APIClient) Ask(ctx context.Context, message, collectionID string) (any, error) {
	body := map[string]any{"message": message}
	if collectionID != "" {
		body["collection_id"] = collectionID
	}
	raw, _ := json.Marshal(body)
	return c.do(ctx, http.MethodPost, "/api/v1/agent/chat", raw)
}

func (c *APIClient) do(ctx context.Context, method, path string, body []byte) (any, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("mcp: 创建请求失败: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: 请求 KnowFlow 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp: KnowFlow status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp: 解析 KnowFlow 响应失败: %w", err)
	}
	return out, nil
}
