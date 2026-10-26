# AGENTS.md · KnowFlow 项目规则

> 本文件是**所有 AI agent 在本仓库工作的最高约束**。dsh / Zed / Cursor / Codex 都会在会话开始时自动加载它。
> 与本文件冲突的「聪明做法」一律不做；拿不准就先问人，**宁可停一下，也不要自作主张**。
>
> **本仓库是生产项目仓库**：只放能上线的工程代码与设计文档。
> 个人学习练习在仓库外 `~/knowflow-labs/`，个人规划与日志在仓库外 `~/knowflow-plan/`——
> **不要**把它们提交进本仓库，也不要因为「顺手」把仓库外的文件搬进来。

---

## 0. 先读这一节（30 秒版）

| 项 | 内容 |
|---|---|
| 项目 | **KnowFlow**：AI Native 企业知识库 + Agent 平台。目标是**可上线的生产服务**，不是演示代码 |
| 权威文档 | `README.md`（怎么跑、怎么用）、`docs/`（设计与决策记录：ADR / schema / 架构） |
| 用户画像 | **学习者，不是资深工程师**。默认他不懂你正在用的技巧；讲清楚比写出来更重要 |
| 三条最高优先级 | ① 企业级标准不可降级 ② 技术选型必须主流且高效 ③ 教学式协作：**关键代码用户写，agent 教**（§1–§3） |
| 判断「做完了」的唯一标准 | 有**真实执行输出**为证据（见 §1.3） |

---

## 1. 工程标准：企业级 / 大厂级（不可降级）

> 用户明确要求：agent 给出的代码、对代码的要求、以及整个开发流程，都要按企业级规范执行。
> 「他还在学」**不是**降低标准的理由。标准可以**分批达到**，但每一步的产出必须是当时能站住的那一版。

### 1.1 代码规范

- **错误处理**：错误必须被处理或显式向上传递；禁止 `_ = err` 吞错（确有理由时要写注释说明）；包装错误用 `fmt.Errorf("...: %w", err)`；业务错误统一走 `internal/apperr`，不把 `error` 字符串当协议。
- **context**：任何可能阻塞/跨进程/跨 goroutine 的调用都必须接收 `ctx context.Context` 并作为第一个参数；不在 struct 里存 ctx；`WithTimeout/WithCancel` 必须 `defer cancel()`。
- **并发**：共享状态必须有明确同步（`Mutex` / channel / atomic），并说明「谁写谁读」；goroutine 必须有退出路径（不许泄漏）；循环变量按 Go 1.22+ 语义书写，但**不要依赖隐式捕获**，显式传参更清楚。
- **资源**：`defer Close()` 成对出现；连接池、文件、response body 都要关；注意 `defer` 在循环里的累积。
- **分层**：`cmd/`（只做装配）、`internal/`（业务实现，外部不可导入）、`pkg/`（确实需要才放）、`api/`（契约）、`migrations/`（只追加）。禁止 `internal` 被外部模块导入，**禁止 handler 直接写 SQL**，禁止领域包依赖传输层（依赖方向只能由外向内）。
- **命名**：Go 惯例优先（短、无匈牙利前缀、`ErrXxx` 哨兵、`xxxTest` 表驱动命名）；导出符号必须有注释且以符号名开头。
- **注释**：解释**为什么**，不解释**是什么**；每个文件头写清职责；`TODO` 必须带条件（`TODO(day3): ...`），禁止无语 TODO。
- **配置**：不许硬编码地址/端口/密钥；一切经 `internal/config`，支持环境变量覆盖；密钥只从环境变量读，仓库内只留 `.env.example`。

### 1.2 流程规范（每个改动都要走完）

1. **先方案后动手**：超过「1 个文件 + 30 行」的改动，先给方案（改什么、为什么、影响面、怎么验证），等用户确认再写。
2. **小步提交**：一个 commit 只做一件事；commit 前必须 `make check` 全绿
   （= `gofmt` + `go vet` + `golangci-lint` + `go test -race`）。
