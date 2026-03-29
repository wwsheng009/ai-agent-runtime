package httpclient

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/dnscache"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	gorillaws "github.com/gorilla/websocket"
)

const defaultWebSocketHandshakeTimeout = 30 * time.Second

// IsWebSocketUpgradeRequest 检测请求是否为 WebSocket 升级请求。
func IsWebSocketUpgradeRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	upgrade := strings.ToLower(strings.TrimSpace(req.Header.Get("Upgrade")))
	connection := strings.ToLower(req.Header.Get("Connection"))
	return upgrade == "websocket" && strings.Contains(connection, "upgrade")
}

// GetWebSocketDialerWithProvider 创建 WebSocket Dialer。
// 该实现不直接复用 http.Client，但会复用 HTTP client pool 中已经存在的
// proxy / DNS / dial timeout / keepalive 策略。
func GetWebSocketDialerWithProvider(cfg *agentconfig.Config, provider *agentconfig.Provider) *gorillaws.Dialer {
	if cfg == nil {
		return gorillaws.DefaultDialer
	}

	httpTimeout := cfg.Providers.HTTPTimeout

	proxyCfg := &cfg.Providers.Proxy
	hasProviderProxy := provider != nil && provider.Proxy != nil
	if hasProviderProxy {
		proxyCfg = cfg.Providers.Proxy.Merge(provider.Proxy)
	}

	proxyType := GetProxyType(proxyCfg)

	var smartDialerOpts []dnscache.SmartDialerOption
	if httpTimeout.DNSServer != "" {
		smartDialerOpts = append(smartDialerOpts, dnscache.WithSmartDialerDNSServer(httpTimeout.DNSServer))
	}

	var dialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)
	var proxyFunc func(*http.Request) (*url.URL, error)

	switch proxyType {
	case ProxyTypeSOCKS5:
		dialContextFunc = createSOCKS5DialContextWithDNS(&httpTimeout, proxyCfg)
		proxyFunc = func(*http.Request) (*url.URL, error) { return nil, nil }
	case ProxyTypeHTTP, ProxyTypeHTTPS:
		dialContextFunc = createSmartDialerWithDNS(&httpTimeout, smartDialerOpts)
		proxyFunc = ProxyFunc(proxyCfg)
	default:
		dialContextFunc = createSmartDialerWithDNS(&httpTimeout, smartDialerOpts)
		proxyFunc = func(*http.Request) (*url.URL, error) { return nil, nil }
	}

	handshakeTimeout := httpTimeout.ResponseHeaderTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = httpTimeout.TLSHandshakeTimeout
	}
	if handshakeTimeout <= 0 {
		handshakeTimeout = defaultWebSocketHandshakeTimeout
	}

	if provider != nil {
		logger.Info("Initializing WebSocket dialer",
			logger.String("provider_protocol", provider.GetProtocol()),
			logger.String("proxy_type", proxyTypeToString(proxyType)),
			logger.String("proxy", proxyCfg.String()),
			logger.Duration("handshake_timeout", handshakeTimeout.Nanoseconds()),
		)
	}

	return &gorillaws.Dialer{
		Proxy:             proxyFunc,
		HandshakeTimeout:  handshakeTimeout,
		NetDialContext:    dialContextFunc,
		ReadBufferSize:    32 << 10,
		WriteBufferSize:   32 << 10,
		EnableCompression: false,
	}
}
