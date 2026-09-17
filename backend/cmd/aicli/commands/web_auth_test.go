package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withTestWebToken 设置测试令牌并在结束后恢复（避免污染其他用例）。
func withTestWebToken(t *testing.T, token string) {
	t.Helper()
	old := ChatWebAuthToken()
	setChatWebAuthTokenForTest(token)
	t.Cleanup(func() { setChatWebAuthTokenForTest(old) })
}

func guardProbeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
}

func TestChatWebAuthGuardHostAndOrigin(t *testing.T) {
	withTestWebToken(t, "test-token")

	tests := []struct {
		name       string
		method     string
		host       string
		origin     string
		token      string
		queryToken string
		wantStatus int
	}{
		{"loopback host ok", http.MethodGet, "127.0.0.1:12345", "", "", "", http.StatusOK},
		{"localhost host ok", http.MethodGet, "localhost:12345", "", "", "", http.StatusOK},
		{"ipv6 loopback ok", http.MethodGet, "[::1]:12345", "", "", "", http.StatusOK},
		{"non-loopback host rejected", http.MethodGet, "evil.com:12345", "", "", "", http.StatusForbidden},
		{"dns rebinding host rejected", http.MethodPost, "evil.com", "", "t", "", http.StatusForbidden},
		{"cross origin rejected", http.MethodPost, "127.0.0.1:12345", "https://evil.com", "test-token", "", http.StatusForbidden},
		{"same origin accepted", http.MethodPost, "127.0.0.1:12345", "http://127.0.0.1:12345", "test-token", "", http.StatusOK},
		{"write without token rejected", http.MethodPost, "127.0.0.1:12345", "", "", "", http.StatusForbidden},
		{"write with header token accepted", http.MethodPost, "127.0.0.1:12345", "", "test-token", "", http.StatusOK},
		{"write with wrong token rejected", http.MethodPost, "127.0.0.1:12345", "", "nope", "", http.StatusForbidden},
		{"write with query token accepted", http.MethodPost, "127.0.0.1:12345", "", "", "test-token", http.StatusOK},
		{"read without token accepted", http.MethodGet, "127.0.0.1:12345", "", "", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := "http://" + tt.host + "/web/api/invoke"
			if tt.queryToken != "" {
				target += "?token=" + tt.queryToken
			}
			req := httptest.NewRequest(tt.method, target, nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.token != "" {
				req.Header.Set(ChatWebAuthTokenHeader, tt.token)
			}
			rec := httptest.NewRecorder()
			ChatWebAuthGuard(guardProbeHandler())(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// 令牌未初始化（同包单测直接调用处理器）时不强制写令牌，但 Host/Origin 仍生效。
func TestChatWebAuthGuardWithoutTokenAllowsWrites(t *testing.T) {
	withTestWebToken(t, "")
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1/web/api/input", nil)
	req.Host = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	ChatWebAuthGuard(guardProbeHandler())(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestChatWebInjectAuthToken(t *testing.T) {
	withTestWebToken(t, "abc123")
	html := []byte("<html><head><title>x</title></head><body></body></html>")
	out := string(chatWebInjectAuthToken(html))
	if !strings.Contains(out, `name="aicli-web-token" content="abc123"`) {
		t.Fatalf("meta tag missing: %s", out)
	}
	if !strings.Contains(out, "X-AICLI-Token") {
		t.Fatalf("fetch wrapper missing: %s", out)
	}
	if strings.Index(out, "aicli-web-token") > strings.Index(out, "</head>") {
		t.Fatalf("snippet must be injected before </head>")
	}

	// 无令牌时原样返回（不注入空 meta）。
	withTestWebToken(t, "")
	if got := string(chatWebInjectAuthToken(html)); got != string(html) {
		t.Fatalf("token-less injection changed html: %s", got)
	}
}

func TestChatWebHostIsLoopback(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:1":    true,
		"[::1]:9000":     true,
		"127.0.0.1":      true,
		"LOCALHOST:80":   true,
		"evil.com:80":    false,
		"10.0.0.5:80":    false,
		"":               false,
	}
	for host, want := range tests {
		if got := ChatWebHostIsLoopback(host); got != want {
			t.Fatalf("ChatWebHostIsLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}
