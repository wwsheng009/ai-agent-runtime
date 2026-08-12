package httpclient

import (
	"net/http"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

const (
	defaultProviderDialTimeout         = 30 * time.Second
	defaultProviderKeepAlive           = 30 * time.Second
	defaultProviderTLSHandshakeTimeout = 10 * time.Second
)

// ProviderHTTPClientOptions carries optional transport-level timeouts for
// provider HTTP clients.
type ProviderHTTPClientOptions struct {
	// ResponseHeaderTimeout bounds the wait for the upstream response headers
	// once the request has been sent. It guards against upstreams that accept
	// the connection but never reply, without imposing a total-body deadline
	// that would break long streaming responses.
	ResponseHeaderTimeout time.Duration
	IdleConnTimeout       time.Duration
}

// NewProviderHTTPClient builds an HTTP client configured for upstream provider
// calls. It reuses the shared proxy and dial-context helpers so callers in other
// packages can avoid duplicating transport setup logic.
func NewProviderHTTPClient(
	timeout time.Duration,
	proxyCfg *agentconfig.ProxyConfig,
	stream bool,
	opts ...ProviderHTTPClientOptions,
) *http.Client {
	transport := &http.Transport{
		Proxy:               ProxyFunc(proxyCfg),
		DialContext:         CreateDialContextFromProxy(proxyCfg, providerDialTimeout(timeout), defaultProviderKeepAlive),
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: providerTLSHandshakeTimeout(timeout),
	}
	for _, opt := range opts {
		if opt.ResponseHeaderTimeout > 0 {
			transport.ResponseHeaderTimeout = opt.ResponseHeaderTimeout
		}
		if opt.IdleConnTimeout > 0 {
			transport.IdleConnTimeout = opt.IdleConnTimeout
		}
	}

	clientTimeout := timeout
	if stream {
		clientTimeout = 0
	}

	return &http.Client{
		Timeout: clientTimeout,
		// Ensure provider traffic never falls back to Go-http-client/1.1.
		Transport: WithDefaultUserAgent(transport),
	}
}

func providerDialTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 && timeout < defaultProviderDialTimeout {
		return timeout
	}
	return defaultProviderDialTimeout
}

func providerTLSHandshakeTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 && timeout < defaultProviderTLSHandshakeTimeout {
		return timeout
	}
	return defaultProviderTLSHandshakeTimeout
}
