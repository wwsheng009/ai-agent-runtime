//go:build !win7compat

package transport

import (
	"context"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StreamableTransport Streamable HTTP 传输封装。
//
// 对应 MCP 2025-03-26 规范中的 Streamable HTTP transport，适用于
// 形如 http://127.0.0.1:12306/mcp 的单一端点（POST 请求 + 可选 SSE 流）。
type StreamableTransport struct {
	cfg     *Config
	emitter lifecycleEmitter
}

// NewStreamableTransport 创建 Streamable HTTP 传输
func NewStreamableTransport(cfg *Config) *StreamableTransport {
	return &StreamableTransport{cfg: cfg}
}

// Type 返回传输类型
func (t *StreamableTransport) Type() string {
	return "streamable"
}

// Config 返回传输配置
func (t *StreamableTransport) Config() interface{} {
	return t.cfg
}

// AddLifecycleObserver 订阅传输生命周期事件
func (t *StreamableTransport) AddLifecycleObserver(observer LifecycleObserver) {
	t.emitter.AddLifecycleObserver(observer)
}

// ToMCPSdkTransport 转换为官方 SDK 的 StreamableClientTransport
func (t *StreamableTransport) ToMCPSdkTransport(_ context.Context) mcp.Transport {
	inner := &mcp.StreamableClientTransport{
		Endpoint: strings.TrimSpace(t.cfg.URL),
	}
	headers := buildHeaders(t.cfg.Headers, t.cfg.Env)
	if len(headers) > 0 || t.cfg.AccessToken != nil {
		inner.HTTPClient = &http.Client{
			Transport: headerRoundTripper{base: http.DefaultTransport, headers: headers, provider: t.cfg.AccessToken},
		}
	}
	return newObservedMCPTransport("streamable", t.cfg.URL, inner, &t.emitter)
}

// headerRoundTripper 为每个请求注入配置的 HTTP 头（Headers 优先，Env 兜底）。
//
// provider 非空且未配置静态 Authorization 时，改为按需注入 OAuth Bearer 令牌，
// 并在 401/403 时强制刷新后重试一次（见 oauth_roundtripper.go）。
type headerRoundTripper struct {
	base     http.RoundTripper
	headers  http.Header
	provider AccessTokenProvider
}

func (rt headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	if rt.provider != nil && !hasStaticAuthorization(rt.headers) {
		return roundTripWithAccessToken(base, req, rt.headers, rt.provider)
	}
	return base.RoundTrip(cloneRequestWithHeaders(req, rt.headers))
}