3. **测试**：业务逻辑（`internal/` 下的 service / 工具函数）必须有表驱动单测；HTTP 层用 `httptest`；外部依赖（MySQL/Redis）用接口隔离，单测不连真库。禁止「为了让测试过」而改断言或删用例。
4. **可观测**：所有服务用 `log/slog` 结构化日志；请求链路带 `trace_id`；关键路径有明确的日志级别（error/warn/info/debug）。
5. **不许假完成**：不做「看起来跑通了」的演示代码；不留「以后再补」的空实现（宁可显式 `panic("not implemented")` 并写进日志）；**没实现完的模块不许在 README 里写成已可用**。

### 1.3 证据要求（这条最容易被违反）

任何「完成 / 通过 / 修好了」的结论，必须附：

- 可复现的**命令**（原样可粘贴）
- 该命令的**真实输出**（失败就贴失败原文，禁止转述成「大致通过」）
- 涉及接口的，给 `curl` 例子与返回体
- 有取舍的改动，写进 `docs/adr/NNN-标题.md`（背景 / 选项 / 决定 / 代价）

没有证据的完成，在本项目里等于没做。

---

## 2. 技术选型：主流、合理、开发效率高

### 2.1 默认技术栈（已定，不要擅自更换）

| 领域 | 选型 | 备注 |
|---|---|---|
| 语言 | Go 1.27+ | `go.mod` 声明 `go 1.27.2`；GOTOOLCHAIN=auto 自动拉取工具链 |
| HTTP | 先标准库 `net/http`，业务 CRUD 阶段引入 **gin** | 先用标准库是为了看懂 `Server.Shutdown`、`ServeMux` 这些被框架藏起来的机制 |
| 配置 | YAML（`gopkg.in/yaml.v3`）+ 环境变量覆盖 | 不用 viper（体积大、行为隐晦） |
| 日志 | 标准库 `log/slog` + JSON handler | 不引第三方日志库 |
| 数据库 | MySQL 8 + `database/sql`；确有收益时再上 `sqlc` / `sqlx` | ORM 只在明确收益时引入，且要写 ADR |
| 迁移 | **`golang-migrate`**（ADR 002；曾选 goose，同日改选） | 禁止手改数据库 |
| 缓存 | Redis 7 + `github.com/redis/go-redis/v9` | 用到时再引入 |
| 测试 | 标准库 `testing` + `github.com/stretchr/testify`（断言） | 表驱动优先 |
| 代码质量 | `gofmt` + `go vet` + `golangci-lint` + `go test -race` | 见 `Makefile` |
| 本地编排 | Docker Compose（`deploy/`） | 接数据库时引入 |

### 2.2 引入新依赖的流程（硬性）

1. 说明：**解决什么问题**、为什么标准库/已有依赖不够
2. 对比：至少 1 个替代方案，说明为什么选它（维护活跃度、最近发版、是否有 CVE）
3. 征得用户**明确同意**后再 `go get`
4. 在 commit message 里写清引入理由；影响架构的写 `docs/adr/NNN-*.md`

### 2.3 禁止

- **现阶段约定：不引入 Kratos / go-zero 等全家桶框架**（框架替你做的事越多，你越难说清线上出了什么问题）；等主干功能稳定后可以评估
- 自研轮子：日志库、配置框架、ORM、Web 框架
- 小众 / 停维护 / 需要付费或必须联网的依赖
- 为「看起来高级」而引入的设计模式（DI 容器、事件总线、CQRS…）

---

## 3. 教学式协作：按「学习者」而不是「资深工程师」对待

> 用户明确要求：很多知识他不会，agent 不能默认他懂，要**给提示、要求补知识、或直接教**。

- **任务开场**：先给「这次你会学到什么（1–3 条）」+「为什么现在做这件事」，再动手。
- **提示分级**（默认从最低一级开始）：
  - **L1**：只指方向 / 问一个问题，让他自己找答案
  - **L2**：给出关键 API、数据结构或伪代码，不给完整实现
  - **L3**：完整代码 + 逐段讲解（仅当他明确说「不会 / 直接教我 / 给我答案」时）
