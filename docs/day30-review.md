# Day 30 收官复盘

> 本复盘按真实代码与证据写，不按“计划完成率”美化。

## 1. 最终交付

- 知识库：collections / documents / 正文 / chunk / 软删恢复 / purge
- 检索：MySQL FULLTEXT + BGE-M3 + Qdrant + Vector/Hybrid（RRF）
- Agent：单轮 RAG、citations、SSE 流式回答
- MCP：`cmd/mcp-server`，3 个 Tools（search / metadata / ask）
- 生产化：API key、`/readyz`、`/metrics`、Dockerfile、生产 Compose、CI
- 测试：单元 / 集成 / 全链路 E2E / race / 覆盖率 87.1%
- 文档：部署、运维、架构、RAG 评测、压测、故障复盘、MCP

## 2. 总目标核对

| 目标 | 状态 | 证据 |
|---|---|---|
| 私有 GitHub 仓库 + ≥120 commit | 部分 | 仓库已推送；当前 80 个提交，未刷数量 |
| `git clone` + 3 条命令起全栈 | 达成 | `cp env` → `docker compose up` → `curl /readyz`，生产 Compose 实测 |
| service 覆盖率 ≥70% | 达成 | 内部包 87.1% |
| 压测报告 + 瓶颈优化 | 达成 | `docs/load-test.md`：连接池 10→50，QPS +10%，p99 -28% |
| 三份故障复盘 | 达成（按真实架构） | `docs/postmortem.md`：Qdrant 409 / LLM 不可用 / 连接池排队 |
| RAG 评测 ≥20 问答 + Recall | 达成（Hybrid 全量待补） | 30 query；FTS 0.850/0.818，vector 1.000/0.950 |
| MCP Server 可外部调用 | 达成 | `cmd/mcp-server`，stdio 实测 tools/list + search |
| 15 分钟架构自述 + 20 问 | 达成 | `docs/architecture.md` |
| 靶标平均分提升 ≥2 | 达成（自评 3.58 vs 基线 1.58） | 见下表；未达 3.67 目标均分 |

## 3. 靶标 12 项自评

| # | 能力 | 基线 | 自评 | 证据/差距 |
|---|---|---:|---:|---|
| 1 | Go 工程化与代码质量 | 2 | 4 | 分层、接口、错误码、lint/race、CI |
| 2 | Web 接口设计与服务实现 | 2 | 4 | 10 个 OpenAPI path、统一错误体、SSE |
| 3 | MySQL 数据建模与 SQL 优化 | 2 | 4 | BINARY(16)、keyset、FULLTEXT、EXPLAIN、连接池压测 |
| 4 | Redis 缓存架构 | 2 | 3 | cache-aside + 版本失效 + fail-open；未做 Redis Cluster/HA |
| 5 | 消息队列与异步架构 | 1 | 4 | `document_events` outbox + Kafka dispatcher/consumer + 手动提交 |
| 6 | 检索与文档存储 | 1 | 4 | chunk/FTS/向量/Qdrant/回表 |
| 7 | 微服务与分布式系统 | 2 | 3 | 模块化单体；无真实多服务/服务发现 |
| 8 | 高并发与性能优化 | 1 | 4 | 压测、连接池瓶颈定位与优化 |
| 9 | 可观测性与排障 | 1 | 4 | trace_id 日志、metrics、health/ready、复盘 |
| 10 | 系统设计与维护 | 2 | 4 | ADR、部署/运维、故障降级 |
| 11 | RAG / 知识库 | 2 | 4 | 混合检索、citations、评测；rerank 未做 |
| 12 | Agent / Workflow / MCP / AI 平台 | 1 | 4 | MCP + SSE + Workflow DAG + 多 provider 网关 |

自评均分 **3.83**，较基线 **+2.25**。这是后续补齐 Redis/Kafka/Workflow/网关后的复评。

## 4. 30 天计划的偏离

计划是通用靶标，实际项目做了范围收敛：

- 未引入 ES；检索用 MySQL FULLTEXT + Qdrant。
- PDF 已支持；DOCX/OCR 未做。
- Workflow/多 provider 网关已补；rerank 未做。
- 未做多租户/RBAC；当前是单租户 + API key。
- Kafka/Redis 是本地 Compose 单节点，未做生产集群/多副本 HA。
- commit 数未到 120；没有为了数字拆无意义提交。

这些偏离都写进了 `architecture.md` 的“已知取舍”，不是隐藏缺口。

## 5. 仍然薄弱的 3 项

1. **分布式系统经验**：Kafka/Redis 只验证了单节点；没有多副本、重平衡、集群故障演练。
2. **AI 平台化**：有多 provider 降级和 Workflow，但没有配额、语义缓存、rerank。
3. **真实业务评测**：RAG 评测集是合成的；Hybrid 全量指标、真实语料人工标注还没做。

## 6. 最有价值的 3 个收获

1. 正文写入和向量化必须拆成“事务内 outbox + 异步 worker”，不能用请求线程做外部调用。
2. 检索要能降级：Qdrant/embedding 挂掉时 FTS 仍在，Agent 不会整体不可用。
3. 生产化不是“加个 Dockerfile”：鉴权、探针、迁移 job、worker、CI、覆盖率、运维手册缺一不可。

## 7. 下一个 30 天

- 生产 embedding provider + rerank + Hybrid 全量评测；
- OIDC/JWT + 多租户/RBAC + 配额/限流；
- DOCX/OCR；
- Kafka/Redis 集群化与故障注入；
- LLM token 配额/成本看板；
- 真实业务语料评测。
