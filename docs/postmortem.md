# 故障复盘 / 演练

> 原 30 天计划的 Redis/Kafka 故障演练不适用于当前架构：KnowFlow 用 MySQL outbox +
> 独立 worker 代替 Kafka，没有引入 Redis 缓存。下面按**实际系统**做三份同等级复盘。

## 1. Qdrant collection 并发创建 409

### 现象

生产 Compose 启动后 `embed` 容器反复退出：

```text
embed 失败: 初始化 Qdrant collection 失败:
qdrant: 创建 collection status=409 body={"status":{"error":"Wrong input: Collection `document_chunks` already exists!"}}
```

### 影响面

- `/agent/chat` 仍可用（FTS fallback）；
- 新正文不会被向量化，`embedding_jobs` 积压；
- `vector-clean` 正常。

### 定位过程

1. `docker compose ps` 看到 `embed` 重启，`vector-clean` 正常；
2. 日志显示 `EnsureCollection` 先 GET 404，再 PUT 409；
3. 两个 worker 同时启动，都执行了 GET 404 → PUT，后到的拿到 409。

### 根因

`EnsureCollection` 把“已存在”当失败，没有处理并发创建的幂等性。

### 修复

- PUT 返回 409 时重新 GET，并校验 size/distance；
- 增加单测 `TestEnsureCollectionCreateConflict`；
- 生产 Compose 两个 worker 同时启动后不再崩溃。

### 预防

- 所有 `Ensure*` 都按幂等设计；
- 并发启动测试进入 E2E/Compose 验收。

## 2. LLM provider 不可用

### 现象

生产 Compose E2E 中 `/agent/chat` 返回 500，日志：

```text
code: internal_error, message: 生成回答失败,
cause: deepseek: 请求失败: Post "http://host.docker.internal:18091/chat/completions":
dial tcp ... connect: connection refused
```

CRUD/search 正常。

### 影响面

- Agent 问答不可用；
- 知识库写入、FTS/vector 检索不受影响；
- 没有自动重试/多 provider 降级（当前单 provider）。

### 定位过程

1. `/readyz` 200（只检查 MySQL，不把 LLM 当 readiness）；
2. `/api/v1/search` 200；
3. 日志直接显示 LLM 连接失败；
4. 检查 `KF_LLM_BASE_URL` 与容器网络（`host.docker.internal`/端口）。

### 根因

外部 LLM 不可达；应用没有对 LLM 做重试/熔断/多 provider fallback。

### 修复 / 缓解

- 修正 provider 地址/密钥；
- 明确 `/readyz` 不依赖 LLM：LLM 挂掉不应该让整个服务被摘流；
- 文档记录：Agent 500 时先看 LLM key/网络/额度。

### 预防（下一步）

- 多 provider 路由 + 超时/重试/熔断；
- prompt/模型版本与 token 成本观测。

## 3. 200 并发下搜索 p99 升高（连接池排队）

### 现象

200 并发压测 `/api/v1/search`：

```text
连接池 10：p99=63.47ms qps=13734
连接池 50：p99=45.70ms qps=15143
```

无错误，但 p99 随并发明显升高。

### 影响面

搜索延迟升高；Agent 总延迟进一步叠加 LLM 延迟。

### 定位过程

1. `/healthz` 同并发 p99 2.64ms，排除 HTTP 层；
2. 搜索 SQL 单条很快，但 QPS 不再线性增长；
3. `database/sql` 连接池默认 10，200 并发在等连接；
4. 调大 `max_open_conns` 后 p99/QPS 改善。

### 根因

MySQL 连接池容量小于并发需求，请求在连接池排队。

### 修复

- 压测环境把 `KF_DATABASE_MAX_OPEN_CONNS` 调到 50；
- 生产按副本数 × 连接数 ≤ MySQL `max_connections` 规划；
- 不把连接池调到无限大：会压垮 MySQL。

### 预防

- 将 p99 和连接池等待纳入监控；
- 压测报告进入发布检查清单。

## 与 30 天计划的差异

原计划要求 Redis 挂、Kafka 堆积、慢 SQL 三份复盘。当前项目：

- 没有 Redis 缓存（所以没有缓存穿透/雪崩路径）；
- 没有 Kafka，使用 MySQL `embedding_jobs` / `vector_cleanup_jobs` outbox + worker；
- 搜索使用 MySQL FULLTEXT + Qdrant，不是 ES。

这三份复盘对应真实组件：Qdrant、LLM、MySQL 连接池；没有为了“对齐计划”引入不必要的中间件。
