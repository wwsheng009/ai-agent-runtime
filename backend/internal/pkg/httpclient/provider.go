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

// NewProviderHTTPClient builds an HTTP client configured for upstream provider
// calls. It reuses the shared proxy and dial-context helpers so callers in other
// packages can avoid duplicating transport setup logic.
func NewProviderHTTPClient(
	timeout time.Duration,
	proxyCfg *agentconfig.ProxyConfig,
	stream bool,
) *http.Client {
	transport := &http.Transport{
		Proxy:               ProxyFunc(proxyCfg),
		DialContext:         CreateDialContextFromProxy(proxyCfg, providerDialTimeout(timeout), defaultProviderKeepAlive),
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: providerTLSHandshakeTimeout(timeout),
	}

	clientTimeout := timeout
	if stream {
		clientTimeout = 0
	}

	return &http.Client{
		Timeout:   clientTimeout,
		Transport: transport,
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
