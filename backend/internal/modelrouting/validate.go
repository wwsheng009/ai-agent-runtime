package modelrouting

import (
	"fmt"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ValidateConfig validates static routing configuration without requiring a
// runtime provider catalog.
func ValidateConfig(cfg *agentconfig.AICLISubagentRoutingConfig) error {
	if cfg == nil {
		return nil
	}
	if _, ok := NormalizeCompatibilityMode(cfg.CompatibilityMode); !ok {
		return fmt.Errorf("invalid subagent compatibility mode %q", cfg.CompatibilityMode)
	}
	if strings.TrimSpace(cfg.DefaultDifficulty) != "" {
		if _, ok := NormalizeDifficulty(cfg.DefaultDifficulty); !ok {
			return fmt.Errorf("invalid default subagent difficulty %q", cfg.DefaultDifficulty)
		}
	}
	if strings.TrimSpace(cfg.UnsupportedReasoningPolicy) != "" {
		if _, ok := NormalizeUnsupportedReasoningPolicy(cfg.UnsupportedReasoningPolicy); !ok {
			return fmt.Errorf("invalid subagent unsupported reasoning policy %q", cfg.UnsupportedReasoningPolicy)
		}
	}
	if strings.TrimSpace(cfg.OnReasoningUnsupported) != "" {
		if _, ok := NormalizeUnsupportedReasoningPolicy(cfg.OnReasoningUnsupported); !ok {
			return fmt.Errorf("invalid subagent on reasoning unsupported policy %q", cfg.OnReasoningUnsupported)
		}
	}
	for key, profile := range cfg.Levels {
		difficulty, ok := NormalizeDifficulty(key)
		if !ok {
			return fmt.Errorf("invalid subagent difficulty route key %q", key)
		}
		if err := validateRouteProfile(difficulty, profile, cfg); err != nil {
			return err
		}
	}
	for role, levels := range cfg.Roles {
		if strings.TrimSpace(role) == "" {
			return fmt.Errorf("subagent role override key cannot be empty")
		}
		for key, profile := range levels {
			difficulty, ok := NormalizeDifficulty(key)
			if !ok {
				return fmt.Errorf("invalid subagent role route key %q.%q", role, key)
			}
			if err := validateRouteProfile(role+"."+difficulty, profile, cfg); err != nil {
				return err
			}
		}
	}
	if cfg.MaxExpertConcurrency < 0 {
		return fmt.Errorf("max_expert_concurrency cannot be negative")
	}
	return nil
}

func validateRouteProfile(label string, profile agentconfig.AICLISubagentRouteProfile, cfg *agentconfig.AICLISubagentRoutingConfig) error {
	if profile.MaxTokens < 0 {
		return fmt.Errorf("subagent route %s max_tokens cannot be negative", label)
	}
	if profile.Timeout < 0 {
		return fmt.Errorf("subagent route %s timeout cannot be negative", label)
	}
	if !InheritParentWhenMissing(cfg) && (strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == "") {
		return fmt.Errorf("subagent route %s must set provider and model when inherit_parent_when_missing=false", label)
	}
	return nil
}
