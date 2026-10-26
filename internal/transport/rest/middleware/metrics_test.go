package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huakaifugui114514/working/internal/observability"
)

func TestMetricsMiddlewareNormalizesPath(t *testing.T) {
	m := observability.NewMetrics()
	h := Metrics(m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/collections/11111111-1111-1111-1111-111111111111/documents", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	var buf strings.Builder
	if err := m.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	if !strings.Contains(buf.String(), `path="/api/v1/collections/{id}/documents"`) {
		t.Fatalf("路径未归一化:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `status="404"`) {
		t.Fatalf("状态码未记录:\n%s", buf.String())
	}
}
