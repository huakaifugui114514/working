// Package gateway 是多 LLM provider 的路由/降级层。
//
// 当前策略：按顺序尝试，第一个成功即返回；记录每 provider 的请求/token/错误。
// 不做加权路由/配额/语义缓存，这些需要真实流量数据后再定。
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/huakaifugui114514/working/internal/agent"
)

// Provider 是一个命名 LLM 客户端。
type Provider struct {
	Name   string
	Client agent.LLMClient
}

// ProviderStats 是单 provider 的累计统计。
type ProviderStats struct {
	Requests         int
	Errors           int
	PromptTokens     int
	CompletionTokens int
}

// Stats 是并发安全的 token/请求统计。
type Stats struct {
	mu   sync.Mutex
	data map[string]ProviderStats
}

// NewStats 创建空统计。
func NewStats() *Stats { return &Stats{data: map[string]ProviderStats{}} }

func (s *Stats) record(name string, usage agent.TokenUsage, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[name]
	st.Requests++
	if err != nil {
		st.Errors++
	}
	st.PromptTokens += usage.PromptTokens
	st.CompletionTokens += usage.CompletionTokens
	s.data[name] = st
}

// Snapshot 返回统计副本。
func (s *Stats) Snapshot() map[string]ProviderStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]ProviderStats, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// Gateway 实现 agent.LLMClient 和 agent.StreamingLLMClient。
type Gateway struct {
	providers []Provider
	stats     *Stats
	logger    *slog.Logger
}

// New 创建网关；provider 顺序就是降级顺序。
func New(providers []Provider, logger *slog.Logger) (*Gateway, error) {
	if len(providers) == 0 {
		return nil, errors.New("gateway: 至少一个 provider")
	}
	for _, p := range providers {
		if p.Name == "" || p.Client == nil {
			return nil, errors.New("gateway: provider name/client 不能为空")
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Gateway{providers: providers, stats: NewStats(), logger: logger}, nil
}

// Chat 按顺序尝试 provider，失败自动切下一个。
func (g *Gateway) Chat(ctx context.Context, req agent.ChatRequest) (*agent.ChatResponse, error) {
	var errs []error
	for _, p := range g.providers {
		resp, err := p.Client.Chat(ctx, req)
		if err == nil {
			g.stats.record(p.Name, resp.Usage, nil)
			if p.Name != g.providers[0].Name {
				g.logger.Warn("LLM 已降级到备用 provider", "provider", p.Name)
			}
			return resp, nil
		}
		g.stats.record(p.Name, agent.TokenUsage{}, err)
		errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		g.logger.Warn("LLM provider 失败", "provider", p.Name, "error", err)
	}
	return nil, errors.Join(errs...)
}

// ChatStream 优先尝试支持流式的 provider；成功后返回其 stream。
func (g *Gateway) ChatStream(ctx context.Context, req agent.ChatRequest) (agent.ChatStream, error) {
	var errs []error
	for _, p := range g.providers {
		streamer, ok := p.Client.(agent.StreamingLLMClient)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: 不支持流式", p.Name))
			continue
		}
		stream, err := streamer.ChatStream(ctx, req)
		if err == nil {
			g.stats.record(p.Name, agent.TokenUsage{}, nil)
			if p.Name != g.providers[0].Name {
				g.logger.Warn("LLM 流式已降级到备用 provider", "provider", p.Name)
			}
			return stream, nil
		}
		g.stats.record(p.Name, agent.TokenUsage{}, err)
		errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		g.logger.Warn("LLM 流式 provider 失败", "provider", p.Name, "error", err)
	}
	return nil, errors.Join(errs...)
}

// Stats 返回累计统计。
func (g *Gateway) Stats() map[string]ProviderStats { return g.stats.Snapshot() }
