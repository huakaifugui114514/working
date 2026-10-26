# ADR 013：LLM 模型网关

- **状态**：已接受并实现
- **日期**：2026-10-27

## 背景

Agent 直接依赖一个 DeepSeek client。provider 超时/限流/故障时请求直接 500，
也没有 token 统计。需要一个最小的模型网关做顺序降级和用量观测。

## 决定

- `internal/llm/gateway` 实现 `agent.LLMClient` + `agent.StreamingLLMClient`：
  - provider 按顺序尝试，第一个成功即返回；
  - Chat / ChatStream 都支持降级；
  - 每个 provider 记录 requests/errors/prompt tokens/completion tokens；
  - 失败错误用 `errors.Join` 汇总，日志带 provider 名。
- 配置：
  - 主 provider：`KF_LLM_BASE_URL / MODEL / API_KEY`；
  - 可选备用：`KF_LLM_FALLBACK_BASE_URL / MODEL / API_KEY`；
  - 备用只配 base 不配 model 会被 Validate 拒绝。
- `cmd/apid` 启动时装配 gateway；退出时输出 token 统计日志。
- Agent 用 gateway 作为 LLMClient，HTTP 契约不变。
- 配额：
  - `KF_LLM_REQUESTS_PER_MINUTE` 滑动窗口；
  - `KF_LLM_TOKENS_PER_DAY` 日累计；
  - 进程内有效，多副本需 Redis/入口限流。
- 语义缓存：
  - `internal/llm/semanticcache`，BGE-M3 向量余弦；
  - 阈值/条目数/TTL 可配；
  - embedding 失败 fail-open；
  - 当前进程内，重启失效。
- Prometheus：
  - `/metrics` 输出 `llm_requests_total` / `llm_errors_total` /
    `llm_tokens_total{provider,kind}`。

## 被否掉的方案

1. **引入 LiteLLM/OpenRouter 代理**：多一个网络跳；当前只需顺序降级。
2. **按权重/延迟路由**：没有真实流量数据，先不做。
3. **语义缓存**：当前用进程内 BGE-M3 余弦；生产多副本应换成 Redis/Qdrant 共享缓存。
4. **配额**：当前进程内滑动窗口；多副本/多租户需要集中式限流。

## 代价

- 顺序降级会在主 provider 超时后才切备用，总延迟叠加；
- 没有熔断/半开，持续故障时每个请求都会先打主 provider；
- token 统计/配额只在进程内，重启归零、多副本不共享；
- 语义缓存也是进程内，重启失效；阈值过低可能返回不合适的旧答案；
- 备用 provider 的模型能力/价格可能不同，prompt 兼容性需人工保证。
