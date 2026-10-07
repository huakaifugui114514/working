# KnowFlow

> AI Native 企业知识库与 Agent 平台 —— 30 天能力靶标项目的贯穿式工程。

**当前状态**：Day 0（启动与基线）。快速开始文档将在 **Day 7（M1 交付日）** 重写为「3 条命令跑起来」。

## 计划与进度

| 材料 | 位置 |
|---|---|
| 30 天计划书 | `KnowFlow-30天Go+AI-Native能力靶标计划书.md` |
| 进度跟踪表 | `KnowFlow-30天进度跟踪表.xlsx` |
| 能力基线 | `docs/00-baseline.md` |
| 每日日志 | `docs/daily-log.md` |
| 文档索引 | `docs/README.md` |

## 目录结构

```
cmd/         可执行程序入口（每个服务一个子目录）
internal/    私有代码，外部模块不可导入（config / transport / service / repository / cache / lock ...）
pkg/         可被外部复用的库（确实需要时才放）
api/         接口契约（proto / openapi）
migrations/  数据库迁移脚本（禁止手改数据库）
deploy/      部署与编排（docker-compose / grafana）
docs/        设计与复盘文档
labs/        每日练习（labs/w1 = Week 1 的 10 道 Go 题）
```

## 里程碑

| 里程碑 | 时间 | 可演示的东西 |
|---|---|---|
| M1 | Week 1 | 三条命令起来 + 知识库/文档 CRUD + 测试 + 优雅退出 |
| M2 | Week 2 | 上传 PDF → 异步解析入库 → 搜得到 |
| M3 | Week 3 | 压测报告（优化前后）+ 三份故障复盘 + Grafana 看板 |
| M4 | Week 4 | 带引用的流式 RAG 问答 + 可被外部调用的 MCP Server |
