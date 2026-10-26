// Command workflow 执行 YAML DAG 工作流。
//
// 用法：
//
//	go run ./cmd/workflow -file configs/workflows/pdf-ingest.yaml \
//	  -var pdf_path=/tmp/a.pdf -var document_id=<uuid> -var api_url=http://127.0.0.1:8080
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/internal/workflow"
)

type varsFlag map[string]any

func (v varsFlag) String() string { return fmt.Sprint(map[string]any(v)) }
func (v varsFlag) Set(s string) error {
	key, value, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return fmt.Errorf("格式必须是 key=value")
	}
	v[key] = value
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "workflow 失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	file := flag.String("file", "", "工作流 YAML 路径")
	runIDRaw := flag.String("run-id", "", "复用/恢复的 run id；为空则新建")
	apiURL := flag.String("api-url", "http://127.0.0.1:8080", "http_request 默认 API 前缀（模板可用 input.api_url）")
	apiKey := flag.String("api-key", os.Getenv("KF_AUTH_API_KEY"), "http_request 的 Bearer API key")
	vars := varsFlag{}
	flag.Var(vars, "var", "输入变量 key=value，可重复")
	flag.Parse()
	if *file == "" {
		return fmt.Errorf("必须指定 -file")
	}
	if *apiURL != "" {
		vars["api_url"] = strings.TrimRight(*apiURL, "/")
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("读取工作流失败: %w", err)
	}
	def, err := workflow.Parse(data)
	if err != nil {
		return err
	}

	path := os.Getenv(config.EnvConfigFile)
	if path == "" {
		path = config.DefaultPath
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
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

	actions := map[string]workflow.Action{
		"pdf_extract":  workflow.PDFExtractAction,
		"http_request": workflow.HTTPAction(workflow.HTTPActionOptions{APIKey: *apiKey}),
		"sleep":        workflow.SleepAction,
		"noop":         workflow.NoopAction,
	}
	engine, err := workflow.NewEngine(def, actions, workflow.NewMySQLStore(db), logger)
	if err != nil {
		return err
	}

	var runID uuid.UUID
	if *runIDRaw != "" {
		runID, err = uuid.Parse(*runIDRaw)
		if err != nil {
			return fmt.Errorf("run-id 无效: %w", err)
		}
	} else {
		runID, err = uuid.NewV7()
		if err != nil {
			return fmt.Errorf("生成 run id: %w", err)
		}
	}

	start := time.Now()
	result, execErr := engine.Execute(ctx, runID, map[string]any(vars))
	if result == nil {
		return execErr
	}
	raw, _ := json.MarshalIndent(map[string]any{
		"run_id":   runID,
		"name":     def.Name,
		"status":   result.Status,
		"output":   result.Output,
		"error":    result.Error,
		"duration": time.Since(start).String(),
	}, "", "  ")
	fmt.Println(string(raw))
	return execErr
}
