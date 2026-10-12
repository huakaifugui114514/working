package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huakaifugui114514/working/internal/observability"
)

// newTestLogger 用一个真实的 observability logger（text 格式）写进 buffer。
//
// 关键：必须走 observability.NewLogger，而不是裸的 slog.NewTextHandler——
// 后者没有 traceHandler，日志里永远不会有 trace_id，
// 于是 TestAccessLogCarriesTraceID 这种测试会失败（而失败原因很容易被误判成中间件写错了）。
func newTestLogger(t *testing.T, buf *bytes.Buffer) *slog.Logger {
	t.Helper()
	logger, err := observability.NewLogger("debug", "text", buf)
	if err != nil {
		t.Fatalf("创建测试 logger 失败: %v", err)
	}
	return logger
}

func TestRequestID(t *testing.T) {
	tests := []struct {
		name      string
		upstream  string
		wantEqual bool // 响应头里的 id 是否应等于上游传来的
	}{
		{name: "沿用合法的上游 id", upstream: "upstream-123", wantEqual: true},
		{name: "上游没带则自动生成", upstream: "", wantEqual: false},
		{name: "上游带了控制字符则拒绝并重新生成", upstream: "bad\x01id", wantEqual: false},
		{name: "上游带了非 ASCII 则拒绝并重新生成", upstream: "中文-id", wantEqual: false},
		{name: "上游 id 超长则拒绝并重新生成", upstream: strings.Repeat("a", maxTraceIDLen+1), wantEqual: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotInContext string
			h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotInContext = observability.TraceIDFrom(r.Context())
			}))

			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			if tt.upstream != "" {
				req.Header.Set(HeaderRequestID, tt.upstream)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			gotHeader := rec.Header().Get(HeaderRequestID)
			if gotHeader == "" {
				t.Fatal("响应头里没有 X-Request-ID")
			}
			if tt.wantEqual && gotHeader != tt.upstream {
				t.Errorf("X-Request-ID = %q, want %q", gotHeader, tt.upstream)
			}
			if !tt.wantEqual && gotHeader == tt.upstream {
				t.Errorf("不合法/缺失的上游 id 应被替换，实际沿用了 %q", gotHeader)
			}
			if gotInContext != gotHeader {
				t.Errorf("ctx 里的 trace_id = %q 与响应头 %q 不一致", gotInContext, gotHeader)
			}
		})
	}
}

func TestInFlight(t *testing.T) {
	t.Run("处理中为 1，结束后回到 0", func(t *testing.T) {
		var counter atomic.Int64
		var during int64

		h := InFlight(&counter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			during = counter.Load()
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

		if during != 1 {
			t.Errorf("处理中的计数 = %d, want 1", during)
		}
		if after := counter.Load(); after != 0 {
			t.Errorf("请求结束后的计数 = %d, want 0", after)
		}
	})

	t.Run("panic 路径也必须减回去", func(t *testing.T) {
		var counter atomic.Int64
		var buf bytes.Buffer

		// 组合成和真实链路一样的顺序：InFlight 在外，Recover 在内
		h := InFlight(&counter)(Recover(newTestLogger(t, &buf))(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) { panic("boom") })))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		if after := counter.Load(); after != 0 {
			t.Errorf("panic 之后的计数 = %d, want 0（defer 没生效？）", after)
		}
	})
}

func TestAccessLogLevelByStatus(t *testing.T) {
	tests := []struct {
		status    int
		wantLevel string
	}{
		{http.StatusOK, "level=INFO"},
		{http.StatusNotFound, "level=WARN"},
		{http.StatusInternalServerError, "level=ERROR"},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			var buf bytes.Buffer
			h := AccessLog(newTestLogger(t, &buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("body"))
			}))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x?q=1", nil))

			out := buf.String()
			if !strings.Contains(out, tt.wantLevel) {
				t.Errorf("状态码 %d 的日志级别应为 %s，实际: %s", tt.status, tt.wantLevel, out)
			}
			if !strings.Contains(out, "status="+strconv.Itoa(tt.status)) {
				t.Errorf("日志里应包含 status=%d，实际: %s", tt.status, out)
			}
			if !strings.Contains(out, "bytes=4") {
				t.Errorf("日志里应记录响应字节数 4，实际: %s", out)
			}
			if !strings.Contains(out, "path=/x") {
				t.Errorf("日志里应包含 path=/x，实际: %s", out)
			}
		})
	}
}

func TestAccessLogCarriesTraceID(t *testing.T) {
	var buf bytes.Buffer
	// 真实链路顺序：RequestID 在外，AccessLog 在内
	h := RequestID(AccessLog(newTestLogger(t, &buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "abc123")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(buf.String(), "trace_id=abc123") {
		t.Errorf("访问日志里应带 trace_id（说明用了带 ctx 的日志方法），实际: %s", buf.String())
	}
}

func TestRecoverTurnsPanicIntoUnifiedError(t *testing.T) {
	var buf bytes.Buffer
	h := Recover(newTestLogger(t, &buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom：数据库连接为空")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"internal_error"`) {
		t.Errorf("响应体应是统一错误结构，实际: %s", body)
	}
	// 安全属性：panic 详情只能进日志，不能进响应
	if strings.Contains(body, "boom") {
		t.Errorf("响应体泄露了 panic 详情: %s", body)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("日志里必须记录 panic 内容，实际: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "goroutine") {
		t.Errorf("日志里必须包含堆栈，实际: %s", buf.String())
	}
}

// TestAccessLogSeesRecoveredPanic 钉住"AccessLog 必须在 Recover 外面"这条顺序约定：
// 如果两者调换，panic 会穿过 AccessLog，这条请求就不会出现在访问日志里。
func TestAccessLogSeesRecoveredPanic(t *testing.T) {
	var buf bytes.Buffer
	h := AccessLog(newTestLogger(t, &buf))(Recover(newTestLogger(t, &bytes.Buffer{}))(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { panic("boom") })))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))

	out := buf.String()
	if !strings.Contains(out, "status=500") {
		t.Errorf("访问日志应记录到 Recover 写出的 500，实际: %s", out)
	}
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("500 应以 ERROR 级别记录，实际: %s", out)
	}
}

// TestAccessLogDefaultsTo200WhenHandlerWritesNothing 钉住那个容易漏的边界：
// handler 既不 Write 也不 WriteHeader 时（空 handler），net/http 会隐式发 200，
// 日志里也必须是 200 —— 如果记成 0，日志就和客户端实际收到的状态码不一致（"日志撒谎"）。
func TestAccessLogDefaultsTo200WhenHandlerWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	h := AccessLog(newTestLogger(t, &buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 什么都不做
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/empty", nil))

	out := buf.String()
	if strings.Contains(out, "status=0") {
		t.Errorf("日志记成了 status=0，与客户端实际收到的 200 不一致，实际: %s", out)
	}
	if !strings.Contains(out, "status=200") {
		t.Errorf("空 handler 的访问日志应为 status=200，实际: %s", out)
	}
}
