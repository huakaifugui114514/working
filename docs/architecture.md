# KnowFlow 架构与答辩文档

## 1. 一句话

KnowFlow 是一个 Go 实现的单租户企业知识库 + RAG/向量检索服务：
正文入库 → chunk → FTS/向量索引 → Hybrid 检索 → 带 citations 的回答，附生产部署、worker、CI 与运维。

## 2. 拓扑

```text
Client / IDE / MCP Client
        │  HTTP + API key
        ▼
   apid (net/http)
     │        │
     │        ├── MySQL 8.4（事实源：collections/documents/content/chunks/jobs/events/runs）
     │        ├── Redis（可选搜索结果缓存）
     │
     ├── Kafka（document_events outbox → dispatcher → kafka-embed）
     ├── Qdrant（派生向量索引，可重建）
     ├── Embedding 服务（Ollama / GPU / 云 API）
     └── LLM（DeepSeek OpenAI-compatible）

   embed worker ── embedding_jobs ──► Qdrant
   vector-clean worker ── vector_cleanup_jobs ──► Qdrant
   purge ── 软删物理清理 + 写 vector_cleanup_jobs
```

## 3. 分层

```text
cmd/                 装配 + 生命周期，无业务 if-else
internal/transport    HTTP / middleware / SSE / auth / metrics
internal/agent        单轮 RAG、Retriever、Vector/Hybrid、LLM client、MCP 之外的领域
internal/knowledge    collection/document/content/search repository + service
internal/indexer      embedding/cleanup job 抢占与 worker
internal/embedding    Embedder 接口 + Ollama 实现
internal/qdrant       Qdrant REST 客户端
internal/purge        软删物理清理
internal/mcp          MCP stdio server
internal/config       YAML + env + 校验
internal/observability logs/trace/metrics
```

依赖方向：`transport → agent/knowledge → repository`；`knowledge` 不认识 HTTP/Qdrant；
`agent.Retriever` 是消费方接口，FTS/Vector/Hybrid 可替换。

## 4. 核心流程

### 正文入库

```text
PUT /documents/{id}/content
  → 校验 2 MiB / 正文非空
  → chunker（rune 上限、序号）
  → 事务：锁 document → 删旧 chunk → upsert content → 插新 chunk → upsert embedding_jobs
  → 提交
```

正文替换生成全新 chunk UUID，所以向量任务按 `document_id` 重建，不按旧 chunk id。

### 向量化

```text
embed worker
  → RequeueExpired
  → Claim (FOR UPDATE SKIP LOCKED)
  → 校验 content_hash
  → 批量 BGE-M3
  → DeleteByDocument + Upsert
  → Complete WHERE content_hash=...
```

`content_hash` 乐观校验防止旧 worker 覆盖新正文；lease + attempts + dead 处理崩溃/毒任务。

### 检索

```text
HybridRetriever
  ├─ KeywordRetriever → MySQL FULLTEXT
  └─ VectorRetriever  → BGE-M3 → Qdrant topK×4 → MySQL GetChunksByIDs
  → RRF 1/(60+rank) → top5
```

单路失败降级；两路都失败才报错。Qdrant/embedding 不可用时仍可用 FTS。

### 回答

```text
AgentService.Chat / ChatStream
  → 校验 message/collection
  → 检索
  → 0 条：固定拒答，不调 LLM
  → 有结果：BuildMessages([1]..[n]) → LLM → answer + citations
```

SSE 版本逐 token 输出，最后 `event: done` 带 citations；LLM 不支持流式时自动降级。

## 5. 数据模型

- `collections` / `documents` / `collection_documents`
- `document_contents`（正文 + SHA-256 + chunker_version）
- `document_chunks`（UUIDv7，FULLTEXT ngram）
- `embedding_jobs`（正文 → 向量 outbox）
- `vector_cleanup_jobs`（purge → Qdrant 清理 outbox）

所有时间 UTC；主键 BINARY(16)；无外键；软删 + purge；keyset 分页。

## 6. 可靠性与安全