- **禁止话术**：「你应该知道」「自行查阅」「这个很简单」「显然」。他不会就是不会。
- **讲新概念时的固定结构**：一句话定义 → 最小可运行例子 → 在本项目哪里会用到 → 常见坑。
- **每完成一个可运行节点**：给「你现在应该能回答的 3 个问题」，答不出就补讲。
- **每个任务收尾**：给「今日知识清单 + 需要补的短板 + 下一步」，用户自己决定要不要记到仓库外的笔记里。
- **卡壳规则**：同一问题卡超过 **40 分钟**，agent 必须主动升级提示（L1→L2→L3）并在对话里点明卡点，**不允许放着不管，也不允许一个卡点拖垮两天**。
- **代码里的注释用中文**，且要解释「为什么这样写」。

### 3.1 谁来写代码：默认用户写，agent 教（最重要的一条）

> 用户 2026-10-07 明确要求：**「主要的关键代码最好还是由我来写，不然没有实际意义，你来教我怎么写。」**
> agent 把关键代码写完 = 剥夺用户唯一有效的学习方式。**本仓库是生产项目，但实现过程仍然由用户主导。**

**关键代码（默认必须由用户写，agent 只教不写）**

- 业务逻辑：`internal/` 下 service / repository 的核心实现
- 数据设计：表结构、字段与索引、SQL 语句、迁移内容
- 接口契约：请求/响应结构、错误码语义、路由设计
- 并发与状态：锁的粒度、事务边界、channel/goroutine 的所有权
- 错误处理策略：什么错该包、该重试、该吞（并说明理由）
- 测试用例设计：测什么、边界在哪、断言什么
- 有取舍的决定：选型结论与 ADR

**可以委派给 agent 的「管道代码」（需用户明确说「这段你来写」，且写完要逐段讲解）**

- 目录/文件骨架、纯样板（struct 定义、wiring、接口实现转发）
- Makefile / CI / Dockerfile / 配置模板
- 机械性重复改动（批量重命名、格式化、迁移文件编号）
- 文档排版整理

**教学五步（每次只推进「一个能跑起来的最小步」）**

1. **讲场景**：这段代码解决什么问题、不写会怎样、验收标准是什么
2. **给契约**：接口签名、数据结构、约束条件（**不给实现**）
3. **用户写**：写的过程中按 §3 的分级提示给线索，不抢键盘
4. **agent review**：先肯定对的部分 → 指出**具体哪一行/哪个分支**有问题 → 给可复现的验证方式 → 由用户改
5. **收尾**：补测试、跑 `make check`、提交

**硬性约束**

- ❌ 用户没说「你写」时，**不得**直接给出完整实现（包括"顺手补上"）
- ❌ **「下一步」不等于授权写代码**：用户问"下一步"是在问"接下来该做什么"，是让你给方案，
  不是让你动手。只有出现「你写」「你帮我改」「直接教我」这类明确授权，才能碰关键代码。
  （2026-10-10 踩过：agent 把四个中间件全写完了，用户要的是自己写。）
- ❌ 不得在 review 时直接改成自己的版本——要描述问题，让用户自己改
- ✅ 每段用户写完的代码，agent 必须指出「这段代码在什么输入下会出问题」（帮他建立边界意识）
- ✅ 若用户明确说「不会/直接教我」，才切到 L3：完整代码 + 逐段讲解 + 让他复述一遍

---

## 4. 目录与代码归属

| 目录 / 内容 | 默认由谁写 | agent 的权限 |
|---|---|---|
| `cmd/ internal/ pkg/ api/ migrations/ deploy/ configs/` 的**关键代码** | **用户** | 按 §3.1 教学五步走；agent 只在被明确委派时写，且必须讲解 |
| 同上，**管道代码**（骨架/样板/Makefile/CI/配置模板） | 可委派 agent | 写完逐段讲解，并说清"哪些地方将来需要你改" |
| `docs/` 设计文档、`README.md`、`AGENTS.md` | 可委派 agent | 内容必须与代码实际状态一致，不许把未实现的写成已可用 |
| 仓库外的 `~/knowflow-labs/`、`~/knowflow-plan/` | 用户私有 | **不得**提交进本仓库；用户把它们贴进对话时，仍按 §3.1 当学习内容处理（只教不代写） |

