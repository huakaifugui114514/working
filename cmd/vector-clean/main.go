// Command vector-clean 消费 vector_cleanup_jobs，删除已物理删除文档的 Qdrant 向量。
//
// purge 只在 MySQL 事务里写 outbox；真正的 Qdrant 删除由本命令异步完成，失败可重试。
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
	"github.com/huakaifugui114514/working/internal/indexer"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/qdrant"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "vector-clean 失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		once        = flag.Bool("once", false, "处理完当前到期任务后退出")
		interval    = flag.Duration("interval", 30*time.Second, "轮询间隔")
		workerID    = flag.String("worker-id", "", "worker 唯一标识（默认 hostname-pid）")
		lease       = flag.Duration("lease", 5*time.Minute, "running 任务 lease")
		maxAttempts = flag.Int("max-attempts", 5, "最大尝试次数")
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

	worker, err := indexer.NewCleanupWorker(indexer.CleanupWorkerOptions{
		Jobs:        indexer.NewCleanupStore(db),
		Vectors:     vectors,
		WorkerID:    *workerID,
		Lease:       *lease,
		MaxAttempts: *maxAttempts,
		Logger:      logger,
	})
	if err != nil {
		return fmt.Errorf("初始化 cleanup worker 失败: %w", err)
	}

	if *once {
		var firstErr error
		for {
			processed, err := worker.RunOnce(ctx)
			if err != nil {
				logger.Error("vector cleanup 任务失败", "error", err)
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

	logger.Info("vector cleanup worker 启动",
		"worker_id", *workerID,
		"qdrant_collection", cfg.Qdrant.Collection,
		"interval", *interval)
	err = worker.Run(ctx, *interval)
	if errors.Is(err, context.Canceled) {
		logger.Info("vector cleanup worker 收到退出信号，已停止")
		return nil
	}
	return err
}
