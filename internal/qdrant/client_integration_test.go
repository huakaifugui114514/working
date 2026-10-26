//go:build integration

package qdrant

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestQdrantIntegration 需要真实 Qdrant。
//
//	KF_QDRANT_URL=http://127.0.0.1:6333 go test -tags=integration -run TestQdrantIntegration -v ./internal/qdrant
func TestQdrantIntegration(t *testing.T) {
	baseURL := os.Getenv("KF_QDRANT_URL")
	if baseURL == "" {
		t.Skip("未设置 KF_QDRANT_URL，跳过真实 Qdrant 测试")
	}

	collection := fmt.Sprintf("knowflow_it_%s", uuid.NewString()[:8])
	client, err := NewClient(Options{
		BaseURL:    baseURL,
		Collection: collection,
		VectorSize: 4,
		Timeout:    10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, baseURL+"/collections/"+collection, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	})

	if err := client.EnsureCollection(ctx); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	// 再调一次应走“已存在且配置匹配”分支。
	if err := client.EnsureCollection(ctx); err != nil {
		t.Fatalf("EnsureCollection 第二次: %v", err)
	}

	docID := uuid.New()
	chunkIDs := []uuid.UUID{uuid.New(), uuid.New()}
	if err := client.Upsert(ctx, []Point{
		{ID: chunkIDs[0], Vector: []float32{1, 0, 0, 0}, Payload: map[string]any{"document_id": docID.String()}},
		{ID: chunkIDs[1], Vector: []float32{0, 1, 0, 0}, Payload: map[string]any{"document_id": docID.String()}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	hits, err := client.Search(ctx, []float32{0.9, 0.1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 || hits[0].ChunkID != chunkIDs[0] || hits[0].DocumentID != docID {
		t.Fatalf("hits = %+v", hits)
	}

	if err := client.DeleteByDocument(ctx, docID); err != nil {
		t.Fatalf("DeleteByDocument: %v", err)
	}
	hits, err = client.Search(ctx, []float32{0.9, 0.1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("删除后仍有 hits: %+v", hits)
	}
}