用户已写的代码（含当前骨架里的实现）**默认不重写、不"顺手优化"**；要动必须先说明理由并得到同意。

---

## 5. 绝对禁止（防止出乎意料的 agent 行为）

- ❌ 擅自更改技术栈、目录结构、需求范围；擅自「重构」用户没让改的代码
- ❌ `git push` / `git push --force` / `git reset --hard` / 改写历史 / 删分支（提交可以，**推送必须用户明确同意**）
- ❌ 破坏性命令：`rm -rf`（自己创建的临时目录除外）、`DROP DATABASE` / `TRUNCATE`、`docker system prune -a`、改动 `~/` 下与本项目无关的配置
- ❌ 在代码、日志、commit message、文档里出现密钥 / token / 真实密码 / 内网地址
- ❌ 编造 API、库函数、命令输出、测试结果；**不确定就说不确定，然后去验证**
- ❌ 修改 `migrations/` 里**已经应用过**的迁移文件（只能追加新迁移）
- ❌ 在未告知的情况下安装系统级软件、改 shell 配置、改全局 `go env`
- ❌ 用 `git checkout -- <file>` / `git restore <file>` 去还原**未提交**的文件——它会永久丢掉工作区改动。
  需要"临时改坏文件做验证"（如变异测试）时，先 `cp` 到 `/tmp` 备份，改完再 `cp` 回来
  （2026-10-14 踩过：变异测试后用 `git checkout` 还原，把尚未提交的 `Database` 实现全丢了）
- ❌ 把「用户没问的东西」一次性做大（顺手加个 Redis、顺手接个监控）——先问
- ❌ 把仓库外的个人文件提交进本仓库

---

## 6. 交付与提交规范

- commit message：`<type>(<scope>): <中文说明>`，`type ∈ feat | fix | refactor | test | docs | chore | build | perf | revert`
  - 例：`feat(apid): 新增 /healthz 与优雅退出`
- 每个 commit 必须：能编译 + 测试通过；做不到就用 `wip(scope): ...` 并在正文说明差在哪
- 一次提交只做一件事；**不要把用户的半成品和 agent 的改动混在一个 commit 里**
- 有取舍的实现写 `docs/adr/NNN-标题.md`：背景 / 选项 / 决定 / 代价
- 推送前必须确认：`git status` 干净、无密钥、无仓库外文件

---

## 7. 项目速查

**当前进度（agent 每次开工前先核对，过时就要更新）**

- 仓库已完成整理：移出学习用的 `labs/` 与个人规划文档，只保留生产项目内容
- 工程骨架（`5c500cf`）：`cmd/apid`、`internal/{config,apperr,observability,version,transport/rest}`；
  管道代码 `Makefile`、`configs/config.yaml`、`internal/version` 已就绪
- ✅ `internal/apperr`（`037f7be`）：业务错误与错误码，含表驱动单测
- ✅ `internal/transport/rest/respond`（`87e91b4`）：`JSON` / `Error` / `StatusForCode` / `FromError`，
  4xx 回显业务 message、5xx 固定文案且原因只进日志（有测试钉死"响应体不泄露内部信息"）
- ✅ `internal/config`（`00cf995`）：三层加载（默认值 < YAML < 环境变量）+ `errors.Join` 全量校验；
  强类型取值集合 `Env`/`LogLevel`/`LogFormat` + `Valid()`；**默认值一致性由 `TestConfigFileMatchesDefaults` 钉死**
- ✅ `internal/observability`（`96ab940`）：`NewLogger`（json/text + 级别过滤）、`traceHandler` 自动注入
  `trace_id`、`WithTraceID`/`TraceIDFrom`/`NewTraceID`（16 位十六进制，hex 编码）；7 组单测
