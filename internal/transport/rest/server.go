// Package rest 是 HTTP 传输层：路由注册、中间件装配、服务生命周期管理。
//
// 为什么包名叫 rest 而不是 http：包名叫 http 会与标准库 net/http 撞名，
// 每个引用它的文件都被迫写别名（nethttp "net/http"），读起来更累。
//
// 本包只做装配与生命周期，不写业务逻辑：业务代码放 internal/ 下的 service/repository。
package rest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/transport/rest/middleware"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
	"github.com/huakaifugui114514/working/internal/version"
)

const (
	healthzStatus = "ok"
	readyzStatus  = "ok"
	// /debug/sleep 的参数范围与默认值。上限是"防护"而不只是"合理性"：
	// 没有上限的话，任何人都能用 ?d=24h 占住一个连接和 goroutine。
	defaultDebugSleep = 2 * time.Second
	maxDebugSleep     = 30 * time.Second
)

// Server 持有 HTTP 服务的全部运行期状态。
type Server struct {
	cfg         config.Config
	logger      *slog.Logger
	info        version.Info
	collections CollectionService
	documents   DocumentService
	contents    ContentService
	searches    SearchService
	agents      AgentService
	readiness   Readiness
	metrics     *observability.Metrics
	startedAt   time.Time

	// inFlight 正在处理的请求数；用 atomic 而不是 Mutex：只有一个计数器、无关联状态，
	// 原子操作更轻，也不会出现"忘记解锁"。
	// 注意零值即可用——不要写成 `inFlight: atomic.Int64{}` 去"初始化"它：
	// atomic.Int64 内含 noCopy，复制它会破坏原子性保证（go vet 会直接报 copylocks）。
	inFlight atomic.Int64

	http *http.Server
}

// Readiness 是就绪探针依赖的最小接口；*sql.DB 天然满足。
type Readiness interface {
	PingContext(ctx context.Context) error
}

// SetReadiness 注入依赖检查。
//
// 不在 New 里加参数：就绪检查是可选的（单测/纯 handler 测试不需要 DB），
// 但生产装配必须调用；未注入时 /readyz 返回 503，避免误报 ready。
func (s *Server) SetReadiness(r Readiness) { s.readiness = r }

// New 组装一个可运行的 Server（不启动监听）。
//
// 构造与启动分离，是为了让测试可以直接拿 Handler() 跑 httptest —— 不占端口、不起网络。
// collections / documents / contents / searches / agents 由 main 注入；五者都是必需依赖，
// 传 nil 说明装配代码有 bug，直接在启动期 panic，避免请求打进来才发现空指针。
func New(
	cfg config.Config,
	logger *slog.Logger,
	info version.Info,
	collections CollectionService,
	documents DocumentService,
	contents ContentService,
	searches SearchService,
	agents AgentService,
) *Server {
	if collections == nil {
		panic("rest.New: collections is nil")
	}
	if documents == nil {
		panic("rest.New: documents is nil")
	}
	if contents == nil {
		panic("rest.New: contents is nil")
	}
	if searches == nil {
		panic("rest.New: searches is nil")
	}
	if agents == nil {
		panic("rest.New: agents is nil")
	}
	s := &Server{
		cfg:         cfg,
		logger:      logger,
		info:        info,
		collections: collections,
		documents:   documents,
		contents:    contents,
		searches:    searches,
		agents:      agents,
		metrics:     observability.NewMetrics(),
		startedAt:   time.Now(),
	}

	s.http = &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      s.routes(), // 此时 s.cfg / s.logger / &s.inFlight 都已就绪
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
		// 把 net/http 内部的错误日志接到 slog，否则会走标准库日志，出现"两套日志格式"
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	return s
}

// Handler 暴露装配好的 handler，供 httptest 使用（测试不经过真实网络、不占端口）。
func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

// baseAttrs 返回每条服务级日志都该带的字段，避免在每个调用点重复写四遍。
// 每次调用返回新切片，调用方可以安全地 append。
func (s *Server) baseAttrs() []slog.Attr {
	return []slog.Attr{
		slog.String("addr", s.cfg.Server.Addr),
		slog.String("env", string(s.cfg.App.Env)),
		slog.String("version", s.info.Version),
		slog.String("commit", s.info.Commit),
	}
}

