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

// recorder 包一层 ResponseWriter，只为拿到"实际写了什么状态码、多少字节"。
// 嵌入 http.ResponseWriter：其余方法（Header 等）自动转发给底层，只需要覆盖下面两个。
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *recorder) WriteHeader(status int) {
	if r.status != 0 {
		return // 已经写过，忽略（跟 net/http 的行为保持一致）
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK // 没显式 WriteHeader 就走默认 200
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap 让 http.ResponseController 能拿到底层 ResponseWriter（Flush / Hijack / SetWriteDeadline），
// 将来做 SSE 流式输出会用到。
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// RequestID 为每个请求确定一个 trace_id：优先沿用上游传来的（合法时），没有就新生成。
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
				// trace_id 是观测手段、不是业务前提；把它当硬依赖，会让"日志关联不上"
				// 这个降级问题被放大成"每个请求都 500"。
				slog.Default().Error("生成 trace_id 失败，本请求将不带 trace_id", "error", err)
			}
			id = generated
		}

		if id != "" {
			// 回写响应头：客户端/压测工具拿到后能直接贴进工单
			w.Header().Set(HeaderRequestID, id)
		}

		// 放进 ctx：后续所有 InfoContext / LogAttrs(ctx, ...) 的日志都会自动带上它。
		// 必须接住 WithContext 的返回值——http.Request 不可变，它返回的是浅拷贝。
		next.ServeHTTP(w, r.WithContext(observability.WithTraceID(r.Context(), id)))
	})
}

// InFlight 统计"正在处理中"的请求数。优雅退出时运维最关心的就是这个数字。
//
// 用 defer 减回去：panic 路径也要减，否则计数只增不减，
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

// AccessLog 记录每个请求的结果：method / path / query / status / bytes / duration / remote_addr。
//
// 日志级别按状态码分级（5xx=error、4xx=warn、其余=info），日志平台靠这个做告警。
// 用 LogAttrs(ctx, ...) 而不是 Log(...)：前者在编译期就能管住 key 必须是 string，
// 也避免 kv 数量不匹配时写出 !BADKEY；同时带 ctx 才有 trace_id。
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w}

			// 关键：把 rec 传下去（不是 w），否则状态码和字节数都截不到
			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				// handler 既没 Write 也没 WriteHeader（例如空 handler）时，net/http 会隐式发 200。
				// 不兜底就会记成 status=0，而客户端收到的是 200——日志撒谎比没有日志更糟。
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

// sanitizeTraceID 校验上游传来的 trace_id，不合法就返回空串（交给调用方新生成）。
//
// 为什么要校验上游的 header：它是**不受信任的输入**。控制字符会污染日志（甚至能伪造出
// 额外的日志行），非 ASCII 字节会让响应头非法——而响应头非法会让请求在协议层直接失败。
func sanitizeTraceID(id string) string {
	if id == "" || len(id) > maxTraceIDLen {
		return ""
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c < 0x21 || c > 0x7e { // 只放行可见 ASCII
			return ""
		}
	}
	return id
}

// levelForStatus 把状态码映射成日志级别：5xx→Error、4xx→Warn、其余→Info。
func levelForStatus(status int) slog.Level {
	if status >= 500 {
		return slog.LevelError
	}
	if status >= 400 {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}
