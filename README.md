# KnowFlow

AI Native 企业知识库与 Agent 平台（Go 服务端）。

> **当前状态：M4.1 正文入库、M4.2 MySQL FULLTEXT 检索 MVP、M4.3 单轮 RAG Agent、M4.4a/b 向量写入、M4.4c Vector/Hybrid 检索、M4.4d Qdrant 清理已实现。**
> 配置、日志、HTTP 中间件、`/healthz`、优雅退出，知识库与文档的创建/查询/分页/更新/软删/恢复、文档关联、正文整体替换、chunk 级检索、单轮 RAG 问答与引用，以及 `cmd/purge` 分批物理清理**已实现并有真实验收**（见「快速开始」「知识库 CRUD」「文档与关联 CRUD」「检索」「Agent」「M4.4 本地向量基础设施」「软删恢复与物理清理」）。
> **Agent 默认仍用 MySQL FTS**；可通过 `KF_RETRIEVAL_MODE=vector|hybrid` 切到 Qdrant / Hybrid。
> 默认保持 `fts`，避免未完成生产评测前改变线上行为。

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

### 软删恢复与物理清理（restore / purge）

`DELETE /api/v1/...` 只做软删；`POST /api/v1/.../{id}/restore` 恢复软删资源；
`cmd/purge` 负责在保留期（默认 30 天）后物理删除。restore 和 purge 对同一父行
`SELECT ... FOR UPDATE`，谁先拿到锁谁赢：restore 先锁则 purge 跳过，purge 先锁则 restore 404。
它**不在请求路径上运行**，建议由 cron / Kubernetes CronJob 在低峰期调用：

```bash
set -a; . deploy/.env; set +a
export KF_DATABASE_HOST=127.0.0.1 KF_DATABASE_PORT="${MYSQL_PORT:-3306}" \
       KF_DATABASE_NAME="$MYSQL_DATABASE" KF_DATABASE_USER="$MYSQL_USER" \
       KF_DATABASE_PASSWORD="$MYSQL_PASSWORD"

# 恢复已软删资源（200；本来没删 409；已被物理删除 404）
curl -sS -X POST http://127.0.0.1:18080/api/v1/documents/<did>/restore
curl -sS -X POST http://127.0.0.1:18080/api/v1/collections/<cid>/restore

# 先看会删多少行，不执行 DELETE
go run ./cmd/purge -dry-run

# 再真正清理；batch / interval 可按数据库规格调整
go run ./cmd/purge -batch 1000 -interval 1s
```

| flag | 默认值 | 说明 |
|---|---|---|
| `-retention` | `720h` | 软删后保留多久才物理删除（30 天） |
| `-batch` | `1000` | 每批处理的父行数；建议 500–2000 |
| `-interval` | `1s` | 批间 sleep；`0` 仅建议测试 |
| `-dry-run` | `false` | 只统计，不删除 |
| `-timeout` | `30m` | 单次运行上限，防止异常数据让任务无限跑 |

清理顺序：逐个父行开事务 → 锁父行并重检 `deleted_at` → 删关联 → 删父行 → 提交。
重复执行是幂等的；0002 迁移为它补了
`(deleted_at, id)` 索引，取批按 `ORDER BY deleted_at, id`；正常数据量下走索引、避免 filesort。

真实 MySQL 8.4 验收（插入 2 个过期父资源 + 2 个孤儿行，batch=1）：

```text
go run ./cmd/purge -dry-run -batch 1 -interval 0
{"time":"2026-10-21T05:48:27.441106578+08:00","level":"INFO","msg":"purge dry-run 完成","associations":2,"documents":2,"collections":2}

go run ./cmd/purge -batch 1 -interval 0
{"time":"2026-10-21T05:48:27.516299181+08:00","level":"INFO","msg":"purge 完成","associations":2,"documents":2,"collections":2,"batches":2}
{"time":"2026-10-21T05:48:27.516325381+08:00","level":"INFO","msg":"purge 统计","associations":2,"documents":2,"collections":2,"batches":2,"dry_run":false,"finished_at":"2026-10-20T21:48:27.516323787Z"}

验证：过期/孤儿行 count 全为 0；活跃 collection/document 仍为 1；关联已清空。
```

