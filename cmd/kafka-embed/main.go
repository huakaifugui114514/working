// Command kafka-embed 消费 Kafka 事件并触发 embedding worker。
//
// Kafka 只是通知通道：worker 仍从 MySQL embedding_jobs 抢任务；
// 重复消息/重复消费不会破坏数据，靠 job 状态和 content_hash 幂等。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
	"github.com/huakaifugui114514/working/internal/embedding"
	"github.com/huakaifugui114514/working/internal/event"
	"github.com/huakaifugui114514/working/internal/indexer"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/qdrant"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "kafka-embed 失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	path := os.Getenv(config.EnvConfigFile)
	if path == "" {
		path = config.DefaultPath
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	if !cfg.Kafka.Enabled() {
		return fmt.Errorf("未配置 KF_KAFKA_BROKERS")
	}
	logger, err := observability.NewLogger(string(cfg.Log.Level), string(cfg.Log.Format), os.Stdout)
	if err != nil {
		return fmt.Errorf("初始化 logger 失败: %w", err)
	}
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer func() { _ = db.Close() }()

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

	host, _ := os.Hostname()
	worker, err := indexer.NewWorker(indexer.WorkerOptions{
		Jobs:        indexer.NewJobStore(db),
		Content:     knowledge.NewMySQLRepository(db),
		Embedder:    embedder,
		Vectors:     vectors,
		WorkerID:    fmt.Sprintf("%s-kafka-%d", host, os.Getpid()),
		Model:       cfg.Embedding.Model,
		BatchSize:   cfg.Embedding.BatchSize,
		Lease:       15 * time.Minute,
		MaxAttempts: 3,
		Logger:      logger,
	})
	if err != nil {
		return fmt.Errorf("初始化 embedding worker 失败: %w", err)
	}

	consumer, err := event.NewConsumer(
		cfg.Kafka.BrokerList(), cfg.Kafka.Topic, cfg.Kafka.GroupID,
		func(handlerCtx context.Context, ev event.Event) error {
			// 一条事件触发一次“清空当前到期任务”；即使消息重复，
			// job 状态/ content_hash 也保证幂等。
			for {
				processed, err := worker.RunOnce(handlerCtx)
				if err != nil {
					return err
				}
				if !processed {
					return nil
				}
			}
		},
	)
	if err != nil {
		return err
	}
	logger.Info("Kafka embedding consumer 启动", "topic", cfg.Kafka.Topic, "group", cfg.Kafka.GroupID)
	return consumer.Run(ctx)
}
