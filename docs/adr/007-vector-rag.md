# ADR 007：向量化 RAG（本地 BGE-M3 + Qdrant + Hybrid）

- **状态**：已接受（M4.4a/b/c/d 完成；生产化与全量评测待做）
- **日期**：2026-10-23
- **决策者**：项目所有者（本地 embedding、Qdrant、Hybrid、评测集由 agent 定）
- **相关规则**：`AGENTS.md` §3.1（接口契约由用户定）、§1.2（测试与证据）

## 背景

M4.2 的 MySQL FULLTEXT 是关键词级检索，能跑通 RAG，但存在明确上限：

- 字符级 ngram，不懂语义；
- 英文短词受 stopword 影响；
- 没有纠错、同义词、拼音；
- 排名能力弱。

M4.3 的 Agent 已经依赖 `Retriever` 接口，这给替换检索实现留好了缝。

本 ADR 定 M4.4 的向量化 RAG 方案：

- **本地** embedding（BGE-M3 via Ollama），先跑通与评测；
- **Qdrant** 作为向量存储；
- **Hybrid**：MySQL FTS + Qdrant vector，RRF 融合；
- embedding 走**异步任务**，不阻塞正文入库；
- 评测集由 agent 先定，量化 baseline 与改进。

## 决定

### 1. Embedding：本地 BGE-M3，接口化

本地开发用 Ollama 跑 `bge-m3`：

```text
维度：1024
距离：Cosine
上下文：8192 token（BGE-M3）
语言：中英文/多语言
```

定义消费方接口：

```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

实现：

- `OllamaEmbedder`：本地开发/评测，调 `POST /api/embed`
- 以后 `OpenAICompatibleEmbedder`：生产 GPU/云 API

| 方案 | 优点 | 代价 |
|---|---|---|
| **本地 BGE-M3（选中，dev）** | 免费、数据不出内网、可离线评测 | CPU 慢；占内存；不适合高并发生产 |
| OpenAI-compatible embedding API | 快、稳定、无需本地模型 | 另一个 key、按量付费、网络依赖 |
| 更小的 bge-small-zh | 快 | 质量低，维度不同 |

本机没有 GPU，本地 BGE-M3 只定位为**开发/评测 provider**；生产走 GPU 或 API。

### 2. 向量存储：Qdrant

Qdrant 独立服务，Docker 启动：

```text
HTTP :6333
gRPC :6334
collection: document_chunks
distance: Cosine
vector size: 1024
point id: chunk UUID
```

payload：

```json
{
  "chunk_id": "...",
  "document_id": "...",
  "chunk_index": 0,
  "embedding_model": "bge-m3",
  "chunk_version": 1
}
```

- MySQL 仍是事实源；Qdrant 里的向量可重建。
- 不在 MySQL 里存 `BLOB` 做全表余弦：没有 ANN 索引，不能当生产方案。
- M4.4a 先用 Qdrant REST（`net/http`）探通；如果过滤/重试复杂，再评估官方 Go client（新依赖需确认）。

| 方案 | 优点 | 代价 |
|---|---|---|
| **Qdrant（选中）** | 独立、过滤/payload 好、Docker 简单 | 多一个服务；要同步生命周期 |
| pgvector | 和 Postgres 一体 | 引入第二个数据库 |
| Elasticsearch kNN | 全文+向量一体 | 重；运维成本高 |
| MySQL BLOB + 应用余弦 | 无新服务 | 无 ANN，数据量大就不可用 |

### 3. 异步 embedding，不阻塞正文写入

`PUT /content` 已经在事务里删旧 chunk、插新 chunk；不能把外部 embedding 调用塞进去：

- 失败会让正文入库失败；
- 延迟不可控；
- 旧 chunk 被删后 chunk id 变化，向量要跟着换。

方案：

```text
PUT /content 事务提交
  → INSERT embedding_jobs(document_id, status=pending)
  → cmd/embed worker 取 job
  → 读当前 document 的 chunks
  → 调 Embedder
  → 删除该 document 的旧向量
  → upsert 新向量
  → 标记 job done / failed（带 retry）
