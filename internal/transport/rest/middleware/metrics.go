package middleware

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/observability"
)

// Metrics 记录 HTTP 请求数。路径中的 UUID 会归一化成 {id}，避免 Prometheus 基数爆炸。
func Metrics(metrics *observability.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if metrics == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &recorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			metrics.Observe(r.Method, normalizeMetricPath(r.URL.Path), status)
		})
	}
}

// normalizeMetricPath 把 UUID 段替换成 {id}，例如：
// /api/v1/collections/<uuid>/documents -> /api/v1/collections/{id}/documents
func normalizeMetricPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if _, err := uuid.Parse(part); err == nil {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}