restore 真实验收（软删文档 → restore → 再 restore）：

```text
DELETE /api/v1/documents/{id}          → 204
POST   /api/v1/documents/{id}/restore  → 200（返回恢复后的 Document）
POST   同上（重复）                     → 409 {"error":{"code":"conflict","message":"文档未被软删"}}
GET    /api/v1/collections/{cid}/documents → 200，关联仍然存在
```

## 配置

加载优先级（高者覆盖低者）：**内置默认值 < `configs/config.yaml` < 环境变量（`KF_` 前缀）**。

| 环境变量 | 作用 |
|---|---|
| `KF_CONFIG_FILE` | 指定配置文件路径（默认 `configs/config.yaml`） |
| `KF_APP_ENV` | 运行环境：`dev` / `test` / `prod` |
| `KF_AUTH_API_KEY` | API 鉴权 key；prod 必填，dev/test 留空表示关闭 |
| `KF_SERVER_ADDR` | 监听地址，如 `:8080` |
| `KF_SERVER_SHUTDOWN_TIMEOUT` | 优雅退出等待在途请求的上限，如 `15s` |
| `KF_LOG_LEVEL` | `debug` / `info` / `warn` / `error` |
| `KF_LOG_FORMAT` | `json`（线上，便于按字段检索）/ `text`（本地肉眼读） |
| `KF_LLM_BASE_URL` | OpenAI-compatible base URL，默认 `https://api.deepseek.com` |
| `KF_LLM_MODEL` | 模型名，默认 `deepseek-chat` |
| `KF_LLM_API_KEY` | DeepSeek API key；只从环境变量读，不写进仓库 |
| `KF_LLM_TIMEOUT` | LLM 请求超时，默认 `60s` |
| `KF_LLM_MAX_TOKENS` | 生成 token 上限，默认 `1024` |
| `KF_EMBEDDING_OLLAMA_URL` | 本地 Ollama embedding 地址，默认 `http://127.0.0.1:11434` |
| `KF_EMBEDDING_MODEL` | embedding 模型，默认 `bge-m3` |
| `KF_EMBEDDING_TIMEOUT` | embedding 请求超时，默认 `120s` |
| `KF_EMBEDDING_BATCH_SIZE` | 每批 embedding 的 chunk 数，默认 `16` |
| `KF_QDRANT_URL` | Qdrant HTTP 地址，默认 `http://127.0.0.1:6333` |
| `KF_QDRANT_COLLECTION` | collection 名，默认 `document_chunks` |
| `KF_QDRANT_VECTOR_SIZE` | 向量维度，默认 `1024` |
| `KF_QDRANT_TIMEOUT` | Qdrant 请求超时，默认 `10s` |
| `KF_REDIS_ADDR` | Redis 地址；空 = 关闭搜索缓存，默认空 |
| `KF_REDIS_PASSWORD` | Redis 密码；只从环境变量读 |
| `KF_REDIS_SENTINEL_ADDRS` | Sentinel 地址，逗号分隔；与 `KF_REDIS_ADDR` 二选一 |
| `KF_REDIS_MASTER_NAME` | Sentinel master 名，例如 `knowflow-master` |
| `KF_REDIS_DB` | Redis DB，默认 `0` |
| `KF_REDIS_TIMEOUT` | Redis 连接/读写超时，默认 `2s` |
| `KF_REDIS_SEARCH_TTL` | 搜索结果缓存 TTL，默认 `30s` |
| `KF_KAFKA_BROKERS` | Kafka brokers，逗号分隔；空 = 关闭事件通道 |
| `KF_KAFKA_TOPIC` | 事件 topic，默认 `knowflow.document.events` |
| `KF_KAFKA_GROUP_ID` | 消费组，默认 `knowflow-embed` |
| `KF_KAFKA_TIMEOUT` | Kafka 请求超时，默认 `10s` |
| `KF_KAFKA_BATCH_SIZE` | dispatcher 每批事件数，默认 `20` |
| `KF_KAFKA_LEASE` | publishing lease，默认 `30s` |
| `KF_LLM_FALLBACK_BASE_URL` | 可选备用 LLM base URL；空 = 不降级 |
| `KF_LLM_FALLBACK_MODEL` | 备用模型名 |
| `KF_LLM_FALLBACK_API_KEY` | 备用 API key；只从环境变量读 |
| `KF_LLM_REQUESTS_PER_MINUTE` | 进程内请求配额；0 = 不限 |
| `KF_LLM_TOKENS_PER_DAY` | 进程内 token 日配额；0 = 不限 |
| `KF_LLM_SEMANTIC_CACHE` | 是否启用进程内语义缓存 |
| `KF_LLM_SEMANTIC_THRESHOLD` | 余弦阈值，默认 `0.92` |
| `KF_LLM_SEMANTIC_MAX_ENTRIES` | 最大条目，默认 `1000` |
| `KF_LLM_SEMANTIC_TTL` | 缓存 TTL，默认 `1h` |
| `KF_RETRIEVAL_MODE` | `fts`（默认）/ `vector` / `hybrid` |
| `KF_RETRIEVAL_CANDIDATES` | Hybrid 每路候选数，默认 `20` |
| `KF_RETRIEVAL_RRF_K` | RRF 常数，默认 `60` |

