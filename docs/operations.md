# KnowFlow 运维手册

配套部署步骤见 [`deployment.md`](deployment.md)。

## 1. 健康检查

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

- `/healthz`：进程存活，不依赖 DB；容器 liveness 用它。
- `/readyz`：Ping MySQL；容器 readiness / 负载均衡摘流用它。
- `readyz` 失败只返回 `{"status":"unavailable"}`，具体错误在日志。

## 2. 日志

- 全部写 stdout，JSON（生产）或 text（本地）。
- 每条请求日志带 `trace_id`，响应头 `X-Request-ID` 可回传。
- 关键字段：`method`、`path`、`status`、`duration`、`remote_addr`、`trace_id`。
- 5xx 响应体不包含内部原因；原因只在日志。

检索日志示例：

```bash
docker compose -f deploy/docker-compose.prod.yml logs -f apid | jq 'select(.msg=="http 请求")'
```

worker：

```bash
docker compose -f deploy/docker-compose.prod.yml logs -f embed
docker compose -f deploy/docker-compose.prod.yml logs -f vector-clean
```

## 2.1 Prometheus 指标

```bash
curl -H "Authorization: Bearer $KF_AUTH_API_KEY" \
  http://127.0.0.1:8080/metrics
```

当前是进程级 HTTP 指标，例如：

```text
http_requests_total{method="POST",path="/api/v1/agent/chat",status="200"} 10
```

生产建议按 `path`/`status` 做 5xx 比例和 P95 延迟告警；worker 队列指标用 SQL 查询。

## 2.2 Redis 缓存

```bash
docker compose -f deploy/docker-compose.prod.yml exec redis redis-cli ping
# PONG

docker compose -f deploy/docker-compose.prod.yml exec redis redis-cli get search:version
docker compose -f deploy/docker-compose.prod.yml exec redis redis-cli --scan --pattern 'search:v*' | head
```

- `KF_REDIS_ADDR` 为空时缓存关闭。
- Redis 不可用不影响读写；搜索会退化为直查 MySQL。
- 写操作会 `INCR search:version`，旧 key 靠 TTL 自然过期。
- 不要用 `KEYS *` / `FLUSHALL` 做日常操作。

## 2.3 Kafka 事件

```bash
# 未发布事件
docker compose -f deploy/docker-compose.prod.yml exec mysql   mysql -uknowflow -p -D knowflow -e   "SELECT status, COUNT(*) FROM document_events GROUP BY status;"

# topic
docker compose -f deploy/docker-compose.prod.yml exec kafka   /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe   --topic knowflow.document.events

# 消费组 lag
docker compose -f deploy/docker-compose.prod.yml exec kafka   /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092   --describe --group knowflow-embed
```

- `publishing` 超过 lease 会被 dispatcher 回收并重复投递；消费必须幂等。
- `dead` 表示超过 max attempts；需要人工检查后重置为 `pending` 或丢弃。
- Kafka 不可用时 dispatcher 重试，`embed` 轮询 worker 仍可处理 DB job。

## 3. 常用 SQL

当前时间统一 UTC。

```sql
-- embedding 队列积压
SELECT status, COUNT(*) FROM embedding_jobs GROUP BY status;

-- 失败原因
SELECT document_id, attempts, last_error, updated_at
FROM embedding_jobs
WHERE status IN ('failed','dead')
ORDER BY updated_at DESC LIMIT 20;

-- Qdrant 清理队列
SELECT status, COUNT(*) FROM vector_cleanup_jobs GROUP BY status;

-- 软删待 purge 数
SELECT COUNT(*) FROM documents
WHERE deleted_at IS NOT NULL AND deleted_at < UTC_TIMESTAMP(3) - INTERVAL 30 DAY;

-- 正文/chunk 概况
SELECT COUNT(*) AS docs, SUM(char_count) AS chunk_chars FROM document_chunks;
```

## 4. 队列告警建议

| 指标 | 告警 |
|---|---|
| `embedding_jobs.pending` 持续增长 | worker 未运行 / embedding 服务不可用 |
| `embedding_jobs.dead` > 0 | 检查 `last_error`，修复后重置 attempts |
| `vector_cleanup_jobs.dead` > 0 | Qdrant 删除失败，手工重试 |
| `document_contents` 无对应 done job | 可能迁移/入队失败，需要重放 |
| `/readyz` 连续失败 | MySQL 故障 |
| Agent 5xx 比例升高 | LLM key/额度/网络或检索依赖 |

## 5. 重试死任务

`dead` 表示超过 `max-attempts`。修复根因后：

