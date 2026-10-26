# KnowFlow 部署指南

本文面向实际部署，不重复本地开发步骤。本地开发见 `README.md`。

## 1. 拓扑

```text
                 TLS / reverse proxy (Nginx / ALB / Ingress)
                              │
                        :8080 API (apid)
                         │        │
             MySQL 8.4 ──┘        └── Qdrant
             (事实源)                 (向量索引，可重建)
                         │
                  embedding 服务
                  (GPU / 云 API / Ollama)
                         │
                     LLM API
```

生产原则：

- MySQL 是唯一事实源；Qdrant 里的向量可以重建。
- Qdrant 和 MySQL **不暴露公网**，只开放在内部网络。
- TLS 在外层反向代理/负载均衡终止；本服务只监听 HTTP。
- `apid` 不需要访问 Qdrant 才能启动；Hybrid 检索在 Qdrant 故障时降级到 FTS。
- embedding worker 和 cleanup worker 是独立进程，不与 API 共享内存状态。

## 2. 构建镜像

仓库根目录：

```bash
docker build -t knowflow:prod .
```

国内网络可覆盖基础镜像：

```bash
docker build \
  --build-arg GO_IMAGE=docker.m.daocloud.io/library/golang:1.27.2-alpine \
  --build-arg RUNTIME_IMAGE=docker.m.daocloud.io/library/alpine:3.22 \
  -t knowflow:prod .
```

镜像：

- 多阶段构建，运行阶段不含 Go 工具链；
- 非 root 用户 `knowflow`；
- 内置 `apid / migrate / purge / embed / vector-clean / mcp-server / kafka-dispatcher / kafka-embed`；
- 生产 Compose 内置 Redis、Kafka（仅容器网络，不暴露宿主机）；
- 默认入口 `apid`，其他命令通过 compose `command` 覆盖；
- 配置文件位于 `/etc/knowflow/config.yaml`，环境变量覆盖。

## 3. 生产环境变量

复制模板：

```bash
cp deploy/env.prod.example deploy/.env
chmod 600 deploy/.env
```

最少需要设置：

| 变量 | 说明 |
|---|---|
| `MYSQL_ROOT_PASSWORD` | MySQL root 密码 |
| `MYSQL_DATABASE` | 库名，默认 `knowflow` |
| `MYSQL_USER` / `MYSQL_PASSWORD` | 应用账号（不要用 root） |
| `KF_AUTH_API_KEY` | API 鉴权 key，prod 必填；建议 ≥32 字节随机值 |
| `KF_LLM_API_KEY` | DeepSeek / OpenAI-compatible API key |
| `KF_LLM_BASE_URL` | 默认 `https://api.deepseek.com` |
| `KF_EMBEDDING_OLLAMA_URL` | embedding 服务地址 |
| `KF_RETRIEVAL_MODE` | `fts` / `vector` / `hybrid`；建议先 `fts`，评测后切 `hybrid` |
| `API_PORT` | 宿主机 API 端口，默认 8080 |

密钥只放在 secret manager / 运行时环境变量中，**不要**写入镜像、Git 或普通 ConfigMap。

## 4. 启动

```bash
docker compose --env-file deploy/.env \
  -f deploy/docker-compose.prod.yml up -d --build
```

启动顺序由 compose 保证：

```text
mysql healthy
  → migrate 成功
  → apid / embed / vector-clean 启动
```

检查：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml ps
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

`/healthz` 是存活探针；`/readyz` 会 Ping MySQL，失败返回 503。

## 5. 鉴权

生产必须设置：

```bash
KF_AUTH_API_KEY=$(openssl rand -hex 32)
```

客户端：

```bash
curl -H "Authorization: Bearer $KF_AUTH_API_KEY" \
  http://127.0.0.1:8080/api/v1/collections
```

或：

```bash
curl -H "X-API-Key: $KF_AUTH_API_KEY" ...
```

`/healthz`、`/readyz` 不鉴权，供探针调用。

## 6. 数据库迁移

compose 已包含一次性 `migrate` 服务。手工执行：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml \
  run --rm migrate /usr/local/bin/migrate up
```

查看版本：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml \
  run --rm migrate /usr/local/bin/migrate version
```

约束：

- 只能追加迁移，不改已经应用过的文件。
- 先备份 MySQL，再跑破坏性迁移。
- 迁移失败且 `dirty=true` 时，先人工确认，再用 `migrate force <version>` 修复，不要盲目 down。

## 7. Worker

| 命令 | 作用 |
|---|---|
| `embed -interval 30s` | 消费 `embedding_jobs`，写 Qdrant |
| `vector-clean -interval 30s` | 消费 `vector_cleanup_jobs`，删除 Qdrant 向量 |
| `purge` | 物理清理过期软删数据，写 Qdrant 清理任务 |

`purge` 建议由 CronJob / 定时任务执行，不要放进 API 进程：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml \
  run --rm migrate /usr/local/bin/purge -batch 1000 -interval 1s
