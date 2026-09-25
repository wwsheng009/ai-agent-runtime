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
	// 令牌取值顺序：meta（**当前**进程注入的权威值）必须先于 sessionStorage
	// 兜底 —— 反序会让进程重启（随机令牌）后的首个请求带旧令牌 → 403。
	getIdx := strings.Index(out, "function getAICLIToken")
	if getIdx < 0 {
		t.Fatalf("fetch wrapper missing getAICLIToken: %s", out)
	}
	body := out[getIdx:]
	metaIdx := strings.Index(body, "readMetaToken()")
	cacheIdx := strings.Index(body, "sessionStorage.getItem(TOKEN_STORAGE_KEY)")
	if metaIdx < 0 || cacheIdx < 0 || metaIdx > cacheIdx {
		t.Fatalf("令牌取值必须 meta 优先、sessionStorage 兜底（meta=%d, cache=%d）", metaIdx, cacheIdx)
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

// TestChatWebAuthGuardNonLoopbackPageCookie 覆盖「页面导航 cookie」例外：
// 地址栏 ?token= 被前端 history.replaceState 抹掉后，F5 / 新标签页 / 标签页恢复
// 这类浏览器自发的文档导航既没有请求头也读不到 sessionStorage，只有 cookie 会
// 自动回传（web_auth.go 文件头「页面刷新为什么需要 cookie」）。
func TestChatWebAuthGuardNonLoopbackPageCookie(t *testing.T) {
	withTestWebToken(t, "nav-cookie-token-01")
	withTestWebLoopbackMode(t, false)

	tests := []struct {
		name       string
		method     string
		path       string
		cookie     string
		token      string
		wantStatus int
	}{
		{"页面导航无凭据被拒（刷新前态）", http.MethodGet, ChatWebPath, "", "", http.StatusForbidden},
		{"页面导航带导航 cookie 放行（F5）", http.MethodGet, ChatWebPath, "nav-cookie-token-01", "", http.StatusOK},
		{"HEAD 导航同样放行", http.MethodHead, ChatWebPath, "nav-cookie-token-01", "", http.StatusOK},
		{"无尾斜杠 /web 同样放行", http.MethodGet, strings.TrimSuffix(ChatWebPath, "/"), "nav-cookie-token-01", "", http.StatusOK},
		{"旧进程遗留 cookie 被拒（令牌已轮换）", http.MethodGet, ChatWebPath, "stale-token-from-old-process", "", http.StatusForbidden},
		{"API GET 不接受导航 cookie（仍需请求头）", http.MethodGet, "/web/api/sessions", "nav-cookie-token-01", "", http.StatusForbidden},
		{"API GET 带请求头正常放行", http.MethodGet, "/web/api/sessions", "", "nav-cookie-token-01", http.StatusOK},
		{"写方法不接受导航 cookie", http.MethodPost, ChatWebPath, "nav-cookie-token-01", "", http.StatusForbidden},
		{"静态资产本就免令牌", http.MethodGet, ChatWebPath + "js/util.js", "", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://192.168.1.5:8080"+tt.path, nil)
			req.Host = "192.168.1.5:8080"
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: chatWebPageNavCookieName, Value: tt.cookie})
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

// TestChatWebPageAuthCookieLifecycle 验证导航 cookie 只在非回环模式下发，
// 属性满足 HttpOnly + SameSite=Strict + Path=/ 且为浏览器会话级；
// 并端到端验证「把响应里的 cookie 原样回灌到刷新请求上」能通过鉴权。
func TestChatWebPageAuthCookieLifecycle(t *testing.T) {
	withTestWebToken(t, "page-cookie-token-01")

	// 回环模式：GET 导航本就免令牌，不下发 cookie（不扩大暴露面）。
	withTestWebLoopbackMode(t, true)
	rec := httptest.NewRecorder()
	HandleChatWebPage(rec, httptest.NewRequest(http.MethodGet, ChatWebPath, nil))
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("回环模式不应下发导航 cookie，实际 %v", cookies)
	}

	// 非回环模式：页面响应带导航 cookie。
	withTestWebLoopbackMode(t, false)
	rec = httptest.NewRecorder()
	HandleChatWebPage(rec, httptest.NewRequest(http.MethodGet, ChatWebPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("非回环模式应下发 1 枚导航 cookie，实际 %d: %v", len(cookies), cookies)
	}
	c := cookies[0]
	if c.Name != chatWebPageNavCookieName || c.Value != "page-cookie-token-01" {
		t.Fatalf("cookie = %s=%s, want %s=page-cookie-token-01", c.Name, c.Value, chatWebPageNavCookieName)
	}
	if !c.HttpOnly {
		t.Fatalf("导航 cookie 必须 HttpOnly（页面 JS 不得读到令牌原文）")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("导航 cookie 必须 SameSite=Strict，实际 %v", c.SameSite)
	}
	if c.Path != "/" {
		t.Fatalf("Path = %q, want /（同时覆盖 /web 与 /web/）", c.Path)
	}
	if c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Fatalf("导航 cookie 必须是浏览器会话级（不下发 Max-Age/Expires），实际 MaxAge=%d Expires=%v", c.MaxAge, c.Expires)
	}
	// 刷新形态：浏览器把 cookie 原样带回 /web/ 导航 → 放行（此前恒 403）。
	refresh := httptest.NewRequest(http.MethodGet, ChatWebPath, nil)
	refresh.AddCookie(c)
	rec = httptest.NewRecorder()
	ChatWebAuthGuard(guardProbeHandler())(rec, refresh)
	if rec.Code != http.StatusOK {
		t.Fatalf("刷新请求 status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestChatWebAuthFrontendTokenOrder 锁定前端两处取值顺序为 meta 优先：
// js/util.js（SSE 的 ?token= 取值）与 js/ui.js（「关于」页显示）都必须先读
// 当前进程注入的 meta，再回退 sessionStorage —— 反序会把上一个进程的旧令牌
// 带进请求/界面（随机令牌重启后 403）。
func TestChatWebAuthFrontendTokenOrder(t *testing.T) {
	for _, path := range []string{"web/js/util.js", "web/js/ui.js"} {
		data, err := webFS.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)
		metaIdx := strings.Index(body, `meta[name="aicli-web-token"]`)
		cacheIdx := strings.Index(body, "sessionStorage.getItem(")
		if metaIdx < 0 || cacheIdx < 0 || metaIdx > cacheIdx {
			t.Errorf("%s 必须 meta 优先、sessionStorage 兜底（meta=%d, cache=%d）", path, metaIdx, cacheIdx)
		}
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
