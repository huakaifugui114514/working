package workflow

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHTTPAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"x":"y"}` {
			t.Errorf("body = %s", body)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	action := HTTPAction(HTTPActionOptions{APIKey: "key"})
	out, err := action(context.Background(), nil, map[string]any{
		"method": "POST", "url": server.URL, "body": map[string]any{"x": "y"},
	})
	if err != nil {
		t.Fatalf("HTTPAction: %v", err)
	}
	if out["status_code"] != http.StatusOK {
		t.Fatalf("out = %+v", out)
	}
	if _, err := action(context.Background(), nil, map[string]any{"url": "http://127.0.0.1:1"}); err == nil {
		t.Fatal("连接失败应返回错误")
	}
}

func TestPDFExtractAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.pdf")
	if err := os.WriteFile(path, minimalWorkflowPDF("Workflow PDF"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := PDFExtractAction(context.Background(), nil, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("PDFExtractAction: %v", err)
	}
	if out["text"] == "" {
		t.Fatalf("out = %+v", out)
	}
	if _, err := PDFExtractAction(context.Background(), nil, map[string]any{"path": filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("缺失文件应返回错误")
	}
}

func TestSleepAction(t *testing.T) {
	if _, err := SleepAction(context.Background(), nil, map[string]any{"duration": "1ms"}); err != nil {
		t.Fatalf("SleepAction: %v", err)
	}
	if _, err := SleepAction(context.Background(), nil, map[string]any{"duration": "nope"}); err == nil {
		t.Fatal("非法 duration 应返回错误")
	}
}
