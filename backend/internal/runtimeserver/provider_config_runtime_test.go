package runtimeserver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestBuildRuntimeProviderConfigsMergesGlobalAndProviderProxy(t *testing.T) {
	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			Timeout: 45 * time.Second,
			Backoff: agentconfig.BackoffConfig{
				InitialInterval: 350 * time.Millisecond,
				MaxInterval:     4 * time.Second,
				Multiplier:      1.8,
			},
			Proxy: agentconfig.ProxyConfig{
				Enabled: true,
				HTTP:    "http://127.0.0.1:10810",
				NoProxy: "localhost,127.0.0.1",
			},
			Items: map[string]agentconfig.Provider{
				"openai-main": {
					Enabled:      true,
					Protocol:     "openai",
					BaseURL:      "https://api.example.com",
					DefaultModel: "gpt-5",
					ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
						"gpt-5": {
							MaxContextTokens:      272000,
							AutoCompactTokenLimit: 240000,
						},
					},
					Proxy: &agentconfig.ProxyConfig{
						Enabled: true,
						HTTPS:   "socks5://127.0.0.1:10811",
					},
				},
			},
		},
		Retry: &agentconfig.RetryConfig{
			Enabled:           true,
			DefaultMaxRetries: 4,
			Rules: []agentconfig.RetryRuleConfig{
				{
					Name:         "http_5xx_retry",
					Enabled:      true,
					MaxRetries:   4,
					RetryDelayMS: 900,
					StatusCode: agentconfig.RetryStatusCodeConfig{
						Range: "500-504",
					},
				},
			},
		},
	}

	result := buildRuntimeProviderConfigs(cfg)
	require.Contains(t, result, "openai-main")
	require.NotNil(t, result["openai-main"].Proxy)
	require.Equal(t, "http://127.0.0.1:10810", result["openai-main"].Proxy.HTTP)
	require.Equal(t, "socks5://127.0.0.1:10811", result["openai-main"].Proxy.HTTPS)
	require.Equal(t, "localhost,127.0.0.1", result["openai-main"].Proxy.NoProxy)
	require.True(t, result["openai-main"].Proxy.Enabled)
	require.Equal(t, 350*time.Millisecond, result["openai-main"].RetryTuning.BaseDelay)
	require.Equal(t, 4*time.Second, result["openai-main"].RetryTuning.MaxDelay)
	require.Equal(t, 1.8, result["openai-main"].RetryTuning.Multiplier)
	require.Equal(t, 4, result["openai-main"].MaxRetries)
	require.Len(t, result["openai-main"].RetryRules, 1)
	require.Equal(t, "http_5xx_retry", result["openai-main"].RetryRules[0].Name)
	require.Equal(t, 4, result["openai-main"].RetryRules[0].MaxRetries)
	require.Equal(t, 900*time.Millisecond, result["openai-main"].RetryRules[0].RetryDelay)
	require.Equal(t, "500-504", result["openai-main"].RetryRules[0].StatusCode.Range)
	require.Equal(t, 272000, result["openai-main"].ModelCapabilities["gpt-5"].MaxContextTokens)
	require.Equal(t, 240000, result["openai-main"].ModelCapabilities["gpt-5"].AutoCompactTokenLimit)
}
