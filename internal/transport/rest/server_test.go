package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/version"
)

// newTestServer 用最少的噪音造一个 Server：日志级别设成 error，避免测试输出被访问日志刷屏。
func newTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	return newTestServerWithCollections(t, mutate, nil)
}

func newTestServerWithCollections(t *testing.T, mutate func(*config.Config), collections CollectionService) *Server {
	t.Helper()
	return newTestServerWithServices(t, mutate, collections, &fakeDocumentService{})
}

func newTestServerWithServices(
	t *testing.T,
	mutate func(*config.Config),
	collections CollectionService,
	documents DocumentService,
) *Server {
	t.Helper()

	cfg := config.Default()
	if mutate != nil {
		mutate(cfg)
	}
	logger, err := observability.NewLogger("error", "text", io.Discard)
	if err != nil {
		t.Fatalf("创建测试 logger 失败: %v", err)
	}
	if collections == nil {
		collections = &fakeCollectionService{}
	}
	if documents == nil {
		documents = &fakeDocumentService{}
	}
	return New(*cfg, logger, version.Info{Version: "v-test", Commit: "c0ffee", BuildTime: "2026-01-01T00:00:00Z"},
		collections, documents)
}

// do 走 httptest 直接调用装配好的 handler：不起真实端口、不经过网络。
func do(t *testing.T, s *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestHandleHealthz(t *testing.T) {
	tests := []struct {
		name string
		env  config.Env
	}{
		{"dev 环境", config.EnvDev},
		{"prod 环境", config.EnvProd},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t, func(c *config.Config) { c.App.Env = tt.env })

			rec := do(t, s, http.MethodGet, "/healthz")
			if rec.Code != http.StatusOK {
				t.Fatalf("状态码 = %d, want 200", rec.Code)
			}

			var got healthzResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("响应不是合法 JSON: %v（原文 %s）", err, rec.Body.String())
			}

			if got.Status != "ok" {
				t.Errorf("status = %q, want ok", got.Status)
			}
			if got.Version != "v-test" || got.Commit != "c0ffee" {
				t.Errorf("版本信息 = %s/%s, want v-test/c0ffee", got.Version, got.Commit)
			}
			if got.Env != string(tt.env) {
				t.Errorf("env = %q, want %q", got.Env, tt.env)
			}
			// started_at 必须是能解析的 RFC3339（运维/监控要按时间解析）
			if _, err := time.Parse(time.RFC3339, got.StartedAt); err != nil {
				t.Errorf("started_at = %q 不是 RFC3339: %v", got.StartedAt, err)
			}
			// 探针自己是被 InFlight 中间件算进去的，所以这里必须是 0（减 1 生效）
			if got.InFlight != 0 {
				t.Errorf("in_flight = %d, want 0（探针不该把自己算进去）", got.InFlight)
			}
		})
	}
}

func TestHandleHealthzResponseHeaderCarriesTraceID(t *testing.T) {
	s := newTestServer(t, nil)
	rec := do(t, s, http.MethodGet, "/healthz")

	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("响应头没有 X-Request-ID，说明 RequestID 中间件没生效")
	}
}

func TestHandleNotFound(t *testing.T) {
	s := newTestServer(t, nil)

	rec := do(t, s, http.MethodGet, "/does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, want 404", rec.Code)
	}

	body := rec.Body.String()
	// 用传输层自己的码，而不是 apperr.CodeNotFound（那是"资源不存在"的语义）
	if !strings.Contains(body, `"code":"route_not_found"`) {
		t.Errorf("错误码应为 route_not_found，实际: %s", body)
	}
	// 消息里要带方法和路径，排查时有用
	if !strings.Contains(body, "GET") || !strings.Contains(body, "/does-not-exist") {
		t.Errorf("错误消息应包含方法与路径，实际: %s", body)
	}
}

func TestHandleDebugSleep(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantSlept  string
	}{
		{"不传 d 用默认值", "", http.StatusOK, "2s"},
		{"传合法时长", "?d=10ms", http.StatusOK, "10ms"},
		{"非法字符串", "?d=abc", http.StatusBadRequest, ""},
		{"缺单位", "?d=3", http.StatusBadRequest, ""},
		{"负数", "?d=-1s", http.StatusBadRequest, ""},
		{"零", "?d=0s", http.StatusBadRequest, ""},
		{"超过上限", "?d=31s", http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t, nil)

			start := time.Now()
			rec := do(t, s, http.MethodGet, "/debug/sleep"+tt.query)
			elapsed := time.Since(start)

			if rec.Code != tt.wantStatus {
				t.Fatalf("状态码 = %d, want %d（body=%s）", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if tt.wantStatus == http.StatusOK {
				if !strings.Contains(rec.Body.String(), `"slept":"`+tt.wantSlept+`"`) {
					t.Errorf("响应体 = %s, want slept=%s", rec.Body.String(), tt.wantSlept)
				}
				// 默认值 2s 那条会真的睡 2 秒：断言"确实睡了"（否则测的就不是 sleep 语义）
				if tt.query == "" && elapsed < 2*time.Second {
					t.Errorf("默认参数应真的睡 2s，实际只用了 %v", elapsed)
				}
				return
			}

			// 400 分支：必须用业务语义错误码，且状态码由 respond.StatusForCode 映射（不是硬编码 500）
			if !strings.Contains(rec.Body.String(), `"code":"invalid_argument"`) {
				t.Errorf("错误码应为 invalid_argument，实际: %s", rec.Body.String())
			}
		})
	}
}

