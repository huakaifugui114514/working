// Package qdrant 是 Qdrant REST API 的最小客户端。
//
// 只覆盖 M4.4 需要的四个操作：建集合、按文档删除、upsert、向量检索。
// 不引官方 Go client：当前接口很小，标准库足够；等过滤/重试复杂了再评估。
package qdrant

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

	"github.com/google/uuid"
)

// Options 是 Qdrant 客户端配置。
type Options struct {
	BaseURL    string
	Collection string
	VectorSize int
	Timeout    time.Duration
}

// Point 是一个待写入的向量点。
type Point struct {
	ID      uuid.UUID
	Vector  []float32
	Payload map[string]any
}

// SearchHit 是一条向量检索结果。
type SearchHit struct {
	ChunkID    uuid.UUID
	DocumentID uuid.UUID
	Score      float64
	Payload    map[string]any
}

// Client 是 Qdrant REST 客户端。
type Client struct {
	baseURL    string
	collection string
	vectorSize int
	http       *http.Client
}

// NewClient 校验配置并构造客户端。
func NewClient(opts Options) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("qdrant: base_url 不能为空")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("qdrant: base_url=%q 不是合法 http(s) URL", opts.BaseURL)
	}
	if strings.TrimSpace(opts.Collection) == "" {
		return nil, fmt.Errorf("qdrant: collection 不能为空")
	}
	if opts.VectorSize < 1 {
		return nil, fmt.Errorf("qdrant: vector_size 必须为正数")
	}
	if opts.Timeout <= 0 {
		return nil, fmt.Errorf("qdrant: timeout 必须为正数")
	}
	return &Client{
		baseURL:    baseURL,
		collection: opts.Collection,
		vectorSize: opts.VectorSize,
		http:       &http.Client{Timeout: opts.Timeout},
	}, nil
}

// EnsureCollection 确保 collection 存在且向量维度/距离匹配。
func (c *Client) EnsureCollection(ctx context.Context) error {
	path := c.collectionPath()
	raw, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return c.validateCollection(raw)
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("qdrant: 查询 collection status=%d body=%s", status, strings.TrimSpace(string(raw)))
	}

	body := map[string]any{
		"vectors": map[string]any{
			"size":     c.vectorSize,
			"distance": "Cosine",
		},
	}
	raw, status, err = c.do(ctx, http.MethodPut, path, body)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		// 两个 worker 同时 EnsureCollection 时，一个会拿到 409。
		// 重新 GET 并校验配置，把“并发创建”当成幂等成功。
		raw, status, err = c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("qdrant: collection 已存在但查询失败 status=%d body=%s", status, strings.TrimSpace(string(raw)))
		}
		return c.validateCollection(raw)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("qdrant: 创建 collection status=%d body=%s", status, strings.TrimSpace(string(raw)))
	}
	return nil
}

func (c *Client) validateCollection(raw []byte) error {
	var info collectionInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return fmt.Errorf("qdrant: 解析 collection 信息失败: %w", err)
	}
	got := info.Result.Config.Params.Vectors
	if got.Size != c.vectorSize {
		return fmt.Errorf("qdrant: collection %q 维度=%d, want %d", c.collection, got.Size, c.vectorSize)
	}
	if !strings.EqualFold(got.Distance, "Cosine") {
		return fmt.Errorf("qdrant: collection %q distance=%q, want Cosine", c.collection, got.Distance)
	}
	return nil
}

// DeleteByDocument 删除某个文档的全部向量。
//
// 用 payload.document_id filter，而不是旧 chunk id 列表：
// 正文替换后 chunk UUID 全部变化，旧 id 列表无法完整覆盖。
func (c *Client) DeleteByDocument(ctx context.Context, documentID uuid.UUID) error {
	if documentID == uuid.Nil {
		return fmt.Errorf("qdrant: document_id 不能为空")
	}
	body := map[string]any{
		"filter": map[string]any{
			"must": []any{
				map[string]any{
					"key":   "document_id",
					"match": map[string]any{"value": documentID.String()},
				},
			},
		},
	}
	raw, status, err := c.do(ctx, http.MethodPost, c.collectionPath()+"/points/delete?wait=true", body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("qdrant: 删除 points status=%d body=%s", status, strings.TrimSpace(string(raw)))
	}
	return nil
}

// Upsert 批量写入/覆盖向量点。
func (c *Client) Upsert(ctx context.Context, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	payload := make([]pointPayload, 0, len(points))
	for _, p := range points {
		if p.ID == uuid.Nil {
			return fmt.Errorf("qdrant: point id 不能为空")
		}
		if len(p.Vector) != c.vectorSize {
			return fmt.Errorf("qdrant: point %s 维度=%d, want %d", p.ID, len(p.Vector), c.vectorSize)
		}
		payload = append(payload, pointPayload{
			ID:      p.ID.String(),
			Vector:  p.Vector,
			Payload: p.Payload,
		})
	}

	raw, status, err := c.do(ctx, http.MethodPut, c.collectionPath()+"/points?wait=true", map[string]any{
		"points": payload,
	})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("qdrant: upsert points status=%d body=%s", status, strings.TrimSpace(string(raw)))
	}
	return nil
}

// Search 按向量检索 topK。
func (c *Client) Search(ctx context.Context, vector []float32, topK int) ([]SearchHit, error) {
	if len(vector) != c.vectorSize {
		return nil, fmt.Errorf("qdrant: query 维度=%d, want %d", len(vector), c.vectorSize)
	}
	if topK < 1 {
		return nil, fmt.Errorf("qdrant: topK 必须为正数")
	}

	raw, status, err := c.do(ctx, http.MethodPost, c.collectionPath()+"/points/search", map[string]any{
		"vector":       vector,
		"limit":        topK,
		"with_payload": true,
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("qdrant: search status=%d body=%s", status, strings.TrimSpace(string(raw)))
	}

	var out searchResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("qdrant: 解析 search 响应失败: %w", err)
	}
	hits := make([]SearchHit, 0, len(out.Result))
	for _, item := range out.Result {
		id, err := uuid.Parse(item.ID)
		if err != nil {
			return nil, fmt.Errorf("qdrant: 非法 point id %q: %w", item.ID, err)
		}
		hit := SearchHit{ChunkID: id, Score: item.Score, Payload: item.Payload}
		if rawDoc, ok := item.Payload["document_id"].(string); ok {
			docID, err := uuid.Parse(rawDoc)
			if err != nil {
				return nil, fmt.Errorf("qdrant: 非法 document_id %q: %w", rawDoc, err)
			}
			hit.DocumentID = docID
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

type pointPayload struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload,omitempty"`
}

type collectionInfo struct {
	Result struct {
		Config struct {
			Params struct {
				Vectors struct {
					Size     int    `json:"size"`
					Distance string `json:"distance"`
				} `json:"vectors"`
			} `json:"params"`
		} `json:"config"`
	} `json:"result"`
}

type searchResponse struct {
	Result []struct {
		ID      string         `json:"id"`
		Score   float64        `json:"score"`
		Payload map[string]any `json:"payload"`
	} `json:"result"`
}

func (c *Client) collectionPath() string {
	return c.baseURL + "/collections/" + url.PathEscape(c.collection)
}

func (c *Client) do(ctx context.Context, method, endpoint string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("qdrant: 编码请求失败: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("qdrant: 创建请求失败: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("qdrant: 请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("qdrant: 读取响应失败: %w", err)
	}
	return raw, resp.StatusCode, nil
}
