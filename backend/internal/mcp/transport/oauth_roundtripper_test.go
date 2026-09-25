//go:build !win7compat

package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stubTokenProvider struct {
	mu         sync.Mutex
	token      string
	forceToken string
	accessErr  error
	forceErr   error

	accessCalls int
	forceCalls  int
}

func (p *stubTokenProvider) AccessToken(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accessCalls++
	return p.token, p.accessErr
}

func (p *stubTokenProvider) ForceRefresh(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forceCalls++
	return p.forceToken, p.forceErr
}

func (p *stubTokenProvider) NeedsAuth() (bool, string) { return false, "" }

func (p *stubTokenProvider) counts() (access, force int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accessCalls, p.forceCalls
}

func TestHeaderRoundTripperInjectsBearerToken(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := &stubTokenProvider{token: "tok-1"}
	rt := headerRoundTripper{base: http.DefaultTransport, provider: provider}
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"a":1}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got != "Bearer tok-1" {
		t.Fatalf("Authorization = %q", got)
	}
	if access, _ := provider.counts(); access != 1 {
		t.Fatalf("AccessToken 调用次数 = %d, want 1", access)
	}
}

// 手动配置的 Authorization（headers 兜底）优先，不应被 OAuth 覆盖。
func TestHeaderRoundTripperPrefersStaticAuthorization(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := &stubTokenProvider{token: "tok-oauth"}
	rt := headerRoundTripper{
		base:     http.DefaultTransport,
		headers:  http.Header{"Authorization": {"Bearer manual"}},
		provider: provider,
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got != "Bearer manual" {
		t.Fatalf("Authorization = %q, want manual", got)
	}
	if access, _ := provider.counts(); access != 0 {
		t.Fatalf("静态头存在时不应调用 AccessToken，实际 %d 次", access)
	}
}

func TestHeaderRoundTripperRefreshesOn401AndRetriesBody(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		bodies = append(bodies, string(body))
		attempt := len(seen)
		mu.Unlock()
		if attempt == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := &stubTokenProvider{token: "tok-old", forceToken: "tok-new"}
	rt := headerRoundTripper{base: http.DefaultTransport, provider: provider}
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"jsonrpc":"2.0"}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重试后状态码 = %d, want 200", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "Bearer tok-old" || seen[1] != "Bearer tok-new" {
		t.Fatalf("Authorization 序列 = %#v", seen)
	}
	if len(bodies) != 2 || bodies[0] != `{"jsonrpc":"2.0"}` || bodies[1] != bodies[0] {
		t.Fatalf("重试请求体未正确重放: %#v", bodies)
	}
	if _, force := provider.counts(); force != 1 {
		t.Fatalf("ForceRefresh 调用次数 = %d, want 1", force)
	}
}

// 刷新失败时保留原始 401 响应，让上层把 server 标记为需认证。
func TestHeaderRoundTripperKeeps401WhenRefreshFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := &stubTokenProvider{token: "tok-old", forceErr: errors.New("refresh failed")}
	rt := headerRoundTripper{base: http.DefaultTransport, provider: provider}
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401", resp.StatusCode)
	}
	if _, force := provider.counts(); force != 1 {
		t.Fatalf("ForceRefresh 调用次数 = %d, want 1", force)
	}
}

func TestHeaderRoundTripperReturnsProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := &stubTokenProvider{accessErr: errors.New("尚未完成 OAuth 授权")}
	rt := headerRoundTripper{base: http.DefaultTransport, provider: provider}
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("provider 报错时应直接返回错误，而不是发送裸请求")
	}
}

// 断言 streamable/sse 传输在注入 provider 后确实装配了带令牌的 HTTP 客户端。
func TestRemoteTransportsWireAccessTokenProvider(t *testing.T) {
	provider := &stubTokenProvider{token: "tok"}
	streamable := NewStreamableTransport(&Config{Type: "http", URL: "http://127.0.0.1:1/mcp", AccessToken: provider})
	inner := unwrapSDKTransport(t, streamable.ToMCPSdkTransport(context.Background()))
	st, ok := inner.(*mcp.StreamableClientTransport)
	if !ok {
		t.Fatalf("streamable inner transport = %T", inner)
	}
	assertProviderWired(t, st.HTTPClient)

	sse := NewSSETransport(&Config{Type: "sse", URL: "http://127.0.0.1:1/sse", AccessToken: provider})
	sseInner := unwrapSDKTransport(t, sse.ToMCPSdkTransport(context.Background()))
	st2, ok := sseInner.(*mcp.SSEClientTransport)
	if !ok {
		t.Fatalf("sse inner transport = %T", sseInner)
	}
	assertProviderWired(t, st2.HTTPClient)
}

func assertProviderWired(t *testing.T, client *http.Client) {
	t.Helper()
	if client == nil {
		t.Fatal("应注入带 OAuth provider 的 HTTP 客户端")
	}
	rt, ok := client.Transport.(headerRoundTripper)
	if !ok {
		t.Fatalf("RoundTripper = %T", client.Transport)
	}
	if rt.provider == nil {
		t.Fatal("RoundTripper 未携带 AccessTokenProvider")
	}
}
