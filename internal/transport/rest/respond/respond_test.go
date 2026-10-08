package respond

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huakaifugui114514/working/internal/apperr"
)

// newTestLogger 把日志写进 buffer：测试要能断言"该记日志的记了"。
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestStatusForCode(t *testing.T) {
	tests := []struct {
		name string
		code string
		want int
	}{
		{"参数不合法 → 400", apperr.CodeInvalidArgument, http.StatusBadRequest},
		{"资源不存在 → 404", apperr.CodeNotFound, http.StatusNotFound},
		{"路由不存在 → 404", CodeRouteNotFound, http.StatusNotFound},
		{"冲突 → 409", apperr.CodeConflict, http.StatusConflict},
		{"内部错误 → 500", apperr.CodeInternal, http.StatusInternalServerError},
		// 下面两条是安全属性：没登记的码不能被当成 4xx 放出去
		{"未知码 → 500", "who_knows", http.StatusInternalServerError},
		{"空码 → 500", "", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusForCode(tt.code); got != tt.want {
				t.Errorf("StatusForCode(%q) = %d, want %d", tt.code, got, tt.want)
			}
		})
	}
}

func TestErrorWritesUnifiedBody(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, nil, http.StatusNotFound, apperr.CodeNotFound, "知识库不存在")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json 开头", ct)
	}

	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是统一错误结构: %v，原始内容: %s", err, rec.Body.String())
	}
	if body.Error.Code != apperr.CodeNotFound || body.Error.Message != "知识库不存在" {
		t.Errorf("响应体 = %+v", body.Error)
	}
}

func TestFromError(t *testing.T) {
	// 5xx 里绝对不能出现的内部信息
	const secret = "10.0.0.5:3306"

	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{
			name:        "4xx 回显业务 message",
			err:         apperr.New(apperr.CodeNotFound, "知识库 42 不存在"),
			wantStatus:  http.StatusNotFound,
			wantCode:    apperr.CodeNotFound,
			wantMessage: "知识库 42 不存在",
		},
		{
			name:        "被 %w 包过一层也能取出业务 message",
			err:         fmt.Errorf("handler: %w", apperr.New(apperr.CodeInvalidArgument, "name 不能为空")),
			wantStatus:  http.StatusBadRequest,
			wantCode:    apperr.CodeInvalidArgument,
			wantMessage: "name 不能为空",
		},
		{
			name:        "5xx 不回显内部原因，只回固定文案",
			err:         apperr.Wrap(errors.New("dial tcp "+secret+": connection refused"), apperr.CodeInternal, "查询知识库失败"),
			wantStatus:  http.StatusInternalServerError,
			wantCode:    apperr.CodeInternal,
			wantMessage: DefaultInternalMessage,
		},
		{
			name:        "普通 error 按 500 兜底",
			err:         errors.New("boom"),
			wantStatus:  http.StatusInternalServerError,
			wantCode:    apperr.CodeInternal,
			wantMessage: DefaultInternalMessage,
		},
		{
			name:        "nil error 不能静默成功",
			err:         nil,
			wantStatus:  http.StatusInternalServerError,
			wantCode:    apperr.CodeInternal,
			wantMessage: DefaultInternalMessage,
		},
		{
			name:        "业务错误忘了写 message 时给兜底文案",
			err:         apperr.New(apperr.CodeNotFound, ""),
			wantStatus:  http.StatusNotFound,
			wantCode:    apperr.CodeNotFound,
			wantMessage: "请求未被接受",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			rec := httptest.NewRecorder()

			FromError(rec, newTestLogger(&buf), tt.err)

			if rec.Code != tt.wantStatus {
				t.Errorf("状态码 = %d, want %d", rec.Code, tt.wantStatus)
			}

			var body ErrorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("响应不是统一错误结构: %v，原始内容: %s", err, rec.Body.String())
			}
			if body.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.wantCode)
			}
			if body.Error.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", body.Error.Message, tt.wantMessage)
			}

			// 安全属性：内部信息只能出现在日志里，不能出现在响应体里
			if strings.Contains(rec.Body.String(), secret) {
				t.Errorf("响应体泄露了内部信息: %s", rec.Body.String())
			}
			if tt.wantStatus >= 500 && !strings.Contains(buf.String(), "level=ERROR") {
				t.Errorf("5xx 必须记 ERROR 日志，实际日志: %s", buf.String())
			}
		})
	}
}

// TestFromErrorLogsCause 单独把"原因进日志"钉死：没有它，线上 5xx 就查不出原因。
func TestFromErrorLogsCause(t *testing.T) {
	var buf bytes.Buffer
	rec := httptest.NewRecorder()

	cause := errors.New("dial tcp 10.0.0.5:3306: connection refused")
	FromError(rec, newTestLogger(&buf), apperr.Wrap(cause, apperr.CodeInternal, "查询失败"))

	if !strings.Contains(buf.String(), "10.0.0.5:3306") {
		t.Errorf("日志里必须包含底层原因，实际: %s", buf.String())
	}
}
