// Package observability 负责日志与链路标识（trace_id）。
//
// 为什么日志用 JSON：线上日志是按字段检索的（"查出 trace_id=abc 的全部日志"），
// 文本格式得靠正则去抠，日志量一大就没法查。
//
// trace_id 的完整链路：
//
//	RequestID 中间件生成/沿用 id → WithTraceID 放进 ctx
//	→ handler 用 logger.InfoContext(ctx, ...) → traceHandler 从 ctx 取出 id 写进日志
//
// 所以调用方必须用带 Context 的方法（InfoContext/ErrorContext/...）：
// 用 logger.Info(...) 是拿不到 trace_id 的。
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
)

// traceIDKey 是 context 的 key：未导出类型 + 空结构体。
//
// 为什么不导出：导出的 key 会让别的包能构造同名 key，互相覆盖。
// 为什么不用包级变量：类型本身就是唯一标识（traceIDKey{} 是零值），
// 不需要一个可变的包级 var——少一个能被改掉的全局状态。
type traceIDKey struct{}

// traceHandler 给每条日志自动补上 ctx 里的 trace_id。
//
// 为什么做成 Handler 包装，而不是在每个调用点手写 slog.String("trace_id", ...)：
// 手写一定会漏，而漏掉的那条日志往往正是排查时最需要的那条。
type traceHandler struct {
	next slog.Handler
}

// Enabled 直接问下游 handler：级别过滤只由一处决定（HandlerOptions.Level），
// 避免两处规则不一致；Logger.LogAttrs 也会用它短路，省掉无谓的 attr 构造。
func (h *traceHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

// Handle 补 trace_id 后交给下游。
// Clone 是必须的——Record 会被同一条日志的多个 handler 共享，直接改会影响别人。
func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := TraceIDFrom(ctx); id != "" {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", id))
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs 把属性透传给下游。
// 必须实现且必须透传：漏了它，logger.With("service", ...) 之后 trace_id 就没了。
func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{next: h.next.WithAttrs(attrs)}
}

// WithGroup 把分组透传给下游，理由同 WithAttrs。
func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{next: h.next.WithGroup(name)}
}

// NewLogger 按配置创建 logger：json（线上，便于按字段检索）/ text（本地肉眼读）。
//
// 返回 error 而不是 panic：配置错误属于"预期内的失败"，由调用方决定是打印后退出还是怎样。
// 收 io.Writer 而不是写死 os.Stdout：测试里传 &bytes.Buffer{} 就能断言日志内容。
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

// WithTraceID 把 trace_id 放进 context。
// 空 id 直接原样返回、不写入：避免日志里出现 trace_id="" 这种没有信息量的字段。
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
// 为什么不用 UUID：日志标识只要"系统内唯一"就够，16 个字符更短更好读，也不会把日志行撑长。
const traceIDBytes = 8

// NewTraceID 生成一个新的 trace_id，形如 "5fbb5fcc0f102818"（16 个十六进制字符）。
//
// 必须 hex 编码后再返回：直接 string(b) 得到的是原始字节，里面会有控制字符，
// 一旦被写进 HTTP 响应头（X-Request-ID）会让请求在协议层失败：
//
//	net/http: HTTP/1.x transport connection broken: malformed MIME header line
//
// 用 crypto/rand 而不是 math/rand：这个 id 会出现在响应头里，可预测的 id 能被枚举
// （以后做幂等键时更关键）。
//
// 失败时返回 error 而不是内部降级：熵源故障是环境级问题，由调用方决定怎么办。
// 调用方注意：不该因为生成失败就让请求失败——合理做法是记一条日志，然后不带 trace_id 继续。
func NewTraceID() (string, error) {
	b := make([]byte, traceIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成 trace_id 失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}