```

- job 按 `document_id` 聚合，不按 chunk id：一次正文替换会换掉全部 chunk id。
- worker 必须幂等：重复执行 = 删除旧向量 + 重建当前向量。
- 需要新迁移（例如 `embedding_jobs`），M4.4b 实现。
- 软删文档不立刻删向量；检索时按 MySQL 的 active document/collection 过滤。purge 时再清理向量。

### 4. 当前正文替换的 chunk id 会变

`ReplaceDocumentContent` 是先删旧 chunk 再插新 chunk，chunk UUID 全部重新生成。

因此：

- 不能只监听 chunk insert 来做增量 embedding；
- 必须以 `document_id` 为单位重建；
- Qdrant 删除用 `document_id` filter，不用旧 chunk id 列表。

### 5. Hybrid retrieval：FTS + vector + RRF

```text
MySQL FTS top 20
+ Qdrant vector top 20
→ RRF 融合（k=60）
→ 过滤软删 / collection
→ 取 top 5 给 Agent
```

RRF 公式：

```text
score(d) = Σ 1 / (k + rank_i(d))
```

- FTS 保留为 fallback：Qdrant / embedding 不可用时，Agent 仍能关键词检索。
- pure vector 不做默认；中文专有名词、型号、错误码仍然靠关键词更稳。
- 后续可加 rerank，但不在 MVP。

| 方案 | 优点 | 代价 |
|---|---|---|
| **Hybrid（选中）** | 关键词 + 语义互补；可降级 | 两路召回、融合逻辑、评测更复杂 |
| Pure vector | 语义召回强 | 专有名词/短词弱；依赖 embedding 服务 |
| Pure FTS（现状） | 简单、稳定 | 语义能力差 |

### 6. collection 过滤：MVP 先回表

一个 document 可以属于多个 collection，向量 payload 里同步 `collection_ids` 需要处理关联增删。

MVP 选择：

1. 向量检索多取候选（例如 top 40）；
2. 回 MySQL 过滤 active document 与 collection；
3. 截断到 top 5。

代价：过滤后可能不够 5 条，需要扩大候选或 fallback 到 FTS。
后续如果评测显示过滤损失明显，再把 `collection_ids` 同步进 Qdrant payload。

### 7. 评测优先于选型

在接 Qdrant 之前，先建固定评测集：

- 10~15 篇合成文档
- 20 条 query
- 覆盖：精确关键词、中文语义、英文、混合、跨 collection、无答案
- 指标：Recall@5、MRR、无结果率

先跑 MySQL FTS baseline，再跑 vector / hybrid；同一套 query 对比。

没有 baseline 的“感觉更好”不算证据。

### 8. 本地资源与限制

本机：

```text
16 核 CPU / 29 GiB RAM / 无 NVIDIA GPU / 204 GB 可用磁盘
```

- BGE-M3 CPU 推理可行，但吞吐是分钟级回填，不是高并发生产；
- Qdrant 小数据量内存占用低；
- Ollama/Qdrant 通过 Docker Compose profile `ai` 启动，不影响只跑 CRUD 的开发。

## 后果

- 多两个本地服务：Ollama、Qdrant；`docker compose --profile ai up -d`。
- 向量与 MySQL 是最终一致：正文写入后、job 完成前，vector 检索可能看不到新内容；FTS 仍然可用。
- 换 embedding 模型/维度必须重建 Qdrant collection 与全部向量。
- Qdrant 数据可丢弃重建；MySQL 是 source of truth。
- 本地 CPU embedding 只用于开发/评测；生产必须换 GPU 或 API。
- 需要新增迁移、worker、Qdrant client、HybridRetriever、评测脚本。

## 实施顺序

```text
M4.4a  ADR + compose(Ollama/Qdrant) + Embedder 接口 + 评测集 + FTS baseline
M4.4b  embedding_jobs 迁移 + cmd/embed worker + Qdrant REST 客户端（已完成）
M4.4c  VectorRetriever + HybridRetriever + 评测对比（已完成）
M4.4d  Qdrant 清理 outbox + worker + 真实 E2E（已完成）
```

## 验证计划

- Ollama：`POST /api/embed` 返回 1024 维向量
- Qdrant：创建 collection、upsert、search 成功
- Embedder：httptest fake；真实 Ollama 一次 smoke test
- 评测：FTS baseline vs vector vs hybrid，输出 Recall@5 / MRR
- 生命周期：正文替换后 job 重建向量；purge 后向量删除
- 降级：Qdrant 不可用时 Hybrid 退回 FTS

## M4.4a 验证结果

本地环境：16 核 / 29 GiB / 无 GPU。

```text
Qdrant 1.12.4 healthz: passed
Qdrant create + upsert + search: passed
Ollama bge-m3: 1024 维，批量 embedding 请求成功
```

固定评测集：20 篇文档 / 30 条 query。

| 检索方式 | Recall@5 | MRR |
|---|---:|---:|
| MySQL FULLTEXT baseline | 0.850 | 0.818 |
| BGE-M3 pure vector（内存 cosine） | 1.000 | 0.950 |

注意：这是合成评测集，不能直接外推生产；只用于同一套 query 下对比
FTS / vector / hybrid。

### M4.4b 验证结果

```text
迁移 0004：up → version=4；down -y → version=3；再 up → version=4
PUT /content 后 embedding_jobs.status=pending, attempts=0
cmd/embed -once 后 status=done, attempts=1
Qdrant document_chunks 中该 document 的 points=1
再次 PUT /content + worker 后 points 仍=1（旧 chunk 向量被删除）
```

Worker 日志：

```text
开始 embedding 任务 document_id=... chunker_version=1 attempts=1
embedding 任务完成 document_id=... chunks=1
```

失败/并发语义：

- `content_hash` 乐观校验：旧 worker 不能在正文替换后把新任务标 done。
- `FOR UPDATE SKIP LOCKED` + lease：多个 worker 不抢同一任务；崩溃后可回收。
- `cmd/purge` 同步删除 `embedding_jobs`。
- 真实 MySQL/Ollama/Qdrant 集成测试通过；`make check` 全绿。

### M4.4c 验证结果

实现了三种 `agent.Retriever`：

```text
fts    KeywordRetriever
vector VectorRetriever  (BGE-M3 → Qdrant topK×4 → MySQL 回表)
hybrid HybridRetriever  (FTS top20 + vector top20 → RRF k=60 → top5)
```

真实 HTTP E2E：

```text
GET /api/v1/search?q=服务挂了先找谁
FTS hits 0 []

