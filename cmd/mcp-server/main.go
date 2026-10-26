// Command mcp-server 通过 stdio 暴露 KnowFlow 的 MCP Tools。
//
// 用法（Cursor / Claude Desktop 的 MCP 配置）：
//
//	{
//	  "mcpServers": {
//	    "knowflow": {
//	      "command": "/path/to/mcp-server",
//	      "env": {
//	        "KF_API_URL": "http://127.0.0.1:8080",
//	        "KF_AUTH_API_KEY": "<api-key>"
//	      }
//	    }
//	  }
//	}
//
// stdout 只输出 JSON-RPC；日志全部写 stderr。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/huakaifugui114514/working/internal/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-server 失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	apiURL := flag.String("api-url", envOr("KF_API_URL", "http://127.0.0.1:8080"), "KnowFlow API base URL")
	apiKey := flag.String("api-key", os.Getenv("KF_AUTH_API_KEY"), "KnowFlow API key（建议用环境变量）")
	timeout := flag.Duration("timeout", 60*time.Second, "KnowFlow API 请求超时")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := mcp.NewAPIClient(*apiURL, *apiKey, *timeout)
	if err != nil {
		return err
	}
	server := mcp.NewServer(client, logger)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
