package auth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// openBrowserViaHTTP 模拟浏览器：直接 GET 授权 URL（跟随 302 到本地回调）。
func openBrowserViaHTTP(t *testing.T, browserErr error) (func(string) error, *string) {
	t.Helper()
	var opened string
	var mu sync.Mutex
	return func(target string) error {
		mu.Lock()
		opened = target
		mu.Unlock()
		if browserErr != nil {
			return browserErr
		}
		go func() {
			resp, err := http.Get(target)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
		return nil
	}, &opened
}

func newTestSession(t *testing.T, mock *mockOAuthServer, store *TokenStore, browser func(string) error, stdout io.Writer) *Session {
	t.Helper()
	if browser == nil {
		browser, _ = openBrowserViaHTTP(t, nil)
	}
	session, err := NewSession("remote", mock.mcpURL(), testAuthConfig(), SessionOptions{
		Store:       store,
		HTTPClient:  mock.server.Client(),
		OpenBrowser: browser,
		Stdout:      stdout,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session
}

func TestAuthenticateEndToEndPersistsTokenAndUsesIt(t *testing.T) {
	mock := newMockOAuthServer(t)
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	var out bytes.Buffer
	browser, opened := openBrowserViaHTTP(t, nil)
	session := newTestSession(t, mock, store, browser, &out)

	token, err := session.Authenticate(context.Background(), AuthorizeOptions{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !strings.Contains(*opened, "/authorize?") || !strings.Contains(*opened, "code_challenge=") {
		t.Fatalf("授权 URL 缺少 PKCE 参数: %q", *opened)
	}
	if token.AccessToken == "" || token.RefreshToken == "" {
		t.Fatalf("令牌不完整: %#v", token)
	}
	if !mock.hasAccessToken(token.AccessToken) {
		t.Fatalf("mock 不认可签发的令牌: %s", token.AccessToken)
	}
	if register, _, _, _ := mock.counters(); register != 1 {
		t.Fatalf("动态客户端注册调用次数 = %d, want 1", register)
	}
	if v := mock.lastVerifier; v == "" {
		t.Fatal("token 请求缺少 code_verifier（PKCE 未生效）")
	}

	// 令牌已落盘且可复用。
	stored, ok := store.Get("remote")
	if !ok || stored.AccessToken != token.AccessToken {
		t.Fatalf("令牌未落盘: %#v, %v", stored, ok)
	}
	if !session.Ready() {
		t.Fatal("授权后 Ready 应为 true")
	}
	access, err := session.AccessToken(context.Background())
	if err != nil || access != token.AccessToken {
		t.Fatalf("AccessToken = %q, %v", access, err)
	}

	// 令牌可直接通过受保护的 MCP 端点校验。
	req, _ := http.NewRequest(http.MethodPost, mock.mcpURL(), strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := mock.server.Client().Do(req)
	if err != nil {
		t.Fatalf("调用 mock MCP: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mock MCP 状态码 = %d, want 200", resp.StatusCode)
	}
}

func TestAuthenticateWithoutTokenMarksNeedsAuth(t *testing.T) {
	mock := newMockOAuthServer(t)
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	session := newTestSession(t, mock, store, nil, io.Discard)

	if session.Ready() {
		t.Fatal("无令牌时 Ready 应为 false")
	}
	_, err = session.AccessToken(context.Background())
	if err == nil || !IsNeedsAuth(err) {
		t.Fatalf("AccessToken 应返回需认证错误，got %v", err)
	}
	needs, reason := session.NeedsAuth()
	if !needs || !strings.Contains(reason, "aicli mcp auth remote") {
		t.Fatalf("NeedsAuth = %v, %q（应给出可行动提示）", needs, reason)
	}
	if !strings.Contains(err.Error(), "headers") {
		t.Fatalf("错误应包含手动兜底提示: %v", err)
	}
}

func TestAccessTokenRefreshesWhenExpired(t *testing.T) {
	mock := newMockOAuthServer(t)
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	// 预置一个已过期、但带 refresh_token 的令牌。
	if err := store.Put("remote", &Token{
		ServerURL:    mock.mcpURL(),
		AuthServer:   mock.baseURL(),
		ClientID:     "dcr-client",
		AccessToken:  "access-stale",
		RefreshToken: "refresh-seed",
		Scope:        "read",
		ExpiresAt:    time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// mock 只认可自己签发的刷新令牌。
	mock.mu.Lock()
	mock.refreshTokens["refresh-seed"] = true
	mock.mu.Unlock()

	session := newTestSession(t, mock, store, nil, io.Discard)
	if session.Ready() {
		t.Fatal("过期令牌不应视为 Ready")
	}
	access, err := session.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if access == "access-stale" || !mock.hasAccessToken(access) {
		t.Fatalf("应返回刷新后的新令牌，got %q", access)
	}
	if _, _, _, refresh := mock.counters(); refresh != 1 {
		t.Fatalf("refresh 调用次数 = %d, want 1", refresh)
	}
	// 刷新结果已落盘，refresh_token 得到轮换。
	stored, _ := store.Get("remote")
	if stored.AccessToken != access {
		t.Fatalf("刷新结果未落盘: %#v", stored)
	}
	if stored.ExpiresAt.IsZero() || !stored.ExpiresAt.After(time.Now()) {
		t.Fatalf("刷新后应写入新的过期时间: %#v", stored.ExpiresAt)
	}
}

func TestForceRefreshWithoutRefreshTokenMarksNeedsAuth(t *testing.T) {
	mock := newMockOAuthServer(t)
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	if err := store.Put("remote", &Token{
		ServerURL:   mock.mcpURL(),
		AccessToken: "access-only",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	session := newTestSession(t, mock, store, nil, io.Discard)
	if _, err := session.ForceRefresh(context.Background()); err == nil || !IsNeedsAuth(err) {
		t.Fatalf("无 refresh token 时 ForceRefresh 应报需认证，got %v", err)
	}
	needs, reason := session.NeedsAuth()
	if !needs || !strings.Contains(reason, "refresh token") {
		t.Fatalf("NeedsAuth = %v, %q", needs, reason)
	}
}

func TestAuthenticateNoBrowserPrintsUsableURL(t *testing.T) {
	mock := newMockOAuthServer(t)
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	var out bytes.Buffer
	browserCalled := false
	session := newTestSession(t, mock, store, func(string) error {
		browserCalled = true
		return nil
	}, &out)

	done := make(chan error, 1)
	go func() {
		_, err := session.Authenticate(context.Background(), AuthorizeOptions{NoBrowser: true, Timeout: 10 * time.Second})
		done <- err
	}()

	// 从输出里取回授权 URL，模拟用户手动打开。
	url := waitForURL(t, &out)
	if browserCalled {
		t.Fatal("--no-browser 不应自动打开浏览器")
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("手动打开授权 URL: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("手动模式下授权未完成")
	}
	if !strings.Contains(out.String(), "请手动打开以下链接") {
		t.Fatalf("应打印手动打开提示:\n%s", out.String())
	}
}

// waitForURL 轮询输出缓冲，取出其中的 http(s) 链接。
func waitForURL(t *testing.T, out *bytes.Buffer) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		text := out.String()
		for _, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
				return trimmed
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待授权 URL 超时:\n%s", out.String())
	return ""
}

func TestParseManualInput(t *testing.T) {
	result := parseManualInput("http://127.0.0.1:1234/callback?code=abc&state=xyz")
	if result.err != nil || result.code != "abc" || result.state != "xyz" {
		t.Fatalf("完整回调 URL 解析失败: %#v", result)
	}
	codeOnly := parseManualInput("just-a-code")
	if codeOnly.err != nil || codeOnly.code != "just-a-code" {
		t.Fatalf("裸 code 解析失败: %#v", codeOnly)
	}
	denied := parseManualInput("http://127.0.0.1:1234/callback?error=access_denied&error_description=nope")
	if denied.err == nil || !strings.Contains(denied.err.Error(), "access_denied") {
		t.Fatalf("错误回调应报错: %#v", denied)
	}
}

func TestAuthenticateBrowserFailurePrintsURL(t *testing.T) {
	mock := newMockOAuthServer(t)
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	var out bytes.Buffer
	browser, _ := openBrowserViaHTTP(t, io.ErrClosedPipe)
	session := newTestSession(t, mock, store, browser, &out)

	done := make(chan error, 1)
	go func() {
		_, err := session.Authenticate(context.Background(), AuthorizeOptions{Timeout: 10 * time.Second})
		done <- err
	}()
	url := waitForURL(t, &out)
	if !strings.Contains(out.String(), "自动打开浏览器失败") {
		t.Fatalf("浏览器失败时应打印 URL 而不是报错退出:\n%s", out.String())
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("打开授权 URL: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err := <-done; err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}
