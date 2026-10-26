package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case r.URL.Path == "/api/v1/search":
			if r.URL.Query().Get("q") == "err" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = fmt.Fprint(w, `{"error":"boom"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"items":[{"chunk_id":"c1","document_id":"d1","score":0.9}]}`)
		case r.URL.Path == "/api/v1/documents/d1":
			_, _ = fmt.Fprint(w, `{"id":"d1","title":"文档"}`)
		case r.URL.Path == "/api/v1/agent/chat" && r.Method == http.MethodPost:
			_, _ = fmt.Fprint(w, `{"answer":"答案","citations":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	client, err := NewAPIClient(api.URL, "test-key", time.Second)
	if err != nil {
		t.Fatalf("NewAPIClient: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(client, logger), api
}

func runMCP(t *testing.T, server *Server, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	if err := server.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, resp)
	}
	return responses
}

func TestMCPInitializeAndList(t *testing.T) {
	server, _ := testServer(t)
	responses := runMCP(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want 2", len(responses))
	}
	initResult := responses[0]["result"].(map[string]any)
	if initResult["protocolVersion"] != protocolVersion {
		t.Fatalf("protocolVersion = %v", initResult["protocolVersion"])
	}
	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
}

func TestMCPToolCalls(t *testing.T) {
	server, _ := testServer(t)
	responses := runMCP(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"knowledge_search","arguments":{"query":"知识"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"document_metadata","arguments":{"document_id":"d1"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"knowledge_ask","arguments":{"message":"问题"}}}`,
	)
	if len(responses) != 3 {
		t.Fatalf("responses = %d", len(responses))
	}
	for i, resp := range responses {
		result := resp["result"].(map[string]any)
		content := result["content"].([]any)[0].(map[string]any)
		if content["type"] != "text" || content["text"] == "" {
			t.Fatalf("response[%d] = %+v", i, resp)
		}
	}
}

func TestMCPToolErrors(t *testing.T) {
	server, _ := testServer(t)
	responses := runMCP(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"knowledge_search","arguments":{"query":"err"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"unknown","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"knowledge_search","arguments":{}}}`,
	)
	for i, resp := range responses {
		if i == 1 {
			if resp["error"] == nil {
				t.Fatalf("unknown tool 应返回 JSON-RPC error: %+v", resp)
			}
			continue
		}
		result := resp["result"].(map[string]any)
		if result["isError"] != true {
			t.Fatalf("response[%d] 应为 tool error: %+v", i, resp)
		}
	}
}

func TestMCPParseAndInvalidRequest(t *testing.T) {
	server, _ := testServer(t)
	var out bytes.Buffer
	input := "not-json\n" + `{"jsonrpc":"1.0","id":2,"method":"ping"}` + "\n"
	if err := server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !strings.Contains(out.String(), `"code":-32700`) || !strings.Contains(out.String(), `"code":-32600`) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestNewAPIClientValidation(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		timeout time.Duration
	}{
		{name: "empty", baseURL: "", timeout: time.Second},
		{name: "bad scheme", baseURL: "file:///tmp", timeout: time.Second},
		{name: "timeout", baseURL: "http://127.0.0.1:8080", timeout: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewAPIClient(tt.baseURL, "", tt.timeout); err == nil {
				t.Fatal("应当返回错误")
			}
		})
	}
}
