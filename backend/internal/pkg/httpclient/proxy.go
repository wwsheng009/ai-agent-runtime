package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"golang.org/x/net/proxy"
)

// ProxyFunc 返回代理函数，用于 http.Transport
// 支持 HTTP/HTTPS 代理和 SOCKS5 代理
func ProxyFunc(cfg *agentconfig.ProxyConfig) func(*http.Request) (*url.URL, error) {
	if cfg == nil || cfg.IsEmpty() {
		// 使用环境变量的默认行为
		return http.ProxyFromEnvironment
	}

	return func(req *http.Request) (*url.URL, error) {
		// 检查代理是否启用
		if !cfg.Enabled {
			return nil, nil
		}

		// 检查是否在 NoProxy 列表中
		if cfg.NoProxy != "" && shouldBypassProxy(req.URL.Host, cfg.NoProxy) {
			return nil, nil
		}

		// 根据请求协议选择代理
		var proxyURL string
		if req.URL.Scheme == "https" {
			proxyURL = cfg.HTTPS
			if proxyURL == "" {
				proxyURL = cfg.HTTP // 回退到 HTTP 代理
			}
		} else {
			proxyURL = cfg.HTTP
		}

		if proxyURL == "" {
			return nil, nil
		}

		return parseProxyURL(proxyURL)
	}
}

// parseProxyURL 解析代理 URL
// 支持:
// - http://host:port
// - https://host:port
// - socks5://host:port
// - socks5://user:password@host:port
func parseProxyURL(proxyStr string) (*url.URL, error) {
	if proxyStr == "" {
		return nil, nil
	}

	// 如果没有协议前缀，默认为 http
	if !strings.Contains(proxyStr, "://") {
		proxyStr = "http://" + proxyStr
	}

	parsedURL, err := url.Parse(proxyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", err)
	}

	return parsedURL, nil
}

// shouldBypassProxy 检查目标主机是否应该绕过代理
func shouldBypassProxy(host, noProxy string) bool {
	if noProxy == "" {
		return false
	}

	// 移除端口号
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}

	// 分割 NoProxy 列表
	bypassList := strings.Split(noProxy, ",")
	for _, bypass := range bypassList {
		bypass = strings.TrimSpace(bypass)
		if bypass == "" {
			continue
		}

		// 支持通配符，例如 *.example.com
		if strings.HasPrefix(bypass, ".") {
			if strings.HasSuffix(host, bypass) || host == bypass[1:] {
				return true
			}
		} else if host == bypass {
			return true
		}
	}

	return false
}

// ProxyType 代理类型
type ProxyType int

const (
	ProxyTypeNone ProxyType = iota
	ProxyTypeHTTP
	ProxyTypeHTTPS
	ProxyTypeSOCKS5
)

// GetProxyType 获取代理类型
func GetProxyType(cfg *agentconfig.ProxyConfig) ProxyType {
	if cfg == nil || !cfg.Enabled || cfg.IsEmpty() {
		return ProxyTypeNone
	}

	// 优先检查 HTTPS 代理
	if cfg.HTTPS != "" {
		if strings.HasPrefix(strings.ToLower(cfg.HTTPS), "socks5://") {
			return ProxyTypeSOCKS5
		}
		return ProxyTypeHTTPS
	}

	// 其次检查 HTTP 代理
	if cfg.HTTP != "" {
		if strings.HasPrefix(strings.ToLower(cfg.HTTP), "socks5://") {
			return ProxyTypeSOCKS5
		}
		return ProxyTypeHTTP
	}

	return ProxyTypeNone
}

// CreateSOCKS5Dialer 创建 SOCKS5 拨号器
func CreateSOCKS5Dialer(proxyConfig *agentconfig.ProxyConfig, dialTimeout, keepAlive time.Duration) (proxy.Dialer, error) {
	// 选择代理 URL
	proxyURL := proxyConfig.HTTPS
	if proxyURL == "" {
		proxyURL = proxyConfig.HTTP
	}

	if proxyURL == "" {
		return nil, fmt.Errorf("no proxy URL configured")
	}

	// 解析代理 URL
	parsedURL, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse proxy URL: %w", err)
	}

	// 验证协议
	if strings.ToLower(parsedURL.Scheme) != "socks5" {
		return nil, fmt.Errorf("unsupported proxy scheme: %s (expected socks5)", parsedURL.Scheme)
	}

	// 创建基础拨号器
	baseDialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAlive,
	}

	// 创建 SOCKS5 拨号器
	socksDialer, err := proxy.FromURL(parsedURL, baseDialer)
	if err != nil {
		return nil, fmt.Errorf("failed to create SOCKS5 dialer: %w", err)
	}

	return socksDialer, nil
}

// CreateDialContextFromProxy 根据代理配置创建 DialContext 函数
// 返回 nil 表示不使用代理
func CreateDialContextFromProxy(cfg *agentconfig.ProxyConfig, dialTimeout, keepAlive time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	proxyType := GetProxyType(cfg)

	switch proxyType {
	case ProxyTypeSOCKS5:
		// SOCKS5 代理
		socksDialer, err := CreateSOCKS5Dialer(cfg, dialTimeout, keepAlive)
		if err != nil {
			// 如果创建 SOCKS5 拨号器失败，回退到直接连接
			// 记录错误但不阻止程序启动
			return func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialer := &net.Dialer{
					Timeout:   dialTimeout,
					KeepAlive: keepAlive,
				}
				return dialer.DialContext(ctx, network, addr)
			}
		}

		// 包装 socksDialer.Dial 以支持 context
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			// SOCKS5 代理不直接支持 context
			// 我们可以在另一个 goroutine 中调用，使用 context 控制超时
			type result struct {
				conn net.Conn
				err  error
			}

			resultCh := make(chan *result, 1)

			go func() {
				conn, err := socksDialer.Dial(network, addr)
				resultCh <- &result{conn: conn, err: err}
			}()

			select {
			case res := <-resultCh:
				return res.conn, res.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

	default:
		// HTTP/HTTPS 代理或不使用代理，返回 nil
		// HTTP/HTTPS 代理通过 Transport.Proxy 处理
		return nil
	}
}
