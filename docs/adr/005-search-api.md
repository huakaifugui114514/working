# ADR 005：检索 API 与 ngram FULLTEXT 分页

- **状态**：已实现（MySQL FULLTEXT MVP；repository/service/handler + 集成测试 + HTTP E2E）
- **日期**：2026-10-22
- **决策者**：项目所有者（GET、2 rune 下限、offset 分页、chunk 粒度）
- **相关规则**：`AGENTS.md` §3.1（接口契约由用户定）、§1.2（测试与证据）

## 背景

M4.1 已把正文写入 `document_contents`，并把正文切成 `document_chunks`。
`migrations/0003_document_content.up.sql` 已在 `document_chunks.content` 上建了：

```sql
FULLTEXT KEY ft_chunk_content (content) WITH PARSER ngram
```

M4.2 的目标是：给定查询词 `q`，返回最相关的 **chunk**，可选限定某个 collection。
本阶段不引入 embedding / 向量检索，先用 MySQL InnoDB FULLTEXT 做 MVP。

检索和 CRUD 的语义不同，需要先定清楚：

- 用 GET 还是 POST；
- 返回 chunk 还是按 document 聚合；
- 排序和分页怎么做；
- 中文 ngram 的最小查询长度；
- collection 过滤不存在时返回 404 还是空结果。

不先定，handler、SQL、OpenAPI 会各按一套理解实现。

## 决定

### 1. 使用 `GET /api/v1/search`

```http
GET /api/v1/search?q=知识库&collection_id=<uuid>&limit=20&offset=0
```

| 方案 | 优点 | 代价 |
|---|---|---|
| **GET（选中）** | 幂等、可缓存、可直接分享 URL；参数简单 | 查询串长度受 URL 限制；复杂查询以后要扩参数 |
| POST | body 可放复杂查询/过滤器 | 非幂等语义不直观；缓存和调试更麻烦 |

当前查询只有 `q`、`collection_id`、`limit`、`offset`，GET 足够。
以后如果出现复杂 DSL / 多字段过滤，再考虑 `POST /search` 或 `POST /search/query`。

### 2. 返回 chunk 粒度，不按 document 聚合

每条结果是一个 chunk：

```json
{
  "items": [
    {
      "chunk_id": "019...",
      "document_id": "019...",
      "document_title": "产品文档",
      "chunk_index": 0,
      "content": "……",
      "score": 1.23
    }
  ],
  "next_offset": 20
}
```

| 方案 | 优点 | 代价 |
|---|---|---|
| **chunk 粒度（选中）** | 保留排序精度；M4.3 Agent 可直接拿 passage 拼上下文 | 同一文档可能有多条结果；客户端要自己分组 |
| document 聚合 | 结果更像“文档列表” | 一篇文档多个 chunk 的分数怎么合并？会丢失 passage 级信息 |

M4.3 需要的是“相关段落”，不是“相关文档标题”。文档聚合以后可以在 chunk 结果之上再做。

### 3. `q` 至少 2 个 rune

本地 MySQL 8.4 已确认：

```text
ngram_token_size = 2
```

ngram 按 2 个字符切 token。实测有两个需要知道的细节：

- 中文单字有时仍能命中（例如查“检”能命中“检索”），不是完全查不到；
- ASCII 单字（如“A”）查询返回 0，两个 ASCII 字符是否命中还受 stopword 影响（见下）。

我们仍然要求 `q` 至少 2 个 rune，这是**产品精度规则**（减少单字带来的过宽结果），
不是因为 MySQL 一定查不到。因此：

- `q` 先 trim，去空白后少于 2 个 rune → `400 invalid_argument`
- `q` 上限 256 个 rune（防止超长查询打满 FULLTEXT）
- 空结果 → `200` + `items: []`

### 3.1 默认 stopword 会让部分英文短词查不到

ngram 解析器会排除**包含** stopword 的 token。默认 stopword 列表是英文的，
其中包含 `a`、`the` 等。实测：

```text
内容 BCDF：查 "BC" → 1 条；查 "B" → 0 条
内容 ABCDEF：查 "AB" → 0 条（"AB" 含 stopword "a"）；查 "ABC" → 1 条
```

所以中文为主的 MVP 可以先用默认配置；如果产品要认真支持英文/混合内容，
需要配置自定义 stopword 表（`innodb_ft_server_stopword_table` /
`innodb_ft_user_stopword_table`）并重建 FULLTEXT 索引，或者换专门的搜索引擎。
这是 M4.2 的已知限制，不阻塞当前中文 MVP。

如果产品必须稳定支持单字搜索，需要把 `ngram_token_size` 改成 1 并重建 FULLTEXT 索引；
这是另一项决策，不在 MVP 范围内。

### 4. 用 NATURAL LANGUAGE MODE，不开放 boolean 语法

```sql
MATCH(c.content) AGAINST(? IN NATURAL LANGUAGE MODE)
```

| 方案 | 优点 | 代价 |
|---|---|---|
| **NATURAL LANGUAGE（选中）** | 参数绑定简单；用户输入不会被当成 `+/-/*` 操作符 | 不能表达“必须包含/排除” |
| BOOLEAN MODE | 表达力强 | 用户输入要转义/校验；`+ - * " @` 有语法含义，容易 400 或误查 |

MVP 不接受 boolean 语法。以后要支持，新增显式的 `mode=boolean` 参数和一套转义规则，
不要让用户输入直接进 BOOLEAN MODE。

### 5. 排序：score DESC，chunk_id DESC 兜底

