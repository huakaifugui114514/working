# KnowFlow

AI Native 企业知识库与 Agent 平台（Go 服务端）。

> **当前状态：M2 collections CRUD 已可运行。**
> 配置、日志、HTTP 中间件、`/healthz`、优雅退出，以及知识库的创建/查询/分页/更新/软删**已实现并有真实验收**（见「快速开始」与「知识库 CRUD」）。
> 文档管理、检索、Agent 尚未实现——请不要按本文部署到生产。

## 技术栈

| 领域 | 选型 |
|---|---|
| 语言 | Go 1.27+（`go.mod` 声明 `go 1.27.2`，工具链由 GOTOOLCHAIN 自动获取） |
| HTTP | 标准库 `net/http`（业务 CRUD 阶段引入 gin） |
| 配置 | YAML（`gopkg.in/yaml.v3`）+ 环境变量覆盖 |
| 日志 | 标准库 `log/slog`（JSON handler + `trace_id`） |
| 数据库 | MySQL 8 + `golang-migrate`（迁移工具，见 [ADR 002](docs/adr/002-迁移工具改选-golang-migrate.md)） |
| 缓存 | Redis 7 |
| 本地编排 | Docker Compose（`deploy/`） |

## 快速开始

前置：Go 1.27+、本地 MySQL 8（见「本地数据库」）。`apid` 启动时会连接数据库，连不上直接失败退出。

```bash
make build                                   # 编译到 bin/apid，版本号由 git + ldflags 注入

set -a; . deploy/.env; set +a                # 读取 MYSQL_* 环境变量
KF_DATABASE_HOST=127.0.0.1 KF_DATABASE_PORT="${MYSQL_PORT:-3306}" \
KF_DATABASE_NAME="$MYSQL_DATABASE" KF_DATABASE_USER="$MYSQL_USER" \
KF_DATABASE_PASSWORD="$MYSQL_PASSWORD" \
KF_SERVER_ADDR=:18080 ./bin/apid
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

`in_flight` 是"正在处理中的请求数"，**不含探针自己**。它只反映"进程能否处理请求"，**不检查** DB/Redis——依赖检查应放在独立的就绪探针里，混在一起会导致数据库抖动时容器被反复重启。

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

## 本地数据库（M2 起）

```bash
cd deploy
cp .env.example .env        # 填两个密码（.env 已 gitignore，不会进仓库）
docker compose up -d
docker compose ps           # 等 STATE 变成 healthy
docker compose down         # 停（数据保留在卷里）；down -v 连数据一起删
```

| 项 | 值 |
|---|---|
| 镜像 | `mysql:8.4`（MySQL 8 的 LTS） |
| 地址 | `127.0.0.1:3306`（可用 `MYSQL_PORT` 改） |
| 库 / 账号 | `knowflow` / `knowflow`（应用账号，非 root） |
| 字符集 | `utf8mb4` + `utf8mb4_0900_ai_ci` |
| 时区 | 固定 `+00:00`（约定：时间一律存 UTC，展示时再转） |

健康检查用的是**真实查询**（认证 + 选库 + `SELECT 1`）而不是 `mysqladmin ping` ——
后者在密码错误时也返回 0（实测），那种健康检查等于没做。

数据库迁移工具已定为 [`golang-migrate`](docs/adr/002-迁移工具改选-golang-migrate.md)
（曾选 `goose`，同日改选；两次取舍的理由与实测代价都记在 ADR 里）。

首次启动或 schema 变更后执行迁移：

```bash
set -a; . deploy/.env; set +a
KF_DATABASE_HOST=127.0.0.1 KF_DATABASE_PORT="${MYSQL_PORT:-3306}" \
KF_DATABASE_NAME="$MYSQL_DATABASE" KF_DATABASE_USER="$MYSQL_USER" \
KF_DATABASE_PASSWORD="$MYSQL_PASSWORD" \
go run ./cmd/migrate up
```

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
| POST | `/api/v1/collections` | 创建知识库 | ✅ |
| GET | `/api/v1/collections` | 分页列表：`limit` + `cursor` → `items` + `next_cursor` | ✅ |
| GET | `/api/v1/collections/{id}` | 按 id 查询 | ✅ |
| PATCH | `/api/v1/collections/{id}` | PATCH 更新（只改出现的字段） | ✅ |
| DELETE | `/api/v1/collections/{id}` | 软删 | ✅ |

完整契约见 [`api/openapi.yaml`](api/openapi.yaml)；路径、游标和 PATCH 语义的取舍见
[ADR 003](docs/adr/003-collections-api.md)。

错误码分两层：**业务码**在 `internal/apperr`（如 `invalid_argument`），**传输层码**在 `internal/transport/rest/respond`（如 `route_not_found`），映射只在 `respond.StatusForCode` 一处。

统一错误体：

```json
{"error": {"code": "route_not_found", "message": "接口不存在: GET /nope"}}
```

4xx 回显业务 message；**5xx 只回固定文案**，具体原因只进日志（避免把内部信息泄露给调用方）。

### 知识库 CRUD

```bash
BASE=http://127.0.0.1:18080/api/v1/collections

