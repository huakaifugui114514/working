# ADR 004：documents API 与软删清理策略

- **状态**：已接受（M3 契约）
- **实现状态**：已实现（2026-10-20；repository / service / handler + 集成测试）
- **日期**：2026-10-18
- **决策者**：项目所有者
- **相关规则**：`AGENTS.md` §3.1（接口契约由用户定）、§1.2（测试与证据）

## 背景

M2 已完成 collections CRUD。M3 接入 `documents` 与 `collection_documents`：

- 一篇文档可能属于多个知识库；
- 列表排序键是关联表的 `added_at`，不是文档的 `created_at`；
- 重复添加关联、删除不存在的关联需要明确的 HTTP 语义；
- 软删后的行何时物理删除，需要可执行的保留策略。

本 ADR 定契约和清理策略；实现进度以 README 的「实现状态」为准。

## 决定

### 1. 文档是全局资源，多对多关联

```text
documents（全局）
collection_documents（多对多）
```

文档独立创建，再通过关联加入一个或多个 collection。

**选项与代价**：

| 方案 | 优点 | 代价 |
|---|---|---|
| 全局文档 + 多对多（选中） | 复用正文/元数据；与现有 schema 一致；跨库检索自然 | 生命周期复杂；需要处理孤儿文档；删文档影响多个集合 |
| 文档只属于一个集合 | 生命周期/权限简单 | 同一文档进多个集合要复制；`collection_documents` 失去意义 |

### 2. 关联用 `POST`，重复返回 409

```http
POST   /api/v1/collections/{cid}/documents        {"document_id":"..."}
       → 201 + Location: /api/v1/collections/{cid}/documents/{did}

DELETE /api/v1/collections/{cid}/documents/{did}
       → 204；关联不存在 → 404
```

**选项与代价**：

| 方案 | 语义 | 优点 | 代价 |
|---|---|---|---|
| `PUT/DELETE` | 幂等建立/移除关联 | 重试安全；无 409 | 与“重复直接报错”的意图不符 |
| `POST/DELETE`（选中） | 创建关联；重复 409 | 语义明确；重复是客户端错误，不掩盖问题 | 客户端要处理 409；重试不幂等 |
| `POST` + 幂等 204 | 重复也成功 | 重试简单 | 客户端无法知道“是不是刚创建” |

- 重复添加 → `409 conflict`（`apperr.CodeConflict` 已存在，`respond.StatusForCode` 已映射）。
- 成功添加 → `201 Created` + `Location`，响应体是 `CollectionDocument`。
- 删除不存在的关联 → `404`，不假装成功。
- 文档/知识库不存在或已软删 → `404`。

### 3. collection 列表用嵌套结构

```json
{
  "items": [
    {
      "document": {
        "id": "...",
        "title": "...",
        "source_uri": null,
        "mime_type": null,
        "size_bytes": null,
        "created_at": "...",
        "updated_at": "..."
      },
      "added_at": "..."
    }
  ],
  "next_cursor": null
}
```

- `added_at` 属于 `collection_documents`，不属于 document；嵌套让归属清晰。
- document 的表示在所有接口保持一致。
- 代价：客户端多一层 `item.document`。
- 排序：`(collection_documents.created_at DESC, document_id DESC)`；cursor 的 `CreatedAt` 是 `added_at`，`ID` 是 `document_id`。

### 4. 删除语义

| 接口 | 作用 | 影响 |
|---|---|---|
| `DELETE /api/v1/documents/{id}` | 软删文档（全局） | 所有 collection 列表都不再显示；关联行保留 |
| `DELETE /api/v1/collections/{cid}/documents/{did}` | 只移除关联 | 文档还在；其他 collection 不受影响 |

- 软删文档保留 `collection_documents` 行，便于恢复和审计；
- 列表查询必须 `JOIN documents d ... AND d.deleted_at IS NULL`；
- 集合软删同样保留关联（M2 已定），列表从 collections 出发过滤。

### 5. 软删保留期 30 天，物理清理走独立 purge

- `deleted_at` 只负责读路径隐藏；行不会自动消失。
- 保留期默认 **30 天**，过期后物理删除或归档；具体合规要求可调整。
- **不把物理删除挂到正常请求的 SQL 上**：请求路径只做软删；清理是独立任务，分批、限速、低峰期执行。

**为什么不能照搬 Go map 的 incremental evacuation**：

| | Go map | MySQL 软删清理 |
|---|---|---|
| 数据位置 | 进程内存 | 磁盘 + redo/undo + binlog |
| 不清理的后果 | 影响读写正确性/性能 | 只占存储，读路径过滤即可 |
| 失败代价 | 进程重启，内存丢失 | 锁等待、事务回滚、主从延迟 |
| 成本承担者 | runtime | 如果塞进请求，就是用户请求 |

