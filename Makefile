# KnowFlow 工程命令。约定：所有命令都在仓库根目录执行。
# 每个目标都写了 ## 注释，`make help` 会自动列出来——命令一多，help 比记忆靠谱。
#
# 说明：本文件属于「管道代码」，由 agent 提供；你可以按需增删目标。

MODULE     := github.com/huakaifugui114514/working
APP        := apid
BIN_DIR    := bin
BIN        := $(BIN_DIR)/$(APP)

# 版本信息来自 Git，而不是手写在代码里：手写的版本号迟早会和实际代码对不上。
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# -s -w 去掉符号表与调试信息；-X 把版本注入 internal/version
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.BuildTime=$(BUILD_TIME)

# golangci-lint：优先用 PATH 里的，找不到就用 go install 的默认落点 $(go env GOPATH)/bin。
# 版本必须 >= 项目 go.mod 里的 Go 版本，否则 linter 直接拒绝启动：
#   "the Go language version used to build golangci-lint is lower than the targeted Go version"
# 本项目 go 1.27.2，故用 v2.14.0，并且必须**用同一版本的工具链编译**它：
#   GOTOOLCHAIN=go1.27.2 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
# 不加 GOTOOLCHAIN 的话，go install 会按被安装包的 go.mod 选工具链（1.26.9），
# 编出来的 linter 版本号够新、Go 版本却仍然偏低，照样拒绝启动。
GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null || echo $(shell go env GOPATH)/bin/golangci-lint)
GOLANGCI_LINT_VERSION := v2.14.0

.DEFAULT_GOAL := help

.PHONY: help
help: ## 显示所有可用命令
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

.PHONY: fmt
fmt: ## 格式化代码
	@gofmt -l -w .
	@echo "gofmt 完成（上面列出的是被改动的文件，空即为无需改动）"

.PHONY: vet
vet: ## 静态检查
	@go vet ./...
	@echo "go vet 通过"

.PHONY: lint
lint: ## 静态检查（golangci-lint，配置见 .golangci.yml）
	@test -x "$(GOLANGCI_LINT)" || { \
		echo "缺少 golangci-lint，请先执行："; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)"; \
		exit 1; \
	}
	@"$(GOLANGCI_LINT)" run ./...
	@echo "golangci-lint 通过"

.PHONY: test
test: ## 跑全部测试（带竞态检测）
	@go test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## 跑集成测试（需要 MySQL/Qdrant；Ollama 测试可选）
	@go test -race -tags=integration -count=1 ./...

.PHONY: e2e
e2e: ## 全链路 E2E：真实 HTTP + MySQL + Qdrant + fake embedder/LLM
	@go test -race -tags=e2e -v ./test/e2e

.PHONY: coverage
coverage: ## 生成内部包覆盖率并检查 ≥85%（需要 MySQL/Qdrant）
	@go test -tags=integration -coverpkg=./internal/... -coverprofile=coverage.out ./internal/... ./migrations
	@total=$$(go tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d '%'); 	echo "内部包总覆盖率: $$total%"; 	awk -v total="$$total" 'BEGIN { if (total < 85) { print "覆盖率低于 85%"; exit 1 } }'

.PHONY: docker-build
docker-build: ## 构建生产镜像（可用 GO_IMAGE/RUNTIME_IMAGE 覆盖基础镜像）
	@docker build -t knowflow:local \
		--build-arg GO_IMAGE="$${GO_IMAGE:-golang:1.27.2-alpine}" \
		--build-arg RUNTIME_IMAGE="$${RUNTIME_IMAGE:-alpine:3.22}" \
		--build-arg GOPROXY="$${GOPROXY:-https://proxy.golang.org,direct}" \
		--build-arg VERSION="$(VERSION)" \
		--build-arg COMMIT="$(COMMIT)" \
		--build-arg BUILD_TIME="$(BUILD_TIME)" \
		.

.PHONY: build
build: ## 编译到 bin/apid（版本号由 ldflags 注入）
	@mkdir -p $(BIN_DIR)
	@go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/$(APP)
	@echo "已生成 $(BIN)  版本=$(VERSION)  commit=$(COMMIT)  构建时间=$(BUILD_TIME)"

.PHONY: run
run: ## 本地运行（go run，版本号为 dev）
	@go run ./cmd/$(APP)

.PHONY: ai-up
ai-up: ## 启动本地 Ollama + Qdrant（M4.4 用）
	@docker compose -f deploy/docker-compose.yml --profile ai up -d qdrant ollama
	@echo "Ollama :11434  Qdrant :6333"

.PHONY: ai-model
ai-model: ## 拉取 bge-m3（先 make ai-up）
	@docker exec knowflow-ollama ollama pull bge-m3

.PHONY: ai-stop
ai-stop: ## 停止 Ollama + Qdrant（保留 volume）
	@docker compose -f deploy/docker-compose.yml --profile ai stop qdrant ollama

.PHONY: embed-eval
embed-eval: ## 纯向量评测（需要 Ollama + bge-m3）
	@go run ./cmd/embed-eval -set internal/eval/testdata/search_eval.json

.PHONY: embed-once
embed-once: ## 处理当前到期 embedding 任务后退出（需要 Ollama + Qdrant）
	@go run ./cmd/embed -once

.PHONY: kafka-up
kafka-up: ## 启动本地 Kafka（Compose profile kafka）
	@docker compose -f deploy/docker-compose.yml --profile kafka up -d kafka

.PHONY: kafka-dispatcher-once
kafka-dispatcher-once: ## 发布一批 document_events 到 Kafka
	@go run ./cmd/kafka-dispatcher -once

.PHONY: kafka-embed
kafka-embed: ## 从 Kafka 消费事件并触发 embedding worker
	@go run ./cmd/kafka-embed

.PHONY: mcp-build
mcp-build: ## 构建 MCP stdio Server 到 bin/mcp-server
	@mkdir -p $(BIN_DIR)
	@go build -trimpath -o $(BIN_DIR)/mcp-server ./cmd/mcp-server

.PHONY: vector-clean-once
vector-clean-once: ## 处理当前到期 Qdrant 向量清理任务后退出（需要 Qdrant）
	@go run ./cmd/vector-clean -once

.PHONY: check
check: fmt vet lint test ## 提交前一键检查：fmt + vet + lint + test

.PHONY: tidy
tidy: ## 整理依赖
	@go mod tidy

.PHONY: clean
clean: ## 清理编译产物（只删本项目 bin/）
	@rm -rf $(BIN_DIR)
	@echo "已清理 $(BIN_DIR)/"
