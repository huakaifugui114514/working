# KnowFlow

AI Native 企业知识库与 Agent 平台（Go 服务端）。

> **当前状态：开发中，尚不可运行。**
> `internal/` 下的函数目前是 `panic("TODO(day2)")` 占位，服务还起不来——**请勿按本文部署**。
> 实现进度见下方「实现状态」，那里勾上的才是真正可用的。

## 技术栈

| 领域 | 选型 |
|---|---|
| 语言 | Go 1.23+ |
| HTTP | 标准库 `net/http`（业务 CRUD 阶段引入 gin） |
| 配置 | YAML（`gopkg.in/yaml.v3`）+ 环境变量覆盖 |
| 日志 | 标准库 `log/slog`（JSON handler + `trace_id`） |
| 数据库 | MySQL 8（迁移工具待定：`golang-migrate` / `goose`） |
| 缓存 | Redis 7 |
| 本地编排 | Docker Compose（`deploy/`） |

## 目录结构

```
cmd/         可执行程序入口（每个服务一个子目录，只做装配）
internal/    私有代码，外部模块不可导入
  config/        配置加载与校验
  apperr/        业务错误与错误码
  observability/ 日志与 trace_id
  transport/     HTTP 传输层（rest + middleware + respond）
  version/       构建期注入的版本信息
pkg/         可被外部复用的库（确实需要时才放）
api/         接口契约（proto / openapi）
migrations/  数据库迁移脚本（只追加，禁止手改已应用的迁移）
deploy/      部署与编排（docker-compose / grafana）
configs/     配置模板
docs/        设计与决策记录（ADR / schema / 架构）
```

## 本地开发

前置：Go 1.23+。

```bash
make help     # 查看全部工程命令
make check    # 提交前一键检查：fmt + vet + test -race
make build    # 编译到 bin/apid（版本号由 git + ldflags 注入）
make run      # 本地运行
```

### 配置

加载优先级（高者覆盖低者）：**内置默认值 < `configs/config.yaml` < 环境变量（`KF_` 前缀）**。

| 环境变量 | 作用 |
|---|---|
| `KF_CONFIG_FILE` | 指定配置文件路径（默认 `configs/config.yaml`） |
| `KF_APP_ENV` | 运行环境：`dev` / `test` / `prod` |
| `KF_SERVER_ADDR` | 监听地址，如 `:8080` |
| `KF_SERVER_SHUTDOWN_TIMEOUT` | 优雅退出等待在途请求的上限，如 `15s` |
| `KF_LOG_LEVEL` | `debug` / `info` / `warn` / `error` |
| `KF_LOG_FORMAT` | `json`（线上）/ `text`（本地） |

密钥只从环境变量读，**不入库**（仓库内只保留 `.env.example` 这类模板）。

## 接口

| 方法 | 路径 | 说明 | 状态 |
|---|---|---|---|
| GET | `/healthz` | 健康检查：版本号 / 启动时间 / 在途请求数 | 未实现 |
| GET | `/debug/sleep?d=3s` | 人为制造慢请求，用于验证优雅退出；**仅 `dev` 环境注册** | 未实现 |

未匹配的路径统一返回 JSON 404，错误体形如：

```json
{"error": {"code": "not_found", "message": "接口不存在: GET /nope"}}
```

## 实现状态

- [ ] `internal/config` — 配置分层加载（默认值 / YAML / 环境变量）+ 校验 + 单测
- [ ] `internal/apperr` — 业务错误 `Error{Code, Message, Cause}` + `errors.Is/As` 穿透
- [ ] `internal/observability` — `slog` JSON logger + `trace_id` 自动注入
- [ ] `internal/transport/rest/respond` — 统一 JSON 响应与错误体
- [ ] `internal/transport/rest/middleware` — RequestID / InFlight / AccessLog / Recover
- [ ] `internal/transport/rest` — 路由装配、`/healthz`、优雅退出
- [ ] `cmd/apid` — 装配流程（配置 → 日志 → 服务 → 信号 → 退出）
- [ ] 单元测试与 CI 门禁（当前 `go test ./...` 无任何用例）

## 协作与规范

- 提交信息：`<type>(<scope>): <中文说明>`
- 提交前必须 `make check` 全绿；实现细节与协作规则见 [`AGENTS.md`](AGENTS.md)
- 有取舍的设计写进 `docs/adr/NNN-标题.md`（背景 / 选项 / 决定 / 代价）
