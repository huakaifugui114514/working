package middleware

import "net/http"

// SecurityHeaders 给所有响应加一组低风险的安全头。
//
// 这是 JSON API，不渲染 HTML，所以 CSP 可以收紧到 default-src 'none'；
// HSTS 不在这里加：它要求 HTTPS，而 TLS 可能由外层网关终止，应用层不能假设。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