- API key（prod 必填）、常量时间比较
- `/healthz`/`/readyz`、安全响应头、/metrics
- LLM 超时；Agent 路由单独延长写截止时间
- Qdrant 失败 → Hybrid 降级
- worker lease/重试/dead；purge 与 restore 行锁串行化
- Docker 非 root；MySQL/Qdrant 不暴露公网；TLS 在入口
- 备份/恢复/故障手册见 `deployment.md` / `operations.md`

## 7. 15 分钟架构自述提纲

1. 业务问题与范围（2 min）
2. 拓扑与分层（3 min）
3. 正文/向量写入（3 min）
4. Hybrid 检索与 RRF（3 min）
5. 生产化：鉴权/探针/metrics/CI（2 min）
6. 失败模式与取舍（2 min）

## 8. 20 个追问

1. **为什么 internal/ 能阻止外部引用？** Go 的 internal 规则由编译器强制，模块外无法 import。
2. **goroutine 泄漏怎么查？** `runtime.NumGoroutine`、pprof/goroutine、trace；本项目的 SSE/worker 用 ctx + channel 关闭。
3. **为什么系统需要最终一致？** 正文事务不能包含外部 embedding/Qdrant 调用；outbox + worker 保证可重试。
4. **缓存穿透/击穿/雪崩？** Redis 只缓存搜索结果，key 带版本；miss 回 MySQL。没有布隆过滤器/单飞，热点重建会重复查几次 DB。
5. **Kafka 手动提交/重平衡？** `kafka-embed` 手动 CommitMessages；handler 成功才提交。DB `embedding_jobs` 仍是事实源，重复消费幂等。
6. **p99 优化了什么？** 压测发现 MySQL 连接池排队，`max_open_conns` 10→50，QPS +10%，p99 -28%。
7. **Redis 全挂会怎样？** 启动 Ping 失败就不注入 cache；运行中 Get/Set 失败按 miss 处理，搜索直查 MySQL。
8. **RRF 为什么比纯向量好？** 只看排名，融合关键词和语义；专有名词/短词不完全交给向量。
9. **ReAct 终止条件？** 当前 Agent 是单轮 RAG，没有 ReAct；MCP Tools 由外部 IDE 调。未来加 max steps/timeout/tool error。
10. **MCP Tools 和 Resources 区别？** Tools 是可执行动作，Resources 是只读上下文；本实现只做 Tools。
11. **Workflow 节点失败怎么恢复？** `workflow_node_runs` 记录 succeeded 节点；同一 run_id 重跑跳过成功节点，失败节点按 retries 重试。
12. **QPS 再提升 10 倍改哪？** 读路径加缓存/读副本、连接池/索引、Qdrant 分片；写路径批量/队列；LLM 异步/流式。
13. **为什么 chunk 按 rune 不按 token？** 当前 chunker 保证中文长度和稳定性；embedding/LLM token 上限由模型侧；更精确可换 tokenizer。
14. **为什么引用 index 从 1 开始？** prompt 用 `[1]..[n]`，客户端展示自然。
15. **怎么防止模型编造引用？** 当前不校验 inline `[n]`；citations 是检索来源，未来可做引用一致性校验/rerank。
16. **正文替换时旧向量怎么办？** 按 document_id 删除全部旧点再 upsert；chunk UUID 变化。
17. **worker 崩溃怎么办？** running + lease 超时回收；幂等重建。
18. **purge 和 restore 并发？** 同一父行 `FOR UPDATE` 串行化。
19. **为什么没有外键？** 大表 DDL/写入放大/分片；repository + purge 保证一致性。
20. **API key 的局限？** 单租户共享 key，无用户/RBAC；未来 OIDC/JWT + 多 key/限流。

## 9. 已知取舍

- 单租户、单 key、无 RBAC
- MySQL + Qdrant + Redis + Kafka；未引入 ES
- Redis/Kafka 是 Compose 单节点，未做生产集群 HA
- 本地 CPU BGE-M3 只用于开发/评测
- PDF 已支持，DOCX/OCR 未做
- 无 rerank、无多租户/RBAC
- Hybrid 全量评测未完成
