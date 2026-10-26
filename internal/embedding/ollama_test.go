package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewOllamaEmbedderValidation(t *testing.T) {
	tests := []struct {
		name string
		opts OllamaOptions
	}{
		{name: "base_url 为空", opts: OllamaOptions{Model: "bge-m3", Timeout: time.Second}},
		{name: "base_url 非法", opts: OllamaOptions{BaseURL: "file:///tmp", Model: "bge-m3", Timeout: time.Second}},
		{name: "model 为空", opts: OllamaOptions{BaseURL: "http://127.0.0.1:11434", Timeout: time.Second}},
		{name: "timeout 为 0", opts: OllamaOptions{BaseURL: "http://127.0.0.1:11434", Model: "bge-m3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewOllamaEmbedder(tt.opts); err == nil {
				t.Fatal("NewOllamaEmbedder 应当返回错误")
			}
		})
	}
}

func TestOllamaEmbedSuccess(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q, want /api/embed", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("解码请求失败: %v", err)
		}
		_, _ = fmt.Fprint(w, `{"embeddings":[[0.1,0.2],[0.3,0.4]]}`)
	}))
	defer server.Close()

	embedder, err := NewOllamaEmbedder(OllamaOptions{
		BaseURL: server.URL,
		Model:   "bge-m3",
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewOllamaEmbedder: %v", err)
	}

	vectors, err := embedder.Embed(context.Background(), []string{"第一段", "第二段"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 2 || vectors[0][0] != 0.1 {
		t.Fatalf("vectors = %+v", vectors)
	}
	if payload["model"] != "bge-m3" {
		t.Fatalf("model = %v", payload["model"])
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input = %#v", payload["input"])
	}
}

func TestOllamaEmbedErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{name: "非 2xx", status: http.StatusInternalServerError, body: `{"error":"boom"}`, wantSub: "status=500"},
		{name: "非法 JSON", status: http.StatusOK, body: `not-json`, wantSub: "解析"},
		{name: "向量数量不匹配", status: http.StatusOK, body: `{"embeddings":[[0.1]]}`, wantSub: "向量数量"},
		{name: "空向量", status: http.StatusOK, body: `{"embeddings":[[],[]]}`, wantSub: "为空"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			embedder, err := NewOllamaEmbedder(OllamaOptions{
				BaseURL: server.URL,
				Model:   "bge-m3",
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatalf("NewOllamaEmbedder: %v", err)
			}
			_, err = embedder.Embed(context.Background(), []string{"a", "b"})
			if err == nil {
				t.Fatal("期望错误")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error = %v, want contains %q", err, tt.wantSub)
			}
		})
	}
}

func TestOllamaEmbedEmptyInput(t *testing.T) {
	embedder, err := NewOllamaEmbedder(OllamaOptions{
		BaseURL: "http://127.0.0.1:1",
		Model:   "bge-m3",
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewOllamaEmbedder: %v", err)
	}
	_, err = embedder.Embed(context.Background(), nil)
	if !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("error = %v, want ErrEmptyInput", err)
	}
}
