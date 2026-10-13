# KnowFlow

AI Native 企业知识库与 Agent 平台（Go 服务端）。

> **当前状态：M1 可运行骨架。**
> 配置加载、结构化日志、HTTP 中间件、`/healthz`、统一 404、优雅退出**已实现并有真实验收**（见「快速开始」）。
> 业务接口（知识库 CRUD / 检索 / Agent）尚未实现——请不要按本文部署到生产。

## 技术栈

| 领域 | 选型 |
|---|---|
| 语言 | Go 1.27+（`go.mod` 声明 `go 1.27.2`，工具链由 GOTOOLCHAIN 自动获取） |
| HTTP | 标准库 `net/http`（业务 CRUD 阶段引入 gin） |
| 配置 | YAML（`gopkg.in/yaml.v3`）+ 环境变量覆盖 |
| 日志 | 标准库 `log/slog`（JSON handler + `trace_id`） |
| 数据库 | MySQL 8（迁移工具待定：`golang-migrate` / `goose`） |
| 缓存 | Redis 7 |
| 本地编排 | Docker Compose（`deploy/`） |

## 快速开始

前置：Go 1.27+（本机 Go 较旧时，`GOTOOLCHAIN=auto` 会在首次构建自动下载匹配的工具链）。

```bash
make build                                   # 编译到 bin/apid，版本号由 git + ldflags 注入
KF_SERVER_ADDR=:18080 ./bin/apid             # 启动（环境变量覆盖配置里的 :8080）
```

启动日志（JSON，含 addr / env / version / commit）：

```json
{"level":"INFO","msg":"HTTP 服务启动","addr":":18080","env":"dev","version":"a04b570","commit":"a04b570"}
```

### 1. 存活探针

```bash
curl -s localhost:18080/healthz | python3 -m json.tool
```

```json
{
    "status": "ok",
    "version": "a04b570-dirty",
    "commit": "a04b570",
    "build_time": "2026-10-13T16:29:22Z",
    "env": "dev",
    "started_at": "2026-10-13T16:29:29Z",
    "uptime_seconds": 1,
    "in_flight": 0
}
```

`in_flight` 是"正在处理中的请求数"，**不含探针自己**。它只反映"进程能否处理请求"，**不检查** DB/Redis——依赖检查属于以后的 `/readyz`（混在一起会导致数据库抖动时容器被反复重启）。

### 2. 统一 404

```bash
curl -s -D - localhost:18080/nope
```

```
HTTP/1.1 404 Not Found
X-Request-Id: 9a8eb05de801964e
{"error":{"code":"route_not_found","message":"接口不存在: GET /nope"}}
```

`X-Request-Id` 与该请求所有日志里的 `trace_id` 一致，可以直接拿它去日志里捞整条链路。

### 3. 优雅退出（本项目 M1 的核心验收）

```bash
# 终端 A
KF_SERVER_ADDR=:18080 ./bin/apid

# 终端 B：制造一个 3 秒的慢请求，然后立刻在终端 A 按 Ctrl+C
curl -s "localhost:18080/debug/sleep?d=3s" &
```

终端 A 的日志会依次出现：

```json
{"level":"INFO","msg":"收到退出信号，等待在途请求完成","in_flight":1,"timeout":"15s"}
{"level":"INFO","msg":"HTTP 服务已关闭","addr":":18080",...}
{"level":"INFO","msg":"http 请求","method":"GET","path":"/debug/sleep","status":200,"duration":3000929706,"trace_id":"71d74ad248365b26"}
{"level":"INFO","msg":"优雅退出完成，进程即将结束"}
```

而终端 B 的慢请求**仍然正常返回 200**：

```
慢请求结果: 状态码=200 耗时=3.001110s
{"slept":"3s"}
```

要点：
- 优雅退出**不会掐断在途请求**，最多等 `server.shutdown_timeout`（默认 15s）；超时则返回 `context.DeadlineExceeded` 并强制关闭。
- 收到退出信号后会先恢复默认信号处理，**再按一次 Ctrl+C 可以立刻强杀**。
- 生产环境（k8s / `docker stop`）发的是 **SIGTERM**，服务同时监听 SIGINT 与 SIGTERM。
- `/debug/sleep` **只在 `dev` 环境注册**，生产环境该路径返回 404。