// routes 注册路由并装配中间件。
//
// 中间件顺序（外 → 内）：RequestID → InFlight → AccessLog → Recover → 业务 handler
//
// 为什么 AccessLog 必须在 Recover 外面：panic 时写 500 响应的是 Recover，
// AccessLog 要记的是最终状态码；反过来的话 panic 会穿过 AccessLog，那条请求就不会被记录。
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	mux.HandleFunc("POST "+collectionsPath, s.handleCreateCollection)
	mux.HandleFunc("GET "+collectionsPath, s.handleListCollections)
	mux.HandleFunc("GET "+collectionsPath+"/{id}", s.handleGetCollection)
	mux.HandleFunc("PATCH "+collectionsPath+"/{id}", s.handleUpdateCollection)
	mux.HandleFunc("DELETE "+collectionsPath+"/{id}", s.handleDeleteCollection)
	mux.HandleFunc("POST "+collectionsPath+"/{id}/restore", s.handleRestoreCollection)

	mux.HandleFunc("POST "+documentsPath, s.handleCreateDocument)
	mux.HandleFunc("GET "+documentsPath+"/{id}", s.handleGetDocument)
	mux.HandleFunc("PATCH "+documentsPath+"/{id}", s.handleUpdateDocument)
	mux.HandleFunc("DELETE "+documentsPath+"/{id}", s.handleDeleteDocument)
	mux.HandleFunc("POST "+documentsPath+"/{id}/restore", s.handleRestoreDocument)
	mux.HandleFunc("PUT "+documentsPath+"/{id}/content", s.handleReplaceContent)

	mux.HandleFunc("POST "+collectionsPath+"/{id}/documents", s.handleAddDocumentToCollection)
	mux.HandleFunc("GET "+collectionsPath+"/{id}/documents", s.handleListDocumentsByCollection)
	mux.HandleFunc("DELETE "+collectionsPath+"/{id}/documents/{document_id}", s.handleRemoveDocumentFromCollection)

	mux.HandleFunc("GET "+searchPath, s.handleSearch)
	mux.HandleFunc("POST "+agentChatPath, s.handleAgentChat)

	// 调试路由只在 dev 注册：生产环境多一个入口就多一个攻击面
	if s.cfg.App.Env == config.EnvDev {
		mux.HandleFunc("GET /debug/sleep", s.handleDebugSleep)
		s.logger.Warn("/debug/sleep 已启用（仅限开发环境，生产不应出现）")
	}

	// 兜底：未匹配的路径统一走这里，保证 404 也是统一 JSON（而不是 net/http 的纯文本）
	mux.HandleFunc("/", s.handleNotFound)

	var h http.Handler = mux
	h = middleware.APIKey(s.cfg.Auth.APIKey)(h)
	h = middleware.Recover(s.logger)(h)
	h = middleware.AccessLog(s.logger)(h)
	h = middleware.InFlight(&s.inFlight)(h) // 传指针，中间件才能改计数器
	h = middleware.Metrics(s.metrics)(h)
	h = middleware.RequestID(h)
	h = middleware.SecurityHeaders(h)
	return h
}

// healthzResponse 是 /healthz 的响应体。
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

// handleHealthz 是存活探针：只反映"进程能否处理请求"，**不检查** DB/Redis。
// 依赖检查应放在独立的就绪探针里——混在一起会导致数据库抖动时容器被反复重启。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	resp := healthzResponse{
		Status:        healthzStatus,
		Version:       s.info.Version,
		Commit:        s.info.Commit,
		BuildTime:     s.info.BuildTime,
		Env:           string(s.cfg.App.Env),
		StartedAt:     s.startedAt.UTC().Format(time.RFC3339),
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		// 减 1 是减掉探针自己：InFlight 中间件在 handler 之前就已自增，
		// 不减的话运维每次都看到 ≥1，会误判成"有请求堆积"。
		InFlight: max(s.inFlight.Load()-1, 0),
	}
	respond.JSON(w, s.logger, http.StatusOK, resp)
}

// readyzResponse 是就绪探针的响应体。
type readyzResponse struct {
	Status string `json:"status"`
}

// handleReadyz 检查依赖是否可用；失败返回 503，不泄露内部错误。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.readiness == nil {
		respond.JSON(w, s.logger, http.StatusServiceUnavailable, readyzResponse{Status: "not_configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.readiness.PingContext(ctx); err != nil {
		s.logger.WarnContext(r.Context(), "就绪检查失败", "error", err)
		respond.JSON(w, s.logger, http.StatusServiceUnavailable, readyzResponse{Status: "unavailable"})
		return
	}
	respond.JSON(w, s.logger, http.StatusOK, readyzResponse{Status: readyzStatus})
}

