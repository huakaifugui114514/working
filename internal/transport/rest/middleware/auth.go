package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/transport/rest/respond"
)

// HeaderAPIKey 是 API key 的备用传递头。
//
// 同时支持 Authorization: Bearer <key> 与 X-API-Key：前者是标准用法，
// 后者方便不支持自定义 Authorization 的网关/curl 场景。
const HeaderAPIKey = "X-API-Key"

// APIKey 校验 API key。expected 为空表示关闭鉴权（仅 dev/test；prod 由配置校验拦截）。
//
// /healthz 与 /readyz 不鉴权：探针由编排系统调用，不应持有业务密钥。
// 用 subtle.ConstantTimeCompare，避免按字节短路比较泄露 key 前缀。
func APIKey(expected string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if expected == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				next.ServeHTTP(w, r)
				return
			}

			provided := r.Header.Get(HeaderAPIKey)
			if provided == "" {
				if raw := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(raw), "bearer ") {
					provided = strings.TrimSpace(raw[len("Bearer "):])
				}
			}
			if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="knowflow"`)
				respond.Error(w, nil, http.StatusUnauthorized, apperr.CodeUnauthorized, "缺少或错误的 API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