// TestHandleDebugSleepBoundaryIsAccepted 用"已经取消的 request ctx"来测上边界：
// d=30s 是合法值，但真的睡 30s 会让测试慢得无法接受；
// 而 handler 在等待时会监听 r.Context().Done()，所以预取消的 ctx 会让它立刻返回。
// 若 30s 被判非法，就会走 400 分支（写错误体），这条断言就会红。
func TestHandleDebugSleepBoundaryIsAccepted(t *testing.T) {
	s := newTestServer(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/sleep?d=30s", nil).WithContext(ctx))

	if rec.Code == http.StatusBadRequest {
		t.Errorf("d=30s 应该是合法值（边界包含），却被判成 400: %s", rec.Body.String())
	}
}

func TestDebugRouteOnlyRegisteredInDev(t *testing.T) {
	tests := []struct {
		name       string
		env        config.Env
		wantStatus int
	}{
		{"dev 注册调试路由", config.EnvDev, http.StatusOK},
		{"prod 不注册调试路由", config.EnvProd, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t, func(c *config.Config) { c.App.Env = tt.env })

			rec := do(t, s, http.MethodGet, "/debug/sleep?d=1ms")
			if rec.Code != tt.wantStatus {
				t.Errorf("状态码 = %d, want %d（body=%s）", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// startOnRandomPort 用真实监听跑服务：Shutdown 的语义（等在途请求、超时）
// 只有经过 net/http 的连接管理才测得出来，httptest 造不出来。
func startOnRandomPort(t *testing.T, s *Server) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	go func() {
		// Shutdown 之后 Serve 会返回 http.ErrServerClosed，这是正常路径
		_ = s.http.Serve(ln)
	}()
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return "http://" + ln.Addr().String()
}

// waitForInFlight 轮询 /healthz，直到有请求在途（避免用 time.Sleep 猜时间）。
func waitForInFlight(t *testing.T, baseURL string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/healthz")
		if err != nil {
			t.Fatalf("探测失败: %v", err)
		}
		var body struct {
			InFlight int64 `json:"in_flight"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if body.InFlight >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等不到在途请求")
}

func TestShutdownWaitsForInFlightRequest(t *testing.T) {
	s := newTestServer(t, nil)
	baseURL := startOnRandomPort(t, s)

	slowResult := make(chan int, 1)
	go func() {
		resp, err := http.Get(baseURL + "/debug/sleep?d=500ms")
		if err != nil {
			slowResult <- -1
			return
		}
		defer func() { _ = resp.Body.Close() }()
		slowResult <- resp.StatusCode
	}()

	waitForInFlight(t, baseURL)

	// 模拟真实的信号 ctx：传进来时【已经被取消】。
	// Shutdown 内部必须用 context.WithoutCancel 剥掉取消状态，否则它会立刻返回
	// （context.Canceled），"等请求跑完"就变成"立刻关掉"。
	sigCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.Shutdown(sigCtx); err != nil {
		t.Fatalf("优雅退出应当成功，实际: %v", err)
	}

	// 关键断言：优雅退出不会掐断在途请求，它必须跑完并拿到 200
	if code := <-slowResult; code != http.StatusOK {
		t.Errorf("慢请求状态码 = %d, want 200（优雅退出不该掐断在途请求）", code)
	}
}

func TestShutdownTimesOutWhenRequestTooSlow(t *testing.T) {
	// 把优雅退出窗口压到 50ms，而请求要睡 500ms → 必然超时
	s := newTestServer(t, func(c *config.Config) { c.Server.ShutdownTimeout = 50 * time.Millisecond })
	baseURL := startOnRandomPort(t, s)

	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		resp, err := http.Get(baseURL + "/debug/sleep?d=500ms")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	waitForInFlight(t, baseURL)

	err := s.Shutdown(context.Background())
	if err == nil {
		t.Fatal("超时场景应当返回错误")
	}
	// Shutdown 返回的是 ctx 的错误（context.DeadlineExceeded），并且被包了一层上下文
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("错误应可由 errors.Is 匹配 DeadlineExceeded，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("错误消息应说明是超时，实际: %v", err)
	}

	<-slowDone
}

// TestServerLoggerIsQuiet 保证测试用的 logger 不会往 stdout 刷访问日志。
// 这不是"为了测试方便"——它同时钉住 AccessLog 用的是注入的 logger，而不是包级 slog.Default()。
func TestServerLoggerIsQuiet(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	cfg := *config.Default()
	s := New(cfg, logger, version.Info{Version: "v-test"}, &fakeCollectionService{}, &fakeDocumentService{})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if !strings.Contains(buf.String(), "http 请求") {
		t.Errorf("访问日志应当写进注入的 logger，实际缓冲内容: %q", buf.String())
	}
}

// TestNewRejectsNilDependencies 钉住装配期的 fail-fast：
// 空依赖不能被静默接受，否则要等请求打进来才 panic。
func TestNewRejectsNilDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	info := version.Info{Version: "v-test"}

	tests := []struct {
		name        string
		collections CollectionService
		documents   DocumentService
	}{
		{name: "collections nil", collections: nil, documents: &fakeDocumentService{}},
		{name: "documents nil", collections: &fakeCollectionService{}, documents: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("New 应当 panic")
				}
			}()
			New(*config.Default(), logger, info, tt.collections, tt.documents)
		})
	}
}
