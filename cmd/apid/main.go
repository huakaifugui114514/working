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
	"github.com/huakaifugui114514/working/internal/database"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/observability"
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
	//    main 只做装配，业务规则在 knowledge.Service，SQL 在 MySQLRepository。
	collectionService := knowledge.NewService(knowledge.NewMySQLRepository(db))
	svc := rest.New(*cfg, logger, version.Get(), collectionService)

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
