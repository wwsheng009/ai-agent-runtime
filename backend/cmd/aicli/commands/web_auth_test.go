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

// --- 非回环模式 (non-loopback / --web-host 0.0.0.0) ---

// withTestWebLoopbackMode 切换回环/非回环模式并在结束后恢复。
func withTestWebLoopbackMode(t *testing.T, loopback bool) {
	t.Helper()
	old := IsChatWebLoopbackMode()
	setChatWebLoopbackModeForTest(loopback)
	t.Cleanup(func() { setChatWebLoopbackModeForTest(old) })
}

// TestChatWebAuthGuardNonLoopbackMode 验证非回环模式下所有请求都需要令牌，
// Host/Origin 被放宽，跨网络访问仍受令牌保护。
func TestChatWebAuthGuardNonLoopbackMode(t *testing.T) {
	withTestWebToken(t, "lan-token")
	withTestWebLoopbackMode(t, false)

	tests := []struct {
		name       string
		method     string
		host       string
		origin     string
		token      string
		queryToken string
		wantStatus int
	}{
		{"GET without token rejected", http.MethodGet, "192.168.1.5:8080", "", "", "", http.StatusForbidden},
		{"GET with header token accepted", http.MethodGet, "192.168.1.5:8080", "", "lan-token", "", http.StatusOK},
		{"GET with query token accepted", http.MethodGet, "0.0.0.0:8080", "", "", "lan-token", http.StatusOK},
		{"POST without token rejected", http.MethodPost, "192.168.1.5:8080", "", "", "", http.StatusForbidden},
		{"POST with header token accepted", http.MethodPost, "192.168.1.5:8080", "", "lan-token", "", http.StatusOK},
		{"non-loopback host accepted (with token)", http.MethodGet, "evil.com:12345", "", "lan-token", "", http.StatusOK},
		{"cross-origin accepted (with token)", http.MethodPost, "192.168.1.5:8080", "https://evil.com", "lan-token", "", http.StatusOK},
		{"wrong token rejected", http.MethodGet, "192.168.1.5:8080", "", "nope", "", http.StatusForbidden},
		{"HEAD without token rejected", http.MethodHead, "192.168.1.5:8080", "", "", "", http.StatusForbidden},
		{"HEAD with token accepted", http.MethodHead, "192.168.1.5:8080", "", "lan-token", "", http.StatusOK},
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

// TestChatWebAuthGuardNonLoopbackModeWithoutToken 验证非回环模式下即使未预设
// 令牌，所有请求仍被拒绝（防御默认拒绝）。
func TestChatWebAuthGuardNonLoopbackModeWithoutToken(t *testing.T) {
	withTestWebToken(t, "")
	withTestWebLoopbackMode(t, false)
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.5:8080/web/api/status", nil)
	req.Host = "192.168.1.5:8080"
	rec := httptest.NewRecorder()
	ChatWebAuthGuard(guardProbeHandler())(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (non-loopback should always require token)", rec.Code)
	}
}

// TestChatWebInjectAuthTokenNonLoopback 验证非回环模式下页面注入额外的
// __aicli_non_loopback 标志，前端 JS据此决定为所有请求附加令牌。
func TestChatWebInjectAuthTokenNonLoopback(t *testing.T) {
	withTestWebToken(t, "lan-token")
	withTestWebLoopbackMode(t, false)
	html := []byte("<html><head><title>x</title></head><body></body></html>")
	out := string(chatWebInjectAuthToken(html))
	if !strings.Contains(out, `window.__aicli_non_loopback = true;`) {
		t.Fatalf("__aicli_non_loopback flag not injected: %s", out)
	}
	// fetch wrapper 条件应包含非回环判断
	if !strings.Contains(out, "window.__aicli_non_loopback") {
		t.Fatalf("fetch wrapper missing non-loopback check: %s", out)
	}
}

// TestChatWebTokenQueryParam 验证回环模式与非回环模式下查询参数后缀的构建。
func TestChatWebTokenQueryParam(t *testing.T) {
	withTestWebToken(t, "tok-abc123")

	// 回环模式 → 空串
	withTestWebLoopbackMode(t, true)
	if got := ChatWebTokenQueryParam(); got != "" {
		t.Fatalf("loopback mode: want empty query param, got %q", got)
	}

	// 非回环模式 → ?token=tok-abc123
	withTestWebLoopbackMode(t, false)
	if got := ChatWebTokenQueryParam(); got != "?token=tok-abc123" {
		t.Fatalf("non-loopback mode: want ?token=tok-abc123, got %q", got)
	}

	// 非回环模式 + 令牌为空 → 空串（避免泄露空令牌）
	withTestWebToken(t, "")
	if got := ChatWebTokenQueryParam(); got != "" {
		t.Fatalf("non-loopback empty token: want empty, got %q", got)
	}
}

// TestChatWebAuthGuardNonLoopbackStaticAssets 验证非回环模式下静态资产
// （/web/style.css、/web/app.js、/favicon.ico 等）允许无令牌 GET/HEAD，
// 而 API 接口（/web/api/*）、调试端点（/debug/*）仍需令牌。
func TestChatWebAuthGuardNonLoopbackStaticAssets(t *testing.T) {
	withTestWebToken(t, "lan-token")
	withTestWebLoopbackMode(t, false)

	tests := []struct {
		name       string
		path       string
		method     string
		token      string
		wantStatus int
	}{
		{"static CSS without token allowed", "/web/style.css", http.MethodGet, "", http.StatusOK},
		{"static JS without token allowed", "/web/app.js", http.MethodGet, "", http.StatusOK},
		{"static HTML without token rejected", "/web/", http.MethodGet, "", http.StatusForbidden},
		{"static HTML with token accepted", "/web/", http.MethodGet, "lan-token", http.StatusOK},
		{"favicon without token allowed", "/favicon.ico", http.MethodGet, "", http.StatusOK},
		{"POST to static asset without token rejected", "/web/style.css", http.MethodPost, "", http.StatusForbidden},
		{"API GET without token rejected", "/web/api/status", http.MethodGet, "", http.StatusForbidden},
		{"API POST without token rejected", "/web/api/invoke", http.MethodPost, "", http.StatusForbidden},
		{"API GET with token accepted", "/web/api/status", http.MethodGet, "lan-token", http.StatusOK},
		{"debug endpoint without token rejected", "/debug/pprof/heap", http.MethodGet, "", http.StatusForbidden},
		{"debug endpoint with token accepted", "/debug/pprof/heap", http.MethodGet, "lan-token", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://0.0.0.0:8080"+tt.path, nil)
			req.Host = "0.0.0.0:8080"
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

// TestChatWebLocalAddresses 验证本地地址发现函数返回合理格式的地址。
func TestChatWebLocalAddresses(t *testing.T) {
	addrs := ChatWebLocalAddresses()
	// 在 CI/容器环境中可能没有非 loop 接口，仅验证不崩溃且不返回 loopback。
	for _, addr := range addrs {
		if addr == "127.0.0.1" || addr == "localhost" {
			t.Fatalf("should not contain loopback address: %s", addr)
		}
		if strings.Contains(addr, "127.") {
			t.Fatalf("should not contain loopback IPv4: %s", addr)
		}
	}
}

// TestChatWebDevMode 验证开发模式下回环模式会跳过写令牌校验。
func TestChatWebDevMode(t *testing.T) {
	withTestWebToken(t, "dev-token")
	withTestWebLoopbackMode(t, true)

	// 普通回环模式：POST 需令牌
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/web/api/invoke", nil)
	req.Host = "127.0.0.1:8080"
	ChatWebAuthGuard(guardProbeHandler())(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dev mode off: POST without token = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// 开启开发模式：POST 不需令牌
	SetChatWebDevMode(true)
	t.Cleanup(func() { SetChatWebDevMode(false) })
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/web/api/invoke", nil)
	req2.Host = "127.0.0.1:8080"
	ChatWebAuthGuard(guardProbeHandler())(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("dev mode on: POST without token = %d, want %d", rec2.Code, http.StatusOK)
	}

	// 开发模式不影响 Host/Origin 校验
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "http://evil.com:8080/web/api/invoke", nil)
	req3.Host = "evil.com:8080"
	ChatWebAuthGuard(guardProbeHandler())(rec3, req3)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("dev mode on: non-loopback host = %d, want %d", rec3.Code, http.StatusForbidden)
	}
}

// TestChatWebNonLoopbackIPBasedAuth 验证非回环模式下基于客户端 IP 的鉴权行为：
// 回环 IP（127.0.0.1/localhost/[::1]）始终跳过令牌校验，
// 本地私有网段 IP 与远程 IP 均需令牌（--web-dev 不影响非回环模式的 IP 分类）。
func TestChatWebNonLoopbackIPBasedAuth(t *testing.T) {
	withTestWebToken(t, "lan-token")
	withTestWebLoopbackMode(t, false)
	// 不启用 dev mode：回环 IP 仍应跳过（ inherently local），非回环 IP 需令牌
	t.Cleanup(func() { SetChatWebDevMode(false) })

	tests := []struct {
		name       string
		remoteAddr string
		path       string
		method     string
		token      string
		wantStatus int
	}{
		{"loopback IPv4 skip token", "127.0.0.1:12345", "/web/api/invoke", http.MethodPost, "", http.StatusOK},
		{"loopback IPv6 skip token", "[::1]:12345", "/web/api/invoke", http.MethodPost, "", http.StatusOK},
		{"127.x loopback skip token", "127.0.0.2:12345", "/web/api/invoke", http.MethodPost, "", http.StatusOK},
		{"192.168 private needs token", "192.168.1.10:12345", "/web/api/invoke", http.MethodPost, "", http.StatusForbidden},
		{"10.x private needs token", "10.0.0.5:12345", "/web/api/invoke", http.MethodPost, "", http.StatusForbidden},
		{"172.16 private needs token", "172.16.0.3:12345", "/web/api/invoke", http.MethodPost, "", http.StatusForbidden},
		{"172.31 private needs token", "172.31.255.254:12345", "/web/api/invoke", http.MethodPost, "", http.StatusForbidden},
		{"172.32 public needs token", "172.32.0.1:12345", "/web/api/invoke", http.MethodPost, "", http.StatusForbidden},
		{"8.8.8.8 public needs token", "8.8.8.8:12345", "/web/api/invoke", http.MethodPost, "", http.StatusForbidden},
		{"8.8.8.8 with token ok", "8.8.8.8:12345", "/web/api/invoke", http.MethodPost, "lan-token", http.StatusOK},
		{"192.168 with token ok", "192.168.1.10:12345", "/web/api/invoke", http.MethodPost, "lan-token", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://0.0.0.0:8080"+tt.path, nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.token != "" {
				req.Header.Set(ChatWebAuthTokenHeader, tt.token)
			}
			rec := httptest.NewRecorder()
			ChatWebAuthGuard(guardProbeHandler())(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

// TestChatWebNonLoopbackStaticAssetAccess 验证非回环模式下：
// index.html 页面本身需要令牌，而 CSS/JS/favicon 允许无令牌。
func TestChatWebNonLoopbackStaticAssetAccess(t *testing.T) {
	withTestWebToken(t, "tok-abc123")
	withTestWebLoopbackMode(t, false)
	// 关闭 dev mode：所有非回环访问都需要令牌
	t.Cleanup(func() { SetChatWebDevMode(false) })

	tests := []struct {
		path       string
		wantStatus int
	}{
		{"/web/", http.StatusForbidden},           // index.html 本身需要令牌
		{"/web/", http.StatusOK},                  // (with token) — will be set below
		{"/web/style.css", http.StatusOK},         // CSS: static, no token needed
		{"/web/app.js", http.StatusOK},            // JS: static, no token needed
		{"/web/js/ui.js", http.StatusOK},          // JS module: static, no token needed
		{"/favicon.ico", http.StatusOK},           // favicon: static, no token needed
		{"/web/api/invoke", http.StatusForbidden}, // API: needs token (POST below)
	}

	for i, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "http://0.0.0.0:8080"+tt.path, nil)
		if i == 1 { // the second /web/ test case, with token
			req.Header.Set(ChatWebAuthTokenHeader, "tok-abc123")
		}
		rec := httptest.NewRecorder()
		ChatWebAuthGuard(guardProbeHandler())(rec, req)
		if rec.Code != tt.wantStatus {
			t.Fatalf("GET %s: status = %d, want %d", tt.path, rec.Code, tt.wantStatus)
		}
	}

	// POST to API without token → 403
	req := httptest.NewRequest(http.MethodPost, "http://0.0.0.0:8080/web/api/invoke", nil)
	rec := httptest.NewRecorder()
	ChatWebAuthGuard(guardProbeHandler())(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /web/api/invoke without token: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestChatWebLocalAddressesIPv6Format 验证 IPv6 地址被 [] 包裹。
func TestChatWebLocalAddressesIPv6Format(t *testing.T) {
	addrs := ChatWebLocalAddresses()
	for _, addr := range addrs {
		// IPv6 地址应被 [] 包裹
		if strings.Count(addr, ":") > 1 {
			if len(addr) < 2 || addr[0] != '[' || addr[len(addr)-1] != ']' {
				t.Fatalf("IPv6 address should be bracketed: %s", addr)
			}
		}
	}
}
