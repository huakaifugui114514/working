// Package observability 负责日志与链路标识（trace_id）。
//
// 为什么日志用 JSON：线上日志是按字段检索的（"查出 trace_id=abc 的全部日志"），
// 文本格式得靠正则去抠，日志量一大就没法查。
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
)

// NewLogger 按配置创建 logger。
// 返回值带 error 而不是 panic：配置错误应该在启动阶段以"人能看懂的错误信息"暴露，
// 由 main 决定怎么处理（打日志 + 非 0 退出）。
func NewLogger(level, format string, w io.Writer) (*slog.Logger, error) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "info":
		lv = slog.LevelInfo
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		return nil, fmt.Errorf("未知日志级别 %q，只允许 debug/info/warn/error", level)
	}

	opts := &slog.HandlerOptions{Level: lv}

	var h slog.Handler
	switch format {
	case "json":
		h = slog.NewJSONHandler(w, opts)
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("未知日志格式 %q，只允许 json/text", format)
	}

	return slog.New(&traceHandler{next: h}), nil
}

// traceHandler 给每条日志自动补上 ctx 里的 trace_id。
//
// 为什么做成 Handler 包装，而不是在每个调用点手写 slog.String("trace_id", ...)：
// 手写一定会漏，而漏掉的那条日志往往正是排查时最需要的那条。包装在 Handler 层，
// 只要调用方用的是 xxxContext 系列方法（如 logger.InfoContext(ctx, ...)），就自动带上。
type traceHandler struct {
	next slog.Handler
}

// Enabled 直接问下游 handler：级别过滤只由一处决定，避免两处规则不一致。
func (h *traceHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

// Handle 补 trace_id 后交给下游。Clone 是必须的——Record 会被同一条日志的多个
// handler 共享，直接改会影响别人。
func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := TraceIDFrom(ctx); id != "" {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", id))
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs 与 WithGroup 必须把包装透传下去，否则 logger.With(...) 之后 trace_id 就丢了。
func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{next: h.next.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{next: h.next.WithGroup(name)}
}

// traceIDKey 用未导出的空结构体做 context key。
// 为什么不用字符串 key：任何包都能构造同名字符串，会互相覆盖；
// 空结构体零内存占用，且外部包无法构造同名类型，从类型系统上杜绝撞车。
type traceIDKey struct{}

// WithTraceID 把 trace_id 放进 context。空 id 直接原样返回，避免写入无意义的值。
func WithTraceID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDKey{}, id)
}

// TraceIDFrom 取出 trace_id；不存在时返回空字符串（调用方无需判 nil）。
func TraceIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(traceIDKey{}).(string)
	return id
}

// traceIDBytes = 8 字节 → 16 个十六进制字符。
// 为什么不用 UUID：日志标识只需要"在系统内唯一"，16 个字符更短、更好读，
// 也不会把日志行撑长。
const traceIDBytes = 8

// NewTraceID 生成一个新的 trace_id。
// 用 crypto/rand 而不是 math/rand：请求 ID 会出现在响应头里，可预测的 ID
// 能被用来猜测/枚举（未来做幂等键时这一点更关键）。
func NewTraceID() string {
	var b [traceIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 熵源失败属于环境级故障，极罕见。这里不 panic：日志标识降级不该让服务起不来。
		return "trace-unknown"
	}
	return hex.EncodeToString(b[:])
}
