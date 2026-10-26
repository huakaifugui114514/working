package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIKeyDisabled(t *testing.T) {
	h := APIKey("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/collections", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestAPIKeyAcceptsHeaders(t *testing.T) {
	h := APIKey("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name   string
		header string
		value  string
	}{
		{name: "Authorization Bearer", header: "Authorization", value: "Bearer secret"},
		{name: "X-API-Key", header: HeaderAPIKey, value: "secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/collections", nil)
			req.Header.Set(tt.header, tt.value)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
		})
	}
}

func TestAPIKeyRejects(t *testing.T) {
	h := APIKey("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler 不应被调用")
	}))
	tests := []struct {
		name  string
		value string
	}{
		{name: "缺失"},
		{name: "错误", value: "wrong"},
		{name: "空 Bearer", value: "Bearer "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/collections", nil)
			if tt.value != "" {
				req.Header.Set("Authorization", tt.value)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("解析响应: %v", err)
			}
			if body.Error.Code != "unauthorized" {
				t.Fatalf("code = %q", body.Error.Code)
			}
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("缺少 WWW-Authenticate")
			}
		})
	}
}

func TestAPIKeyBypassesHealth(t *testing.T) {
	h := APIKey("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	for _, key := range []string{
		"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy",
		"Permissions-Policy", "Content-Security-Policy",
	} {
		if rec.Header().Get(key) == "" {
			t.Errorf("缺少安全头 %s", key)
		}
	}
}
