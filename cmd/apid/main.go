// Command apid 是 KnowFlow 的主 API 服务入口。
//
// 它只做装配：读配置 → 建 logger → 建 server → 起服务 → 等信号 → 优雅退出。
// 业务逻辑一律放在 internal/ 下——main 里出现业务 if-else，就说明分层白做了。
//
// TODO(day2 · 你来实现 run())。按顺序完成下面 6 步：
//
//  1. 取配置路径：优先环境变量 config.EnvConfigFile，为空则用 config.DefaultPath
//  2. config.Load 加载配置；出错要包一层上下文再返回（fmt.Errorf("...: %w", err)）
//  3. observability.NewLogger 建 logger；成功后 slog.SetDefault(logger)
//  4. rest.New(*cfg, logger, version.Get()) 组装服务
//  5. signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM) 得到 ctx 与 stop
//  6. 在 goroutine 里跑 svc.Start()，主 goroutine 用 select 等两件事：
//     a) 服务自己挂了（channel 收到 error）→ 直接返回该错误
//     b) ctx.Done()（收到信号）→ 先调 stop()（让第二次 Ctrl+C 能强杀），
//     再调 svc.Shutdown(ctx)，返回它的错误
//
// 思考题（写之前先答，答不出问我）：
//   - 为什么 main 里只调用 run()，而不是把逻辑直接写在 main 里？
//   - 装 Start() 结果的 channel 为什么要带缓冲（make(chan error, 1)）？
package main

import (
	"fmt"
	"os"
)

func main() {
	// 这层已经实现好了：错误只在最外层处理一次，退出码由它决定。
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "apid 启动失败: %v\n", err)
		os.Exit(1)
	}
}

// run 是真正的启动流程。
// 返回 error 而不是在内部 os.Exit：这样所有清理都能靠 defer 完成（os.Exit 不执行 defer）。
func run() error {
	panic("TODO(day2): 按文件头的 6 步实现装配流程")
}
