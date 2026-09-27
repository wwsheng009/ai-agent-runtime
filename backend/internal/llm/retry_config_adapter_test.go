package llm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestRetryConfigAdapterPreservesScheduleAndUnlimitedRetries(t *testing.T) {
	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			MaxRetries: -1,
			Backoff: agentconfig.BackoffConfig{
				MaxInterval:   6 * time.Minute,
				Randomization: 0.1,
				Schedule:      []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute},
			},
		},
	}

	tuning := RetryTuningFromAgentConfig(cfg)
	require.Equal(t, cfg.Providers.Backoff.Schedule, tuning.Schedule)
	require.Equal(t, -1, ProviderMaxRetriesFromAgentConfig(cfg))
	require.Equal(t, -1, ProviderMaxRetriesFromAgentConfig(nil))
}

func TestProviderResponseHeaderTimeoutFromAgentConfigFallback(t *testing.T) {
	cases := []struct {
		name string
		cfg  *agentconfig.Config
		want time.Duration
	}{
		{"nil config gets default", nil, DefaultResponseHeaderTimeout},
		{"unset gets default", &agentconfig.Config{}, DefaultResponseHeaderTimeout},
		{"explicit zero gets default", &agentconfig.Config{Providers: agentconfig.ProvidersConfig{HTTPTimeout: agentconfig.HTTPTimeout{ResponseHeaderTimeout: 0}}}, DefaultResponseHeaderTimeout},
		{"negative gets default", &agentconfig.Config{Providers: agentconfig.ProvidersConfig{HTTPTimeout: agentconfig.HTTPTimeout{ResponseHeaderTimeout: -1}}}, DefaultResponseHeaderTimeout},
		{"explicit positive preserved", &agentconfig.Config{Providers: agentconfig.ProvidersConfig{HTTPTimeout: agentconfig.HTTPTimeout{ResponseHeaderTimeout: 20 * time.Second}}}, 20 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ProviderResponseHeaderTimeoutFromAgentConfig(tc.cfg))
		})
	}
}

func TestProviderMaxTransportRetriesFromAgentConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  *agentconfig.Config
		want int
	}{
		{"nil config gets default", nil, DefaultTransportMaxRetries},
		{"unset gets default", &agentconfig.Config{}, DefaultTransportMaxRetries},
		{"explicit zero gets default", &agentconfig.Config{Providers: agentconfig.ProvidersConfig{TransportMaxRetries: 0}}, DefaultTransportMaxRetries},
		{"negative means unlimited", &agentconfig.Config{Providers: agentconfig.ProvidersConfig{TransportMaxRetries: -1}}, -1},
		{"explicit positive preserved", &agentconfig.Config{Providers: agentconfig.ProvidersConfig{TransportMaxRetries: 5}}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ProviderMaxTransportRetriesFromAgentConfig(tc.cfg))
		})
	}
}

func TestRetryRulesFromAgentConfigArmsInvalidEncryptedContentRecovery(t *testing.T) {
	cfg := &agentconfig.Config{
		Retry: &agentconfig.RetryConfig{
			Enabled: true,
			InvalidEncryptedContentRecovery: agentconfig.InvalidEncryptedContentRecoveryConfig{
				StripClientStateOnce: true,
			},
		},
	}
	rules := RetryRulesFromAgentConfig(cfg)
	require.Len(t, rules, 1)
	require.Equal(t, "invalid_encrypted_content_strip_client_state", rules[0].Name)
	require.Equal(t, RetryRuleActionStripClientState, rules[0].Action)
	require.Contains(t, rules[0].ErrorCode.Codes, "invalid_encrypted_content")

	policy := newProviderRetryPolicy(-1, 0, RetryTuning{}, rules)
	require.True(t, policy.StripClientStateOnInvalidEncryptedContent)

	// 未开启开关时不得凭空注入恢复规则（其余规则照旧）。
	off := RetryRulesFromAgentConfig(&agentconfig.Config{Retry: &agentconfig.RetryConfig{Enabled: true}})
	require.Empty(t, off)

	existing := RetryRulesFromAgentConfig(&agentconfig.Config{Retry: &agentconfig.RetryConfig{
		Enabled: true,
		Rules: []agentconfig.RetryRuleConfig{{
			Name:    "http_5xx_retry",
			Enabled: true,
			Keyword: agentconfig.RetryKeywordConfig{Values: []string{"internal server error"}},
		}},
	}})
	require.Len(t, existing, 1)
	require.False(t, newProviderRetryPolicy(-1, 0, RetryTuning{}, existing).StripClientStateOnInvalidEncryptedContent)
}