## 配置

加载优先级（高者覆盖低者）：**内置默认值 < `configs/config.yaml` < 环境变量（`KF_` 前缀）**。

| 环境变量 | 作用 |
|---|---|
| `KF_CONFIG_FILE` | 指定配置文件路径（默认 `configs/config.yaml`） |
| `KF_APP_ENV` | 运行环境：`dev` / `test` / `prod` |
| `KF_SERVER_ADDR` | 监听地址，如 `:8080` |
| `KF_SERVER_SHUTDOWN_TIMEOUT` | 优雅退出等待在途请求的上限，如 `15s` |
| `KF_LOG_LEVEL` | `debug` / `info` / `warn` / `error` |
| `KF_LOG_FORMAT` | `json`（线上，便于按字段检索）/ `text`（本地肉眼读） |

- 配置文件**必须存在**：找不到文件会在启动时报错退出（fail fast），不会静默改用默认值。
- 本地要改配置用 `KF_CONFIG_FILE=configs/config.local.yaml`（已 gitignore），**不要**改提交进仓库的那份。
- 日志一律写 **stdout**，由容器运行时 / 日志 agent 收集，应用自身不写文件、不做轮转。
- 密钥只从环境变量读，**不入库**。

## 接口

| 方法 | 路径 | 说明 | 状态 |
|---|---|---|---|
| GET | `/healthz` | 存活探针：版本号 / 启动时间 / 在途请求数 | ✅ |
| GET | `/debug/sleep?d=3s` | 人为制造慢请求，用于验证优雅退出；**仅 `dev` 注册** | ✅ |

错误码分两层：**业务码**在 `internal/apperr`（如 `invalid_argument`），**传输层码**在 `internal/transport/rest/respond`（如 `route_not_found`），映射只在 `respond.StatusForCode` 一处。

统一错误体：

```json
{"error": {"code": "route_not_found", "message": "接口不存在: GET /nope"}}
```

4xx 回显业务 message；**5xx 只回固定文案**，具体原因只进日志（避免把内部信息泄露给调用方）。

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

## 工程门禁

```bash
make help     # 查看全部工程命令
make check    # 提交前一键检查：gofmt + go vet + golangci-lint + go test -race
make build    # 编译到 bin/apid（版本号由 git + ldflags 注入）
make run      # 本地运行（go run，版本号为 dev）
```

`make check` 全绿是提交的前提，当前状态：

```
gofmt 完成 ／ go vet 通过 ／ 0 issues. golangci-lint 通过
ok  internal/apperr
ok  internal/config
ok  internal/observability
ok  internal/transport/rest
ok  internal/transport/rest/middleware
ok  internal/transport/rest/respond
```

## 实现状态

- [x] `internal/config` — 三层配置加载（默认值 / YAML / 环境变量）+ `errors.Join` 全量校验
- [x] `internal/apperr` — 业务错误 `Error{Code, Message, Cause}` + `errors.Is/As` 穿透
- [x] `internal/observability` — `slog` logger（json/text）+ `trace_id` 自动注入
- [x] `internal/transport/rest/respond` — 统一 JSON 响应与错误体（5xx 不泄露内部信息）
- [x] `internal/transport/rest/middleware` — RequestID / InFlight / AccessLog / Recover
- [x] `internal/transport/rest` — 路由装配、`/healthz`、统一 404、优雅退出
- [x] `cmd/apid` — 装配流程（配置 → 日志 → 服务 → 信号 → 优雅退出）
- [x] 单元测试与竞态检测门禁 — 6 个包，42 个用例 + 55 个表驱动子用例
- [ ] 业务接口（知识库 CRUD / 检索 / Agent）、数据库与迁移 — **M2 起**

## 协作与规范

- 提交信息：`<type>(<scope>): <中文说明>`
- 提交前必须 `make check` 全绿；实现细节与协作规则见 [`AGENTS.md`](AGENTS.md)
- 有取舍的设计写进 `docs/adr/NNN-标题.md`（背景 / 选项 / 决定 / 代价）
