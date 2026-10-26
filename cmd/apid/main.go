// Command apid 是 KnowFlow 的主 API 服务入口。
//
// 它只做装配：读配置 → 建 logger → 建 server → 起服务 → 等信号 → 优雅退出。
// 业务逻辑一律放在 internal/ 下——main 里出现业务 if-else，就说明分层白做了。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/huakaifugui114514/working/internal/agent"
	"github.com/huakaifugui114514/working/internal/cache"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
	"github.com/huakaifugui114514/working/internal/embedding"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/llm/gateway"
	"github.com/huakaifugui114514/working/internal/llm/semanticcache"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/qdrant"
	"github.com/huakaifugui114514/working/internal/transport/rest"
	"github.com/huakaifugui114514/working/internal/version"
)

func main() {
	// 这层只做一件事：错误在最外层处理一次，退出码由它决定。
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "apid 启动失败: %v\n", err)
		os.Exit(1)
	}
}

// run 是真正的启动流程。
// 返回 error 而不是在内部 os.Exit：这样所有清理都能靠 defer 完成（os.Exit 不执行 defer）。
func run() error {
	// 1. 配置路径：环境变量优先，为空则用仓库内的默认路径。
	path := os.Getenv(config.EnvConfigFile)
	if path == "" {
		path = config.DefaultPath
	}

	// 2. 加载配置。配置错误属于"启动期致命错误"，直接失败退出：
	//    带着错配置跑起来，故障会从"启动就报错"推迟成"线上行为诡异"，排查成本高一个数量级。
	//    消息里带上 path——运维第一句话一定是"你用的是哪份配置"。
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("加载配置失败（path=%s）: %w", path, err)
	}

	// 3. logger：级别与格式都来自配置，不能写死，否则 configs/config.yaml 的 log 段就是摆设。
	//    SetDefault 不能省：包级 slog 和第三方库都走默认 logger，而标准库默认写的是 stderr——
	//    不设置就会出现"两套格式 + 两个目的地"。
	logger, err := observability.NewLogger(string(cfg.Log.Level), string(cfg.Log.Format), os.Stdout)
	if err != nil {
		return fmt.Errorf("初始化 logger 失败: %w", err)
	}
	slog.SetDefault(logger)

	// 4. 打开数据库连接池。连不上就启动失败：
	//    带着坏连接跑起来，故障会从"启动就报错"推迟成"每个请求都 500"，排查成本更高。
	db, err := database.Open(context.Background(), cfg.Database, logger)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("关闭数据库连接池失败", "error", err)
		}
	}()

	// 5. 装配依赖：repository → service → HTTP server。
	//    main 只做装配，业务规则在 knowledge.Service / DocumentService / ContentService，SQL 在 MySQLRepository。
	//    collections / documents / content / search 共用同一个连接池；仓储本身无状态，多建一个实例没有额外连接。
	repo := knowledge.NewMySQLRepository(db)
	collectionService := knowledge.NewService(repo)
	documentService := knowledge.NewDocumentService(repo)
	contentService := knowledge.NewContentService(repo)
	searchService := knowledge.NewSearchService(repo, collectionService)

	// Redis 是可选加速层：Redis 不可用不能阻塞服务启动，也不能让读请求变慢。
	// 只有 Ping 成功才把 cache 注入 service；失败就完全退化为查 MySQL。
	if cfg.Redis.Enabled() {
		redisCache, err := cache.NewRedis(cache.RedisOptions{
			Addr:          cfg.Redis.Addr,
			SentinelAddrs: cfg.Redis.SentinelList(),
			MasterName:    cfg.Redis.MasterName,
			Password:      cfg.Redis.Password,
			DB:            cfg.Redis.DB,
			Timeout:       cfg.Redis.Timeout,
		})
		if err != nil {
			return fmt.Errorf("初始化 Redis 客户端失败: %w", err)
		}
		pingCtx, cancel := context.WithTimeout(context.Background(), cfg.Redis.Timeout)
		pingErr := redisCache.Ping(pingCtx)
		cancel()
		if pingErr != nil {
			logger.Warn("Redis 不可用，搜索缓存降级为直查 MySQL", "error", pingErr)
			_ = redisCache.Close()
		} else {
			defer func() {
				if err := redisCache.Close(); err != nil {
					logger.Error("关闭 Redis 失败", "error", err)
				}
			}()
			searchService.WithCache(redisCache, cfg.Redis.SearchTTL)
			collectionService.WithCacheVersioner(redisCache)
			documentService.WithCacheVersioner(redisCache)
			contentService.WithCacheVersioner(redisCache)
			logger.Info("搜索缓存已启用", "addr", cfg.Redis.Addr, "sentinels", cfg.Redis.SentinelList(), "master", cfg.Redis.MasterName, "ttl", cfg.Redis.SearchTTL)
		}
	}

	keywordRetriever := agent.NewKeywordRetriever(searchService)
	var retriever agent.Retriever = keywordRetriever
	var vectorEmbedder *embedding.OllamaEmbedder
	if cfg.Retrieval.Mode != config.RetrievalFTS || cfg.LLM.SemanticCache {
		vectorEmbedder, err = embedding.NewOllamaEmbedder(embedding.OllamaOptions{
			BaseURL: cfg.Embedding.OllamaURL,
			Model:   cfg.Embedding.Model,
			Timeout: cfg.Embedding.Timeout,
		})
		if err != nil {
			return fmt.Errorf("初始化 embedding 客户端失败: %w", err)
		}
	}
	if cfg.Retrieval.Mode != config.RetrievalFTS {
		vectorStore, err := qdrant.NewClient(qdrant.Options{
			BaseURL:    cfg.Qdrant.URL,
			Collection: cfg.Qdrant.Collection,
			VectorSize: cfg.Qdrant.VectorSize,
			Timeout:    cfg.Qdrant.Timeout,
		})
		if err != nil {
			return fmt.Errorf("初始化 Qdrant 客户端失败: %w", err)
		}
		vectorRetriever := agent.NewVectorRetriever(vectorEmbedder, vectorStore, repo)
		switch cfg.Retrieval.Mode {
		case config.RetrievalVector:
			retriever = vectorRetriever
		case config.RetrievalHybrid:
			retriever = agent.NewHybridRetriever(
				keywordRetriever,
				vectorRetriever,
				cfg.Retrieval.Candidates,
				cfg.Retrieval.RRFK,
			)
		}
	}
	logger.Info("Agent 检索模式", "mode", cfg.Retrieval.Mode)

	primaryLLM, err := agent.NewDeepSeekClient(agent.DeepSeekOptions{
		BaseURL:   cfg.LLM.BaseURL,
		APIKey:    cfg.LLM.APIKey,
		Model:     cfg.LLM.Model,
		Timeout:   cfg.LLM.Timeout,
		MaxTokens: cfg.LLM.MaxTokens,
	})
	if err != nil {
		return fmt.Errorf("初始化主 LLM 客户端失败: %w", err)
	}
	providers := []gateway.Provider{{Name: "primary", Client: primaryLLM}}
	if cfg.LLM.FallbackBaseURL != "" {
		fallbackLLM, err := agent.NewDeepSeekClient(agent.DeepSeekOptions{
			BaseURL:   cfg.LLM.FallbackBaseURL,
			APIKey:    cfg.LLM.FallbackAPIKey,
			Model:     cfg.LLM.FallbackModel,
			Timeout:   cfg.LLM.Timeout,
			MaxTokens: cfg.LLM.MaxTokens,
		})
		if err != nil {
			return fmt.Errorf("初始化备用 LLM 客户端失败: %w", err)
		}
		providers = append(providers, gateway.Provider{Name: "fallback", Client: fallbackLLM})
	}
	if cfg.LLM.APIKey == "" {
		logger.Warn("KF_LLM_API_KEY 未配置，/api/v1/agent/chat 会返回 500")
	}
	var semantic gateway.SemanticCache
	if cfg.LLM.SemanticCache {
		sc, err := semanticcache.New(vectorEmbedder, cfg.LLM.SemanticThreshold, cfg.LLM.SemanticTTL, cfg.LLM.SemanticMaxEntries)
		if err != nil {
			return fmt.Errorf("初始化语义缓存失败: %w", err)
		}
		semantic = sc
		logger.Info("LLM 语义缓存已启用", "threshold", cfg.LLM.SemanticThreshold, "max_entries", cfg.LLM.SemanticMaxEntries)
	}
	quota := gateway.NewQuota(cfg.LLM.RequestsPerMinute, cfg.LLM.TokensPerDay)
	llmGateway, err := gateway.NewWithOptions(providers, gateway.Options{Logger: logger, Quota: quota, Cache: semantic})
	if err != nil {
		return fmt.Errorf("初始化 LLM gateway 失败: %w", err)
	}
	logger.Info("LLM gateway 已装配", "providers", len(providers))
	defer func() { logger.Info("LLM token 统计", "stats", llmGateway.Stats()) }()
	agentService := agent.NewService(
		retriever,
		llmGateway,
		collectionService,
		agent.DefaultTopK,
		cfg.LLM.MaxTokens,
	)

	svc := rest.New(*cfg, logger, version.Get(),
		collectionService, documentService, contentService, searchService, agentService)
	svc.SetReadiness(db)
	svc.SetLLMStats(func() map[string]observability.LLMStat {
		out := make(map[string]observability.LLMStat)
		for name, st := range llmGateway.Stats() {
			out[name] = observability.LLMStat{
				Requests: st.Requests, Errors: st.Errors,
				PromptTokens: st.PromptTokens, CompletionTokens: st.CompletionTokens,
			}
		}
		return out
	})

	// 6. 信号 ctx：Ctrl+C 是 SIGINT，而 k8s / docker stop 发的是 SIGTERM。
	//    只监听 SIGINT，线上就会被 SIGKILL 硬杀，优雅退出白写。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 7. 在 goroutine 里启动：Start 会阻塞到服务关闭，放在主 goroutine 就没人处理信号了。
	//
	//    channel 必须带缓冲：Shutdown 之后 Start 会返回（ErrServerClosed 已被它吞成 nil），
	//    而那时主 goroutine 已经离开 select、没人接收——无缓冲 channel 会让这个 goroutine
	//    永久阻塞在发送上，也就是 goroutine 泄漏。
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- svc.Start()
	}()

	select {
	case err := <-serveErr:
		// 服务自己退出了。正常路径下只有 Shutdown 之后才会走到这里（返回 nil），
		// 所以这里只做防御：先判 nil——fmt.Errorf 用 %w 包住 nil 会返回一个非 nil 错误，
		// 文本里还带 %!w(<nil>)。
		if err == nil {
			return nil
		}
		return fmt.Errorf("HTTP 服务异常退出: %w", err)

	case <-ctx.Done():
		// 收到退出信号。先 stop()：让第二次 Ctrl+C 能立刻强杀（卡住时的逃生口）。
		// 必须在这里调、不能只靠 defer——defer 要等 Shutdown 返回（最长 shutdown_timeout）
		// 才执行，而那段时间恰恰是最需要逃生口的时候。
		//
		// 注意它恢复的是"进程【启动时】的信号处置"，不是无条件的系统默认：
		// 若进程启动时 SIGINT 就被忽略（非交互 shell 的后台任务、nohup 会这样），
		// stop() 之后它仍然是忽略的——所以优雅退出必须依赖 SIGTERM（k8s/docker stop 发的就是它）。
		//
		// 再交给 Shutdown 优雅退出——它内部用 context.WithoutCancel 处理了
		// "信号 ctx 此刻已取消"这个坑。
		stop()
		return svc.Shutdown(ctx)
	}
}
