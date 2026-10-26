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
	for _, k := range keys {
		path := strings.ReplaceAll(strings.ReplaceAll(k.path, `\`, `\\`), `"`, `\"`)
		if _, err := fmt.Fprintf(w,
			"http_requests_total{method=%q,path=%q,status=%q} %d\n",
			k.method, path, fmt.Sprintf("%d", k.status), values[k]); err != nil {
			return err
		}
	}
	return nil
}
