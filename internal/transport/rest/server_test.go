package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/transport/rest/middleware"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
	"github.com/huakaifugui114514/working/internal/version"
)

// newTestServer 构造一个不监听端口的 Server（测试走 httptest，不占真实端口，
// 也就不会出现"本地 8080 被占用导致测试失败"这类环境依赖问题）。
func newTestServer(t *testing.T, env string) *Server {
	t.Helper()

	cfg := *config.Default()
	cfg.App.Env = env // test 环境不注册 /debug/sleep

	logger, err := observability.NewLogger("debug", "text", io.Discard)
	if err != nil {
		t.Fatalf("创建测试 logger 失败: %v", err)
	}
	return New(cfg, logger, version.Info{Version: "test-1.0", Commit: "deadbeef", BuildTime: "2026-01-01T00:00:00Z"})
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, "test")

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json 开头", ct)
	}

	var got healthzResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v，原始内容: %s", err, rec.Body.String())
	}
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok", got.Status)
	}
	if got.Version != "test-1.0" || got.Commit != "deadbeef" {
		t.Errorf("版本信息 = %s/%s, want test-1.0/deadbeef", got.Version, got.Commit)
	}
	if got.Env != "test" {
		t.Errorf("env = %q, want test", got.Env)
	}
	if got.StartedAt == "" {
		t.Error("started_at 不应为空——排查问题时需要知道进程是什么时候起的")
	}
	// /healthz 自己也是一个在途请求，但对外报数必须排除它：
	// 否则探针每次都看到 1，会被误读成"有请求堆积"。
	if got.InFlight != 0 {
		t.Errorf("in_flight = %d, want 0（不应把探针请求自己算进去）", got.InFlight)
	}
}

func TestNotFoundReturnsJSON(t *testing.T) {
	srv := newTestServer(t, "test")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/not-exist", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, want 404", rec.Code)
	}

	var body respond.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 响应不是统一错误结构: %v，原始内容: %s", err, rec.Body.String())
	}
	if body.Error.Code != respond.CodeNotFound {
		t.Errorf("error.code = %q, want %q", body.Error.Code, respond.CodeNotFound)
	}
}

// TestRequestID 覆盖两种路径：上游带了就用上游的；没带就自己生成。
// 用表驱动，因为这两种情况的断言只差一个输入。
func TestRequestID(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		wantSame bool // 响应头是否应原样等于请求头
	}{
		{name: "沿用上游 trace_id", header: "upstream-id-123", wantSame: true},
		{name: "上游没带则自动生成", header: "", wantSame: false},
	}

	srv := newTestServer(t, "test")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			if tt.header != "" {
				req.Header.Set(middleware.HeaderRequestID, tt.header)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			got := rec.Header().Get(middleware.HeaderRequestID)
			if got == "" {
				t.Fatal("响应头里没有 X-Request-ID")
			}
			if tt.wantSame && got != tt.header {
				t.Errorf("X-Request-ID = %q, want %q", got, tt.header)
			}
			if !tt.wantSame && got == tt.header {
				t.Errorf("应当自动生成新的 X-Request-ID，实际沿用了 %q", got)
			}
		})
	}
}

// TestDebugRouteOnlyInDev 验证环境开关：非 dev 环境不能暴露调试路由。
func TestDebugRouteOnlyInDev(t *testing.T) {
	// 非 dev：404
	notDev := newTestServer(t, "test")
	rec := httptest.NewRecorder()
	notDev.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/sleep?d=1ms", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("test 环境下 /debug/sleep 状态码 = %d, want 404", rec.Code)
	}

	// dev：200
	dev := newTestServer(t, "dev")
	rec = httptest.NewRecorder()
	dev.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/sleep?d=1ms", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("dev 环境下 /debug/sleep 状态码 = %d, want 200", rec.Code)
	}
}