# 创建：成功返回 201 + Location
curl -sS -X POST "$BASE" -H 'Content-Type: application/json' \
  -d '{"name":"产品文档","description":"可选"}'

# 查询
curl -sS "$BASE/<id>"

# 分页：next_cursor 为 null 表示没有下一页
curl -sS "$BASE?limit=20"
curl -sS "$BASE?limit=20&cursor=<next_cursor>"

# PATCH：只改 name，description 不动；空对象 {} 是合法 no-op
curl -sS -X PATCH "$BASE/<id>" -H 'Content-Type: application/json' \
  -d '{"name":"新名字"}'

# 软删：204，无响应体；重复删除返回 404
curl -sS -i -X DELETE "$BASE/<id>"
```

## 目录结构

```
cmd/         可执行程序入口（apid / migrate，只做装配）
internal/    私有代码，外部模块不可导入
  config/        配置加载与校验
  apperr/        业务错误与错误码
  database/      连接池、DSN、迁移连接
  knowledge/     知识库领域模型、repository、service
  observability/ 日志与 trace_id
  transport/     HTTP 传输层（rest + middleware + respond）
  version/       构建期注入的版本信息
pkg/         可被外部复用的库（确实需要时才放）
api/         接口契约（openapi.yaml）
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
make run      # 本地运行（go run，版本号为 dev；需要先启动 MySQL 并配好 KF_DATABASE_*）
```

`make check` 全绿是提交的前提，当前状态：

```
gofmt 完成 ／ go vet 通过 ／ 0 issues. golangci-lint 通过
ok  internal/apperr
ok  internal/config
ok  internal/database
ok  internal/knowledge
ok  internal/observability
ok  internal/transport/rest
ok  internal/transport/rest/middleware
ok  internal/transport/rest/respond
ok  migrations
```

repository 的集成测试单独跑（需要本地 MySQL）：

```bash
go test -race -tags=integration -count=1 ./internal/knowledge
```

## 实现状态

- [x] `internal/config` — 三层配置加载（默认值 / YAML / 环境变量）+ `errors.Join` 全量校验
- [x] `internal/apperr` — 业务错误 `Error{Code, Message, Cause}` + `errors.Is/As` 穿透
- [x] `internal/observability` — `slog` logger（json/text）+ `trace_id` 自动注入
- [x] `internal/database` — DSN / 迁移 DSN / 连接池 / 集成测试
- [x] `migrations` — `0001_init`（collections / documents / collection_documents）+ `cmd/migrate`
- [x] `internal/knowledge` — 领域模型、repository（keyset 分页 / PATCH / 软删）、service 校验与错误翻译
- [x] `internal/transport/rest/collection.go` — collections 五个 HTTP 端点 + cursor 编解码
- [x] `internal/transport/rest/respond` — 统一 JSON 响应与错误体（5xx 不泄露内部信息）
- [x] `internal/transport/rest/middleware` — RequestID / InFlight / AccessLog / Recover
- [x] `internal/transport/rest` — 路由装配、`/healthz`、统一 404、优雅退出
- [x] `cmd/apid` — 配置 → 日志 → DB → repository → service → 信号 → 优雅退出
- [x] `api/openapi.yaml` — collections API 契约
- [x] 本地 MySQL 8 编排（`deploy/docker-compose.yml` + 真实健康检查）
- [x] 单元测试 + repository 集成测试 + 竞态检测门禁
- [ ] 文档管理（documents / collection_documents 的 service + handler）
- [ ] 检索 / Agent 接口

## 协作与规范

- 提交信息：`<type>(<scope>): <中文说明>`
- 提交前必须 `make check` 全绿；实现细节与协作规则见 [`AGENTS.md`](AGENTS.md)
- 有取舍的设计写进 `docs/adr/NNN-标题.md`（背景 / 选项 / 决定 / 代价）