- ✅ `internal/transport/rest/middleware`（`41de9c1`）：**用户实现**了 recorder / RequestID / InFlight /
  AccessLog 骨架；agent 经明确委派补完 AccessLog 的日志行与 status 兜底、Recover，并补 8 组单测
- ✅ `internal/transport/rest/server.go`（`8c2f448`）：**用户实现** `New` / `routes` / `handleHealthz` /
  `handleDebugSleep` / `handleNotFound` / `Start` / `Shutdown`；agent 做了一轮检查优化
  （copylocks、边界、日志去重、注释整理）+ `server_test.go`（14 个用例，含优雅退出与边界，
  经变异测试确认可捕获回归）
- ✅ `cmd/apid`（本次提交）：6 步装配（配置 → logger + SetDefault → server → 信号 → 优雅退出）。
  **服务已可真实运行**：`make build && KF_SERVER_ADDR=:18080 ./bin/apid`
- ✅ `README.md`：已更新为「可运行」状态，含快速开始、真实验收输出、配置与接口说明、实现状态
- ✅ 历史清理（2026-10-14）：用 `git filter-branch --prune-empty` 清除历史中的 `labs/` 与个人文档，
  39 → 35 个提交，**最终代码树逐字节未变**（`git diff backup/pre-history-clean main` 为空）
- ✅ 本地 MySQL 8 编排（`cfa8989`）：`deploy/docker-compose.yml` + 真实健康检查；
  密码走 `deploy/.env`（已 gitignore），仓库只留 `.env.example`。实测 8.4.11 healthy
- ✅ 迁移工具选型（`3098e90` 选 goose → **同日改选 `golang-migrate`**，见
  `docs/adr/001-迁移工具选型.md`（已标注被取代）与 `docs/adr/002-迁移工具改选-golang-migrate.md`。
  改选理由：决策者预期后续有多数据库需求。实测代价：模块图 209 vs 95、go.sum 68 vs 10 行，
  **但二进制几乎一样（3.9M vs 3.7M）**——成本在供应链面，不在运行时
- ✅ M2 数据模型决策（用户 2026-10-14 确认）：① **同名不建唯一约束**（软删 + 唯一约束会逼着每条
  查询都带 `deleted_at` 过滤，而"同名"尚非确定需求；加索引是向后兼容 DDL，随时能补）
  ② M2 查询清单 7 条（知识库 CRUD + 软删 + 文档关联增删 + 按知识库列文档分页）
  ③ **同意引入 `github.com/google/uuid`**（用 `NewV7()`，时间有序避免 InnoDB 页分裂）
  ④ **主键定 `BINARY(16)`**（2026-10-14 拍板，推翻早先的 `CHAR(36)`）：按"数据极限多"的假设，
  InnoDB 二级索引叶子都存主键，CHAR(36) 每条多 36 字节（10 亿行 × 3 索引 ≈ 多 100GB）；
  且大表上 CHAR→BINARY 是重建表+全部索引的不可逆 DDL，不能"以后再迁"
  ⑤ **分页定 keyset**（不用 OFFSET）：游标为 `(created_at, id)` 复合；SQL 必须写**展开形式**
  `created_at < ? OR (created_at = ? AND id < ?)` —— 实测行构造器 `(a,b) < (?,?)` 会退化成
  `type=index` 全索引扫描（靠 LIMIT 提前停），而展开形式是 `type=range`；对照 OFFSET 4000 要扫 4020 行
  ⑥ **不加外键**（大厂惯例）：FK 的写入放大/跨分片不可行/大表 DDL 灾难；一致性靠 repository 校验 + 对账
  ⑦ M2 字段全上：collections(description)、documents(source_uri/mime_type/size_bytes)
- ✅ config 的 `database` 段（`60d7411`）：9 个 `KF_DATABASE_*` env、7 条校验（含 max_idle≤max_open
  跨字段不变量）、密码脱敏（`LogValue`+`String`）、`yaml:"-" json:"-"`；新测试覆盖默认值/env/校验/
  YAML 写 password 被拒/脱敏 3 路径，并经变异测试确认可捕获回归
