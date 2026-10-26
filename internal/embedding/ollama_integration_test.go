//go:build integration

package embedding

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestOllamaEmbedIntegration 需要真实 Ollama + bge-m3。
// 默认跳过，避免普通 integration suite 强依赖本地模型服务：
//
//	KF_OLLAMA_URL=http://127.0.0.1:11434 go test -tags=integration -run TestOllamaEmbedIntegration -v ./internal/embedding
func TestOllamaEmbedIntegration(t *testing.T) {
	baseURL := os.Getenv("KF_OLLAMA_URL")
	if baseURL == "" {
		t.Skip("未设置 KF_OLLAMA_URL，跳过真实 Ollama 测试")
	}

	embedder, err := NewOllamaEmbedder(OllamaOptions{
		BaseURL: baseURL,
		Model:   "bge-m3",
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOllamaEmbedder: %v", err)
	}

	vectors, err := embedder.Embed(context.Background(), []string{
		"KnowFlow 使用 MySQL FULLTEXT 做关键词检索。",
		"How does vector search work?",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("向量数 = %d, want 2", len(vectors))
	}
	if len(vectors[0]) != 1024 || len(vectors[1]) != 1024 {
		t.Fatalf("维度 = %d/%d, want 1024", len(vectors[0]), len(vectors[1]))
	}
	t.Logf("BGE-M3 dim=%d first3=%v", len(vectors[0]), vectors[0][:3])
}
