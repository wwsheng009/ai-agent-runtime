package llm

import (
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// RetryTuningFromAgentConfig builds llm retry tuning from the gateway/agent config.
func RetryTuningFromAgentConfig(cfg *agentconfig.Config) RetryTuning {
	if cfg == nil {
		return RetryTuning{}
	}
	tuning := RetryTuning{
		BaseDelay:      cfg.Providers.Backoff.InitialInterval,
		MaxDelay:       cfg.Providers.Backoff.MaxInterval,
		MaxElapsedTime: cfg.Providers.Backoff.MaxElapsedTime,
		Multiplier:     cfg.Providers.Backoff.Multiplier,
		Randomization:  cfg.Providers.Backoff.Randomization,
		Schedule:       append([]time.Duration(nil), cfg.Providers.Backoff.Schedule...),
	}
	if cfg.Retry != nil {
		if tuning.BaseDelay <= 0 && cfg.Retry.DefaultRetryDelayMS > 0 {
			tuning.BaseDelay = time.Duration(cfg.Retry.DefaultRetryDelayMS) * time.Millisecond
		}
		if tuning.Multiplier < 1 && cfg.Retry.DefaultBackoffMultiplier >= 1 {
			tuning.Multiplier = cfg.Retry.DefaultBackoffMultiplier
		}
	}
	return tuning
}

// RetryRulesFromAgentConfig converts configured retry rules into llm retry rules.
func RetryRulesFromAgentConfig(cfg *agentconfig.Config) []RetryRule {
	if cfg == nil || cfg.Retry == nil || !cfg.Retry.Enabled || len(cfg.Retry.Rules) == 0 {
		return nil
	}
	result := make([]RetryRule, 0, len(cfg.Retry.Rules))
	for _, rule := range cfg.Retry.Rules {
		result = append(result, RetryRule{
			Name:              rule.Name,
			Description:       rule.Description,
			Enabled:           rule.Enabled,
			Action:            RetryRuleAction(rule.Action),
			MaxRetries:        rule.MaxRetries,
			RetryDelay:        time.Duration(rule.RetryDelayMS) * time.Millisecond,
			BackoffMultiplier: rule.BackoffMultiplier,
			Keyword: RetryKeywordMatcher{
				CaseSensitive: rule.Keyword.CaseSensitive,
				Values:        append([]string(nil), rule.Keyword.Values...),
				Patterns:      append([]string(nil), rule.Keyword.Patterns...),
			},
			ErrorCode: RetryErrorCodeMatcher{
				Codes:   append([]string(nil), rule.ErrorCode.Codes...),
				Pattern: rule.ErrorCode.Pattern,
			},
			StatusCode: RetryStatusCodeMatcher{
				Codes: append([]int(nil), rule.StatusCode.Codes...),
				Range: rule.StatusCode.Range,
			},
		})
	}
	return result
}

// ProviderMaxRetriesFromAgentConfig resolves the default provider retry count.
func ProviderMaxRetriesFromAgentConfig(cfg *agentconfig.Config) int {
	if cfg == nil {
		return -1
	}
	maxRetries := cfg.Providers.MaxRetries
	if maxRetries < 0 {
		return maxRetries
	}
	if maxRetries <= 0 && cfg.Retry != nil && cfg.Retry.DefaultMaxRetries > 0 {
		maxRetries = cfg.Retry.DefaultMaxRetries
	} else if maxRetries == 0 && cfg.Retry != nil && cfg.Retry.DefaultMaxRetries < 0 {
		return cfg.Retry.DefaultMaxRetries
	}
	if maxRetries <= 0 {
		maxRetries = 10
	}
	return maxRetries
}

// DefaultTransportMaxRetries is the default transport-level retry budget,
// inspired by codex-rs' tight request-level transport budget. Note the
// semantics: aicli treats it as a cap on total transport attempts (like
// MaxAttempts), not as an additional retry count.
// Transport-layer failures -- connection errors, response-header timeouts,
// TLS failures -- get a tighter budget than business-level retries: retrying
// a dead connection from scratch rarely succeeds immediately, so a small
// budget bounds the hang (e.g. 4 x response-header timeout) while generous
// business retries keep 429/5xx flows retrying to the business budget.
const DefaultTransportMaxRetries = 4

// ProviderMaxTransportRetriesFromAgentConfig resolves the transport-level
// retry budget. A negative value means unlimited (the header-timeout streak
// guard then provides the bound); zero falls back to
// DefaultTransportMaxRetries.
func ProviderMaxTransportRetriesFromAgentConfig(cfg *agentconfig.Config) int {
	if cfg == nil {
		return DefaultTransportMaxRetries
	}
	transportRetries := cfg.Providers.TransportMaxRetries
	if transportRetries < 0 {
		return transportRetries
	}
	if transportRetries <= 0 {
		transportRetries = DefaultTransportMaxRetries
	}
	return transportRetries
}

// ProviderStreamReadTimeoutFromAgentConfig resolves the streaming idle timeout
// (chunk-level, "no data for N") from the global HTTP timeout config. Returns 0
// when unset, meaning the idle guard is disabled.
func ProviderStreamReadTimeoutFromAgentConfig(cfg *agentconfig.Config) time.Duration {
	if cfg == nil {
		return 0
	}
	return cfg.Providers.HTTPTimeout.StreamReadTimeout
}

// DefaultResponseHeaderTimeout bounds how long a request may wait for the
// upstream's response headers once the request bytes have been sent. It is
// the last line of defense against a hung upstream that neither responds nor
// closes: without it, client.Do can block forever on the response-header
// select while every agent waiting on the request looks wedged.
const DefaultResponseHeaderTimeout = 60 * time.Second

// ProviderResponseHeaderTimeoutFromAgentConfig resolves the response-header
// wait bound (the transport's ResponseHeaderTimeout) from the global HTTP
// timeout config. An unset or non-positive value falls back to
// DefaultResponseHeaderTimeout so a missing field can never leave requests
// waiting for headers indefinitely.
func ProviderResponseHeaderTimeoutFromAgentConfig(cfg *agentconfig.Config) time.Duration {
	if cfg == nil {
		return DefaultResponseHeaderTimeout
	}
	if t := cfg.Providers.HTTPTimeout.ResponseHeaderTimeout; t > 0 {
		return t
	}
	return DefaultResponseHeaderTimeout
}
