package httpclient

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestGetProxyType(t *testing.T) {
	tests := []struct {
		name     string
		config   *agentconfig.ProxyConfig
		expected ProxyType
	}{
		{
			name:     "nil config",
			config:   nil,
			expected: ProxyTypeNone,
		},
		{
			name:     "empty config",
			config:   &agentconfig.ProxyConfig{},
			expected: ProxyTypeNone,
		},
		{
			name: "HTTP proxy",
			config: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTP:    "http://localhost:8080",
			},
			expected: ProxyTypeHTTP,
		},
		{
			name: "HTTPS proxy",
			config: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTPS:   "https://localhost:8443",
			},
			expected: ProxyTypeHTTPS,
		},
		{
			name: "SOCKS5 proxy",
			config: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTP:    "socks5://localhost:10808",
			},
			expected: ProxyTypeSOCKS5,
		},
		{
			name: "SOCKS5 with HTTPS",
			config: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTPS:   "socks5://localhost:10808",
			},
			expected: ProxyTypeSOCKS5,
		},
		{
			name: "disabled proxy",
			config: &agentconfig.ProxyConfig{
				Enabled: false,
				HTTP:    "http://localhost:8080",
			},
			expected: ProxyTypeNone,
		},
		{
			name: "HTTPS takes precedence over HTTP",
			config: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTP:    "http://localhost:8080",
				HTTPS:   "socks5://localhost:10808",
			},
			expected: ProxyTypeSOCKS5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetProxyType(tt.config)
			if result != tt.expected {
				t.Errorf("GetProxyType() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestProxyTypeToString(t *testing.T) {
	tests := []struct {
		name     string
		proxy    ProxyType
		expected string
	}{
		{"NONE", ProxyTypeNone, "NONE"},
		{"HTTP", ProxyTypeHTTP, "HTTP"},
		{"HTTPS", ProxyTypeHTTPS, "HTTPS"},
		{"SOCKS5", ProxyTypeSOCKS5, "SOCKS5"},
		{"UNKNOWN", ProxyType(99), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := proxyTypeToString(tt.proxy)
			if result != tt.expected {
				t.Errorf("proxyTypeToString() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestCreateSOCKS5Dialer(t *testing.T) {
	tests := []struct {
		name        string
		proxyConfig *agentconfig.ProxyConfig
		shouldError bool
	}{
		{
			name: "valid SOCKS5 proxy",
			proxyConfig: &agentconfig.ProxyConfig{
				HTTP: "socks5://localhost:10808",
			},
			shouldError: false,
		},
		{
			name: "SOCKS5 with auth",
			proxyConfig: &agentconfig.ProxyConfig{
				HTTP: "socks5://user:pass@localhost:10808",
			},
			shouldError: false,
		},
		{
			name: "invalid proxy URL",
			proxyConfig: &agentconfig.ProxyConfig{
				HTTP: "://invalid",
			},
			shouldError: true,
		},
		{
			name: "unsupported scheme",
			proxyConfig: &agentconfig.ProxyConfig{
				HTTP: "http://localhost:8080",
			},
			shouldError: true,
		},
		{
			name:        "empty proxy URL",
			proxyConfig: &agentconfig.ProxyConfig{},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialer, err := CreateSOCKS5Dialer(tt.proxyConfig, 10*time.Second, 30*time.Second)
			if tt.shouldError {
				if err == nil {
					t.Errorf("CreateSOCKS5Dialer() expected error, got nil")
				}
				if dialer != nil {
					t.Errorf("CreateSOCKS5Dialer() expected nil dialer, got %v", dialer)
				}
			} else {
				if err != nil {
					t.Errorf("CreateSOCKS5Dialer() unexpected error: %v", err)
				}
				if dialer == nil {
					t.Errorf("CreateSOCKS5Dialer() expected non-nil dialer, got nil")
				}
			}
		})
	}
}

func TestCreateDialContextFromProxy(t *testing.T) {
	type testCase struct {
		name        string
		proxyConfig *agentconfig.ProxyConfig
		shouldBeNil bool
	}

	tests := []testCase{
		{
			name: "nil config",
			proxyConfig: &agentconfig.ProxyConfig{
				Enabled: false,
			},
			shouldBeNil: true,
		},
		{
			name: "HTTP proxy",
			proxyConfig: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTP:    "http://localhost:8080",
			},
			shouldBeNil: true, // HTTP proxy uses Proxy field, not DialContext
		},
		{
			name: "SOCKS5 proxy",
			proxyConfig: &agentconfig.ProxyConfig{
				Enabled: true,
				HTTP:    "socks5://localhost:10808",
			},
			shouldBeNil: false, // SOCKS5 uses DialContext
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialContext := CreateDialContextFromProxy(tt.proxyConfig, 10*time.Second, 30*time.Second)
			if tt.shouldBeNil {
				if dialContext != nil {
					t.Errorf("CreateDialContextFromProxy() expected nil, got non-nil")
				}
			} else {
				if dialContext == nil {
					t.Errorf("CreateDialContextFromProxy() expected non-nil, got nil")
				}
			}
		})
	}
}