KF_RETRIEVAL_MODE=hybrid POST /api/v1/agent/chat
ANSWER 答案 [1]
CITATIONS 1
CITATION 1 线上故障响应
线上故障按照 P0 到 P3 分级，P0 故障要求十五分钟内拉起应急会议并通知值班负责人。
```

说明 FTS 完全漏掉的语义 query，vector 路补回了正确 chunk；
Hybrid 的单路失败降级、RRF 排序、去重均有单测。

默认 `retrieval.mode=fts`；评测/灰度通过后再切 `hybrid`。

### M4.4d 验证结果

`cmd/purge` 在删除 document 的同一个 MySQL 事务里写 `vector_cleanup_jobs`；
`cmd/vector-clean` 异步消费并调用 Qdrant `DeleteByDocument`。

真实 E2E：

```text
purge 后 vector_cleanup_jobs = pending, attempts=0
Qdrant points = 1
cmd/vector-clean -once 后 cleanup job = done, attempts=1
Qdrant points = 0
```

清理失败会留在 outbox，按退避重试；不会回滚已完成的 DB 删除。

### 剩余缺口

- 生产 embedding provider（GPU / 云 API）、灰度、监控、对账还没做。
- 合成评测集的 hybrid 全量指标还没跑；当前已有 FTS baseline 与纯向量 in-memory 指标。
- 没有 rerank。

## 未决问题

- Qdrant 用 REST 还是官方 Go client？M4.4a 先 REST。
- collection_ids 是否同步进 payload？MVP 先回表，评测后再定。
- embedding 批量大小、并发度、重试策略。
- 是否需要 rerank；M4.4c 评测后再决定。
