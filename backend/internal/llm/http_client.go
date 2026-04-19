package llm

import (
	"net/http"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimehttpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
)

const (
	defaultProviderDialTimeout         = 30 * time.Second
	defaultProviderKeepAlive           = 30 * time.Second
	defaultProviderTLSHandshakeTimeout = 10 * time.Second
)

func newProviderHTTPClient(
	timeout time.Duration,
	proxyCfg *agentconfig.ProxyConfig,
	stream bool,
) *http.Client {
	transport := &http.Transport{
		Proxy:               runtimehttpclient.ProxyFunc(proxyCfg),
		DialContext:         runtimehttpclient.CreateDialContextFromProxy(proxyCfg, providerDialTimeout(timeout), defaultProviderKeepAlive),
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
