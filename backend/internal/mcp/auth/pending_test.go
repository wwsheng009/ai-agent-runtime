package auth

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pending_test.go 覆盖 BeginAuth/PendingAuth 的「分段授权」契约：起流程只产出
// URL 与回调端口，兑换发生在 Wait / TakeCallback / Complete 之一，且三者的
// state 校验与持久化语义一致（chat TUI 与 Web 面板据此在自己的交互节奏里完成授权）。

func newPendingTestSession(t *testing.T, mock *mockOAuthServer, browser func(string) error) (*Session, *TokenStore) {
	t.Helper()
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	return newTestSession(t, mock, store, browser, &strings.Builder{}), store
}

// 非阻塞 TakeCallback：浏览器命中本地回调后应能直接兑换并落盘。
func TestPendingAuthTakeCallbackCompletesFlow(t *testing.T) {
	mock := newMockOAuthServer(t)
	browser, _ := openBrowserViaHTTP(t, nil)
	session, store := newPendingTestSession(t, mock, browser)

	pending, err := session.BeginAuth(context.Background(), AuthorizeOptions{NoBrowser: true})
	if err != nil {
		t.Fatalf("BeginAuth: %v", err)
	}
	defer func() { _ = pending.Close() }()

	authURL := pending.AuthURL()
	if !strings.HasPrefix(authURL, mock.baseURL()+"/authorize") {
		t.Fatalf("AuthURL = %q", authURL)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse AuthURL: %v", err)
	}
	query := parsed.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("state") == "" {
		t.Fatalf("授权 URL 缺少 PKCE/state: %s", authURL)
	}
	if query.Get("redirect_uri") != pending.RedirectURI() {
		t.Fatalf("redirect_uri = %q, RedirectURI() = %q", query.Get("redirect_uri"), pending.RedirectURI())
	}
	if pending.Expired() {
		t.Fatal("刚发起的流程不应立即过期")
	}
	if pending.BrowserErr() != nil {
		t.Fatalf("NoBrowser 不应打开浏览器: %v", pending.BrowserErr())
	}

	if _, ok, err := pending.TakeCallback(context.Background()); ok || err != nil {
		t.Fatalf("尚无回调时 TakeCallback = (ok=%v, err=%v)", ok, err)
	}

	// 模拟用户在浏览器完成授权（mock 会 302 回本地回调）。
	if err := browser(authURL); err != nil {
		t.Fatalf("browser: %v", err)
	}
	token := waitForPendingToken(t, pending)
	if token == nil || token.AccessToken == "" {
		t.Fatalf("TakeCallback 未取到令牌: %#v", token)
	}
	stored, ok := store.Get("remote")
	if !ok || stored == nil || stored.AccessToken == "" {
		t.Fatalf("令牌未持久化: %#v", stored)
	}
	status := session.Status()
	if !status.Authenticated || status.NeedsAuth {
		t.Fatalf("授权后状态异常: %#v", status)
	}
}

// 粘贴完整回调 URL 或裸 code 都必须能完成兑换（chat 的手动路径）。
func TestPendingAuthCompleteWithPastedInput(t *testing.T) {
	for _, mode := range []string{"callback-url", "code-only"} {
		t.Run(mode, func(t *testing.T) {
			mock := newMockOAuthServer(t)
			session, store := newPendingTestSession(t, mock, nil)

			pending, err := session.BeginAuth(context.Background(), AuthorizeOptions{NoBrowser: true, Port: 0})
			if err != nil {
				t.Fatalf("BeginAuth: %v", err)
			}
			defer func() { _ = pending.Close() }()

			callbackURL := authorizeWithoutFollowingRedirects(t, mock, pending.AuthURL())
			input := callbackURL
			if mode == "code-only" {
				parsed, err := url.Parse(callbackURL)
				if err != nil {
					t.Fatalf("parse callback: %v", err)
				}
				input = parsed.Query().Get("code")
			}

			token, err := pending.Complete(context.Background(), input)
			if err != nil {
				t.Fatalf("Complete(%s): %v", mode, err)
			}
			if token.AccessToken == "" {
				t.Fatalf("Complete 未返回访问令牌: %#v", token)
			}
			if _, ok := store.Get("remote"); !ok {
				t.Fatal("Complete 后令牌应已持久化")
			}
		})
	}
}

// 粘贴内容里的 state 与本次流程不符时必须拒绝（CSRF 防护在分段路径同样生效）。
func TestPendingAuthCompleteRejectsStateMismatch(t *testing.T) {
	mock := newMockOAuthServer(t)
	session, _ := newPendingTestSession(t, mock, nil)

	pending, err := session.BeginAuth(context.Background(), AuthorizeOptions{NoBrowser: true})
	if err != nil {
		t.Fatalf("BeginAuth: %v", err)
	}
	defer func() { _ = pending.Close() }()

	_, err = pending.Complete(context.Background(), pending.RedirectURI()+"?code=abc&state=not-the-state")
	if err == nil || !strings.Contains(err.Error(), "state 校验失败") {
		t.Fatalf("state 不匹配应报错，实际: %v", err)
	}
}

// 过期只作提示与清理依据；Close 幂等且能释放回调端口。
func TestPendingAuthExpiryAndClose(t *testing.T) {
	mock := newMockOAuthServer(t)
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	current := time.Now()
	session, err := NewSession("remote", mock.mcpURL(), testAuthConfig(), SessionOptions{
		Store:       store,
		HTTPClient:  mock.server.Client(),
		OpenBrowser: func(string) error { return nil },
		Now:         func() time.Time { return current },
		FlowTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	pending, err := session.BeginAuth(context.Background(), AuthorizeOptions{NoBrowser: true})
	if err != nil {
		t.Fatalf("BeginAuth: %v", err)
	}
	if pending.Expired() {
		t.Fatal("未到过期时间")
	}
	if got := pending.ExpiresAt(); got.IsZero() {
		t.Fatal("ExpiresAt 不应为零值")
	}
	current = current.Add(31 * time.Second)
	if !pending.Expired() {
		t.Fatal("超过 FlowTimeout 后应报告过期")
	}
	if err := pending.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := pending.Close(); err != nil {
		t.Fatalf("Close 应可重复调用: %v", err)
	}
	if _, ok, err := pending.TakeCallback(context.Background()); ok || err != nil {
		t.Fatalf("关闭后 TakeCallback = (ok=%v, err=%v)", ok, err)
	}
}

// waitForPendingToken 轮询非阻塞 TakeCallback，避免对回调到达时机做假设。
func waitForPendingToken(t *testing.T, pending *PendingAuth) *Token {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		token, ok, err := pending.TakeCallback(context.Background())
		if err != nil {
			t.Fatalf("TakeCallback: %v", err)
		}
		if ok {
			return token
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待浏览器回调超时")
	return nil
}

// authorizeWithoutFollowingRedirects 只取 /authorize 的 302 Location（即回调 URL）。
func authorizeWithoutFollowingRedirects(t *testing.T, mock *mockOAuthServer, authURL string) string {
	t.Helper()
	client := &http.Client{
		Transport: mock.server.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("GET authorize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize 状态码 = %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if strings.TrimSpace(location) == "" {
		t.Fatal("authorize 未返回 Location")
	}
	return location
}
