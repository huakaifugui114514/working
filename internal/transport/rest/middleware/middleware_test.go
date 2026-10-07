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

// testLogger 把日志写进 buffer，便于断言"该记的记了、级别对不对"。
// 日志本身也是行为的一部分，不测就会出现"线上出问题却查不到日志"。
func testLogger(t *testing.T, buf *bytes.Buffer) *slog.Logger {
	t.Helper()
	logger, err := observability.NewLogger("debug", "text", buf)
	if err != nil {
		t.Fatalf("创建测试 logger 失败: %v", err)
	}
	return logger
}

func TestRecoverTurnsPanicIntoJSON500(t *testing.T) {
	var buf bytes.Buffer
	logger := testLogger(t, &buf)

	h := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom：数据库连接为空")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"internal_error"`) {
		t.Errorf("响应体应是统一错误结构，实际: %s", rec.Body.String())
	}
	// 安全边界：panic 详情只进日志，不进响应
	if strings.Contains(rec.Body.String(), "boom") {
		t.Error("响应体泄露了 panic 详情")
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Error("日志里必须记录 panic 内容，否则线上无法定位")
	}
	if !strings.Contains(buf.String(), "goroutine") {
		t.Error("日志里必须包含堆栈（debug.Stack）")
	}
}

// TestAccessLogLevelByStatus 验证"日志级别随状态码分级"，
// 这是能否用日志平台做告警的前提（5xx 必须能被 level=ERROR 捞出来）。
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
			logger := testLogger(t, &buf)

			h := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

			out := buf.String()
			if !strings.Contains(out, tt.wantLevel) {
				t.Errorf("状态码 %d 的日志级别应为 %s，实际输出: %s", tt.status, tt.wantLevel, out)
			}
			if !strings.Contains(out, "status="+strconv.Itoa(tt.status)) {
				t.Errorf("日志里应包含 status=%d，实际输出: %s", tt.status, out)
			}
		})
	}
}

func TestInFlightCountsDuringAndAfter(t *testing.T) {
	var counter atomic.Int64
	var duringRequest int64

	h := InFlight(&counter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		duringRequest = counter.Load() // 处理中：应为 1
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if duringRequest != 1 {
		t.Errorf("处理中的计数 = %d, want 1", duringRequest)
	}
	if after := counter.Load(); after != 0 {
		t.Errorf("请求结束后的计数 = %d, want 0（defer 必须把它减回去，否则优雅退出会一直显示有请求在跑）", after)
	}
}

func TestRequestIDPutsTraceIDInContextAndHeader(t *testing.T) {
	var inContext string

	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inContext = observability.TraceIDFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if inContext == "" {
		t.Fatal("context 里应该有 trace_id")
	}
	if got := rec.Header().Get(HeaderRequestID); got != inContext {
		t.Errorf("响应头 X-Request-ID = %q，与 context 中的 %q 不一致", got, inContext)
	}
}
