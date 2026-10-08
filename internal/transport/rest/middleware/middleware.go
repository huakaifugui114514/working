// Package middleware 提供 HTTP 层通用中间件。
//
// 中间件是"洋葱模型"：外层先进、后出。
// TODO(day2 · 你来实现)。四个中间件都会由 rest.routes() 按固定顺序装配（外 → 内）：
//
//	RequestID → InFlight → AccessLog → Recover → 业务 handler
//
// 顺序不是随便排的，实现完请回答：为什么 AccessLog 必须在 Recover 外面？
package middleware

import (
	"log/slog"
	"net/http"
	"sync/atomic"
)

// HeaderRequestID 是请求标识的传递头。用 X-Request-ID 是业界通行做法，
// 网关、Nginx、各类 APM 都认这个头。
const HeaderRequestID = "X-Request-ID"

// RequestID 为每个请求确定一个 trace_id：优先沿用上游传来的，没有就新生成。
//
// 契约：
//   - 沿用而不是重新生成：网关 → 本服务 → 下游应共用同一个 id，否则一次调用在日志里碎成三段
//   - 把 id 写回响应头 X-Request-ID（客户端/压测工具拿到后能直接贴进工单）
//   - 把 id 放进 request 的 context（用 observability.WithTraceID），后续日志才能带上
func RequestID(next http.Handler) http.Handler {
	panic("TODO(day2): 实现 RequestID")
}

// InFlight 统计"正在处理中"的请求数。优雅退出时运维最关心的就是这个数字。
//
// 契约：处理前 +1、处理后 -1；必须用 defer——panic 路径也要减回去，
// 否则计数只增不减，退出时永远显示"还有请求在处理"。
func InFlight(counter *atomic.Int64) func(http.Handler) http.Handler {
	panic("TODO(day2): 实现 InFlight")
}

// AccessLog 记录每个请求的结果：method / path / query / status / bytes / duration / remote_addr。
//
// 契约：
//   - 日志级别按状态码分级：5xx=error、4xx=warn、其余=info（日志平台靠这个做告警）
//   - 难点：http.ResponseWriter 不提供"已写状态码是多少"的查询，需要自己包一层
//     recorder 记录 status 与写入字节数（并实现 Unwrap() 让 http.ResponseController 能拿到底层）
//   - panic 场景下 status 会是 Recover 写的 500——这正是 AccessLog 必须在 Recover 外面的原因
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	panic("TODO(day2): 实现 AccessLog（含 recorder）")
}

// Recover 把 panic 限制在单个请求内。
//
// 契约：没有它，handler 里一次 nil 解引用就会带走整个进程（线上表现为"服务突然没了"）。
//   - panic 内容与堆栈（debug.Stack()）**只进日志**，响应里只回统一错误体
//   - 响应体走 respond.Error(w, logger, http.StatusInternalServerError,
//     apperr.CodeInternal, respond.DefaultInternalMessage)：
//     错误码用 apperr 的业务码，文案用固定文案——panic 详情绝不能出现在响应里
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	panic("TODO(day2): 实现 Recover")
}
