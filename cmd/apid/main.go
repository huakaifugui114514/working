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

	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/transport/rest"
	"github.com/huakaifugui114514/working/internal/version"
)

func main() {
	// 为什么 main 里只调用 run()：
	// 1) 任何一处 return 都不会跳过 defer（os.Exit 不会执行 defer，收尾逻辑会被吞掉）
	// 2) 错误处理只有一处，不会散落在十几个地方
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "apid 启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 配置文件路径属于"启动参数"，用环境变量给（容器里最常见），默认取仓库内的 configs/config.yaml
	cfgPath := os.Getenv(config.EnvConfigFile)
	if cfgPath == "" {
		cfgPath = config.DefaultPath
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}

	logger, err := observability.NewLogger(cfg.Log.Level, cfg.Log.Format, os.Stdout)
	if err != nil {
		return fmt.Errorf("初始化日志: %w", err)
	}
	// 设为默认 logger：第三方库或内部包用 slog.Default() 时也会走同一套格式与 trace_id
	slog.SetDefault(logger)

	svc := rest.New(*cfg, logger, version.Get())

	// signal.NotifyContext 把"收到 SIGINT/SIGTERM"变成一次 ctx 取消，
	// 比手写 signal channel 少了注册/注销和 select 的样板，也不容易漏掉某个信号。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 服务在独立 goroutine 里跑，主 goroutine 只等两件事：服务自己挂了，或者收到退出信号。
	// 注意 channel 要带缓冲：万一主 goroutine 已经走了，这个 goroutine 也能把结果写进去而不泄漏。
	serveErr := make(chan error, 1)
	go func() { serveErr <- svc.Start() }()

	select {
	case err := <-serveErr:
		// 监听都没起来（例如端口被占），此时没有请求需要等，直接返回错误
		return err

	case <-ctx.Done():
		// 恢复默认信号处理：再按一次 Ctrl+C 直接强杀。
		// 否则一旦优雅退出卡住（比如某个请求一直不结束），用户会发现"Ctrl+C 没反应了"。
		stop()
		logger.Info("收到退出信号，开始优雅退出")

		if err := svc.Shutdown(ctx); err != nil {
			return err
		}
		return nil
	}
}