```

worker 都可横向扩展；任务表使用 `FOR UPDATE SKIP LOCKED`，通过 lease 防重复。

## 8. 向量重建

Qdrant 可重建。换 embedding 模型或维度时：

1. 停止 `embed` worker；
2. 备份 MySQL；
3. 用新模型创建新 collection（或清空旧 collection）；
4. 把所有 `document_contents` 重新入队：

```sql
INSERT INTO embedding_jobs
  (document_id, content_hash, chunker_version, status, attempts,
   available_at, created_at, updated_at)
SELECT document_id, content_hash, chunker_version, 'pending', 0,
       UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), UTC_TIMESTAMP(3)
FROM document_contents
ON DUPLICATE KEY UPDATE
  content_hash = VALUES(content_hash),
  status = 'pending',
  attempts = 0,
  available_at = VALUES(available_at);
```

5. 启动 `embed` worker；
6. 验证 Qdrant 点数和 `/agent/chat` citations。

embedding 维度变化时，Qdrant collection 的 `vector_size` 必须同步修改配置并重建 collection。

## 8.1 监控

`/metrics` 暴露 Prometheus text 格式（需要 API key）：

```bash
curl -H "Authorization: Bearer $KF_AUTH_API_KEY" \
  http://127.0.0.1:8080/metrics
```

当前指标：

```text
http_requests_total{method,path,status}
```

路径中的 UUID 会归一化成 `{id}`，避免标签基数爆炸。
Prometheus scrape 配置示例：

```yaml
scrape_configs:
  - job_name: knowflow
    authorization:
      credentials: <KF_AUTH_API_KEY>
    static_configs:
      - targets: ["knowflow-api:8080"]
```

Worker 指标暂未暴露；通过 SQL 队列积压与日志告警，见 `operations.md`。

## 9. TLS / 反向代理

应用不终止 TLS。Nginx 示例：

```nginx
server {
    listen 443 ssl;
    server_name knowflow.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Request-ID $request_id;
        proxy_read_timeout 120s;
    }
}
```

Agent 路由可能调用 LLM 几十秒，反向代理读超时至少设置 90s。
应用内已把 Agent 写截止时间延长到 `llm.timeout + 30s`。

## 10. 扩缩容

- `apid` 无状态，可水平扩展；所有实例共享 MySQL/Qdrant。
- `embed` 可多副本，任务抢占保证不重复；CPU embedding 时不要盲目扩容。
- `vector-clean` 可多副本。
- `purge` 只跑单实例或由分布式锁/CronJob 保证单实例。

## 11. 备份与恢复

至少备份：

- MySQL（全量 + binlog）；
- Qdrant volume（可选；可重建，但备份能缩短恢复时间）；
- 部署配置（不含明文密钥）。

示例：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.prod.yml \
  exec -T mysql sh -c 'mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" --single-transaction --routines --triggers knowflow' \
  | gzip > knowflow-$(date +%Y%m%d%H%M).sql.gz
```

恢复前先停 API/worker，恢复后跑 `migrate up`，再启动。

## 12. 升级与回滚

升级：

1. 备份 MySQL；
2. 构建新镜像；
3. 跑迁移；
4. 滚动重启 `apid`；
5. 重启 worker；
6. 检查 `/readyz`、日志、Agent E2E。

回滚：

- 应用镜像可回滚；
- 数据库迁移不保证可逆，破坏性变更必须先备份并有回滚 SQL；
- Qdrant collection 可重建。

## 13. 安全清单

- [ ] `KF_AUTH_API_KEY` 已设置，且通过 secret manager 注入
- [ ] MySQL/Qdrant 不暴露公网
- [ ] TLS 在入口终止
- [ ] `KF_APP_ENV=prod`（禁用 `/debug/sleep`）
- [ ] 数据库账号非 root、最小权限
- [ ] 日志不包含 API key / LLM key / 正文敏感信息
- [ ] 备份加密并定期恢复演练
- [ ] 反向代理限制请求体大小（正文上限 2 MiB + JSON）
- [ ] 对 `/api/v1/*` 做入口限流（本服务不内置全局限流）

## 14. 故障排查

| 现象 | 检查 |
|---|---|
| 401 | `KF_AUTH_API_KEY` 与客户端 header 是否一致 |
| `/readyz` 503 | MySQL 连接、账号密码、网络 |
| Agent 500 且日志 `缺少 KF_LLM_API_KEY` | LLM key 未注入 |
| Agent 慢/超时 | 反向代理 read timeout、`llm.timeout`、DeepSeek 状态 |
| citations 为空 | 检索无结果或 collection 过滤过严 |
| Qdrant 写入失败 | `embed` 日志、Qdrant 地址、collection 维度 |
| 向量检索不生效 | `KF_RETRIEVAL_MODE`、`embedding_jobs` 是否 done、Qdrant 点数 |
| purge 后向量仍在 | `vector-clean` 是否运行、`vector_cleanup_jobs` 状态 |
