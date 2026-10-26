package qdrant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewClientValidation(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{name: "base_url 为空", opts: Options{Collection: "c", VectorSize: 4, Timeout: time.Second}},
		{name: "base_url 非法", opts: Options{BaseURL: "file:///tmp", Collection: "c", VectorSize: 4, Timeout: time.Second}},
		{name: "collection 为空", opts: Options{BaseURL: "http://127.0.0.1:6333", VectorSize: 4, Timeout: time.Second}},
		{name: "vector_size 为 0", opts: Options{BaseURL: "http://127.0.0.1:6333", Collection: "c", Timeout: time.Second}},
		{name: "timeout 为 0", opts: Options{BaseURL: "http://127.0.0.1:6333", Collection: "c", VectorSize: 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClient(tt.opts); err == nil {
				t.Fatal("NewClient 应当返回错误")
			}
		})
	}
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(Options{
		BaseURL:    server.URL,
		Collection: "document_chunks",
		VectorSize: 4,
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestEnsureCollectionExisting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/collections/document_chunks" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"result":{"config":{"params":{"vectors":{"size":4,"distance":"Cosine"}}}}}`)
	}))
	defer server.Close()

	if err := newTestClient(t, server).EnsureCollection(context.Background()); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
}

func TestEnsureCollectionCreates(t *testing.T) {
	var created map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"status":{"error":"not found"}}`)
		case http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Errorf("解码创建请求: %v", err)
			}
			_, _ = fmt.Fprint(w, `{"result":true,"status":"ok"}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	if err := newTestClient(t, server).EnsureCollection(context.Background()); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	vectors, ok := created["vectors"].(map[string]any)
	if !ok || vectors["size"] != float64(4) || vectors["distance"] != "Cosine" {
		t.Fatalf("创建参数 = %#v", created)
	}
}

func TestEnsureCollectionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"result":{"config":{"params":{"vectors":{"size":8,"distance":"Cosine"}}}}}`)
	}))
	defer server.Close()

	if err := newTestClient(t, server).EnsureCollection(context.Background()); err == nil {
		t.Fatal("维度不匹配应返回错误")
	}
}

func TestDeleteByDocument(t *testing.T) {
	docID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/collections/document_chunks/points/delete" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解码删除请求: %v", err)
		}
		filter := body["filter"].(map[string]any)
		must := filter["must"].([]any)[0].(map[string]any)
		match := must["match"].(map[string]any)
		if match["value"] != docID.String() {
			t.Errorf("document_id = %v", match["value"])
		}
		_, _ = fmt.Fprint(w, `{"result":{"status":"completed"}}`)
	}))
	defer server.Close()

	if err := newTestClient(t, server).DeleteByDocument(context.Background(), docID); err != nil {
		t.Fatalf("DeleteByDocument: %v", err)
	}
}

func TestUpsertAndSearch(t *testing.T) {
	docID := uuid.New()
	chunkID := uuid.New()
	var upserted map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/collections/document_chunks/points":
			if r.Method != http.MethodPut {
				t.Errorf("upsert method = %s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&upserted); err != nil {
				t.Errorf("解码 upsert: %v", err)
			}
			_, _ = fmt.Fprint(w, `{"result":{"status":"completed"}}`)
		case "/collections/document_chunks/points/search":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("解码 search: %v", err)
			}
			if body["limit"] != float64(3) {
				t.Errorf("limit = %v", body["limit"])
			}
			_, _ = fmt.Fprintf(w, `{"result":[{"id":%q,"score":0.9,"payload":{"chunk_id":%q,"document_id":%q}}]}`,
				chunkID.String(), chunkID.String(), docID.String())
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	if err := client.Upsert(context.Background(), []Point{{
		ID:      chunkID,
		Vector:  []float32{0.1, 0.2, 0.3, 0.4},
		Payload: map[string]any{"document_id": docID.String()},
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	points, ok := upserted["points"].([]any)
	if !ok || len(points) != 1 {
		t.Fatalf("upsert body = %#v", upserted)
	}

	hits, err := client.Search(context.Background(), []float32{0.1, 0.2, 0.3, 0.4}, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkID != chunkID || hits[0].DocumentID != docID || hits[0].Score != 0.9 {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestUpsertRejectsWrongDimension(t *testing.T) {
	client, err := NewClient(Options{
		BaseURL:    "http://127.0.0.1:1",
		Collection: "c",
		VectorSize: 4,
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	err = client.Upsert(context.Background(), []Point{{
		ID:     uuid.New(),
		Vector: []float32{1, 2},
	}})
	if err == nil {
		t.Fatal("维度不匹配应返回错误")
	}
}

func TestQdrantNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"status":{"error":"boom"}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	if err := client.DeleteByDocument(context.Background(), uuid.New()); err == nil {
		t.Fatal("非 2xx 应返回错误")
	}
	if _, err := client.Search(context.Background(), []float32{1, 2, 3, 4}, 1); err == nil {
		t.Fatal("非 2xx 应返回错误")
	}
}
