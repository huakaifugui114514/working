# MCP Server（Model Context Protocol）

`cmd/mcp-server` 通过 **JSON-RPC 2.0 over stdio** 把 KnowFlow 暴露给 Cursor / Claude Desktop。

stdout **只输出协议消息**；日志写 stderr。

## 构建

```bash
go build -o bin/mcp-server ./cmd/mcp-server
```

## 配置

| 环境变量 | 说明 |
|---|---|
| `KF_API_URL` | KnowFlow API 地址，默认 `http://127.0.0.1:8080` |
| `KF_AUTH_API_KEY` | KnowFlow API key；与服务端一致 |

也支持命令行：

```bash
bin/mcp-server -api-url http://127.0.0.1:8080 -api-key "$KF_AUTH_API_KEY"
```

## Cursor / Claude Desktop 配置

```json
{
  "mcpServers": {
    "knowflow": {
      "command": "/absolute/path/to/bin/mcp-server",
      "env": {
        "KF_API_URL": "http://127.0.0.1:8080",
        "KF_AUTH_API_KEY": "your-api-key"
      }
    }
  }
}
```

> 不要把真实 key 写进仓库；上面只是示例。

## Tools

### `knowledge_search`

```json
{
  "query": "服务挂了先找谁",
  "collection_id": "可选 uuid",
  "limit": 5
}
```

返回 KnowFlow `/api/v1/search` 的 `items` 和 score。

### `document_metadata`

```json
{"document_id": "uuid"}
```

返回文档元数据。

### `knowledge_ask`

```json
{
  "message": "服务挂了先找谁",
  "collection_id": "可选 uuid"
}
```

返回 `answer` + `citations`。

## 手动验证

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | bin/mcp-server
```

真实调用（需要 API 已启动、有正文和向量）：

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"knowledge_search","arguments":{"query":"检索"}}}' \
  | KF_API_URL=http://127.0.0.1:8080 KF_AUTH_API_KEY="$KF_AUTH_API_KEY" bin/mcp-server
```

## 协议范围

当前实现：

- `initialize`
- `notifications/initialized`（忽略）
- `ping`
- `tools/list`
- `tools/call`

未实现 Resources / Prompts / sampling；这是有意的最小 MCP Server，先保证外部 IDE 能真实调用 Tools。

## 安全

- MCP Server 不直接连 MySQL，只调 KnowFlow API；
- API key 通过环境变量注入；
- stdio 由 IDE 拉起，不监听网络端口；
- 工具返回内容可能包含知识库正文，按 IDE 的信任边界处理。
