# ADR 002：迁移工具改选 `golang-migrate`

- **状态**：已接受（取代 [ADR 001](001-迁移工具选型.md)）
- **日期**：2026-10-14
- **决策者**：项目所有者
- **相关规则**：`AGENTS.md` §2.1（技术栈）、§2.2（引入新依赖的流程）

## 背景

ADR 001 选定 `pressly/goose`，核心理由是"只用 MySQL，不必为多数据库买单"。

决策者随后提出：**后续可能有多数据库需求**，因此更看重 `golang-migrate` 的数据库广度。
按 `§2.1`，选型结论归项目所有者，故改选。

改选前，agent 在**临时模块**中实测了两者的依赖规模（不动项目 `go.mod`）：

| 指标 | `golang-migrate` v4.20.1 | `goose` v3.29.0 |
|---|---|---|
| 构建列表模块数（`go list -m all`） | **209** | 95 |
| 其中与云厂商/各类数据库驱动相关的模块 | **53** | 少量 |
| 工具自身的 `go.mod` require 数量 | 207 | 94 |
| `go.sum` 行数 | 68 | 10 |
| **实际链接出的二进制大小** | **3.9M** | 3.7M |

（对照：本项目当前只依赖 `yaml.v3`，构建列表是 3 个模块。）

**关键发现**：二进制体积几乎无差别（3.9M vs 3.7M）——因为只有被 import 的包才会被链接，
不用的驱动（Spanner / ClickHouse / Mongo…）不会进二进制。
所以 migrate 的真实成本是**模块图与供应链面**（209 个模块要跟安全通告、`go mod tidy` 抖动更大），
**不是运行时开销**。

## 决定

改用 **`golang-migrate/migrate` v4.20.1**。

理由：决策者预期后续接入多种数据库，广度是长期价值；而实测表明其代价主要体现在模块图，
不在运行时——这个代价是可以接受的。

## 代价与约束（必须知道）

1. **模块图更大**：209 个模块、53 个与云厂商/数据库驱动相关，`go.sum` 会显著变长；
   需要定期跟进 `govulncheck` 与依赖升级（`AGENTS.md §2.2` 的关注点）。
2. **`embed.FS` 接入比 goose 绕**：需要 `source/iofs` 显式构造 source，再 `NewWithSourceInstance`。
3. **dirty 状态要手工修**：迁移失败后版本表进入 dirty，必须 `m.Force(version)` 才能继续。
   ⚠️ 与 ADR 001 记录的同一条前提相关：**MySQL 的 DDL 不支持事务**，
   迁移中途失败会留下半应用的表结构——工具换了，这个 MySQL 固有限制不变。
4. **DSN 有额外要求**：迁移文件里若含多条语句，需要在连接串加 `multiStatements=true`，
   否则第二个 `;` 之后的语句会被拒绝。
5. **推翻 ADR 001 的假设**："只用 MySQL" 这个前提不再成立。若将来多数据库需求没有兑现，
   本次改选就是一次 YAGNI 的代价（模块图变重）。

## 后续动作

- [ ] `internal/config` 增加 `database` 段（**密码只从环境变量读**）
- [ ] 接入 migrate：`//go:embed migrations/*.sql` + `source/iofs` + `database/mysql` 驱动
- [ ] `migrations/0001_init.sql`：表结构由项目所有者设计（`AGENTS.md §3.1`）
- [ ] 迁移入口：独立 `cmd/migrate`，还是 `apid` 启动时自动迁移？（后者要评估多副本并发迁移）
- [ ] 引入依赖时按 `§2.2` 在 commit message 里写明理由，并引用本 ADR

## 参考

- golang-migrate v4.20.0 release notes：<https://newreleases.io/project/github/golang-migrate/migrate/release/v4.20.0>
- 包文档：<https://pkg.go.dev/github.com/golang-migrate/migrate/v4>
- 被取代的 ADR：[001-迁移工具选型（goose）](001-迁移工具选型.md)