- 配置文件**必须存在**：找不到文件会在启动时报错退出（fail fast），不会静默改用默认值。
- 本地要改配置用 `KF_CONFIG_FILE=configs/config.local.yaml`（已 gitignore），**不要**改提交进仓库的那份。
- 日志一律写 **stdout**，由容器运行时 / 日志 agent 收集，应用自身不写文件、不做轮转。
- 密钥只从环境变量读，**不入库**。
- `prod` 环境必须设置 `KF_AUTH_API_KEY`，否则配置校验会拒绝启动。

## 接口

| 方法 | 路径 | 说明 | 状态 |
|---|---|---|---|
| GET | `/healthz` | 存活探针：版本号 / 启动时间 / 在途请求数 | ✅ |
| GET | `/readyz` | 就绪探针：检查 MySQL Ping；失败返回 503 | ✅ |
| GET | `/metrics` | Prometheus text 指标（需 API key） | ✅ |
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
| POST | `/api/v1/documents/{id}/restore` | 恢复软删文档；本来没删 → 409 | ✅ |
| POST | `/api/v1/collections/{id}/restore` | 恢复软删知识库；本来没删 → 409 | ✅ |
| PUT | `/api/v1/documents/{id}/content` | 整体替换正文（content + SHA-256 + chunks 原子替换） | ✅ |
| POST | `/api/v1/documents/{id}/file` | 上传 PDF，抽取文本后替换正文（multipart `file`） | ✅ |
| GET | `/api/v1/search` | ngram FULLTEXT 检索 chunk；`q` 2..256 rune，`offset+limit<=1000` | ✅ |
| POST | `/api/v1/agent/chat` | 单轮 RAG 问答；答案 + citations | ✅ |
| POST | `/api/v1/agent/chat/stream` | 单轮 RAG SSE 流式回答；`token` / `done` / `error` | ✅ |
| POST | `/api/v1/collections/{id}/documents` | 添加文档关联；重复添加 → 409 | ✅ |
| GET | `/api/v1/collections/{id}/documents` | 按 `added_at` 分页列出文档 | ✅ |
| DELETE | `/api/v1/collections/{id}/documents/{document_id}` | 只移除关联，不删文档 | ✅ |

鉴权：配置 `KF_AUTH_API_KEY` 后，除 `/healthz`、`/readyz` 外的接口都需要：

```bash
curl -H "Authorization: Bearer $KF_AUTH_API_KEY" ...
# 或
curl -H "X-API-Key: $KF_AUTH_API_KEY" ...
```

