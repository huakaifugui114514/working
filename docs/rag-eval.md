# RAG 检索评测

## 评测集

`internal/eval/testdata/search_eval.json`：

- 20 篇合成文档
- 30 条 query
- 类型：keyword / semantic / semantic-zh / semantic-en / english
- 每条 query 标注期望命中的 document id
- 指标：Recall@5、MRR、无结果数

局限：合成语料、每篇一个 chunk，不代表真实业务分布；用途是同一套 query 下对比策略。

## 运行

### MySQL FULLTEXT baseline

```bash
go test -tags=integration -run TestSearchEvalBaseline -v ./internal/knowledge
```

实测：

```text
BASELINE MySQL FULLTEXT: queries=30 hits=26 recall@5=0.850 mrr=0.818 no_results=0
```

FTS 的典型漏召回：

```text
How do I get reimbursed for a hotel?      expected doc-travel       got 无
What documents should I bring on my first day? expected doc-onboarding got 无
When is the nightly database backup?      expected doc-backup       got 无
服务挂了先找谁                              expected doc-incident     got doc-auth
```

### BGE-M3 pure vector（内存 cosine）

```bash
make embed-eval
```

实测：

```text
VECTOR bge-m3: queries=30 hits=30 recall@5=1.000 mrr=0.950 no_results=0
```

说明：

- 向量路补回了英文语义 query；
- 关键词/专有名词仍可能靠 FTS 更稳；
- 这是模型质量评测，不是 Qdrant 最终链路。

### Hybrid（真实 Qdrant）

真实 HTTP E2E：FTS 对 `服务挂了先找谁` 返回 0 条，Hybrid 返回：

```text
CITATION 1 线上故障响应
线上故障按照 P0 到 P3 分级，P0 故障要求十五分钟内拉起应急会议并通知值班负责人。
```

完整 30 条 Hybrid Recall/MRR 还没跑；这是 M4.5 的待办。

## 对比

| 策略 | Recall@5 | MRR | 说明 |
|---|---:|---:|---|
| MySQL FULLTEXT | 0.850 | 0.818 | 关键词强，语义/英文弱 |
| BGE-M3 pure vector | 1.000 | 0.950 | 语义强，合成集上饱和 |
| Hybrid | 未跑全量 | 未跑全量 | 真实 E2E 已证明能补 FTS 漏召回 |

## 结论

- 纯 FTS 不够；语义 query 会漏。
- 纯向量在合成集上效果很好，但真实业务要防止专有名词/短词退化，因此保留 Hybrid。
- 下一步：把同一套 query 跑成 Hybrid 全量指标，并加入真实业务语料与人工标注。