```sql
UPDATE embedding_jobs
SET status='pending', attempts=0, available_at=UTC_TIMESTAMP(3), last_error=NULL
WHERE status='dead' AND document_id=UUID_TO_BIN('<document-id>');
```

`vector_cleanup_jobs` 同理。不要无条件重置全部 `dead`，先看错误原因，避免风暴。

## 6. 重新向量化

单篇：

```sql
UPDATE embedding_jobs
SET status='pending', attempts=0, available_at=UTC_TIMESTAMP(3), last_error=NULL
WHERE document_id=UUID_TO_BIN('<document-id>');
```

然后等 `embed` worker 消费。正文替换会自动 reset。

全量：见 `deployment.md` 的“向量重建”。

## 7. Qdrant 检查

```bash
curl -fsS http://127.0.0.1:6333/healthz
curl -fsS http://127.0.0.1:6333/collections/document_chunks
curl -fsS -X POST http://127.0.0.1:6333/collections/document_chunks/points/count \
  -H 'Content-Type: application/json' -d '{"exact":true}'
```

Qdrant 是派生数据：collection 丢失时可以重建，不要把它当唯一数据源。

## 8. 软删 / purge

```bash
# 预演
docker compose ... run --rm migrate /usr/local/bin/purge -dry-run

# 执行
docker compose ... run --rm migrate /usr/local/bin/purge -batch 1000 -interval 1s

# 清理 Qdrant（或让 vector-clean 常驻）
docker compose ... run --rm migrate /usr/local/bin/vector-clean -once
```

顺序：

1. `purge` 删除 MySQL 数据并写 `vector_cleanup_jobs`；
2. `vector-clean` 删除 Qdrant 点；
3. 检查两个队列表都无 pending/dead。

## 9. 备份恢复

备份命令见 `deployment.md`。恢复原则：

1. 停 `apid`、`embed`、`vector-clean`；
2. 恢复 MySQL；
3. `migrate up`；
4. 如 Qdrant 也丢失，重建 collection 并重放 `embedding_jobs`；
5. 启动 API/worker；
6. 验证 `/readyz`、检索、Agent citations。

## 10. 密钥轮换

### API key

1. 生成新 key；
2. 更新 secret；
3. 滚动重启 `apid`；
4. 通知客户端更新；旧 key 立即失效。

如果需要零停机双 key，需要后续实现 key 列表/版本化；当前只有一个 key。

### LLM key

1. 更新 `KF_LLM_API_KEY`；
2. 重启 `apid`；
3. 用 `/agent/chat` 做一次真实 E2E。

### embedding 服务

换模型/维度：

1. 停 `embed`；
2. 改 `KF_EMBEDDING_*` 与 `KF_QDRANT_VECTOR_SIZE`；
3. 新建/清空 collection；
4. 重放 embedding job；
5. 重启 worker；
6. 检查 Qdrant 点数、评测、Agent。

## 11. 容量与性能

- BGE-M3 CPU 适合开发/评测，不适合高并发生产；生产用 GPU 或 embedding API。
- `embedding_jobs` 批量大小由 `KF_EMBEDDING_BATCH_SIZE` 控制；CPU 环境不要开大。
- Agent 延迟 = 检索 + embedding + LLM；LLM 通常占大头。
- Qdrant 内存随 collection 增长；生产按点数/维度规划。
- MySQL `document_chunks` 的 FULLTEXT 是同步写入成本；大批量导入建议分批。

## 12. 故障处理

### MySQL 不可用

- `/readyz` 503；
- CRUD/search 500；
- 修复 DB 后自动恢复；API 无需重启。

### Qdrant 不可用

- `hybrid` 模式自动降级到 FTS；
- `vector` 模式返回 500；
- `embed`/`vector-clean` 反复失败并退避，任务不丢。

### LLM 不可用

- Agent 500；CRUD/search 不受影响；
- 检查 key、额度、网络、`llm.timeout`；
- Agent 路由已把写超时延长到 `llm.timeout + 30s`。

### worker 堆积

1. 看 `last_error`；
2. 确认 embedding/Qdrant 可达；
3. 扩容 worker 或降低 batch；
4. 修复后重置 `dead` 任务。

## 13. 发布检查清单

- [ ] `make check` 通过
- [ ] 集成测试 / 覆盖率通过
- [ ] 迁移已在预发执行
- [ ] 备份完成
- [ ] 新镜像可启动，`/readyz` 200
- [ ] API key / LLM key / embedding 地址已注入
- [ ] Agent E2E：有检索、有 citations、无结果时固定回答
- [ ] 观察 5xx、P95 延迟、队列积压 30 分钟
