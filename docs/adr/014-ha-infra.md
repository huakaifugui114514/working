# ADR 014：Redis Sentinel 与 Kafka 3 broker HA

- **状态**：已接受并实现
- **日期**：2026-10-27
- **相关**：ADR 009（Redis 缓存）、ADR 010（Kafka outbox）

## 背景

单节点 Redis/Kafka 只能证明功能，不能证明故障时可恢复。生产需要：

- Redis master 挂掉后自动选主，客户端自动切新 master；
- Kafka broker 挂掉后分区 leader 迁移，副本仍在 ISR；
- topic 预创建，不依赖 broker 自动创建；
- consumer lag 可被 Prometheus 抓取。

## 决定

### Redis Sentinel

- `redis-master` + `redis-replica` + 3 个 Sentinel（quorum=2）。
- 客户端使用 `go-redis` `FailoverClient`：
  - 配置 `KF_REDIS_SENTINEL_ADDRS` / `KF_REDIS_MASTER_NAME`；
  - 单机 `KF_REDIS_ADDR` 仍保留，便于本地开发。
- Sentinel 配置用**静态 IP**（172.30.0.10/11）：
  - Docker DNS 在 master 容器停止后会移除 `redis-master` 别名；
  - Sentinel 用 hostname 时无法在故障后解析，演练会卡在 tilt。
  - 静态 IP 是 Compose 单机部署的取舍；K8s 应使用 StatefulSet headless service。

### Kafka KRaft 3 broker

- 3 个 `apache/kafka:3.9.0` broker/controller 合并节点。
- topic `knowflow.document.events`：
  - 3 partitions；
  - replication-factor=3；
  - min.insync.replicas=2；
  - `kafka-init` 一次性 job 预创建，broker 关闭 auto-create。
- Producer `RequiredAcks=RequireAll`。
- `kafka-exporter` 暴露 consumer lag，Prometheus 抓 9308。

## 演练结果

Redis：

```text
set failover:key before
stop redis-master
sentinel get-master-addr-by-name knowflow-master
=> 172.30.0.11:6379
replica get failover:key => before
start old master
```

Kafka：

```text
stop kafka-2 (partition 0 leader)
partition 0 leader -> kafka-3
ISR: 3,1（从 3,1,2 收缩，仍 >= min.insync.replicas=2）
start kafka-2
ISR -> 3,1,2
```

## 代价

- Compose 静态 IP 与单机网络绑定，不适合多宿主机；生产 K8s 用 StatefulSet/Operator。
- Redis Sentinel 没有密码/TLS（内网 compose）；生产应加 ACL/TLS。
- Kafka 3 broker 资源开销明显；单机开发仍用单 broker profile。
- exporter 依赖第三方镜像；上线前要固定 digest/扫描 CVE。
- Kafka 只做了 broker 故障，没有做 consumer 滚动重启/分区再均衡的自动化测试。