- ✅ `internal/database`（本次提交）：`DSN()` / `MigrateDSN()` / `Open()` 实现完成。
  4 个单测 + 1 个集成测试（`//go:build integration`）。踩过并修掉：
  ① `Open` 误用 `MigrateDSN` → 业务连接开了 multiStatements（集成测试+变异验证可捕获）
  ② `cfg.LogValue()` 直接当 slog 参数 → `!BADKEY` 键名（vet 也报），改 `slog.Any`
- ✅ `migrations/0001_init.{up,down}.sql`（agent 代写，用户委派）：collections / documents /
  collection_documents 三张表，BINARY(16) 主键、DATETIME(3) 存 UTC、软删、无外键、keyset 索引。
  已在真实 MySQL 8.4 上用临时表验证：索引列序正确；"某知识库下文档"的 keyset+JOIN 走
  `idx_collection_created` 覆盖索引（`Using index`，无回表）+ `eq_ref` 关联，无 filesort。
  ⚠️ 文件名必须是 `{版本}_{描述}.up.sql` —— 不匹配的会被 migrate **静默跳过**
- ✅ 迁移接入（`40b8c1c`）：`migrations/embed.go`（`//go:embed *.sql`）+ `cmd/migrate`
  （up / version / down -y / force <n>；`iofs` + `mysql.WithInstance`，迁移连接用 `MigrateDSN`）。
  实测：up → version=1；再 up → 无需迁移；down -y → 只剩 schema_migrations；再 up 恢复；
  人为 dirty=1 → version 报错退出 → force 1 修复。`migrations/embed_test.go` 钉住文件名规则。
  0002_purge_indexes 已追加并在真实 MySQL 8.4 上应用，version=2、dirty=false
- ✅ repository（`internal/knowledge`，`e205616`）：model.go（Collection/Cursor/PATCH 语义）、
  repository.go（接口）、mysql.go（Create / Get / List keyset / Update PATCH / SoftDelete）+ 集成测试。
  关键边界：cursor.ID 传 `id[:]`、分页 `limit+1`、PATCH 用事务 + `FOR UPDATE`、软删看 `RowsAffected`
- ✅ service（`internal/knowledge/service.go`）：校验（name trim、rune 长度、limit 1..100、uuid.Nil）、
  `ErrNotFound → not_found`、其他错误 → `internal_error`；表驱动单测 + fake repository，覆盖率 100%
- ✅ HTTP handler（`internal/transport/rest/collection.go`）：`/api/v1/collections` 五个端点、
  cursor base64url（8B ms + 16B UUID）、`httptest` 覆盖 201/200/204/400/404/500 与客户端取消
- ✅ `cmd/apid` 装配 DB → repository → service → server；启动连不上 MySQL 直接失败
- ✅ M2 collections CRUD 完成
- ✅ M3 documents（本轮）：`document_repository.go` 契约（`77e118d`）+ `mysql_documents.go`
  （Create/Get/Update PATCH/SoftDelete + 关联增删 + keyset 列表；1062 → `ErrConflict`）+
  `document_service.go`（title/source_uri/mime_type/size_bytes 校验、404/409 映射，单测覆盖率 100%）+
  `rest/document.go`（documents 4 个端点 + 关联 3 个端点，PATCH 三态解码）。
  集成测试覆盖 CRUD / 重复关联 / 软删过滤 / 同毫秒分页；真实 MySQL `EXPLAIN` 走
  `idx_collection_created`（Using index + Backward index scan，无 filesort）。
  add 关联只读 collection，用 `FOR SHARE` 共享锁；document 仍 `FOR UPDATE`（软删互斥）
- ✅ 软删恢复（本轮）：documents / collections 的 `/restore`，事务 + `SELECT ... FOR UPDATE`；
  deleted_at NULL → 409，物理删除/不存在 → 404；关联保持原样
- ✅ 软删物理清理（本轮重构）：`migrations/0002_purge_indexes`（`(deleted_at, id)`）+
  `internal/purge`（按父行短事务、先锁父行再删关联、dry-run、批间 sleep、幂等）+
  `cmd/purge`（retention / batch / interval / dry-run / timeout）。restore 与 purge 通过
  同一父行锁串行化；真实 MySQL 验收：过期/孤儿行清零，保留期内数据与活跃关联保留
