// Command embed 从 embedding_jobs 取任务，把 chunk 写成 Qdrant 向量。
//
// 它不在 HTTP 请求路径上：正文替换只写 outbox，worker 异步生成向量。
// 用法：
//
//	go run ./cmd/embed -once                 # 处理当前到期任务后退出
//	go run ./cmd/embed -interval 5s          # 常驻轮询
//	go run ./cmd/embed -worker-id embed-1    # 多副本时显式指定唯一 id
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
	"github.com/huakaifugui114514/working/internal/embedding"
	"github.com/huakaifugui114514/working/internal/indexer"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/qdrant"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "embed 失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		once        = flag.Bool("once", false, "处理完当前到期任务后退出")
		interval    = flag.Duration("interval", 5*time.Second, "轮询间隔")
		workerID    = flag.String("worker-id", "", "worker 唯一标识（默认 hostname-pid）")
		lease       = flag.Duration("lease", 15*time.Minute, "running 任务 lease；超时后可被重新抢占")
		maxAttempts = flag.Int("max-attempts", 3, "最大尝试次数；超过后标记 dead")
	)
	flag.Parse()

	path := os.Getenv(config.EnvConfigFile)
	if path == "" {
		path = config.DefaultPath
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("加载配置失败（path=%s）: %w", path, err)
	}

	logger, err := observability.NewLogger(string(cfg.Log.Level), string(cfg.Log.Format), os.Stdout)
	if err != nil {
		return fmt.Errorf("初始化 logger 失败: %w", err)
	}
	slog.SetDefault(logger)

	if *workerID == "" {
		host, _ := os.Hostname()
		*workerID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("关闭数据库连接池失败", "error", err)
		}
	}()

	embedder, err := embedding.NewOllamaEmbedder(embedding.OllamaOptions{
		BaseURL: cfg.Embedding.OllamaURL,
		Model:   cfg.Embedding.Model,
		Timeout: cfg.Embedding.Timeout,
	})
	if err != nil {
		return fmt.Errorf("初始化 embedding 客户端失败: %w", err)
	}

	vectors, err := qdrant.NewClient(qdrant.Options{
		BaseURL:    cfg.Qdrant.URL,
		Collection: cfg.Qdrant.Collection,
		VectorSize: cfg.Qdrant.VectorSize,
		Timeout:    cfg.Qdrant.Timeout,
	})
	if err != nil {
		return fmt.Errorf("初始化 Qdrant 客户端失败: %w", err)
	}
	if err := vectors.EnsureCollection(ctx); err != nil {
		return fmt.Errorf("初始化 Qdrant collection 失败: %w", err)
	}

	repo := knowledge.NewMySQLRepository(db)
	worker, err := indexer.NewWorker(indexer.WorkerOptions{
		Jobs:        indexer.NewJobStore(db),
		Content:     repo,
		Embedder:    embedder,
		Vectors:     vectors,
		WorkerID:    *workerID,
		Model:       cfg.Embedding.Model,
		BatchSize:   cfg.Embedding.BatchSize,
		Lease:       *lease,
		MaxAttempts: *maxAttempts,
		Logger:      logger,
	})
	if err != nil {
		return fmt.Errorf("初始化 embedding worker 失败: %w", err)
	}

	if *once {
		var firstErr error
		for {
			processed, err := worker.RunOnce(ctx)
			if err != nil {
				logger.Error("embedding 任务失败", "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
			if !processed {
				break
			}
		}
		return firstErr
	}

	logger.Info("embedding worker 启动",
		"worker_id", *workerID,
		"model", cfg.Embedding.Model,
		"qdrant_collection", cfg.Qdrant.Collection,
		"interval", *interval)

	err = worker.Run(ctx, *interval)
	if errors.Is(err, context.Canceled) {
		logger.Info("embedding worker 收到退出信号，已停止")
		return nil
	}
	return err
}