完整契约见 [`api/openapi.yaml`](api/openapi.yaml)；collections 的路径、游标和 PATCH 语义见
[ADR 003](docs/adr/003-collections-api.md)，documents 的关联语义与软删保留策略见
[ADR 004](docs/adr/004-documents-api.md)，检索端点与 ngram 分页策略见
[ADR 005](docs/adr/005-search-api.md)，Agent/RAG 契约见
[ADR 006](docs/adr/006-agent-rag.md)，生产化取舍见
[ADR 008](docs/adr/008-production-baseline.md)。

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

### 检索（MySQL FULLTEXT MVP）

```text
GET /api/v1/search?q=知识库&collection_id={cid}&limit=1&offset=0
200 {"items":[{"chunk_id":"...","document_id":"...","document_title":"搜索文档二","chunk_index":0,"content":"另一个知识库文档，介绍检索","score":0.7249}],"next_offset":1}

GET /api/v1/search?q=知识库&collection_id={cid}&limit=1&offset=1
200 {"items":[...],"next_offset":null}
```

- `q` trim 后 2..256 个 rune；1 个字符 → 400。
- `limit` 1..50，默认 20；`offset + limit <= 1000`。
- `collection_id` 可选；不存在/已软删 → 404。
- `next_offset` 为 null 表示没有下一页，或已到 1000 窗口上限。
- 查询走 `document_chunks` 的 `ft_chunk_content` ngram FULLTEXT 索引，
  按 `(score DESC, chunk_id DESC)` 排序；真实 `EXPLAIN` 为 `type=fulltext`，
  `ORDER BY score` 会产生 filesort。
- 已知限制：MySQL ngram 是字符级关键词检索，不做语义/纠错；默认英文 stopword
  可能让部分英文短词（如含 `a` 的 `AB`）查不到；详见
  [ADR 005](docs/adr/005-search-api.md)。

### Agent（单轮 RAG）

```text
POST /api/v1/agent/chat
{"collection_id":"{cid}","message":"知识库怎么检索"}

200 {
  "answer":"……[1]",
  "citations":[
    {"index":1,"chunk_id":"...","document_id":"...","document_title":"Agent 测试文档",
     "chunk_index":0,"content":"企业知识库支持全文检索","score":2.88}
  ]
}
```

- 单轮：不保存会话，不接收 `conversation_id`。
- `message` trim 后 2..4000 rune；`collection_id` 可选，不存在/已软删 → 404。
- 先检索 topK=5 个 chunk；0 条结果直接返回固定 answer，不调用 LLM。
- `citations` 的 `index` 从 1 开始，对应 prompt 里的 `[1]`；MVP 不校验模型是否真的写了正确的 `[n]`。
- 使用 DeepSeek 的 OpenAI-compatible `/chat/completions`，用标准库 `net/http`，不引 SDK。
- `POST /api/v1/agent/chat/stream` 返回 SSE：`token` 增量、`done` citations、`error` 错误事件。
- 没配 `KF_LLM_API_KEY` 时服务仍可启动，但 `/agent/chat` 返回 500；CRUD/search 不受影响。
- 无 key 的自动化 E2E 使用 fake OpenAI-compatible provider；本地真实 DeepSeek 已验收（约 0.7s）。
- Agent 路由单独把写截止时间延长到 `llm.timeout + 30s`，其他接口仍用 `server.write_timeout`；
  已用 12s 慢 provider 验证（全局 write_timeout=10s，响应仍 200）。

## M4.4 本地向量基础设施与 embedding worker（M4.4a/b）

> 这一节是**基础设施与评测**，不是已经接入 Agent 的向量检索。`/agent/chat` 仍走 FTS。

启动本地 Ollama + Qdrant：

```bash
make ai-up                  # qdrant :6333 / ollama :11434
make ai-model               # 拉取 bge-m3（约 1.2 GB）
```

如果 Docker Hub 拉不动，可以在 `deploy/.env` 里覆盖镜像（本地文件已 gitignore）：

