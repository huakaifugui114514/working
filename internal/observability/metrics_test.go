package observability

import (
	"bytes"
	"strings"
	"testing"
)

func TestMetricsPrometheus(t *testing.T) {
	m := NewMetrics()
	m.Observe("GET", "/healthz", 200)
	m.Observe("GET", "/healthz", 200)
	m.Observe("POST", "/api/v1/collections/{id}", 401)

	var buf bytes.Buffer
	if err := m.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"# TYPE http_requests_total counter",
		`http_requests_total{method="GET",path="/healthz",status="200"} 2`,
		`http_requests_total{method="POST",path="/api/v1/collections/{id}",status="401"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics 缺少 %q:\n%s", want, out)
		}
	}
}

func TestMetricsNil(t *testing.T) {
	var m *Metrics
	m.Observe("GET", "/", 200)
	if err := m.WritePrometheus(&bytes.Buffer{}); err != nil {
		t.Fatalf("nil metrics: %v", err)
	}
}
