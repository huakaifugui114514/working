# ADR 010：Kafka 事件 outbox

- **状态**：已接受并实现
- **日期**：2026-10-27

## 背景

正文替换后需要通知 embedding worker。直接在 HTTP 事务里发 Kafka 有两个问题：

1. Kafka 慢/失败会拖住 MySQL 事务；
2. 先提交 DB 再发 Kafka，进程崩溃会丢事件。

## 决定

- MySQL 新增 `document_events` outbox，和正文/chunks/`embedding_jobs` **同一事务**写入。
- `cmd/kafka-dispatcher`：
  - `FOR UPDATE SKIP LOCKED` 抢 `pending` 事件；
  - 改成 `publishing` + lease；
  - 发布成功 `published`；失败按 attempts 退避，超过 max attempts 进 `dead`；
  - 崩溃留下的 `publishing` 在 lease 到期后回收。
- `cmd/kafka-embed`：
  - 消费 topic，触发现有 embedding worker；
  - 只有 handler 成功才提交 offset；
  - 消费重复消息不会破坏数据：job 状态 + `content_hash` 幂等。
- Kafka 是**通知通道**，不是任务事实源；`embedding_jobs` 仍在 MySQL，轮询 worker 可独立工作。

## 被否掉的方案

1. **只在请求里发 Kafka**：丢事件、拖事务。
2. **完全用 Kafka 替代 DB job 表**：消费位点/重试/毒消息会变成唯一事实源，运维复杂度高；
   保留 DB outbox 更适合当前规模。
3. **Kafka 事务 + MySQL XA**：跨系统事务成本远高于收益。

## 代价

- 至少一次投递，消费端必须幂等；
- 多一个 Kafka 组件和 dispatcher/consumer 进程；
- `publishing` lease 过期会让事件重复发布（可接受，消费幂等）；
- 没有死信 topic/告警，`dead` 仅落库；生产应加监控。
