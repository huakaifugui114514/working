package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/huakaifugui114514/working/internal/extract"
)

// PDFExtractAction 从 params.path 读取 PDF 并抽取文本。
func PDFExtractAction(_ context.Context, _ map[string]any, params map[string]any) (map[string]any, error) {
	path, _ := params["path"].(string)
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("pdf_extract: path 不能为空")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("pdf_extract: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, extract.MaxPDFBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pdf_extract: read: %w", err)
	}
	text, err := extract.PDFText(data)
	if err != nil {
		return nil, fmt.Errorf("pdf_extract: %w", err)
	}
	return map[string]any{"text": text, "bytes": len(data)}, nil
}

// HTTPActionOptions 控制 HTTP action 的鉴权与客户端。
type HTTPActionOptions struct {
	APIKey string
	Client *http.Client
}

// HTTPAction 调用 HTTP API；body 可以是 map（JSON）或 string。
func HTTPAction(opts HTTPActionOptions) Action {
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return func(ctx context.Context, _ map[string]any, params map[string]any) (map[string]any, error) {
		url, _ := params["url"].(string)
		if url == "" {
			return nil, fmt.Errorf("http_request: url 不能为空")
		}
		method := "GET"
		if v, ok := params["method"].(string); ok && v != "" {
			method = strings.ToUpper(v)
		}
		var body io.Reader
		if rawBody, ok := params["body"]; ok {
			raw, err := json.Marshal(rawBody)
			if err != nil {
				return nil, fmt.Errorf("http_request: marshal body: %w", err)
			}
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, fmt.Errorf("http_request: new request: %w", err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if opts.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+opts.APIKey)
		}
		if headers, ok := params["headers"].(map[string]any); ok {
			for k, v := range headers {
				req.Header.Set(k, fmt.Sprint(v))
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("http_request: do: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		var decoded any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &decoded)
		}
		if decoded == nil && len(raw) > 0 {
			decoded = string(raw)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("http_request: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return map[string]any{"status_code": resp.StatusCode, "body": decoded}, nil
	}
}

// SleepAction 用于演示/节奏控制。
func SleepAction(ctx context.Context, _ map[string]any, params map[string]any) (map[string]any, error) {
	raw, _ := params["duration"].(string)
	d, err := time.ParseDuration(raw)
	if err != nil {
		return nil, fmt.Errorf("sleep: duration: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(d):
		return map[string]any{"slept": d.String()}, nil
	}
}

// NoopAction 什么都不做。
func NoopAction(context.Context, map[string]any, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}