```sql
ORDER BY score DESC, c.id DESC
```

- `score` 是 `MATCH ... AGAINST` 的计算结果，只在同一次查询内有排序意义；
- 不承诺跨 MySQL 版本分数稳定；
- `c.id` 是 UUIDv7，作为同分时的稳定 tie-breaker。

### 6. 分页先用有上限的 offset

- `limit`：1..50，默认 20
- `offset`：≥ 0，默认 0；且 `offset + limit <= 1000`
- 仓库层取 `limit+1` 行判断是否还有下一页
- 有下一页且 `offset + limit < 1000` 时返回 `next_offset = offset + limit`，
  否则 `next_offset: null`（到达 1000 窗口上限后不再提供下一页）
- `offset + limit > 1000` → `400 invalid_argument`

| 方案 | 优点 | 代价 |
|---|---|---|
| **offset（选中）** | 简单；和 `LIMIT/OFFSET` 直接对应 | 深分页会扫更多行；所以设 1000 上限 |
| score keyset | 深分页更稳 | score 是计算值，不是索引列；要物化全部匹配再比较，复杂且可能更慢 |

搜索通常只看前几十条；offset 加硬上限是当前规模下更划算的取舍。
以后如果要做“无限滚动 + 深分页”，再评估按 `(score, chunk_id)` 物化或搜索引擎。

### 7. collection 过滤是可选条件

- 不传 `collection_id`：全局搜索，只过滤软删文档
- 传了 `collection_id`：
  - 不是合法 UUID → `400 invalid_argument`
  - collection 不存在 / 已软删 → `404 not_found`
  - 存在则 `JOIN collection_documents` 限定该 collection

**为什么不存在返回 404 而不是空结果**：和现有
`GET /collections/{id}/documents` 的语义一致；显式指定了一个不存在的资源，
不应该伪装成“搜到了 0 条”。

### 8. SQL 形状

全局：

```sql
SELECT c.id, c.document_id, d.title, c.chunk_index, c.content,
       MATCH(c.content) AGAINST(? IN NATURAL LANGUAGE MODE) AS score
FROM document_chunks c
JOIN documents d ON d.id = c.document_id
WHERE d.deleted_at IS NULL
  AND MATCH(c.content) AGAINST(? IN NATURAL LANGUAGE MODE)
ORDER BY score DESC, c.id DESC
LIMIT ? OFFSET ?
```

指定 collection 时追加：

```sql
JOIN collection_documents cd
  ON cd.document_id = d.id AND cd.collection_id = ?
```

- `MATCH` 的列必须是 FULLTEXT 索引的列（`content`）；
- `q` 用参数绑定，不进字符串拼接；
- `document_chunks` 没有 `deleted_at`，软删过滤通过 `documents` 做；
- `collection_documents` 的 `(collection_id, document_id)` 唯一，不会放大结果。

## 后果

- 1 个字符的查询返回 400；这是 ngram token size 的限制，不是 bug。
- 搜索分页最多看到前 1000 条；超过要改成分页策略或换搜索引擎。
- 分数只用于排序，不作为业务指标；换 MySQL 版本/改 ngram 配置可能变化。
- MVP 不做高亮/摘要，直接返回 chunk 原文；前端按 `q` 自己高亮。
- 软删文档不会被搜到；restore 后重新可搜。
- 不需要新迁移：`ft_chunk_content` 已在 0003 建好。
- FULLTEXT 不能建在 `TEMPORARY TABLE` 上（MySQL 8 报 1796），所以检索集成测试不能复用
  现有临时表方案；测试要用真实表 + 独立 collection/document 数据，或单独测试 schema。

## 接口错误语义

| 情况 | 状态码 | 错误码 |
|---|---|---|
| `q` 缺失 / trim 后 < 2 rune / > 256 rune | 400 | `invalid_argument` |
| `limit` 不在 1..50 / `offset < 0` / `offset+limit > 1000` | 400 | `invalid_argument` |
| `collection_id` 不是 UUID | 400 | `invalid_argument` |
| `collection_id` 不存在 / 已软删 | 404 | `not_found` |
| 其他数据库/内部错误 | 500 | `internal_error` |
| 没有匹配 | 200 | —（`items: []`） |

## 实现顺序

1. `SearchRepository` 接口 + MySQL 实现 + 真实 FULLTEXT 集成测试
2. `SearchService`：`q`/limit/offset 校验、collection 存在性、错误映射
3. `rest/search.go` handler + `Server` 装配
4. `EXPLAIN` 确认 FULLTEXT 索引被使用 + 真实 curl 验收

## 验证计划

- OpenAPI 契约：`GET /api/v1/search` 与 `SearchResponse` schema
- 集成测试：
  - 中文查询（>= 2 rune）能命中
  - 英文查询能命中
  - 1 rune 查询 → 400
  - 软删文档不返回
  - collection 过滤只返回该 collection 的 chunk
  - collection 不存在 → 404
  - `limit+1` 能正确产生 `next_offset`
- 真实 MySQL `EXPLAIN`：确认 `ft_chunk_content` 被使用，记录是否 filesort
- 真实 curl：200 响应体、400、404 各一组

## 未决问题

- 是否需要在 M4.2 就返回 `document_source_uri` / `collection_id` 等附加字段？
  当前先不返回，等前端/Agent 需要再加。
- 是否需要对搜索结果按文档去重？当前按 chunk 返回，客户端或 M4.3 再决定。
- 是否需要请求级超时？当前沿用现有策略，未单独为搜索加超时。
