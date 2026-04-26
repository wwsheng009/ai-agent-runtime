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
		return 3
	}
	maxRetries := cfg.Providers.MaxRetries
	if maxRetries <= 0 && cfg.Retry != nil && cfg.Retry.DefaultMaxRetries > 0 {
		maxRetries = cfg.Retry.DefaultMaxRetries
	}
	if maxRetries <= 0 {
		maxRetries = 3
	}
	return maxRetries
}
