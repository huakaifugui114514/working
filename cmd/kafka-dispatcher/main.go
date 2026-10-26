// Command kafka-dispatcher 把 document_events outbox 发布到 Kafka。
//
// 用法：
//
//	go run ./cmd/kafka-dispatcher -once
//	go run ./cmd/kafka-dispatcher -interval 2s
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
	"github.com/huakaifugui114514/working/internal/event"
	"github.com/huakaifugui114514/working/internal/observability"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "kafka-dispatcher 失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	once := flag.Bool("once", false, "处理一批后退出")
	interval := flag.Duration("interval", 2*time.Second, "轮询间隔")
	batch := flag.Int("batch", 20, "每批事件数")
	lease := flag.Duration("lease", 30*time.Second, "publishing lease")
	backoff := flag.Duration("backoff", 2*time.Second, "失败退避基数")
	maxAttempts := flag.Int("max-attempts", 5, "最大尝试次数")
	flag.Parse()

	path := os.Getenv(config.EnvConfigFile)
	if path == "" {
		path = config.DefaultPath
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	if !cfg.Kafka.Enabled() {
		return errors.New("未配置 KF_KAFKA_BROKERS")
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

	pub, err := event.NewKafkaPublisher(cfg.Kafka.BrokerList(), cfg.Kafka.Topic, cfg.Kafka.Timeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := pub.Close(); err != nil {
			logger.Error("关闭 Kafka writer 失败", "error", err)
		}
	}()

	dispatcher, err := event.NewDispatcher(event.NewMySQLStore(db), pub, event.DispatcherOptions{
		BatchSize:   *batch,
		Lease:       *lease,
		Backoff:     *backoff,
		MaxAttempts: *maxAttempts,
		Logger:      logger,
	})
	if err != nil {
		return err
	}

	if *once {
		n, err := dispatcher.RunOnce(ctx)
		logger.Info("事件发布完成", "published", n)
		return err
	}
	logger.Info("Kafka dispatcher 启动", "brokers", cfg.Kafka.BrokerList(), "topic", cfg.Kafka.Topic)
	return dispatcher.Run(ctx, *interval)
}
