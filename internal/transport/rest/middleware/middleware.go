// Package middleware 提供 HTTP 层的通用中间件：请求标识、在途计数、访问日志、panic 兜底。
//
// 中间件是"洋葱模型"：外层先进、后出。本项目由 rest.routes() 按固定顺序装配（外 → 内）：
//
//	RequestID → InFlight → AccessLog → Recover → 业务 handler
//
// 顺序不是随便排的：
//   - RequestID 最外层：后面所有日志（含 panic 日志、访问日志）都能带上 trace_id
//   - InFlight 次外层：计数覆盖整个请求生命周期，优雅退出时报出的数字才准
//   - AccessLog 必须在 Recover 外面：panic 被 Recover 转成 500 之后，访问日志仍要记到这条 500；
//     反过来的话 panic 会穿过 AccessLog，那条请求就不会被记录
package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

// HeaderRequestID 是请求标识的传递头。用 X-Request-ID 是业界通行做法，
// 网关、Nginx、各类 APM 都认这个头。
const HeaderRequestID = "X-Request-ID"

// maxTraceIDLen 限制沿用的上游 trace_id 长度：ID 会进日志和响应头，不能无上限地放大。
const maxTraceIDLen = 64

// sanitizeTraceID 校验上游传来的 trace_id，不合法就返回空串（交给调用方新生成）。
//
// 为什么要校验上游的 header：它是**不受信任的输入**。控制字符会污染日志（甚至能伪造出
// 额外的日志行），非 ASCII 字节会让响应头非法——而响应头非法会让请求在协议层直接失败。
// 只放行可见 ASCII。
func sanitizeTraceID(id string) string {
	if id == "" || len(id) > maxTraceIDLen {
		return ""
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c < 0x21 || c > 0x7e {
			return ""
		}
	}
	return id
}

// RequestID 为每个请求确定一个 trace_id：沿用上游传来的（合法时），否则新生成。
//
// 为什么沿用而不是重新生成：网关 → 本服务 → 下游应共用同一个 id，
// 否则一次调用会在日志里碎成三段，串不起来。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeTraceID(r.Header.Get(HeaderRequestID))

		if id == "" {
			generated, err := observability.NewTraceID()
			if err != nil {
				// 生成失败不该让请求失败：记一条日志，然后不带 trace_id 继续。
				// 这里还没有 request 级的 logger，用 slog.Default()（main 里已 SetDefault）。
				slog.Default().Error("生成 trace_id 失败，本请求将不带 trace_id", "error", err)
			}
			id = generated
		}

		if id != "" {
			// 回写响应头：客户端/压测工具拿到后能直接贴进工单
			w.Header().Set(HeaderRequestID, id)
		}
		// 放进 ctx：后续所有用 InfoContext/LogAttrs(ctx, ...) 的日志都会自动带上它
		next.ServeHTTP(w, r.WithContext(observability.WithTraceID(r.Context(), id)))
	})
}

// InFlight 统计"正在处理中"的请求数。优雅退出时运维最关心的就是这个数字。
//
// 必须用 defer 减回去：panic 路径也要减，否则计数只增不减，
// 退出时永远显示"还有请求在处理"，最后只能强杀。
func InFlight(counter *atomic.Int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			defer counter.Add(-1)
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog 记录每个请求的结果。
//
// 日志级别按状态码分级（5xx=error、4xx=warn、其余=info），日志平台靠这个做告警。
// 用 logger.LogAttrs(r.Context(), ...) 而不是 logger.Info：带 ctx 才有 trace_id。
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
//
// 为什么需要：http.ResponseWriter 不提供查询已写状态码的方法，而访问日志没有状态码就没价值。
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *recorder) WriteHeader(status int) {
	if r.status != 0 {
		return // 状态码只能写一次，net/http 内部也会忽略重复调用，这里提前挡掉
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK // 没显式 WriteHeader 时，net/http 默认 200
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap 让 http.ResponseController（Go 1.20+）能拿到底层 ResponseWriter，
// 从而支持 Flush / Hijack / SetWriteDeadline —— 将来做 SSE 流式输出会用到。
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Recover 把 panic 限制在单个请求内。
//
// 没有它，handler 里一次 nil 解引用就会带走整个进程（线上表现为"服务突然没了"），
// 而这种故障本可以只影响一个请求。
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}

				// http.ErrAbortHandler 是 net/http 的约定：handler 主动要求"掐断连接、别打堆栈"。
				// 交回给 net/http 处理，不要在这里吞掉。
				// 用 errors.Is 而不是 ==：panic 值可能是被包装过的 ErrAbortHandler。
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}

				// 堆栈只进日志：响应里带堆栈等于把内部结构送给攻击者
				logger.ErrorContext(r.Context(), "处理请求时发生 panic",
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				)

				// 注意：如果 handler 已经写过响应头（比如先 Write 了 200 再 panic），
				// 这里无法再把状态码改成 500，只能尽量写一个统一错误体。
				respond.Error(w, logger, http.StatusInternalServerError,
					apperr.CodeInternal, respond.DefaultInternalMessage)
			}()

			next.ServeHTTP(w, r)
		})
	}
}