```bash
QDRANT_IMAGE=docker.m.daocloud.io/qdrant/qdrant:v1.12.4
OLLAMA_IMAGE=docker.m.daocloud.io/ollama/ollama:0.5.7
```

验证：

```bash
curl -fsS http://127.0.0.1:6333/healthz
# healthz check passed
# Qdrant 1.12.4；create / upsert / search smoke test 通过

curl -fsS http://127.0.0.1:11434/api/embed \
  -H 'Content-Type: application/json' \
  -d '{"model":"bge-m3","input":["KnowFlow 使用 MySQL FULLTEXT。"]}'
# count=1 dim=1024
```

真实 BGE-M3 向量评测（不写 Qdrant、不连 MySQL）：

```bash
make embed-eval
```

固定评测集：`internal/eval/testdata/search_eval.json`，20 篇文档 / 30 条 query。

当前结果：

| 检索方式 | Recall@5 | MRR |
|---|---:|---:|
| MySQL FULLTEXT baseline | 0.850 | 0.818 |
| BGE-M3 pure vector（内存 cosine） | 1.000 | 0.950 |

注意：

- 这是**合成评测集**，数字不代表生产效果；它的价值是同一套 query 下对比 FTS / vector / hybrid。
- 纯向量评测用内存 cosine，不代表 Qdrant 的最终检索链路。
- 本机无 GPU，BGE-M3 只用于本地开发/评测；生产建议 GPU 或 OpenAI-compatible embedding API。
- 设计决策见 [ADR 007](docs/adr/007-vector-rag.md)。

### Workflow（YAML DAG）

`cmd/workflow` 执行 YAML DAG，节点支持 `depends_on` / `retries` / `timeout` / `when`，
运行记录落 `workflow_runs` / `workflow_node_runs`，同一 `run_id` 重跑会跳过已成功节点。

示例：`configs/workflows/pdf-ingest.yaml`（PDF → 抽取 → PUT 正文）：

```bash
go run ./cmd/workflow -file configs/workflows/pdf-ingest.yaml \
  -api-url http://127.0.0.1:8080 -api-key "$KF_AUTH_API_KEY" \
  -var pdf_path=/tmp/a.pdf -var document_id=<uuid>
```

详见 [ADR 012](docs/adr/012-workflow-engine.md)。

### M4.4b 异步 embedding worker

`PUT /documents/{id}/content` 在同一个 MySQL 事务里写 `embedding_jobs`（outbox）；
`cmd/embed` 异步消费：

```text
PUT /content
  → document_contents / document_chunks / embedding_jobs 同一事务
  → cmd/embed 抢占 job
  → BGE-M3 批量 embedding
  → 删除该 document 的旧 Qdrant 向量
  → upsert 新向量
  → job done
```

本地跑一次：

```bash
make ai-up
make ai-model

# 先通过 API PUT 一篇正文，然后：
make embed-once
# 或常驻：
go run ./cmd/embed -interval 5s
```

已验证：

```text
PUT /content 后 job = pending
cmd/embed -once 后 job = done，attempts=1
Qdrant 中该 document 的 points = 1
再次 PUT /content + worker 后 points 仍为 1（旧 chunk 向量被删除）
```

worker 细节：

- 任务按 `document_id` 重建，因为正文替换会换掉所有 chunk UUID。
- `content_hash` 做乐观校验：旧 worker 不能把新任务误标成 done。
- `FOR UPDATE SKIP LOCKED` 支持多 worker；lease 过期可回收。
- embedding 失败会按指数退避重试，超过 `max_attempts` 进 `dead`。
- `cmd/purge` 会同时删除 `embedding_jobs`，避免孤儿任务。
- 迁移：`0004_embedding_jobs` / `0005_vector_cleanup_jobs`；当前 DB version=5。

### M4.4c Vector / Hybrid 检索

`Retriever` 现在有三种实现：

