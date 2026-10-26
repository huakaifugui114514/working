// Package agent 实现单轮 RAG Agent：检索 chunk、构造上下文、调用 LLM、返回引用。
//
// 设计见 docs/adr/006-agent-rag.md。
// 关键边界：
//   - Agent 依赖 Retriever / LLMClient 接口，不直接依赖具体搜索或 HTTP 实现；
//   - 当前是单轮，不保存会话；
//   - 没有检索结果时不调用 LLM；
//   - citations 返回本次检索来源，index 对应 prompt 里的 [n]。
package agent

// TODO(m4.3a): 按 ADR 006 实现 Retriever / LLMClient / AgentService。
