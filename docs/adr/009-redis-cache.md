# ADR 009：Redis 搜索结果缓存

- **状态**：已接受并实现
- **日期**：2026-10-27

## 背景

FULLTEXT 搜索会随重复 query 反复查 MySQL。单次查询很快，但热点 query 在高 QPS 下会浪费连接池；
需要在不引入强一致复杂度的前提下降低读压力。

## 决定

- Redis 作为**可选**缓存；`KF_REDIS_ADDR` 为空即关闭。
- 缓存对象：`GET /api/v1/search` 的整页结果（含 `next_offset`）。
- 缓存 key：
  ```text
  search:v<version>:<sha256(q|collection|limit|offset)>
  ```
- 版本号 `search:version`：
  - 任何 collection/document/content 写操作成功后 `INCR`；
  - 读路径把版本放进 key，版本变化后自然 miss，不需要 `SCAN`/`KEYS` 删除。
- TTL 默认 30s，作为兜底。
- Redis 故障 fail-open：
  - 启动时 Ping 失败 → 不注入 cache，直接查 MySQL；
  - 运行中 Get/Set 失败 → 当作 miss/忽略，不影响请求。
- 不在请求路径做分布式锁/单飞；热点重建的代价是重复查几次 MySQL。

## 被否掉的方案

1. **按 query 删除 key / `SCAN` 前缀删除**：`SCAN` 会遍历全库，线上不可控；版本号是 O(1)。
2. **强一致缓存（写后删 + 延迟双删）**：写路径已经用版本号失效，不需要再维护删除时序。
3. **缓存 chunk 实体**：对象多、失效面大；先缓存整页结果，收益/复杂度更平衡。

## 代价

- 任一写操作都会让**全局**搜索结果失效；写多读少时命中率下降。
- 缓存的是搜索结果快照；`score` 只用于展示/排序，不承诺跨 MySQL 版本稳定。
- 多副本共享 Redis，版本号是全局的；这符合“所有副本看到同一份知识库”的语义。
- Redis 增加一个运维组件；生产必须配置持久化/内存上限/监控。