```text
fts    → KeywordRetriever（MySQL FULLTEXT）
vector → VectorRetriever（BGE-M3 + Qdrant + MySQL 回表）
hybrid → HybridRetriever（FTS + vector，RRF 融合）
```

切换：

```bash
KF_RETRIEVAL_MODE=hybrid make run
```

`VectorRetriever`：

```text
query → BGE-M3 embedding → Qdrant topK×4
      → MySQL 回表（过滤软删/collection）
      → topK
```

`HybridRetriever`：

```text
FTS top 20 + vector top 20
  → RRF(1/(60+rank))
  → 去重 / 排序
  → top 5
```

单路失败会降级到另一路；两路都失败才返回错误。

真实 HTTP E2E（FTS 完全没命中，Hybrid 命中）：

```text
FTS hits 0 []

ANSWER 答案 [1]
CITATIONS 1
CITATION 1 线上故障响应
线上故障按照 P0 到 P3 分级，P0 故障要求十五分钟内拉起应急会议并通知值班负责人。
```

这说明语义 query `服务挂了先找谁` 在 FTS 中无结果，但 vector 路补回了正确 chunk。

### M4.4d Qdrant 向量清理

`cmd/purge` 不在 MySQL 事务里直接调 Qdrant，而是写清理 outbox：

```text
purge 事务：
  删除 collection_documents / embedding_jobs / 正文 / chunks
  → INSERT vector_cleanup_jobs(document_id)
  → 删除 documents

cmd/vector-clean：
  → 抢占 cleanup job
  → Qdrant DeleteByDocument
  → done；失败按退避重试
```

使用：

```bash
# 先执行 purge（写入清理任务）
go run ./cmd/purge -batch 1000 -interval 1s

# 再消费清理任务（可常驻）
make vector-clean-once
# 或
go run ./cmd/vector-clean -interval 30s
```

真实 E2E：

```text
purge 后 vector_cleanup_jobs = pending, attempts=0
Qdrant points = 1
cmd/vector-clean -once 后 cleanup job = done, attempts=1
Qdrant points = 0
```

purge 日志：

```text
purge 完成 associations=1 contents=1 chunks=1 jobs=1 documents=1
```

清理失败不会回滚已完成的 DB 删除；任务保留在 `vector_cleanup_jobs`，可重试/对账。

## 生产部署

生产部署、鉴权、TLS、迁移、worker、备份、扩缩容和故障处理见：

- [`docs/deployment.md`](docs/deployment.md)
- [`docs/operations.md`](docs/operations.md)
- [`docs/mcp.md`](docs/mcp.md)：MCP Server 接入 Cursor / Claude Desktop
- [`docs/architecture.md`](docs/architecture.md)：架构与 20 个追问
- [`docs/day30-review.md`](docs/day30-review.md)：Day 30 收官复盘
- [`docs/load-test.md`](docs/load-test.md)：压测报告
- [`docs/postmortem.md`](docs/postmortem.md)：故障复盘
- [`docs/rag-eval.md`](docs/rag-eval.md)：RAG 评测
- [`docs/adr/009-redis-cache.md`](docs/adr/009-redis-cache.md)：Redis 搜索缓存取舍
- [`docs/adr/010-kafka-outbox.md`](docs/adr/010-kafka-outbox.md)：Kafka 事件 outbox 取舍
- [`docs/adr/011-pdf-extraction.md`](docs/adr/011-pdf-extraction.md)：PDF 文本抽取取舍
- [`docs/adr/012-workflow-engine.md`](docs/adr/012-workflow-engine.md)：Workflow 引擎取舍
- [`docs/adr/013-llm-gateway.md`](docs/adr/013-llm-gateway.md)：LLM 模型网关取舍
- [`docs/adr/014-ha-infra.md`](docs/adr/014-ha-infra.md)：Redis/Kafka HA 与演练

最短路径：

