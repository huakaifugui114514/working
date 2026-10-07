// Package rest 是 HTTP 传输层：路由注册、中间件装配、服务生命周期管理。
//
// 为什么包名叫 rest 而不是 http：包名叫 http 会与标准库 net/http 撞名，
// 每个引用它的文件都被迫写别名（nethttp "net/http"），读起来更累。
// 这个名字也正好对应计划书 Day 4 的「RESTful 接口」。
package rest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/transport/rest/middleware"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
	"github.com/huakaifugui114514/working/internal/version"
)

// Server 持有 HTTP 服务的全部运行期状态。
type Server struct {
	cfg       config.Config
	logger    *slog.Logger
	info      version.Info
	startedAt time.Time

	// inFlight 用 atomic 而不是 Mutex：这里只有一个计数器、无关联状态，
	// 原子操作比加锁更轻，也不会出现"忘记解锁"的问题。
	inFlight atomic.Int64

	http *http.Server
}

// New 组装一个可运行的 Server。
// 注意它不启动监听——启动是 Start 的事。这样构造与启动分离，
// 测试里可以只拿 Handler 做 httptest，不必真的占用端口。
func New(cfg config.Config, logger *slog.Logger, info version.Info) *Server {
	s := &Server{
		cfg:       cfg,
		logger:    logger,
		info:      info,
		startedAt: time.Now(),
	}

	s.http = &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      s.routes(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
		// 把 net/http 内部的错误日志接到 slog 上，避免出现"两套日志格式"
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	return s
}

// Handler 暴露装配好的 handler，供 httptest 使用（测试不经过真实网络）。
func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

// routes 注册路由并装配中间件。
//
// 中间件顺序（外 → 内）：RequestID → InFlight → AccessLog → Recover → 业务 handler
//
//	RequestID 放最外：后面所有日志（包括 panic 日志、访问日志）都能带上 trace_id
//	InFlight 放次外：计数覆盖整个请求生命周期，优雅退出时报出的数字才准
//	AccessLog 必须在 Recover 外面：panic 被 Recover 转成 500 之后，访问日志仍要记到这条 500；
//	                              若反过来的话，panic 会穿过 AccessLog，那条请求就不会被记录
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Go 1.22+ 的 ServeMux 支持"方法 + 路径"模式，不再需要手动判断 r.Method
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// 开发环境才有：给优雅退出留一个"人为制造慢请求"的入口。
	// 生产环境不注册它（既少一个攻击面，也避免有人拿它做压测）。
	if s.cfg.App.Env == "dev" {
		mux.HandleFunc("GET /debug/sleep", s.handleDebugSleep)
		s.logger.Warn("已启用调试路由 /debug/sleep，仅限开发环境使用")
	}

	// 兜底：未匹配的路径统一返回 JSON 404，而不是 net/http 默认的纯文本
	mux.HandleFunc("/", s.handleNotFound)

	var h http.Handler = mux
	h = middleware.Recover(s.logger)(h)
	h = middleware.AccessLog(s.logger)(h)
	h = middleware.InFlight(&s.inFlight)(h)
	h = middleware.RequestID(h)
	return h
}

// healthzResponse 是 /healthz 的响应体。
// 为什么返回这些字段：负载均衡和运维靠它判断"这个实例是不是活着、跑的是哪个版本"，
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
// 注意：这里只反映"进程能否处理请求"，不检查 DB/Redis——依赖检查应放到 Day 3 的 /readyz，
// 两者混在一起会导致数据库抖动时容器被反复重启。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	resp := healthzResponse{
		Status:    "ok",
		Version:   s.info.Version,
		Commit:    s.info.Commit,
		BuildTime: s.info.BuildTime,
		Env:       s.cfg.App.Env,
		StartedAt: s.startedAt.UTC().Format(time.RFC3339),
		// 减去当前这个 /healthz 请求自己：InFlight 中间件在 handler 之前就已自增，
		// 不减的话探针每次都看到 1，运维会误以为"一直有请求在堆积"。
		// max(...,0) 是防御性写法：将来中间件顺序若变动，也不会返回负数。
		InFlight: max(s.inFlight.Load()-1, 0),
	}
	resp.UptimeSeconds = int64(time.Since(s.startedAt).Seconds())

	respond.JSON(w, s.logger, http.StatusOK, resp)
}

// handleDebugSleep 睡一会儿，用来验证优雅退出（仅 dev）。
// 参数上限 30s：调试接口也要有边界，否则它能被用来占满连接。
func (s *Server) handleDebugSleep(w http.ResponseWriter, r *http.Request) {
	d := 2 * time.Second
	if v := r.URL.Query().Get("d"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil || parsed <= 0 || parsed > 30*time.Second {
			respond.Error(w, s.logger, http.StatusBadRequest, respond.CodeBadRequest, "d 必须是 0~30s 之间的时长，例如 3s")
			return
		}
		d = parsed
	}

	select {
	case <-time.After(d):
		respond.JSON(w, s.logger, http.StatusOK, map[string]any{"slept": d.String()})
	case <-r.Context().Done():
		// 客户端断开或服务端关停：立刻返回，不要傻等满时长
		s.logger.InfoContext(r.Context(), "sleep 被取消", slog.String("reason", r.Context().Err().Error()))
	}
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	respond.Error(w, s.logger, http.StatusNotFound, respond.CodeNotFound, "接口不存在: "+r.Method+" "+r.URL.Path)
}

// Start 阻塞运行 HTTP 服务，直到 Shutdown 被调用。
func (s *Server) Start() error {
	s.logger.Info("HTTP 服务启动",
		"addr", s.cfg.Server.Addr,
		"env", s.cfg.App.Env,
		"version", s.info.Version,
		"commit", s.info.Commit,
	)

	// ListenAndServe 在 Shutdown 时会返回 ErrServerClosed，那是正常退出路径，不是错误
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	}
	return nil
}

// Shutdown 优雅退出：停止接收新连接，等在途请求处理完（最多 cfg.Server.ShutdownTimeout）。
//
// 这里有个非常容易踩的坑：入参 ctx 通常是"信号触发的那个 ctx"，它此刻**已经取消**了。
// 如果直接用它做超时，Shutdown 会立刻返回 context.Canceled，"等请求处理完"变成"立刻关掉"，
// 优雅退出形同虚设。context.WithoutCancel 剥离取消信号、保留 context 上的值，
// 再套一层自己的超时，才是正确写法。
func (s *Server) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.Server.ShutdownTimeout)
	defer cancel()

	if n := s.inFlight.Load(); n > 0 {
		s.logger.Info("收到退出信号，等待在途请求完成",
			"in_flight", n,
			"timeout", s.cfg.Server.ShutdownTimeout.String(),
		)
	} else {
		s.logger.Info("收到退出信号，当前没有在途请求")
	}

	if err := s.http.Shutdown(ctx); err != nil {
		return fmt.Errorf("等待在途请求超时（%s），已强制关闭: %w", s.cfg.Server.ShutdownTimeout, err)
	}

	s.logger.Info("优雅退出完成，进程即将结束")
	return nil
}
