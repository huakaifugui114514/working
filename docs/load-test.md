# 压测报告

## 环境与方法

- 本机：16 核 / 29 GiB / 无 GPU
- MySQL 8.4、Qdrant 1.12.4、Ollama 本地
- API：`apid`，`KF_RETRIEVAL_MODE=fts`（只测 API + MySQL；不把 LLM 延迟混进搜索）
- 工具：`cmd/loadtest`（标准库实现，固定并发 + 持续时间 + p50/p95/p99）
- 数据：1 个 collection、1 篇文档、1 个 chunk，查询 `知识库`

命令：

```bash
go run ./cmd/loadtest \
  -url 'http://127.0.0.1:18096/api/v1/search?q=%E7%9F%A5%E8%AF%86%E5%BA%93&limit=5' \
  -concurrency 200 -duration 5s -api-key "$KF_AUTH_API_KEY"
```

## 结果

### /healthz（20 并发，3s）

```text
requests=113084 ok=113084 errors=0
qps=37686.94
p50=332.766µs p95=1.373032ms p99=2.639872ms
```

### /api/v1/search（200 并发，5s，连接池 10）

```text
requests=68677 ok=68677 errors=0
qps=13734.08
p50=10.325017ms p95=41.779787ms p99=63.466783ms
```

### /api/v1/search（200 并发，5s，连接池 50）

```text
requests=75731 ok=75731 errors=0
qps=15142.85
p50=10.633156ms p95=31.307348ms p99=45.697729ms
```

## 分析

- 健康检查 p99 2.64ms，说明 HTTP 层和中件件不是瓶颈。
- 搜索 p99 63.47ms，QPS 13.7k；把 MySQL `max_open_conns` 从 10 提到 50 后：
  - QPS +10.2%
  - p99 -28.0%
- 瓶颈定位：**MySQL 连接池排队**，不是 CPU/HTTP。
  200 并发时单条 SQL 本身很快，但连接池只有 10 个，请求在 `database/sql` 等待。
- 调大连接池不是无脑结论：每个 API 副本都会占连接，生产要结合
  `max_connections` 和副本数配置；建议单副本 20–50，并用本报告的方法复测。

## 结论

- 当前本地单节点 QPS 约 1.4–1.5 万（简单 FULLTEXT 查询）。
- 生产容量规划优先看：MySQL 连接池、FULLTEXT 写入放大、Qdrant/embedding 延迟、LLM 延迟。
- 本次没有把 LLM 延迟混入搜索压测；Agent 的端到端延迟主要由 LLM 决定。

## Redis 缓存命中压测

同一台机器、同一查询、200 并发 / 5s，先预热一次让结果进入 Redis：

```text
无缓存（连接池 10）：qps=13734, p99=63.47ms
有缓存（Redis 命中）：qps=38562, p99=20.20ms
```

结论：缓存命中把 QPS 提升约 2.8 倍，p99 降到约 1/3。缓存未命中时仍回源 MySQL；
Redis 故障时 search cache fail-open。

## HA 演练

### Redis Sentinel

```text
master = 172.30.0.10:6379
stop master
sentinel get-master-addr-by-name knowflow-master
=> 172.30.0.11:6379
replica get failover:key => before
apid /search 仍返回 200（FailoverClient 自动切 master）
```

### Kafka broker

```text
topic: partitions=3, replication-factor=3, min.insync.replicas=2
stop kafka-2 (partition 0 leader)
partition 0 leader -> kafka-3；ISR 从 3,1,2 收缩为 3,1
start kafka-2；ISR 恢复为 3,1,2
```

### 消费者 lag

`kafka-exporter` 已暴露 `kafka_topic_partitions` / `kafka_consumergroup_lag`，
Prometheus 可在 `:9308/metrics` 抓取。
