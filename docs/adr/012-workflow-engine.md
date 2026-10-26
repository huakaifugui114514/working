# ADR 012：YAML DAG Workflow 引擎

- **状态**：已接受并实现
- **日期**：2026-10-27

## 背景

正文/PDF 入库目前是固定的代码路径。需要能把“解析 → 替换正文 → 外部调用”等步骤
用 YAML 编排，并在节点失败时重试/从中间恢复。

## 决定

- `internal/workflow`：
  - YAML 定义 `name/nodes`；
  - 节点字段：`id/action/depends_on/params/retries/timeout/when`；
  - 启动时校验：id 唯一、依赖存在、无环；
  - 稳定拓扑序执行；
  - 节点输出进入变量表，供 `${node.field}` / `${input.x}` 模板引用；
  - 每个节点可重试、超时；`when` 为假时 skip，依赖未成功的节点 skip。
- 运行记录落 MySQL：
  - `workflow_runs`（输入/输出/状态/错误/时间）
  - `workflow_node_runs`（节点输出/尝试次数/时间）
  - 相同 `run_id` 再次执行时跳过 succeeded 节点，实现断点续跑。
- 内置 action：
  - `pdf_extract`：PDF 文本抽取；
  - `http_request`：调 KnowFlow API（Bearer key）；
  - `sleep` / `noop`。
- `cmd/workflow` 只做装配；示例 `configs/workflows/pdf-ingest.yaml` 把 PDF 抽取和
  正文替换串起来。

## 被否掉的方案

1. **引入 Temporal/Cadence**：对当前单机规模过重，且需要额外服务。
2. **纯内存 Workflow**：进程退出丢运行记录，无法断点续跑。
3. **把 LLM/Agent 放进 Workflow 节点**：Agent 有自己的循环/超时语义，先把确定性工具链跑通。

## 代价

- 不是通用编程语言：只支持注册好的 action + 简单模板，复杂分支要加 action 或扩展。
- 模板是字符串替换，不做完整表达式求值；`when` 只支持布尔/真值。
- 节点失败不会自动回滚已成功节点；需要 action 自身幂等或补补偿节点。
- 运行记录没有 UI/告警；通过 SQL 查看。
