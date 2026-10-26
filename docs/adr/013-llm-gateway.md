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

## 被否掉的方案

1. **引入 LiteLLM/OpenRouter 代理**：多一个网络跳；当前只需顺序降级。
2. **按权重/延迟路由**：没有真实流量数据，先不做。
3. **语义缓存**：需要 embedding + 相似度阈值 + 失效策略，当前不引入；否则容易返回旧答案。
4. **配额**：单租户 API key，先靠入口网关；多租户时再做。

## 代价

- 顺序降级会在主 provider 超时后才切备用，总延迟叠加；
- 没有熔断/半开，持续故障时每个请求都会先打主 provider；
- token 统计只在进程内，重启归零；生产应接 Prometheus/日志聚合；
- 备用 provider 的模型能力/价格可能不同，prompt 兼容性需人工保证。
