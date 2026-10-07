// Package rest 是 HTTP 传输层：路由注册、中间件装配、服务生命周期管理。
//
// 为什么包名叫 rest 而不是 http：包名叫 http 会与标准库 net/http 撞名，
// 每个引用它的文件都被迫写别名（nethttp "net/http"），读起来更累。
//
// TODO(day2 · 你来实现)
package rest

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/version"
)

// Server 持有 HTTP 服务的全部运行期状态。
// 字段已经给好（这是装配所需的形状），你负责让它们被正确初始化与使用。
type Server struct {
	cfg       config.Config
	logger    *slog.Logger
	info      version.Info
	startedAt time.Time

	// inFlight 正在处理的请求数；用 atomic 而不是 Mutex：只有一个计数器、无关联状态，
	// 原子操作更轻，也不会出现"忘记解锁"。
	inFlight atomic.Int64

	http *http.Server
}

// New 组装一个可运行的 Server（不启动监听）。
//
// 契约：
//   - ReadTimeout / WriteTimeout / IdleTimeout 从 cfg 取，不能省
//   - Handler 用 s.routes() 的结果
//   - ErrorLog 接到 slog（提示：slog.NewLogLogger(logger.Handler(), slog.LevelError)），
//     否则 net/http 内部错误会走标准库日志，出现"两套日志格式"
func New(cfg config.Config, logger *slog.Logger, info version.Info) *Server {
	panic("TODO(day2): 组装 Server")
}

// Handler 暴露装配好的 handler，供 httptest 使用（测试不经过真实网络、不占端口）。
func (s *Server) Handler() http.Handler {
	panic("TODO(day2): 返回 http handler")
}

// routes 注册路由并装配中间件。
//
// 路由：
//   - GET /healthz          → s.handleHealthz
//   - GET /debug/sleep      → s.handleDebugSleep，**只在 cfg.App.Env == "dev" 时注册**
//     （并在注册时打一条 Warn 日志：调试路由不该出现在生产）
//   - "/"                   → s.handleNotFound，兜底，保证 404 也是统一 JSON
//
// 中间件顺序（外 → 内）：RequestID → InFlight → AccessLog → Recover → 业务 handler
// 思考题（写之前先答）：为什么 AccessLog 必须在 Recover 外面，而不能反过来？
func (s *Server) routes() http.Handler {
	panic("TODO(day2): 注册路由 + 装配中间件")
}

// healthzResponse 是 /healthz 的响应体（字段已定好，实现时填值即可）。
// 为什么返回这些：负载均衡和运维靠它判断"实例是不是活着、跑的是哪个版本"，
// 排查线上问题时第一句话通常是"你确认一下版本号"。
type healthzResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	BuildTime     string `json:"build_time"`
	Env           string `json:"env"`
	StartedAt     string `json:"started_at"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	InFlight      int64  `json:"in_flight"`
}

// handleHealthz 就绪/存活探针。
//
// 契约：
//   - 只反映"进程能否处理请求"，**不检查** DB/Redis（依赖检查属于 Day 3 的 /readyz；
//     混在一起会导致数据库抖动时容器被反复重启）
//   - status 固定 "ok"；started_at 用 RFC3339；uptime 取整秒
//
// 思考题：in_flight 要不要把 /healthz 这个请求自己算进去？为什么？
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	panic("TODO(day2): 实现健康检查")
}

// handleDebugSleep 睡一会儿，用来验证优雅退出（仅 dev 注册）。
//
// 契约：查询参数 d 默认 2s，必须是 0~30s 之间的合法时长，否则返回 400 + 统一错误体；
// 等待期间要监听 r.Context().Done()（客户端断开或服务端关停时立刻返回，不傻等）。
func (s *Server) handleDebugSleep(w http.ResponseWriter, r *http.Request) {
	panic("TODO(day2): 实现调试用慢请求")
}

// handleNotFound 统一 404。
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	panic("TODO(day2): 实现统一 404")
}

// Start 阻塞运行 HTTP 服务，直到 Shutdown 被调用。
//
// 契约：ListenAndServe 在 Shutdown 后会返回 http.ErrServerClosed，
// 那是**正常退出路径**，不能当错误返回（提示：errors.Is 判断）。
func (s *Server) Start() error {
	panic("TODO(day2): 启动监听")
}

// Shutdown 优雅退出：停止接收新连接 + 等在途请求处理完（最多 cfg.Server.ShutdownTimeout）。
//
// 契约：
//   - 先把当前 in_flight 打出来（>0 时日志要有"等待在途请求完成"的意思）
//   - 再调 s.http.Shutdown(ctx)
//
// 思考题（本项目最容易踩的坑，先自己想再看提示）：
//
//	入参 ctx 是"信号触发的那个 ctx"，此刻**已经取消**了。
//	直接拿它做超时会发生什么？应该怎么修？（提示：context.WithoutCancel）
func (s *Server) Shutdown(ctx context.Context) error {
	panic("TODO(day2): 实现优雅退出")
}
