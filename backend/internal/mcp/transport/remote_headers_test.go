//go:build !win7compat

package transport

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestBuildHeaders_PrefersHeadersFallsBackToEnv 固化 P2 的语义：
// Headers 是远程传输的正式字段；Env 只在 Headers 为空时作为历史兼容兜底
// （计划 §5-D3 / §6.3）。
func TestBuildHeaders_PrefersHeadersFallsBackToEnv(t *testing.T) {
	headers := buildHeaders(
		map[string]string{"Authorization": "Bearer structured"},
		map[string]string{"Authorization": "Bearer legacy", "X-Env": "1"},
	)
	if got := headers.Get("Authorization"); got != "Bearer structured" {
		t.Fatalf("Headers 优先失效: Authorization = %q", got)
	}
	if got := headers.Get("X-Env"); got != "" {
		t.Fatalf("Headers 非空时不应混入 Env: X-Env = %q", got)
	}

	fallback := buildHeaders(nil, map[string]string{"X-Env": "1"})
	if got := fallback.Get("X-Env"); got != "1" {
		t.Fatalf("Env 兜底失效: X-Env = %q", got)
	}

	if got := buildHeaders(nil, nil); got != nil {
		t.Fatalf("无头配置应返回 nil，实际 %v", got)
	}
}

// TestRemoteTransports_WireHeaderClient 断言 http(streamable)/sse 两条远程传输
// 真的把凭据注入 HTTP 客户端，且未配置头时不注入任何客户端（避免无谓包装）。
func TestRemoteTransports_WireHeaderClient(t *testing.T) {
	ctx := context.Background()

	streamable := NewStreamableTransport(&Config{
		Type:    "http",
		URL:     "http://127.0.0.1:1/mcp",
		Headers: map[string]string{"Authorization": "Bearer s"},
	})
	inner := unwrapSDKTransport(t, streamable.ToMCPSdkTransport(ctx))
	st, ok := inner.(*mcp.StreamableClientTransport)
	if !ok {
		t.Fatalf("streamable inner transport = %T", inner)
	}
	assertHeaderClient(t, st.HTTPClient, "Authorization", "Bearer s")

	sse := NewSSETransport(&Config{
		Type:    "sse",
		URL:     "http://127.0.0.1:1/sse",
		Headers: map[string]string{"X-Api-Key": "k"},
	})
	sseInner := unwrapSDKTransport(t, sse.ToMCPSdkTransport(ctx))
	st2, ok := sseInner.(*mcp.SSEClientTransport)
	if !ok {
		t.Fatalf("sse inner transport = %T", sseInner)
	}
	assertHeaderClient(t, st2.HTTPClient, "X-Api-Key", "k")

	plain := NewStreamableTransport(&Config{Type: "http", URL: "http://127.0.0.1:1/mcp"})
	plainInner := unwrapSDKTransport(t, plain.ToMCPSdkTransport(ctx))
	st3, ok := plainInner.(*mcp.StreamableClientTransport)
	if !ok {
		t.Fatalf("plain inner transport = %T", plainInner)
	}
	if st3.HTTPClient != nil {
		t.Fatal("未配置凭据时不应包装 HTTP 客户端")
	}
}

func unwrapSDKTransport(t *testing.T, tr mcp.Transport) mcp.Transport {
	t.Helper()
	observed, ok := tr.(*observedMCPTransport)
	if !ok {
		t.Fatalf("transport = %T, want *observedMCPTransport", tr)
	}
	return observed.inner
}

func assertHeaderClient(t *testing.T, client *http.Client, key, want string) {
	t.Helper()
	if client == nil {
		t.Fatalf("HTTP 客户端未注入（缺少 %s）", key)
	}
	rt, ok := client.Transport.(headerRoundTripper)
	if !ok {
		t.Fatalf("HTTP 客户端 Transport = %T, want headerRoundTripper", client.Transport)
	}
	if got := rt.headers.Get(key); got != want {
		t.Fatalf("注入头 %s = %q, want %q", key, got, want)
	}
}
