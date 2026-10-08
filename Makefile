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
# 版本必须是 v2.3.1 —— 它要求 Go 1.23，与本项目 go.mod 一致（v2.4.0+ 要 Go 1.24）。
# 装：go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.3.1
GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null || echo $(shell go env GOPATH)/bin/golangci-lint)
GOLANGCI_LINT_VERSION := v2.3.1

.DEFAULT_GOAL := help

.PHONY: help
help: ## 显示所有可用命令
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

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

.PHONY: build
build: ## 编译到 bin/apid（版本号由 ldflags 注入）
	@mkdir -p $(BIN_DIR)
	@go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/$(APP)
	@echo "已生成 $(BIN)  版本=$(VERSION)  commit=$(COMMIT)  构建时间=$(BUILD_TIME)"

.PHONY: run
run: ## 本地运行（go run，版本号为 dev）
	@go run ./cmd/$(APP)

.PHONY: check
check: fmt vet lint test ## 提交前一键检查：fmt + vet + lint + test

.PHONY: tidy
tidy: ## 整理依赖
	@go mod tidy

.PHONY: clean
clean: ## 清理编译产物（只删本项目 bin/）
	@rm -rf $(BIN_DIR)
	@echo "已清理 $(BIN_DIR)/"
