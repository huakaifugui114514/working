# KnowFlow

AI Native 企业知识库与 Agent 平台（Go 服务端）。

> **当前状态：M3 documents CRUD + 软删清理已实现，collections + documents 可运行。**
> 配置、日志、HTTP 中间件、`/healthz`、优雅退出，知识库与文档的创建/查询/分页/更新/软删、文档关联，以及 `cmd/purge` 分批物理清理**已实现并有真实验收**（见「快速开始」「知识库 CRUD」「文档与关联 CRUD」「软删物理清理」）。
> 检索、Agent 尚未实现——请不要按本文部署到生产。

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

## 本地数据库

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

### 软删物理清理（purge）

`DELETE /api/v1/...` 只做软删；`cmd/purge` 负责在保留期（默认 30 天）后物理删除。
它**不在请求路径上运行**，建议由 cron / Kubernetes CronJob 在低峰期调用：

```bash
set -a; . deploy/.env; set +a
export KF_DATABASE_HOST=127.0.0.1 KF_DATABASE_PORT="${MYSQL_PORT:-3306}" \
       KF_DATABASE_NAME="$MYSQL_DATABASE" KF_DATABASE_USER="$MYSQL_USER" \
       KF_DATABASE_PASSWORD="$MYSQL_PASSWORD"

# 先看会删多少行，不执行 DELETE
go run ./cmd/purge -dry-run

# 再真正清理；batch / interval 可按数据库规格调整
go run ./cmd/purge -batch 1000 -interval 1s
```

| flag | 默认值 | 说明 |
|---|---|---|
| `-retention` | `720h` | 软删后保留多久才物理删除（30 天） |
| `-batch` | `1000` | 每批处理的关联/父行数；建议 500–2000 |
| `-interval` | `1s` | 批间 sleep；`0` 仅建议测试 |
| `-dry-run` | `false` | 只统计，不删除 |
| `-timeout` | `30m` | 单次运行上限，防止异常数据让任务无限跑 |

清理顺序：过期 document 的关联 → 过期 collection 的关联 → 已无关联的过期 document →
已无关联的过期 collection。重复执行是幂等的；0002 迁移为它补了
`(deleted_at, id)` 索引，取批按 `ORDER BY deleted_at, id`；正常数据量下走索引、避免 filesort。

真实 MySQL 8.4 验收（插入 2 个过期父资源 + 2 个孤儿行，batch=1）：

```text
go run ./cmd/purge -dry-run -batch 1 -interval 0
{"time":"2026-10-20T13:45:45.060557994+08:00","level":"INFO","msg":"purge dry-run 完成","associations":2,"documents":2,"collections":2}

go run ./cmd/purge -batch 1 -interval 0
{"time":"2026-10-20T13:45:45.124931459+08:00","level":"INFO","msg":"purge 完成","associations":2,"documents":2,"collections":2,"batches":3}
{"time":"2026-10-20T13:45:45.124940146+08:00","level":"INFO","msg":"purge 统计","associations":2,"documents":2,"collections":2,"batches":3,"dry_run":false,"finished_at":"2026-10-20T05:45:45.124938382Z"}

验证：过期/孤儿行 count 全为 0；活跃 collection/document 仍为 1；关联已清空。
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
| POST | `/api/v1/documents` | 创建文档元数据 | ✅ |
| GET | `/api/v1/documents/{id}` | 按 id 查询文档 | ✅ |
| PATCH | `/api/v1/documents/{id}` | PATCH 更新（可空字段支持“缺省 / null / 值”三态） | ✅ |
| DELETE | `/api/v1/documents/{id}` | 软删文档（关联行保留） | ✅ |
| POST | `/api/v1/collections/{id}/documents` | 添加文档关联；重复添加 → 409 | ✅ |
| GET | `/api/v1/collections/{id}/documents` | 按 `added_at` 分页列出文档 | ✅ |
| DELETE | `/api/v1/collections/{id}/documents/{document_id}` | 只移除关联，不删文档 | ✅ |

完整契约见 [`api/openapi.yaml`](api/openapi.yaml)；collections 的路径、游标和 PATCH 语义见
[ADR 003](docs/adr/003-collections-api.md)，documents 的关联语义与软删保留策略见
[ADR 004](docs/adr/004-documents-api.md)。

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

### 文档与关联 CRUD

```bash
DOC=http://127.0.0.1:18080/api/v1/documents
COL=http://127.0.0.1:18080/api/v1/collections

# 创建文档：title 会 trim；source_uri/mime_type 传 null、空串或纯空白都存 NULL
curl -sS -X POST "$DOC" -H 'Content-Type: application/json' \
  -d '{"title":"  设计文档  ","source_uri":"s3://knowflow/docs/design.pdf","mime_type":"application/pdf","size_bytes":1024}'

# PATCH：title:null/缺省 = 不改；source_uri:null、""、纯空白 = 清空为 NULL
curl -sS -X PATCH "$DOC/<did>" -H 'Content-Type: application/json' \
  -d '{"title":"新标题"}'
curl -sS -X PATCH "$DOC/<did>" -H 'Content-Type: application/json' \
  -d '{"source_uri":null,"size_bytes":null}'

# 关联：201 + Location；重复添加返回 409
curl -sS -i -X POST "$COL/<cid>/documents" -H 'Content-Type: application/json' \
  -d '{"document_id":"<did>"}'

# 列表：items[].document + added_at；next_cursor=null 表示最后一页
curl -sS "$COL/<cid>/documents?limit=20"

# 只移除关联，文档本身还在；重复移除返回 404。
# 父资源（collection/document）已软删不阻塞移除：关联行还在就返回 204，便于清理。
curl -sS -i -X DELETE "$COL/<cid>/documents/<did>"

