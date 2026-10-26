# ADR 008：生产化基线（鉴权 / 部署 / 可观测性）

- **状态**：已接受并实现
- **日期**：2026-10-26
- **相关**：ADR 007（向量化 RAG）

## 背景

M4 完成后，功能链路已通，但距离“可实际部署”还缺：

- API 没有鉴权；
- 没有就绪探针；
- 没有生产镜像/编排；
- 没有 CI 与覆盖率门槛；
- 没有 Prometheus 指标；
- 没有部署/运维手册。

本文记录这些生产化取舍。

## 决定

### 1. API 鉴权用 API key，不引入用户体系

单租户内部知识库的第一阶段使用静态 API key：

```text
Authorization: Bearer <KF_AUTH_API_KEY>
或
X-API-Key: <KF_AUTH_API_KEY>
```

- `dev/test` 允许为空（本地 curl 方便）；
- `prod` 配置校验强制非空，否则拒绝启动；
- 常量时间比较；
- `/healthz`、`/readyz` 不鉴权；`/metrics` 需要 key；
- 不实现登录、用户、角色、多租户。

代价：

- 只有一个 key，轮换需要同时改客户端；
- 无法按用户审计/配额；
- 需要入口层限流。

未来如果要多用户，再引入 OIDC/JWT + RBAC，不在当前范围。

### 2. 就绪探针独立于存活探针

- `/healthz`：进程活着，不查 DB；
- `/readyz`：Ping MySQL，失败 503。

不把 DB 检查塞进 `/healthz`：数据库抖动时 liveness 失败会导致容器被反复重启，反而放大故障。

### 3. 多阶段、非 root 生产镜像

- builder：`golang:1.27.2-alpine`；
- runtime：`alpine:3.22` + ca-certificates/tzdata；
- 非 root 用户 `knowflow`；
- 一个镜像包含 `apid / migrate / purge / embed / vector-clean`；
- 默认入口 `apid`，其他命令由 compose `entrypoint/command` 覆盖；
- 配置内置 `/etc/knowflow/config.yaml`，环境变量覆盖；
- 不把 `.env`、源码、测试打进运行镜像。

### 4. 生产 Compose

- 只暴露 API 端口；
- MySQL/Qdrant 只在内部网络；
- `migrate` 作为一次性 job，API/worker 等它成功；
- `embed` / `vector-clean` 独立服务；
- `apid` 带 `/healthz` 健康检查；
- 密钥通过 `deploy/.env` / secret manager 注入，模板提交 `env.prod.example`，真实 `.env` gitignore。

不把 embedding 服务放进生产 Compose：生产应使用 GPU/云 embedding 网关，本机 Ollama 只用于开发/评测。

### 5. CI 与覆盖率

GitHub Actions：

```text
gofmt / go vet / golangci-lint
单元测试 -race
MySQL + Qdrant 集成测试
全链路 E2E（真实 HTTP/MySQL/Qdrant + fake 模型）
内部包覆盖率 ≥85%
Docker 镜像构建
```

覆盖率只统计 `internal/...`，不把 `cmd/` 的 `main` 计入分母；否则门槛会被无法单测的装配代码稀释。

### 6. 指标不引第三方 client

`/metrics` 输出 Prometheus text：

```text
http_requests_total{method,path,status}
```

路径中的 UUID 归一化为 `{id}`，避免标签基数爆炸。

只做最小计数器；直方图/Exemplar/OpenTelemetry 后续按需引入。

### 7. 部署与运维文档

- `docs/deployment.md`：拓扑、构建、环境变量、迁移、TLS、备份、扩缩容；
- `docs/operations.md`：健康检查、日志、SQL、队列、重试、密钥轮换、故障处理。

## 代价

- API key 是共享密钥，不适合多用户/细粒度权限；
- 全局限流依赖入口层（Nginx/网关），应用没有内置分布式限流；
- worker 指标目前通过 SQL/日志观察，未接入 Prometheus；
- 生产 embedding provider 仍需按实际环境接入（当前代码实现 Ollama `/api/embed`）；
- 没有 rerank。

这些都不阻塞单租户内部知识库部署，但上多租户/高并发前需要继续演进。
