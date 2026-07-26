package llm

import (
	"os"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// Slot-reservation defaults mirror Claude Code:
// most turns finish well under 8k output tokens, so requesting the full
// model/provider ceiling over-reserves capacity. Requests that hit the
// capped default can escalate once to ESCALATED_MAX_TOKENS.
const (
	// CappedDefaultMaxTokens is the slot-reservation default applied when
	// no explicit request budget or env override is present.
	CappedDefaultMaxTokens = 8000
	// EscalatedMaxTokens is the one-shot retry budget after a capped
	// request hits max_output_tokens / finish_reason=length.
	EscalatedMaxTokens = 64000
	// DefaultModelMaxOutputTokens is used when neither capability nor
	// protocol/model heuristics know a better default.
	DefaultModelMaxOutputTokens = 32000
	// DefaultModelMaxOutputUpperLimit is the conservative ceiling used when
	// capability.MaxTokens is unset.
	DefaultModelMaxOutputUpperLimit = 64000

	// EnvMaxOutputTokens is the user-facing override for request max_tokens.
	// Kept for Claude Code compatibility; AICLI_MAX_OUTPUT_TOKENS is also accepted.
	EnvMaxOutputTokens      = "CLAUDE_CODE_MAX_OUTPUT_TOKENS"
	EnvAICLIMaxOutputTokens = "AICLI_MAX_OUTPUT_TOKENS"
	// EnvDisableMaxTokensCap disables the 8k slot-reservation default.
	EnvDisableMaxTokensCap = "AICLI_DISABLE_MAX_TOKENS_CAP"
)

// ModelMaxOutputTokens describes a model's default request budget and hard
// output-token ceiling.
type ModelMaxOutputTokens struct {
	Default     int
	UpperLimit  int
	Source      string
	Capped      bool
	EnvOverride bool
}

// ResolveModelMaxOutputTokens returns the model's native default/upperLimit
// pair before slot-reservation or env overrides are applied.
func ResolveModelMaxOutputTokens(
	protocol string,
	model string,
	capability agentconfig.ModelCapabilitySpec,
	hasCapability bool,
	providerLimit int,
) ModelMaxOutputTokens {
	result := ModelMaxOutputTokens{
		Default: DefaultModelMaxOutputTokens,
		// UpperLimit 0 means "unknown" — do not invent a tight ceiling for
		// non-Claude models that may legitimately support large outputs.
		UpperLimit: 0,
		Source:     "family_default",
	}

	if hasCapability && capability.MaxTokens > 0 {
		result.UpperLimit = capability.MaxTokens
		result.Default = minPositive(capability.MaxTokens, DefaultModelMaxOutputTokens)
		result.Source = "capability"
	} else if looksLikeClaudeModel(model) ||
		(strings.EqualFold(strings.TrimSpace(protocol), "anthropic") && strings.Contains(strings.ToLower(model), "claude")) {
		// Claude-family ceilings when capability cards omit max_tokens.
		// Keep defaults closer to Anthropic model tables (2026-07):
		// Fable/Mythos/Opus 5/Sonnet 5/Opus 4.x/Sonnet 4.6 → 128k;
		// older Claude 4 / Haiku 4.5 → 64k.
		normalized := strings.ToLower(strings.TrimSpace(model))
		switch {
		case strings.Contains(normalized, "fable-5") ||
			strings.Contains(normalized, "mythos-5") ||
			strings.Contains(normalized, "mythos-preview") ||
			strings.Contains(normalized, "opus-5") ||
			strings.Contains(normalized, "sonnet-5") ||
			strings.Contains(normalized, "opus-4-8") ||
			strings.Contains(normalized, "opus-4-7") ||
			strings.Contains(normalized, "opus-4-6"):
			result.Default = 64000
			result.UpperLimit = defaultClaudeMaxOutputTokens
			result.Source = "claude_adaptive_recent"
		case strings.Contains(normalized, "sonnet-4-6") || strings.Contains(normalized, "sonnet-4-7"):
			result.Default = 32000
			result.UpperLimit = defaultClaudeMaxOutputTokens
			result.Source = "claude_sonnet_recent"
		case strings.Contains(normalized, "opus-4") || strings.Contains(normalized, "sonnet-4") || strings.Contains(normalized, "haiku-4"):
			result.Default = 32000
			result.UpperLimit = 64000
			result.Source = "claude_4_family"
		default:
			result.Default = 32000
			result.UpperLimit = defaultClaudeMaxOutputTokens
			result.Source = "claude_family"
		}
	}

	// Provider template max_tokens_limit is a hard ceiling when configured,
	// but it must not become the default request budget.
	if providerLimit > 0 {
		if result.UpperLimit <= 0 || providerLimit < result.UpperLimit {
			result.UpperLimit = providerLimit
		}
		if result.Source == "family_default" || result.Source == "capability" {
			result.Source = result.Source + "+provider_limit"
		}
	}

	if result.Default <= 0 {
		result.Default = DefaultModelMaxOutputTokens
	}
	// Only clamp default to upperLimit when a real ceiling is known.
	if result.UpperLimit > 0 && result.Default > result.UpperLimit {
		result.Default = result.UpperLimit
	}
	return result
}

// ResolveRequestMaxTokens chooses the effective request max_tokens using
// Claude Code-style precedence:
//  1. explicit request budget (if > 0), clamped to upperLimit
//  2. env override CLAUDE_CODE_MAX_OUTPUT_TOKENS / AICLI_MAX_OUTPUT_TOKENS
//  3. min(modelDefault, CappedDefaultMaxTokens) when cap enabled
//  4. model default otherwise
func ResolveRequestMaxTokens(
	protocol string,
	model string,
	requested int,
	capability agentconfig.ModelCapabilitySpec,
	hasCapability bool,
	providerLimit int,
) ModelMaxOutputTokens {
	resolved := ResolveModelMaxOutputTokens(protocol, model, capability, hasCapability, providerLimit)

	// Explicit request wins (still clamped to the hard ceiling).
	if requested > 0 {
		if resolved.UpperLimit > 0 && requested > resolved.UpperLimit {
			resolved.Default = resolved.UpperLimit
			resolved.Source = "explicit_clamped"
			return resolved
		}
		resolved.Default = requested
		resolved.Source = "explicit"
		return resolved
	}

	defaultTokens := resolved.Default
	if isMaxTokensCapEnabled() {
		capped := minPositive(defaultTokens, CappedDefaultMaxTokens)
		if capped < defaultTokens {
			resolved.Capped = true
			resolved.Source = resolved.Source + "+capped_default"
		}
		defaultTokens = capped
	}

	if envValue, ok := readMaxOutputTokensEnv(); ok {
		resolved.EnvOverride = true
		if envValue > resolved.UpperLimit {
			envValue = resolved.UpperLimit
		}
		if envValue > 0 {
			resolved.Default = envValue
			resolved.Source = "env_override"
			resolved.Capped = false
			return resolved
		}
	}

	resolved.Default = defaultTokens
	return resolved
}

// CapRequestMaxTokens clamps an already-chosen request budget to the best
// known hard ceiling. Unlike ResolveRequestMaxTokens it never lowers a
// positive budget to the 8k slot-reservation default.
func CapRequestMaxTokens(
	protocol string,
	model string,
	requested int,
	capability agentconfig.ModelCapabilitySpec,
	hasCapability bool,
	providerLimit int,
) int {
	if requested <= 0 {
		return requested
	}
	resolved := ResolveModelMaxOutputTokens(protocol, model, capability, hasCapability, providerLimit)
	if resolved.UpperLimit > 0 && requested > resolved.UpperLimit {
		return resolved.UpperLimit
	}
	return requested
}

// EscalatedRequestMaxTokens returns the one-shot escalation budget for a
// request that hit the capped default. Returns 0 when escalation is not
// applicable (already at/above escalated budget, env override present, etc.).
func EscalatedRequestMaxTokens(current int, upperLimit int) int {
	if current <= 0 {
		return 0
	}
	if !isMaxTokensCapEnabled() {
		return 0
	}
	if _, ok := readMaxOutputTokensEnv(); ok {
		// User chose an explicit budget; do not silently escalate past it.
		return 0
	}
	target := EscalatedMaxTokens
	if upperLimit > 0 && upperLimit < target {
		target = upperLimit
	}
	if target <= current {
		return 0
	}
	return target
}

// IsMaxOutputTokensStop reports provider finish/stop reasons that indicate the
// model hit the requested max_tokens / max_output_tokens budget.
func IsMaxOutputTokensStop(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens", "max_output_tokens_exceeded":
		return true
	default:
		return false
	}
}

func isMaxTokensCapEnabled() bool {
	// Cap is on by default; set AICLI_DISABLE_MAX_TOKENS_CAP=1 to use full
	// model defaults (still subject to upperLimit and env override).
	value := strings.TrimSpace(os.Getenv(EnvDisableMaxTokensCap))
	if value == "" {
		return true
	}
	return !isTruthyEnv(value)
}

func readMaxOutputTokensEnv() (int, bool) {
	for _, key := range []string{EnvMaxOutputTokens, EnvAICLIMaxOutputTokens} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			continue
		}
		return parsed, true
	}
	return 0, false
}

func isTruthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "y":
		return true
	default:
		return false
	}
}

func minPositive(values ...int) int {
	result := 0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if result == 0 || value < result {
			result = value
		}
	}
	return result
}