- ✅ M4.1 正文入库（0003 迁移 / chunker / repository / service / `PUT /documents/{id}/content` +
  2 MiB 请求体上限）；purge 同步删除 `document_contents` / `document_chunks`；
  真实 MySQL + HTTP E2E 验收通过
- ✅ M4.2 MySQL FULLTEXT 检索 MVP（chunk 级；GET /api/v1/search；2..256 rune、
  offset 窗口 1000；service/handler/集成测试/真实 curl + EXPLAIN）；已知英文 stopword/
  短词限制见 ADR 005
- ✅ M4.3 单轮 RAG Agent（DeepSeek OpenAI-compatible；`POST /api/v1/agent/chat`；
  citations / 配置 / service/handler；fake-provider + 真实 DeepSeek E2E 均通过；
  Agent 路由单独延长写超时，12s 慢 provider 验证通过）
- ✅ M4.4a 本地向量基础设施：Ollama + Qdrant compose profile、BGE-M3 1024 维、
  Embedder 接口 + Ollama 客户端、固定评测集 + FTS/vector baseline（0.850/0.818 vs 1.000/0.950）
- ✅ M4.4b `0004_embedding_jobs` + 正文事务 outbox + `cmd/embed` worker +
  Qdrant REST 写入/删除；真实 MySQL/Ollama/Qdrant E2E 通过
- ✅ M4.4c VectorRetriever + HybridRetriever（RRF）+ `KF_RETRIEVAL_MODE` +
  Agent 接入；FTS 无结果、Hybrid 补回正确 citation 的真实 E2E 通过
- ✅ M4.4d `0005_vector_cleanup_jobs` + `cmd/vector-clean`：purge 后异步删除 Qdrant 向量，
  pending→done、points 1→0 真实 E2E 通过
- ✅ 生产化基线：API key 鉴权、`/readyz`、安全响应头、HTTP `/metrics`、Dockerfile、生产 Compose、
  GitHub Actions、集成覆盖率 85% 门槛、部署/运维手册
- ⬜ 下一步：M4.5 生产 embedding provider / rerank / 全量 Hybrid 评测 / 指标与限流
- 错误码归属已定：业务码在 `apperr`，传输层码在 `respond`，映射只在 `respond.StatusForCode`
- 配置约定：`configs/config.yaml` 必须与 `config.Default()` 一致；本地要改配置用
  `KF_CONFIG_FILE=configs/config.local.yaml`（已 gitignore），**不要**改那份提交的文件

**常用命令**

```bash
make help                  # 查看全部工程命令
make check                 # 提交前一键检查：fmt + vet + lint + test -race
make lint                  # 只跑 golangci-lint（配置 .golangci.yml，v2.14.0）
make run                   # 本地起服务（默认 :8080；需要 MySQL + KF_DATABASE_*）
make build                 # 编译到 bin/apid，版本号由 git + ldflags 注入
KF_SERVER_ADDR=:18080 ./bin/apid   # 环境变量覆盖配置（验证配置分层）
curl -s localhost:8080/healthz     # 健康检查：版本号 / 启动时间 / 在途请求数
go run ./cmd/migrate up             # 应用迁移（当前 version=5）
go run ./cmd/purge -dry-run         # 软删清理预演；确认后去掉 -dry-run
make ai-up                          # 启动本地 Ollama + Qdrant（M4.4）
make ai-model                       # 拉取 bge-m3（约 1.2 GB）
make embed-eval                     # 纯向量评测（需要 Ollama + bge-m3）
make embed-once                     # 跑一次 embedding worker（需要 Ollama + Qdrant）
make vector-clean-once              # 跑一次 Qdrant 清理 worker（需要 Qdrant）
KF_RETRIEVAL_MODE=hybrid make run   # 用 Hybrid 检索（默认 fts）
make e2e                            # 全链路 E2E（真实 MySQL/Qdrant，fake 模型）
make coverage                       # 内部包覆盖率（门槛 85%，需要 MySQL/Qdrant）
make docker-build                   # 生产镜像
```