// handleMetrics 输出 Prometheus text 格式指标。生产由 Prometheus 带 API key 抓取。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.metrics.WritePrometheus(w); err != nil {
		s.logger.WarnContext(r.Context(), "写 metrics 失败", "error", err)
	}
}

// handleDebugSleep 睡一会儿，用来验证优雅退出（仅 dev 注册）。
//
// 参数 d 默认 2s，必须在 (0, maxDebugSleep] 内；不合法属于业务语义错误，
// 用 apperr.CodeInvalidArgument（状态码交给 respond.StatusForCode 映射）。
func (s *Server) handleDebugSleep(w http.ResponseWriter, r *http.Request) {
	d := defaultDebugSleep

	if v := r.URL.Query().Get("d"); v != "" {
		parsed, err := time.ParseDuration(v)
		// 注意边界是 >，30s 本身是合法的
		if err != nil || parsed <= 0 || parsed > maxDebugSleep {
			respond.Error(w, s.logger,
				respond.StatusForCode(apperr.CodeInvalidArgument),
				apperr.CodeInvalidArgument,
				fmt.Sprintf("d 必须是 0~%s 之间的时长，例如 3s（不传则默认 %s）", maxDebugSleep, defaultDebugSleep))
			return
		}
		d = parsed
	}

	select {
	case <-r.Context().Done():
		// 客户端断开（或运维用 Server.Close 强关）时立刻返回，不傻等。
		// 注意：优雅退出（Shutdown）不会取消这个 ctx —— 它的语义就是"等这个请求跑完"。
		s.logger.InfoContext(r.Context(), "sleep 被取消", slog.String("err", r.Context().Err().Error()))
		return
	case <-time.After(d):
		respond.JSON(w, s.logger, http.StatusOK, map[string]any{"slept": d.String()})
	}
}

// handleNotFound 统一 404，避免未匹配路径落到 net/http 的纯文本 404。
// 用传输层自己的 route_not_found，与 apperr.CodeNotFound（资源不存在）刻意区分。
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	respond.Error(w, s.logger,
		respond.StatusForCode(respond.CodeRouteNotFound),
		respond.CodeRouteNotFound,
		fmt.Sprintf("接口不存在: %s %s", r.Method, r.URL.Path))
}

// Start 阻塞运行 HTTP 服务，直到 Shutdown 被调用。
func (s *Server) Start() error {
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "HTTP 服务启动", s.baseAttrs()...)

	err := s.http.ListenAndServe()
	if err == nil {
		return nil
	}
	// Shutdown 会让 ListenAndServe 返回 ErrServerClosed —— 那是正常退出路径，不是错误
	if errors.Is(err, http.ErrServerClosed) {
		s.logger.LogAttrs(context.Background(), slog.LevelInfo, "HTTP 服务已关闭", s.baseAttrs()...)
		return nil
	}

	s.logger.LogAttrs(context.Background(), slog.LevelError, "HTTP 服务监听失败",
		append(s.baseAttrs(), slog.Any("error", err))...)
	return err
}

// Shutdown 优雅退出：停止接收新连接 + 等在途请求处理完（最多 cfg.Server.ShutdownTimeout）。
//
// 最容易踩的坑：入参 ctx 通常是"信号触发的 ctx"，此刻**已经取消**了。
// 直接拿它做超时，Shutdown 会立刻返回 context.Canceled —— "等请求处理完"变成"立刻关掉"。
// context.WithoutCancel 剥掉取消信号、保留 ctx 上的值，再套自己的超时才是正确写法。
func (s *Server) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.Server.ShutdownTimeout)
	defer cancel()

	if n := s.inFlight.Load(); n > 0 {
		s.logger.Info("收到退出信号，等待在途请求完成",
			"in_flight", n, "timeout", s.cfg.Server.ShutdownTimeout.String())
	} else {
		s.logger.Info("收到退出信号，当前没有在途请求")
	}

	// 返回值：nil = 在途请求都跑完了；context.DeadlineExceeded = 超时被强制关闭
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("等待在途请求超时（%s），已强制关闭: %w", s.cfg.Server.ShutdownTimeout, err)
	}
	s.logger.Info("优雅退出完成，进程即将结束")
	return nil
}
