package httpclient

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestProxyFunc(t *testing.T) {
	tests := []struct {
		name      string
		config    *agentconfig.ProxyConfig
		reqURL    string
		wantProxy bool
		proxyURL  string
	}{
		{
			name:      "nil config",
			config:    nil,
			reqURL:    "https://api.example.com/v1/chat",
			wantProxy: false, // 使用环境变量，可能返回 nil
		},
		{
			name:      "empty config",
			config:    &agentconfig.ProxyConfig{},
			reqURL:    "https://api.example.com/v1/chat",
			wantProxy: false,
		},
		{
			name: "http proxy for http request",
			config: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTP:    "http://localhost:10808",
			},
			reqURL:    "http://api.example.com/v1/chat",
			wantProxy: true,
			proxyURL:  "http://localhost:10808",
		},
		{
			name: "https proxy for https request",
			config: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTPS:   "http://localhost:10808",
			},
			reqURL:    "https://api.example.com/v1/chat",
			wantProxy: true,
			proxyURL:  "http://localhost:10808",
		},
		{
			name: "fallback to http proxy",
			config: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTP:    "socks5://localhost:10808",
			},
			reqURL:    "https://api.example.com/v1/chat",
			wantProxy: true,
			proxyURL:  "socks5://localhost:10808",
		},
		{
			name: "no proxy bypass",
			config: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTP:    "http://localhost:10808",
				NoProxy: "localhost,example.com",
			},
			reqURL:    "https://example.com/v1/chat",
			wantProxy: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxyFunc := ProxyFunc(tt.config)
			req := &http.Request{
				URL: mustParseURL(tt.reqURL),
			}

			proxy, err := proxyFunc(req)
			if err != nil {
				t.Errorf("ProxyFunc() error = %v", err)
				return
			}

			if tt.wantProxy {
				if proxy == nil {
					t.Errorf("ProxyFunc() expected proxy, got nil")
					return
				}
				if proxy.String() != tt.proxyURL {
					t.Errorf("ProxyFunc() = %v, expected %v", proxy.String(), tt.proxyURL)
				}
			}
		})
	}
}

func TestParseProxyURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "http proxy",
			input:    "http://localhost:10808",
			expected: "http://localhost:10808",
			wantErr:  false,
		},
		{
			name:     "socks5 proxy",
			input:    "socks5://localhost:10808",
			expected: "socks5://localhost:10808",
			wantErr:  false,
		},
		{
			name:     "socks5 with auth",
			input:    "socks5://user:pass@localhost:10808",
			expected: "socks5://user:pass@localhost:10808",
			wantErr:  false,
		},
		{
			name:     "no scheme",
			input:    "localhost:10808",
			expected: "http://localhost:10808",
			wantErr:  false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseProxyURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseProxyURL() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("parseProxyURL() error = %v", err)
				return
			}

			if tt.expected == "" {
				if result != nil {
					t.Errorf("parseProxyURL() expected nil, got %v", result)
				}
				return
			}

			if result.String() != tt.expected {
				t.Errorf("parseProxyURL() = %v, expected %v", result.String(), tt.expected)
			}
		})
	}
}

func TestShouldBypassProxy(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		noProxy  string
		expected bool
	}{
		{
			name:     "empty no_proxy",
			host:     "example.com",
			noProxy:  "",
			expected: false,
		},
		{
			name:     "exact match",
			host:     "example.com",
			noProxy:  "example.com",
			expected: true,
		},
		{
			name:     "wildcard match",
			host:     "api.example.com",
			noProxy:  ".example.com",
			expected: true,
		},
		{
			name:     "wildcard root domain",
			host:     "example.com",
			noProxy:  ".example.com",
			expected: true,
		},
		{
			name:     "no match",
			host:     "other.com",
			noProxy:  "example.com",
			expected: false,
		},
		{
			name:     "multiple entries",
			host:     "internal.com",
			noProxy:  "localhost,example.com,internal.com",
			expected: true,
		},
		{
			name:     "with port",
			host:     "example.com:443",
			noProxy:  "example.com",
			expected: true,
		},
		{
			name:     "localhost",
			host:     "localhost",
			noProxy:  "localhost,127.0.0.1",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldBypassProxy(tt.host, tt.noProxy)
			if result != tt.expected {
				t.Errorf("shouldBypassProxy() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

// 辅助函数
func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