**待处理事项（agent 可在合适时机提醒，但不要擅自替用户决定）**

1. ✅ **推送已完成**（2026-10-14）：历史清理成果已推送（用户配好凭据后执行 `--force-with-lease`），
   随后 agent 补推 3 个提交。核对结果：远端 `main` = 本地 HEAD = `cc58ae0`，40 个提交，
   根提交为 `5c500cf`，**远端历史中已无 `labs/`、xlsx 与个人文档**。
   凭据走 `git config credential.helper store`（PAT 明文存 `~/.git-credentials`，用户自行选择的方式）。
   之后推送：`git push origin main` 即可（已能快进）。
2. **历史清理的恢复点仍在本地**（可择机回收）：
   `backup/pre-history-clean`（清理前的完整 39 提交历史）与 `refs/original/refs/heads/main`
   （filter-branch 自动留下的）。仓库外的 `~/knowflow-labs`(13 文件) 与 `~/knowflow-plan`(7 文件)
   保存着 labs 与个人文档的实体，**长期保留**。
   确认无需回滚后可回收：
   `git branch -D backup/pre-history-clean && git update-ref -d refs/original/refs/heads/main && git gc --prune=now`

3. ✅ M1 全部完成（config / observability / middleware / rest / cmd/apid，服务可运行）。
   ✅ M2 collections CRUD 完成：database → migrate/0001 → repository → service → handler → cmd/apid。
   ✅ M3 documents 完成：repository + service + handler + 集成测试 + 真实 curl/EXPLAIN 验收。
   ✅ 软删恢复/清理完成：restore 行锁 + 0002 索引 + `internal/purge` + `cmd/purge`，真实 MySQL 验收。
   ✅ M4.1 正文入库完成：0003 + chunker + repository + service + `PUT /content` + purge 子表清理。
   ✅ M4.2 MySQL FULLTEXT 检索 MVP 完成：chunk 级 `/api/v1/search` + 集成测试 + E2E。
   ✅ M4.3 单轮 RAG Agent 完成：Retriever 抽象 + DeepSeek OpenAI-compatible + citations；
   真实 DeepSeek HTTP E2E 通过（约 0.7s）。
   ✅ M4.4a 本地向量基础设施完成：Ollama + Qdrant、BGE-M3、固定评测集、FTS/vector baseline。
   ✅ M4.4b embedding worker 完成：0004 outbox + `cmd/embed` + Qdrant 写入/删除。
   ✅ M4.4c Vector/Hybrid 检索完成：RRF、检索模式开关、Agent 接入、真实 E2E。
   ✅ M4.4d Qdrant 清理完成：0005 outbox + `cmd/vector-clean`。
   ✅ 生产化基线完成：鉴权、就绪探针、安全头、Docker/Compose、CI、覆盖率、部署/运维文档。
   ⬜ **下一步**：M4.5 生产 embedding provider、rerank、全量 Hybrid 评测、指标/限流。
   已踩过的坑记录在提交里：`Shutdown` 必须用 `context.WithoutCancel`（信号 ctx 已取消，
   直接做超时会让"等请求处理完"变成"立刻关掉"）；`atomic.Int64` 不能复制（go vet copylocks）
4. ✅ `README.md` 已更新（本次提交）：状态、快速开始、真实输出、配置、接口、实现状态齐备
5. ✅ golangci-lint 已装（v2.14.0，`~/go/bin/`），`make lint` 已接入 `make check`；
   server.go 的 `unused` 临时豁免已随实现完成删除（`8592751`）
6. ✅ 迁移工具已拍板并接入：**`golang-migrate`**（`docs/adr/002-迁移工具改选-golang-migrate.md`）。
   `source/iofs` 构造 embed 源、DSN 多语句用 `multiStatements=true`、dirty 用 `Force(version)` 修；
   `cmd/migrate` 已可用，`0001_init` 已在真实 MySQL 8.4 验证。
