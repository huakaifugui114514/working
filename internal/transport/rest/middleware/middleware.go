// Package middleware 提供 HTTP 层的通用中间件：请求标识、在途计数、访问日志、panic 兜底。
//
// 中间件是"洋葱模型"：外层先进、后出。本项目固定顺序（外 → 内）为
//
//	RequestID → InFlight → AccessLog → Recover → 业务 handler
//
// 顺序不是随便排的，理由见 rest 包中 routes() 的注释。
package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

// HeaderRequestID 是请求标识的传递头。用 X-Request-ID 是业界通行做法，
// 网关、Nginx、各类 APM 都认这个头。
const HeaderRequestID = "X-Request-ID"

// RequestID 为每个请求确定一个 trace_id：优先沿用上游传来的，没有就新生成。
//
// 为什么沿用而不是重新生成：网关 → 本服务 → 下游服务应该共用同一个 id，
// 否则一次调用会在日志里碎成三段，串不起来。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if id == "" {
			id = observability.NewTraceID()
		}
		// 回写响应头：客户端/压测工具拿到 id 后可以直接报给客服或贴进工单
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(observability.WithTraceID(r.Context(), id)))
	})
}

// InFlight 统计"正在处理中"的请求数。
// 优雅退出时运维最关心的就是"还有几个请求没处理完"，这个数字由它提供。
func InFlight(counter *atomic.Int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			// defer 保证 panic 路径也会减回去；否则计数只增不减，退出时永远显示"还有请求在处理"
			defer counter.Add(-1)
			next.ServeHTTP(w, r)
		})
	}
}

// Recover 把 panic 限制在单个请求内。
// 没有它，handler 里一次 nil 解引用就会带走整个进程——线上表现为"服务突然没了"，
// 而这种故障本可以只影响一个请求。
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// 堆栈只进日志，不进响应：响应里带堆栈等于把内部结构送给攻击者。
				logger.ErrorContext(r.Context(), "处理请求时发生 panic",
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				)
				respond.Error(w, logger, http.StatusInternalServerError, respond.CodeInternal, "服务内部错误")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog 记录每个请求的结果。
// 日志级别按状态码分级：5xx=error、4xx=warn、其余=info，便于用日志平台做告警。
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w}

			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				// handler 既没 WriteHeader 也没 Write（例如 204 语义），net/http 默认按 200 处理
				status = http.StatusOK
			}
			// 用 LogAttrs + InfoContext：trace_id 由 observability 的 handler 自动补上
			logger.LogAttrs(r.Context(), levelForStatus(status), "http 请求",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("query", r.URL.RawQuery),
				slog.Int("status", status),
				slog.Int("bytes", rec.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}

// levelForStatus 把状态码映射成日志级别。
func levelForStatus(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// recorder 包一层 ResponseWriter，只为拿到"实际写了什么状态码、多少字节"。
// 为什么需要：http.ResponseWriter 本身不提供查询已写状态码的方法，
// 而访问日志没有状态码就没有价值。
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *recorder) WriteHeader(status int) {
	if r.status != 0 {
		return // 重复写 header 是无效操作，net/http 内部也会忽略，这里提前挡掉
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap 让 http.ResponseController（Go 1.20+）能拿到底层 ResponseWriter，
// 从而支持 Flush / Hijack / SetWriteDeadline 等能力（未来做 SSE 流式输出会用到）。
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
