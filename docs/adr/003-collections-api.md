# ADR 003：collections HTTP API 与分页游标

- **状态**：已接受
- **日期**：2026-10-18
- **决策者**：项目所有者（路径前缀、空 PATCH、`next_cursor` 语义）
- **相关规则**：`AGENTS.md` §3.1（接口契约由用户定）、§1.2（测试与证据）

## 背景

M2 要把已完成的知识库 repository/service 暴露为 HTTP API。需要一次定清楚：

- 路由怎么放，是否加版本前缀；
- 分页游标对客户端是什么形状；
- PATCH 的空对象、`null`、空字符串分别代表什么；
- 客户端断开/上下文取消时，错误怎么表达。

如果不先定，handler 和后续客户端会各按一套理解实现，改起来就是 breaking change。

## 决定

### 1. 路径带版本前缀

- 资源路径：`/api/v1/collections`
- 五个端点：
  - `POST /api/v1/collections` → 201 + `Location`
  - `GET /api/v1/collections?limit=&cursor=` → 200
  - `GET /api/v1/collections/{id}` → 200
  - `PATCH /api/v1/collections/{id}` → 200
  - `DELETE /api/v1/collections/{id}` → 204

**选项与代价**：

| 方案 | 优点 | 代价 |
|---|---|---|
| `/collections` | 短 | 以后加版本前缀会改变所有客户端路径 |
| `/api/v1/collections`（选中） | 现在加成本最低；未来可并存 v2 | 路径稍长 |

### 2. 游标是不透明 token

`next_cursor` / `cursor` 对客户端都是不透明字符串，格式为：

```text
base64url(8 字节大端 UnixMilli + 16 字节 UUID)   // 24 字节 → 32 字符，无 padding
```

**为什么不用 JSON 游标**：JSON 更可读，但更长、要处理转义；这个 token 客户端不应该解析，
可读性没有收益。定长二进制 + base64url 更紧凑，也便于校验长度。

**代价**：

- 客户端不能自己构造 cursor，只能原样回传；
- 服务端改格式必须走版本化，不能悄悄换；
- 游标的 `created_at` 是毫秒精度，和 `DATETIME(3)` 对齐。

### 3. PATCH 的字段语义

- 请求字段用 `*string`：字段缺省或 `null` 都表示“不改”；
- `description: ""` 表示清空；
- `name: ""` / trim 后为空 → 400；
- `{}` 是合法 no-op，返回当前记录，不刷新 `updated_at`；
- 未知字段 → 400。

### 4. 列表响应

```json
{"items": [...], "next_cursor": "..."}
```

没有下一页时 `next_cursor` 显式为 `null`，不是省略字段。这样客户端可以统一按“字段存在但为 null”处理。

### 5. 重复名称不返回 409

当前 schema 对 `name` 没有唯一约束（见 AGENTS M2 决策），同名是允许的。
如果以后要唯一性，需要先加迁移和 ADR，再引入 409；不能在 handler 里做 check-then-insert
（有竞态，且没有唯一索引兜底）。

### 6. 取消与超时

- **客户端取消**（`context.Canceled` 且 `r.Context().Err() != nil`）：不写响应、不记 500；
  客户端已经断开，写响应没有意义。AccessLog 仍会记录该请求。
- **`context.DeadlineExceeded`**：当前没有服务端请求超时策略，仍按 `internal_error`（500）处理。
  以后如果引入请求级超时，再加 `deadline_exceeded` / 504，并同步 OpenAPI。

**选项与代价**：

| 方案 | 行为 | 代价 |
|---|---|---|
| 全部 500（最简单） | 客户端断开也记 500 | 日志噪音，状态码语义不对 |
| 取消不响应 + 超时 504 | 语义最准 | 要新增错误码、OpenAPI 504、测试 |
| **取消不响应 + 超时暂 500（选中）** | 消除最常见的假 500 | 超时语义暂不精确；等有超时策略再补 |

## 后果

- `api/openapi.yaml` 是契约的单一来源；改路径/字段先改它。
- cursor 格式一旦发布不能随意改；需要时走 `/api/v2` 或兼容解码。
- handler 只做 HTTP 编解码/游标/错误映射，业务校验仍在 service。
- `cmd/apid` 启动时连接 MySQL；没有数据库时启动失败，不再有“只跑 healthz”的模式。

## 验证

- 单元测试：`internal/transport/rest/collection_test.go`（201/200/204/400/404、游标往返、客户端取消）。
- 真实运行：`make build` + 本地 MySQL，`curl` 走完 create/get/list/patch/delete。
- 集成测试：`go test -race -tags=integration ./internal/knowledge`。

## 参考

- 契约：[`api/openapi.yaml`](../../api/openapi.yaml)
- 相邻决策：[ADR 002](002-迁移工具改选-golang-migrate.md)
