package agent

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

func TestNewDeepSeekClientValidation(t *testing.T) {
	tests := []struct {
		name string
		opts DeepSeekOptions
	}{
		{name: "base_url 为空", opts: DeepSeekOptions{Model: "deepseek-chat", Timeout: time.Second, MaxTokens: 10}},
		{name: "base_url 不是 http(s)", opts: DeepSeekOptions{BaseURL: "file:///tmp", Model: "deepseek-chat", Timeout: time.Second, MaxTokens: 10}},
		{name: "model 为空", opts: DeepSeekOptions{BaseURL: "https://api.deepseek.com", Timeout: time.Second, MaxTokens: 10}},
		{name: "timeout 为 0", opts: DeepSeekOptions{BaseURL: "https://api.deepseek.com", Model: "deepseek-chat", MaxTokens: 10}},
		{name: "max_tokens 为 0", opts: DeepSeekOptions{BaseURL: "https://api.deepseek.com", Model: "deepseek-chat", Timeout: time.Second}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewDeepSeekClient(tt.opts); err == nil {
				t.Fatal("NewDeepSeekClient 应当返回错误")
			}
		})
	}
}

func TestDeepSeekChatSuccess(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("解码请求失败: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"choices":[{"message":{"content":"答案 [1]"}}],
			"usage":{"prompt_tokens":11,"completion_tokens":7}
		}`)
	}))
	defer server.Close()

	client, err := NewDeepSeekClient(DeepSeekOptions{
		BaseURL:   server.URL,
		APIKey:    "test-key",
		Model:     "deepseek-chat",
		Timeout:   time.Second,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("NewDeepSeekClient: %v", err)
	}

	resp, err := client.Chat(context.Background(), ChatRequest{
		Messages:    []ChatMessage{{Role: "user", Content: "你好"}},
		Temperature: 0.2,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "答案 [1]" {
		t.Fatalf("content = %q", resp.Content)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 7 {
		t.Fatalf("usage = %+v", resp.Usage)
	}

	if payload["model"] != "deepseek-chat" {
		t.Fatalf("model = %v", payload["model"])
	}
	if payload["stream"] != false {
		t.Fatalf("stream = %v, want false", payload["stream"])
	}
	if payload["max_tokens"] != float64(1024) {
		t.Fatalf("max_tokens = %v, want 1024", payload["max_tokens"])
	}
}

func TestDeepSeekChatNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":"bad key"}`)
	}))
	defer server.Close()

	client, err := NewDeepSeekClient(DeepSeekOptions{
		BaseURL:   server.URL,
		APIKey:    "secret-key",
		Model:     "deepseek-chat",
		Timeout:   time.Second,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("NewDeepSeekClient: %v", err)
	}

	_, err = client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "你好"}},
	})
	if err == nil {
		t.Fatal("非 2xx 应返回错误")
	}
	if !strings.Contains(err.Error(), "status=401") {
		t.Fatalf("错误应包含状态码, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("错误不应泄露 API key: %v", err)
	}
}

func TestDeepSeekChatEmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"choices":[]}`)
	}))
	defer server.Close()

	client, err := NewDeepSeekClient(DeepSeekOptions{
		BaseURL:   server.URL,
		APIKey:    "key",
		Model:     "deepseek-chat",
		Timeout:   time.Second,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("NewDeepSeekClient: %v", err)
	}

	_, err = client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "你好"}},
	})
	if err == nil {
		t.Fatal("空 choices 应返回错误")
	}
}

func TestDeepSeekChatMissingKey(t *testing.T) {
	client, err := NewDeepSeekClient(DeepSeekOptions{
		BaseURL:   "http://127.0.0.1:1",
		Model:     "deepseek-chat",
		Timeout:   time.Second,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("NewDeepSeekClient: %v", err)
	}

	_, err = client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "你好"}},
	})
	if !errors.Is(err, ErrLLMNotConfigured) {
		t.Fatalf("error = %v, want ErrLLMNotConfigured", err)
	}
}

func TestDeepSeekChatRejectsEmptyMessages(t *testing.T) {
	client, err := NewDeepSeekClient(DeepSeekOptions{
		BaseURL:   "http://127.0.0.1:1",
		APIKey:    "key",
		Model:     "deepseek-chat",
		Timeout:   time.Second,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("NewDeepSeekClient: %v", err)
	}
	if _, err := client.Chat(context.Background(), ChatRequest{}); err == nil {
		t.Fatal("空 messages 应返回错误")
	}
}

func TestDeepSeekChatEmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"   "}}]}`)
	}))
	defer server.Close()

	client, err := NewDeepSeekClient(DeepSeekOptions{
		BaseURL:   server.URL,
		APIKey:    "key",
		Model:     "deepseek-chat",
		Timeout:   time.Second,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("NewDeepSeekClient: %v", err)
	}
	_, err = client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "你好"}},
	})
	if err == nil {
		t.Fatal("空 content 应返回错误")
	}
}

func TestDeepSeekChatInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `not-json`)
	}))
	defer server.Close()

	client, err := NewDeepSeekClient(DeepSeekOptions{
		BaseURL:   server.URL,
		APIKey:    "key",
		Model:     "deepseek-chat",
		Timeout:   time.Second,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("NewDeepSeekClient: %v", err)
	}
	_, err = client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "你好"}},
	})
	if err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
}