Go map 扩容是“不做不行”；软删清理是“可以延后做”。把 DELETE 塞进用户请求会让延迟尾部、锁竞争、主从延迟不可控，而且清理量依赖流量——低峰期可能没请求，高峰期反而最忙。

**已实现的 purge 设计**：

1. 独立命令/定时任务，低峰期运行；
2. 每批取 500–2000 个父行，批间 sleep；记录进度和指标；
3. **每个父行一个短事务，先锁父行，再删子行**：

```sql
BEGIN;

SELECT deleted_at
FROM documents
WHERE id = ?
FOR UPDATE;

-- deleted_at 为 NULL（restore 赢了）或 >= cutoff：ROLLBACK，跳过
-- 行不存在（已被其他 worker 删除）：ROLLBACK，跳过

DELETE FROM collection_documents WHERE document_id = ?;
-- M4.1 之后：DELETE FROM document_chunks WHERE document_id = ?;
--            DELETE FROM document_contents WHERE document_id = ?;
DELETE FROM documents WHERE id = ?;

COMMIT;
```

collection 同理：先 `collections ... FOR UPDATE`，再删该 collection 的关联，最后删 collection。

4. 需要新迁移加 `(deleted_at, id)` 索引；0001 已应用，不能改。**0002 已追加该索引**。
5. 高并发下的锁协议：
   - 添加关联时对 collection 加 `FOR SHARE`（只读、允许多个 add 并发），对 document 加 `FOR UPDATE` 确认存在；
   - **restore 与 purge 都对同一父行 `SELECT ... FOR UPDATE`**：谁先拿到锁谁赢，后拿到锁的一方重新读 `deleted_at` 再决定恢复或删除；
   - 不在事务里锁父行就开始删关联，会出现“restore 成功但关联被删”的窗口；
   - 大知识库的关联可能很多，单父行事务会变大；如果真遇到超大 collection，再加 `purge_state`/lease，而不是拆开事务破坏锁协议。

### 6. Restore 语义

- `POST /api/v1/documents/{id}/restore`、`POST /api/v1/collections/{id}/restore`。
- 只要行还没被 purge 物理删除，就允许恢复（purge 是硬截止）。
- 事务内 `SELECT ... FOR UPDATE`，根据 `deleted_at` 区分：
  - 行不存在 / 已物理删除 → 404
  - `deleted_at IS NULL`（本来就没删）→ 409
  - `deleted_at` 非空 → 置 NULL、刷新 `updated_at`，返回 200
- 软删时保留的关联和正文在 restore 后原样可见；purge 已经物理删掉的资源无法恢复。

## 后果

- `apperr.CodeConflict` 从“预留”变成实际使用；`respond` 已支持 409。
- `GET /collections/{id}/documents` 必须 join documents 并过滤 `d.deleted_at IS NULL`。
- `POST /collections/{id}/documents` 要区分：collection/document 不存在 → 404；关联已存在 → 409。
- `DELETE` 关联不存在 → 404；删除文档是软删，返回 204。
- 移除关联只检查关联行是否存在；父资源（collection/document）已软删不阻塞，仍返回 204。
  父行物理删除前 purge 会先清关联，因此正常路径不会出现“关联还在、父行已物理消失”。
- 物理 purge 已实现：`internal/purge`（按父行短事务 + 批间 sleep + dry-run）+ `cmd/purge`；
  由 cron / CronJob 调用，不挂请求路径。
- restore 已实现：restore 与 purge 通过同一父行锁串行化；active → 409，物理删除 → 404。
- 0002 已追加 `(deleted_at, id)` 索引；purge 取批用 `ORDER BY deleted_at, id`，
  与索引顺序一致，避免 filesort。
- `source_uri` / `mime_type` 的空串和纯空白在 service 层折叠为 SQL NULL；
  `title: null` 视为“不改”（title 是 NOT NULL）。

## 验证

- 契约：`api/openapi.yaml` 已包含 `Document` / `DocumentCreate` / `DocumentUpdate` / `CollectionDocument`、documents/关联端点与 restore 端点。
- 已实现：
  - `httptest` 覆盖 201/204/400/404/409；
  - repository 集成测试覆盖重复添加 1062 → 409、移除不存在 → 404、软删文档不出现在 collection 列表；
  - restore 集成测试覆盖 200/404/409、关联保留；purge 按父行事务的集成测试覆盖过期/孤儿行清理；
  - 并发集成测试覆盖两种顺序：restore 先锁 → purge 跳过、关联保留；purge 先删 → restore 404；
  - 真实 `curl` 走通 create document / add association / list / remove association / soft delete；
  - `internal/purge` 集成测试覆盖 dry-run、分批、关联清理、保留期内数据保留、幂等；
  - 真实 MySQL `EXPLAIN` 验证 purge 取批走 `idx_deleted_id`（range + Using index，无 filesort）。

## 参考

- 契约：[`api/openapi.yaml`](../../api/openapi.yaml)
- 相邻决策：[ADR 003](003-collections-api.md)
