// Package observability 负责日志与链路标识（trace_id）。
//
// 为什么日志用 JSON：线上日志是按字段检索的（"查出 trace_id=abc 的全部日志"），
// 文本格式得靠正则去抠，日志量一大就没法查。
//
// TODO(day2 · 你来实现)
package observability

import (
	"context"
	"io"
	"log/slog"
)

// NewLogger 按配置创建 logger。
//
// 契约：
//   - level 支持 debug/info/warn/error，其他值返回 error（不要 panic，让 main 去报错退出）
//   - format 支持 json/text，json 用 slog.NewJSONHandler
//   - 返回值必须**再包一层自定义 Handler**：让每条日志自动带上 ctx 里的 trace_id。
//     这样调用方只要用 logger.InfoContext(ctx, ...) 就不会漏——手写 trace_id 一定会漏，
//     而漏掉的那条往往正是排查时最需要的。
func NewLogger(level, format string, w io.Writer) (*slog.Logger, error) {
	panic("TODO(day2): 创建 slog.Logger")
}

// WithTraceID 把 trace_id 放进 context。
//
// 契约：context 的 key 必须用**未导出类型**（例如 type traceIDKey struct{}），
// 否则别的包能构造同名 key 互相覆盖。空 id 直接原样返回。
func WithTraceID(ctx context.Context, id string) context.Context {
	panic("TODO(day2): 把 trace_id 写入 context")
}

// TraceIDFrom 取出 trace_id；不存在时返回空字符串（调用方无需判 nil）。
func TraceIDFrom(ctx context.Context) string {
	panic("TODO(day2): 从 context 取出 trace_id")
}

// NewTraceID 生成一个新的 trace_id（16 个十六进制字符）。
//
// 契约：用 crypto/rand 而不是 math/rand——请求 ID 会出现在响应头里，
// 可预测的 ID 能被用来枚举（以后做幂等键时更关键）。熵源失败时降级返回固定串，不 panic。
func NewTraceID() string {
	panic("TODO(day2): 生成 trace_id")
}