```bash
cp deploy/env.prod.example deploy/.env
chmod 600 deploy/.env
# 填 MYSQL_*、KF_AUTH_API_KEY、KF_LLM_API_KEY、KF_EMBEDDING_OLLAMA_URL

docker compose --env-file deploy/.env \
  -f deploy/docker-compose.prod.yml up -d --build

curl -fsS http://127.0.0.1:8080/readyz
curl -H "Authorization: Bearer $KF_AUTH_API_KEY" \
  http://127.0.0.1:8080/api/v1/collections
```

生产镜像：

```bash
make docker-build
# 或
docker build -t knowflow:prod .
```

安全默认值：

- 运行时非 root；
- MySQL/Qdrant 不暴露宿主机端口；
- `prod` 必须设置 `KF_AUTH_API_KEY`；
- `/healthz`、`/readyz` 不鉴权，其余 API 需要 API key；
- 安全响应头默认开启；
- 镜像不包含 `.env` / 源码。

CI：`.github/workflows/ci.yml`

- gofmt / vet / golangci-lint
- 单元测试 `-race`
- MySQL + Qdrant 集成测试
- 全链路 E2E（真实 HTTP + MySQL + Qdrant，fake embedding/LLM）
- 内部包覆盖率门槛 85%
- Docker 镜像构建

覆盖率：

```bash
make coverage
```

## 目录结构

```
cmd/         可执行程序入口（apid / migrate / purge / embed / embed-eval / vector-clean / mcp-server，只做装配）
internal/    私有代码，外部模块不可导入
  config/        配置加载与校验
  apperr/        业务错误与错误码
  database/      连接池、DSN、迁移连接
  knowledge/     知识库领域模型、repository、service
  agent/         单轮 RAG + Keyword/Vector/Hybrid Retriever
  embedding/     文本向量化接口 + 本地 Ollama 实现
  indexer/       embedding_jobs 抢占 + worker + Qdrant 写入
  qdrant/        Qdrant REST 客户端
  eval/          固定检索评测集与指标
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
make help              # 查看全部工程命令
make check             # 提交前一键检查：gofmt + go vet + golangci-lint + go test -race
make test-integration  # 集成测试（需要 MySQL/Qdrant）
make e2e               # 全链路 E2E（真实 MySQL/Qdrant）
make coverage          # 内部包覆盖率（门槛 85%）
make docker-build      # 生产镜像
make build             # 编译到 bin/apid（版本号由 git + ldflags 注入）
make run               # 本地运行（go run，版本号为 dev；需要先启动 MySQL 并配好 KF_DATABASE_*）
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
- [x] 软删恢复（documents / collections 的 `/restore`，restore 与 purge 行锁串行化）
- [x] 软删物理清理（`cmd/purge`，默认保留 30 天，按父行事务，见 ADR 004）
- [x] M4.1 正文入库（0003 / chunker / repository / service / HTTP handler / 2 MiB 请求体上限；purge 同步清理正文与 chunks）
- [x] M4.2 MySQL FULLTEXT 检索 MVP（chunk 级；service/handler/集成测试/真实 curl + EXPLAIN）
- [x] M4.3 单轮 RAG Agent（DeepSeek OpenAI-compatible；citations；真实 E2E）
- [x] M4.4a 本地 Ollama + Qdrant + BGE-M3（1024 维）+ 固定评测集 + FTS/vector baseline
- [x] M4.4b `0004_embedding_jobs` + 正文 outbox + `cmd/embed` worker + Qdrant 写入/删除
- [x] M4.4c VectorRetriever / HybridRetriever / `KF_RETRIEVAL_MODE` / Agent 接入
- [x] M4.4d `0005_vector_cleanup_jobs` + `cmd/vector-clean`（purge 后清理 Qdrant）
- [ ] M4.5 生产 embedding provider / rerank / 全量 Hybrid 评测 / 灰度与运维文档

## 协作与规范

- 提交信息：`<type>(<scope>): <中文说明>`
- 提交前必须 `make check` 全绿；实现细节与协作规则见 [`AGENTS.md`](AGENTS.md)
- 有取舍的设计写进 `docs/adr/NNN-标题.md`（背景 / 选项 / 决定 / 代价）
