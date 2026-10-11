package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"github.com/gorilla/websocket"
)

// proxyDialerTimeout bounds the proxy handshake.
const proxyDialerTimeout = 20 * time.Second

// parseProxyURL normalises a proxy specification. Accepted forms:
//
//	http://host:port             HTTP CONNECT proxy
//	https://host:port            same as http://
//	socks5://host:port           SOCKS5, no auth
//	socks5://user:pass@host:port SOCKS5 with auth
//	socks5h://...                like socks5 (hostnames resolved by the proxy)
//
// A value without a scheme is treated as socks5:// (the common shorthand).
func parseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("proxy url is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "socks5://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		if u.Host == "" {
			return nil, fmt.Errorf("proxy url %q has no host", raw)
		}
		return u, nil
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (want http/https/socks5)", u.Scheme)
	}
}

// websocketDialer builds the websocket dialer for cfg. With ProxyURL set the
// tunnel is established through the proxy (HTTP CONNECT or SOCKS5); otherwise
// the default dialer is used, which honours HTTP(S)_PROXY environment variables
// for ws/wss upgrades.
func websocketDialer(cfg Config) (*websocket.Dialer, error) {
	raw := strings.TrimSpace(cfg.ProxyURL)
	if raw == "" {
		return websocket.DefaultDialer, nil
	}
	u, err := parseProxyURL(raw)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		// x/net/proxy has no http scheme; use the standard library's HTTP proxy
		// dialer (CONNECT) via Transport semantics wrapped as NetDialContext.
		cd := httpConnectContextDialer(u.Host, proxyDialerTimeout)
		return &websocket.Dialer{
			NetDialContext:   cd,
			HandshakeTimeout: proxyDialerTimeout,
		}, nil
	default: // socks5 / socks5h — auth comes from the URL userinfo
		d, derr := proxy.FromURL(u, &net.Dialer{Timeout: proxyDialerTimeout})
		if derr != nil {
			return nil, derr
		}
		return &websocket.Dialer{
			NetDial:          d.Dial,
			HandshakeTimeout: proxyDialerTimeout,
		}, nil
	}
}

// httpConnectContextDialer returns a dialer that tunnels TCP through an HTTP
// CONNECT proxy at proxyHostPort.
func httpConnectContextDialer(proxyHostPort string, timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		proxyConn, err := d.DialContext(ctx, "tcp", proxyHostPort)
		if err != nil {
			return nil, err
		}
		proxyConn.Write([]byte("CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n\r\n"))
		buf := make([]byte, 512)
		proxyConn.SetReadDeadline(time.Now().Add(timeout))
		if _, err := proxyConn.Read(buf); err != nil {
			proxyConn.Close()
			return nil, err
		}
		proxyConn.SetReadDeadline(time.Time{})
		return proxyConn, nil
	}
}
