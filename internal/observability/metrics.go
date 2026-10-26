package observability

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Metrics 是进程内最小 HTTP 指标注册表。
//
// 不引 Prometheus client：当前只需要“请求数按 method/path/status 计数”，
// 标准库足够；以后要直方图/Exemplar 再评估依赖。
type Metrics struct {
	mu       sync.Mutex
	requests map[metricKey]uint64
	llmStats func() map[string]LLMStat
}

// LLMStat 是模型网关的 provider 统计快照。
type LLMStat struct {
	Requests         int
	Errors           int
	PromptTokens     int
	CompletionTokens int
}

// SetLLMStats 注入 LLM 统计回调；回调在 /metrics 抓取时调用。
func (m *Metrics) SetLLMStats(fn func() map[string]LLMStat) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.llmStats = fn
	m.mu.Unlock()
}

type metricKey struct {
	method string
	path   string
	status int
}

// NewMetrics 创建空指标表。
func NewMetrics() *Metrics {
	return &Metrics{requests: make(map[metricKey]uint64)}
}

// Observe 记录一次请求完成。
func (m *Metrics) Observe(method, path string, status int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.requests[metricKey{method: method, path: path, status: status}]++
	m.mu.Unlock()
}

// WritePrometheus 输出 Prometheus text exposition 格式。
func (m *Metrics) WritePrometheus(w io.Writer) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	keys := make([]metricKey, 0, len(m.requests))
	values := make(map[metricKey]uint64, len(m.requests))
	for k, v := range m.requests {
		keys = append(keys, k)
		values[k] = v
	}
	m.mu.Unlock()

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].path != keys[j].path {
			return keys[i].path < keys[j].path
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].status < keys[j].status
	})

	if _, err := fmt.Fprintln(w, "# HELP http_requests_total HTTP 请求数"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "# TYPE http_requests_total counter"); err != nil {
		return err
	}
	// 先在锁内取回调，避免写 metrics 时持有锁调用网关。
	m.mu.Lock()
	llmStats := m.llmStats
	m.mu.Unlock()
	for _, k := range keys {
		path := strings.ReplaceAll(strings.ReplaceAll(k.path, `\`, `\\`), `"`, `\"`)
		if _, err := fmt.Fprintf(w,
			"http_requests_total{method=%q,path=%q,status=%q} %d\n",
			k.method, path, fmt.Sprintf("%d", k.status), values[k]); err != nil {
			return err
		}
	}

	if llmStats == nil {
		return nil
	}
	stats := llmStats()
	names := make([]string, 0, len(stats))
	for name := range stats {
		names = append(names, name)
	}
	sort.Strings(names)
	if _, err := fmt.Fprintln(w, "# HELP llm_requests_total LLM provider 请求数"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "# TYPE llm_requests_total counter"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "# TYPE llm_errors_total counter"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "# TYPE llm_tokens_total counter"); err != nil {
		return err
	}
	for _, name := range names {
		st := stats[name]
		label := strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), `"`, `\"`)
		if _, err := fmt.Fprintf(w, "llm_requests_total{provider=%q} %d\n", label, st.Requests); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "llm_errors_total{provider=%q} %d\n", label, st.Errors); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "llm_tokens_total{provider=%q,kind=%q} %d\n", label, "prompt", st.PromptTokens); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "llm_tokens_total{provider=%q,kind=%q} %d\n", label, "completion", st.CompletionTokens); err != nil {
			return err
		}
	}
	return nil
}
