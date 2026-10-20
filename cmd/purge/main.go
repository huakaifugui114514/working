// Command purge 物理清理超过保留期的软删数据。
//
// 它是一次性批处理任务，不在请求路径上运行；由 cron / Kubernetes CronJob /
// 低峰期运维命令调用。参数含义与清理顺序见 internal/purge。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/purge"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "purge 失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		retention = flag.Duration("retention", purge.DefaultRetention,
			"软删后保留多久才物理删除（例如 720h 表示 30 天）")
		batchSize = flag.Int("batch", purge.DefaultBatchSize,
			"每批处理的关联/父行数；建议 500–2000")
		interval = flag.Duration("interval", purge.DefaultInterval,
			"批间等待时间；0 表示不等待，仅建议测试使用")
		dryRun = flag.Bool("dry-run", false,
			"只统计会删除多少行，不执行 DELETE")
		timeout = flag.Duration("timeout", 30*time.Minute,
			"本次运行的最长耗时；防止异常数据让清理任务无限跑")
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

	// 批处理任务也需要优雅退出：收到 SIGTERM 时让当前批 SQL 走完，
	// 不要硬杀在 DELETE 中间（InnoDB 会回滚，但会白做一批）。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	db, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("关闭数据库连接池失败", "error", err)
		}
	}()

	stats, err := purge.NewRunner(db, logger).Run(ctx, purge.Options{
		Retention: *retention,
		BatchSize: *batchSize,
		Interval:  *interval,
		DryRun:    *dryRun,
	})
	if err != nil {
		return fmt.Errorf("执行 purge 失败: %w", err)
	}

	logger.Info("purge 统计",
		"associations", stats.Associations,
		"documents", stats.Documents,
		"collections", stats.Collections,
		"batches", stats.Batches,
		"dry_run", stats.DryRun,
		"finished_at", time.Now().UTC())
	return nil
}