# 软删文档：204；之后 GET 返回 404，所有 collection 列表都不再显示
curl -sS -i -X DELETE "$DOC/<did>"
```

本地 MySQL 8.4 + `bin/apid` 的真实验收摘录（2026-10-20，完整请求体见上面的 curl）：

```text
POST /api/v1/documents
201 {"id":"01a157ad-c921-76d4-9c84-bdd43dcec532","title":"设计文档","source_uri":"s3://knowflow/docs/design.pdf","mime_type":"application/pdf","size_bytes":1024,"created_at":"2026-10-20T04:59:29.441Z","updated_at":"2026-10-20T04:59:29.441Z"}

PATCH /api/v1/documents/{id} {"title":"新标题"}
200 {"id":"01a157ad-c921-76d4-9c84-bdd43dcec532","title":"新标题","source_uri":"s3://knowflow/docs/design.pdf","mime_type":"application/pdf","size_bytes":1024,...}

PATCH /api/v1/documents/{id} {"source_uri":null,"size_bytes":null}
200 {"id":"01a157ad-c921-76d4-9c84-bdd43dcec532","title":"新标题","source_uri":null,"mime_type":"application/pdf","size_bytes":null,...}

POST /api/v1/collections/{id}/documents
201 {"document":{"id":"01a157ad-c921-76d4-9c84-bdd43dcec532",...},"added_at":"2026-10-20T04:59:29.506Z"}

POST /api/v1/collections/{id}/documents（重复）
409 {"error":{"code":"conflict","message":"文档已在该知识库中"}}

GET /api/v1/collections/{id}/documents?limit=20
200 {"items":[{"document":{"id":"01a157ad-c921-76d4-9c84-bdd43dcec532",...},"added_at":"..."}],"next_cursor":null}

DELETE /api/v1/collections/{id}/documents/{did}   → 204
DELETE 同上（重复）                              → 404 {"error":{"code":"not_found","message":"关联不存在"}}
DELETE /api/v1/documents/{id}                    → 204
GET    /api/v1/documents/{id}（软删后）           → 404 {"error":{"code":"not_found","message":"文档不存在"}}
```

`GET /collections/{id}/documents` 的 SQL 已用真实 MySQL `EXPLAIN` 验证：
驱动表走 `idx_collection_created`（`Using index` + `Backward index scan`，无 filesort），
`documents` 走主键 `eq_ref`，符合 keyset 分页的索引设计。

## 目录结构

```
cmd/         可执行程序入口（apid / migrate / purge，只做装配）
internal/    私有代码，外部模块不可导入
  config/        配置加载与校验
  apperr/        业务错误与错误码
  database/      连接池、DSN、迁移连接
  knowledge/     知识库领域模型、repository、service
  purge/         软删数据的分批物理清理
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
ok  internal/purge
ok  internal/transport/rest
ok  internal/transport/rest/middleware
ok  internal/transport/rest/respond
ok  migrations
```

repository / purge 的集成测试单独跑（需要本地 MySQL；临时表不会碰真实数据）：

```bash
set -a; . deploy/.env; set +a
KF_DATABASE_HOST=127.0.0.1 KF_DATABASE_PORT="${MYSQL_PORT:-3306}" \
KF_DATABASE_NAME="$MYSQL_DATABASE" KF_DATABASE_USER="$MYSQL_USER" \
KF_DATABASE_PASSWORD="$MYSQL_PASSWORD" \
go test -race -tags=integration -count=1 ./internal/knowledge ./internal/purge
```

## 实现状态

- [x] `internal/config` — 三层配置加载（默认值 / YAML / 环境变量）+ `errors.Join` 全量校验
- [x] `internal/apperr` — 业务错误 `Error{Code, Message, Cause}` + `errors.Is/As` 穿透
- [x] `internal/observability` — `slog` logger（json/text）+ `trace_id` 自动注入
- [x] `internal/database` — DSN / 迁移 DSN / 连接池 / 集成测试
- [x] `migrations` — `0001_init`（collections / documents / collection_documents）+ `0002_purge_indexes` + `cmd/migrate`
- [x] `internal/knowledge` — 领域模型、collections/documents repository（keyset 分页 / PATCH / 软删 / 关联）、service 校验与错误翻译
- [x] `internal/transport/rest/collection.go` — collections 五个 HTTP 端点 + cursor 编解码
- [x] `internal/transport/rest/document.go` — documents CRUD + 关联的添加/列表/移除 + PATCH 三态解码
- [x] `internal/transport/rest/respond` — 统一 JSON 响应与错误体（5xx 不泄露内部信息）
- [x] `internal/transport/rest/middleware` — RequestID / InFlight / AccessLog / Recover
- [x] `internal/transport/rest` — 路由装配、`/healthz`、统一 404、优雅退出
- [x] `cmd/apid` — 配置 → 日志 → DB → repository → service → 信号 → 优雅退出
- [x] `internal/purge` + `cmd/purge` — 分批物理清理、dry-run、批间 sleep、0002 索引
- [x] `api/openapi.yaml` — collections + documents API 契约
- [x] 本地 MySQL 8 编排（`deploy/docker-compose.yml` + 真实健康检查）
- [x] 单元测试 + repository 集成测试 + 竞态检测门禁
- [x] 文档管理（documents / collection_documents 的 repository + service + handler）
- [x] 软删物理清理（`cmd/purge`，默认保留 30 天，见 ADR 004）
- [ ] 检索 / Agent 接口

## 协作与规范

- 提交信息：`<type>(<scope>): <中文说明>`
- 提交前必须 `make check` 全绿；实现细节与协作规则见 [`AGENTS.md`](AGENTS.md)
- 有取舍的设计写进 `docs/adr/NNN-标题.md`（背景 / 选项 / 决定 / 代价）
