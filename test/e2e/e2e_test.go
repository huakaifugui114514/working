//go:build e2e

// Package e2e 跑真实 HTTP + MySQL + Qdrant 的端到端用例。
//
// 不依赖 Ollama/DeepSeek：用 fake embedder 和 fake LLM 隔离外部模型，
// 其他链路（鉴权、正文入库、outbox、worker、Qdrant、Hybrid、citations）都是真的。
//
// 运行：
//
//	KF_DATABASE_HOST=127.0.0.1 ... KF_QDRANT_URL=http://127.0.0.1:6333 \
//	  go test -tags=e2e -v ./test/e2e
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/agent"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
	"github.com/huakaifugui114514/working/internal/embedding"
	"github.com/huakaifugui114514/working/internal/indexer"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/qdrant"
	"github.com/huakaifugui114514/working/internal/transport/rest"
	"github.com/huakaifugui114514/working/internal/version"
)

const authKey = "e2e-test-api-key"

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 1024)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

type fakeLLM struct{}

func (fakeLLM) Chat(_ context.Context, _ agent.ChatRequest) (*agent.ChatResponse, error) {
	return &agent.ChatResponse{Content: "答案 [1]"}, nil
}

func TestFullRAGHTTPFlow(t *testing.T) {
	if os.Getenv("KF_DATABASE_HOST") == "" || os.Getenv("KF_QDRANT_URL") == "" {
		t.Skip("未设置 KF_DATABASE_* / KF_QDRANT_URL，跳过 E2E")
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("加载配置: %v", err)
	}
	cfg.Auth.APIKey = authKey
	cfg.App.Env = config.EnvTest

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), cfg.Database, logger)
	if err != nil {
		t.Fatalf("连接数据库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	collectionName := fmt.Sprintf("e2e_%s", uuid.NewString()[:8])
	qdrantClient, err := qdrant.NewClient(qdrant.Options{
		BaseURL:    os.Getenv("KF_QDRANT_URL"),
		Collection: collectionName,
		VectorSize: 1024,
		Timeout:    10 * time.Second,
	})
	if err != nil {
		t.Fatalf("初始化 Qdrant: %v", err)
	}
	if err := qdrantClient.EnsureCollection(context.Background()); err != nil {
		t.Fatalf("创建 Qdrant collection: %v", err)
	}
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete,
			os.Getenv("KF_QDRANT_URL")+"/collections/"+collectionName, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	})

	repo := knowledge.NewMySQLRepository(db)
	collectionSvc := knowledge.NewService(repo)
	documentSvc := knowledge.NewDocumentService(repo)
	contentSvc := knowledge.NewContentService(repo)
	searchSvc := knowledge.NewSearchService(repo, collectionSvc)

	keyword := agent.NewKeywordRetriever(searchSvc)
	vector := agent.NewVectorRetriever(fakeEmbedder{}, qdrantClient, repo)
	hybrid := agent.NewHybridRetriever(keyword, vector, 20, 60)
	agentSvc := agent.NewService(hybrid, fakeLLM{}, collectionSvc, 5, 1024)

	server := rest.New(*cfg, logger, version.Info{Version: "e2e"},
		collectionSvc, documentSvc, contentSvc, searchSvc, agentSvc)
	server.SetReadiness(db)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	post := func(path string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, httpServer.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+authKey)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s status=%d want=%d body=%s", path, resp.StatusCode, want, b)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	// 未鉴权必须 401。
	req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/collections", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("无鉴权请求: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无鉴权 status=%d, want 401", resp.StatusCode)
	}

	collection := post("/api/v1/collections", map[string]any{"name": "E2E collection"}, http.StatusCreated)
	collectionID := collection["id"].(string)
	document := post("/api/v1/documents", map[string]any{"title": "线上故障响应"}, http.StatusCreated)
	documentID := document["id"].(string)
	post("/api/v1/collections/"+collectionID+"/documents",
		map[string]any{"document_id": documentID}, http.StatusCreated)

	content := "线上故障按照 P0 到 P3 分级，P0 故障要求十五分钟内拉起应急会议并通知值班负责人。"
	req, _ = http.NewRequest(http.MethodPut,
		httpServer.URL+"/api/v1/documents/"+documentID+"/content",
		bytes.NewReader(mustJSON(t, map[string]any{"content": content})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authKey)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("PUT content: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT content status=%d", resp.StatusCode)
	}

	// 真实 outbox + worker + Qdrant。
	worker, err := indexer.NewWorker(indexer.WorkerOptions{
		Jobs:        indexer.NewJobStore(db),
		Content:     repo,
		Embedder:    fakeEmbedder{},
		Vectors:     qdrantClient,
		WorkerID:    "e2e-worker",
		Model:       "fake",
		BatchSize:   4,
		Lease:       time.Minute,
		MaxAttempts: 3,
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("初始化 worker: %v", err)
	}
	for {
		processed, err := worker.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("worker: %v", err)
		}
		if !processed {
			break
		}
	}

	var jobStatus string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM embedding_jobs WHERE document_id = UUID_TO_BIN(?)`, documentID,
	).Scan(&jobStatus); err != nil {
		t.Fatalf("查询 embedding job: %v", err)
	}
	if jobStatus != "done" {
		t.Fatalf("embedding job = %s, want done", jobStatus)
	}

	// FTS 对语义 query 应该没有结果；Hybrid 靠 vector 补回。
	req, _ = http.NewRequest(http.MethodGet,
		httpServer.URL+"/api/v1/search?q="+urlQuery("服务挂了先找谁")+"&limit=5", nil)
	req.Header.Set("Authorization", "Bearer "+authKey)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("FTS: %v", err)
	}
	var fts struct {
		Items []any `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&fts)
	_ = resp.Body.Close()
	if len(fts.Items) != 0 {
		t.Fatalf("FTS 应无结果，实际 %d", len(fts.Items))
	}

	chat := post("/api/v1/agent/chat", map[string]any{
		"collection_id": collectionID,
		"message":       "服务挂了先找谁",
	}, http.StatusOK)
	citations, _ := chat["citations"].([]any)
	if len(citations) != 1 {
		t.Fatalf("citations = %d, want 1", len(citations))
	}
	if chat["answer"] != "答案 [1]" {
		t.Fatalf("answer = %v", chat["answer"])
	}

	// 清理真实数据；Qdrant collection 由 t.Cleanup 删除。
	collectionUUID := uuid.MustParse(collectionID)
	documentUUID := uuid.MustParse(documentID)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = db.ExecContext(ctx, `DELETE FROM vector_cleanup_jobs WHERE document_id=?`, documentUUID[:])
		_, _ = db.ExecContext(ctx, `DELETE FROM embedding_jobs WHERE document_id=?`, documentUUID[:])
		_, _ = db.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id=?`, documentUUID[:])
		_, _ = db.ExecContext(ctx, `DELETE FROM document_contents WHERE document_id=?`, documentUUID[:])
		_, _ = db.ExecContext(ctx, `DELETE FROM collection_documents WHERE collection_id=?`, collectionUUID[:])
		_, _ = db.ExecContext(ctx, `DELETE FROM documents WHERE id=?`, documentUUID[:])
		_, _ = db.ExecContext(ctx, `DELETE FROM collections WHERE id=?`, collectionUUID[:])
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	return raw
}

func urlQuery(s string) string {
	return url.QueryEscape(s)
}

var _ embedding.Embedder = fakeEmbedder{}
var _ agent.LLMClient = fakeLLM{}
