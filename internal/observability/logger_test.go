package observability

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// newTestLogger 把日志写进 buffer：测试要能断言"日志里到底有什么"。
func newTestLogger(t *testing.T, level, format string) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger, err := NewLogger(level, format, &buf)
	if err != nil {
		t.Fatalf("NewLogger(%q, %q) 返回错误: %v", level, format, err)
	}
	return logger, &buf
}

func TestNewLoggerRejectsUnknownValues(t *testing.T) {
	tests := []struct {
		name   string
		level  string
		format string
	}{
		{name: "未知日志级别", level: "verbose", format: "json"},
		{name: "空日志级别", level: "", format: "json"},
		{name: "未知日志格式", level: "info", format: "xml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewLogger(tt.level, tt.format, io.Discard)
			if err == nil {
				t.Fatalf("期望返回错误，实际拿到 logger: %v", logger)
			}
			// 错误信息要能指出是哪个值不合法，否则使用者还得自己猜
			if !strings.Contains(err.Error(), "只允许") {
				t.Errorf("错误信息应说明合法取值，实际: %v", err)
			}
		})
	}
}

func TestLoggerInjectsTraceIDFromContext(t *testing.T) {
	logger, buf := newTestLogger(t, "debug", "json")

	ctx := WithTraceID(context.Background(), "abc123")
	logger.InfoContext(ctx, "hello")

	if !strings.Contains(buf.String(), `"trace_id":"abc123"`) {
		t.Errorf("日志里没有 trace_id，实际输出: %s", buf.String())
	}
}

// 钉住 slog 最容易踩的坑：不带 Context 的方法拿不到 trace_id。
func TestLoggerWithoutContextHasNoTraceID(t *testing.T) {
	logger, buf := newTestLogger(t, "debug", "json")

	logger.Info("hello")

	if strings.Contains(buf.String(), "trace_id") {
		t.Errorf("不带 ctx 的日志不该出现 trace_id，实际输出: %s", buf.String())
	}
}

// 钉住 WithAttrs / WithGroup 的透传：漏了透传，logger.With(...) 之后 trace_id 会消失。
func TestLoggerWithAttrsKeepsTraceID(t *testing.T) {
	logger, buf := newTestLogger(t, "debug", "json")

	ctx := WithTraceID(context.Background(), "abc123")
	logger.With("service", "apid").WithGroup("http").InfoContext(ctx, "hello")

	out := buf.String()
	if !strings.Contains(out, `"trace_id":"abc123"`) {
		t.Errorf("With/WithGroup 之后 trace_id 丢了，检查 traceHandler 的 WithAttrs/WithGroup 是否透传，实际输出: %s", out)
	}
	if !strings.Contains(out, `"service":"apid"`) {
		t.Errorf("With 附加的字段丢了，实际输出: %s", out)
	}
}

func TestLoggerLevelFiltering(t *testing.T) {
	logger, buf := newTestLogger(t, "warn", "text")

	logger.Info("info 不该出现")
	logger.Warn("warn 应该出现")

	out := buf.String()
	if strings.Contains(out, "info 不该出现") {
		t.Errorf("warn 级别下不应输出 Info，实际输出: %s", out)
	}
	if !strings.Contains(out, "warn 应该出现") {
		t.Errorf("warn 级别下应输出 Warn，实际输出: %s", out)
	}
}

func TestTraceIDContextRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "写入后能取出", ctx: WithTraceID(context.Background(), "abc123"), want: "abc123"},
		{name: "空 id 不写入", ctx: WithTraceID(context.Background(), ""), want: ""},
		{name: "没有值的 context 返回空串", ctx: context.Background(), want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TraceIDFrom(tt.ctx); got != tt.want {
				t.Errorf("TraceIDFrom() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewTraceID(t *testing.T) {
	const rounds = 100
	seen := make(map[string]bool, rounds)

	for i := 0; i < rounds; i++ {
		id, err := NewTraceID()
		if err != nil {
			t.Fatalf("NewTraceID 返回错误: %v", err)
		}
		if len(id) != 16 {
			t.Fatalf("trace_id = %q 长度 %d, want 16 个十六进制字符", id, len(id))
		}
		// 必须是合法十六进制：否则扔进 HTTP 头会让请求在协议层炸掉
		if _, err := hex.DecodeString(id); err != nil {
			t.Fatalf("trace_id = %q 不是合法十六进制: %v", id, err)
		}
		if seen[id] {
			t.Fatalf("生成了重复的 trace_id: %q", id)
		}
		seen[id] = true
	}
}
