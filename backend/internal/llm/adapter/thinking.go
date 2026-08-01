package adapter

import (
	"strings"

	anthropictypes "github.com/wwsheng009/ai-agent-runtime/internal/types/anthropic"
)

func cloneAnthropicThinking(thinking *anthropictypes.Thinking) *anthropictypes.Thinking {
	if thinking == nil {
		return nil
	}

	cloned := *thinking
	if thinking.BudgetTokens != nil {
		budget := *thinking.BudgetTokens
		cloned.BudgetTokens = &budget
	}
	return &cloned
}

func normalizeAnthropicThinkingType(thinkingType string) string {
	return strings.ToLower(strings.TrimSpace(thinkingType))
}

func normalizeAnthropicThinkingEffort(effort string) string {
	// Effort values are provider-defined strings. Normalize only whitespace;
	// lower-casing would rewrite a manually supplied provider-specific value.
	return strings.TrimSpace(effort)
}

func normalizeRuntimeReasoningEffort(effort string) string {
	return strings.TrimSpace(effort)
}

func buildAnthropicThinkingFromReasoningEffort(effort string, budgets map[string]int) *anthropictypes.Thinking {
	normalized := normalizeRuntimeReasoningEffort(effort)
	if normalized == "" {
		return nil
	}

	// Check for adaptive mode (used by Claude 5 / late-4 adaptive thinking)
	if t := buildAnthropicAdaptiveThinking(effort, budgets); t != nil {
		return t
	}

	budget, ok := resolveConfiguredReasoningEffortBudget(effort, budgets)
	if !ok || budget <= 0 {
		// Model-card budgets are a transport hint, not an allowlist. If the
		// effort is not mapped locally, preserve it in Anthropic's native
		// adaptive/output_config representation and let the upstream decide
		// whether the value is supported.
		return &anthropictypes.Thinking{
			Type:   "adaptive",
			Effort: normalized,
		}
	}

	return &anthropictypes.Thinking{
		Type:         "enabled",
		Effort:       normalized,
		BudgetTokens: &budget,
	}
}

// buildAnthropicAdaptiveThinking returns a Thinking config with type "adaptive"
// for models that support it (Claude 5 / late-4 adaptive thinking). Returns nil if adaptive mode is not
// applicable or no budget is configured.
func buildAnthropicAdaptiveThinking(effort string, budgets map[string]int) *anthropictypes.Thinking {
	normalized := normalizeRuntimeReasoningEffort(effort)
	if normalized == "" {
		return nil
	}

	// Only use adaptive if the effort maps to a configured budget (i.e. the
	// provider has declared reasoning support for this effort level) but the
	// budget itself is 0 or unspecified — indicating "let the model decide".
	// In practice this is used for adaptive-thinking Claude models which auto-select depth.
	for _, key := range []string{normalized, "*", "default"} {
		if budget, ok := lookupReasoningEffortBudget(budgets, key); ok && budget == 0 {
			return &anthropictypes.Thinking{
				Type:   "adaptive",
				Effort: normalizeRuntimeReasoningEffort(effort),
			}
		}
	}

	return nil
}

// resolveConfiguredReasoningEffortBudget only accepts budgets that are
// explicitly declared in config (exact match, "*" or "default").
func resolveConfiguredReasoningEffortBudget(effort string, budgets map[string]int) (int, bool) {
	normalized := normalizeRuntimeReasoningEffort(effort)
	if normalized == "" {
		return 0, false
	}

	for _, key := range []string{normalized, "*", "default"} {
		if budget, ok := lookupReasoningEffortBudget(budgets, key); ok && budget > 0 {
			return budget, true
		}
	}

	return 0, false
}

// lookupReasoningEffortBudget matches configuration keys case-insensitively
// while leaving the original effort value untouched for wire serialization.
func lookupReasoningEffortBudget(budgets map[string]int, key string) (int, bool) {
	for configuredKey, budget := range budgets {
		if strings.EqualFold(strings.TrimSpace(configuredKey), strings.TrimSpace(key)) {
			return budget, true
		}
	}
	return 0, false
}

func buildGeminiThinkingConfigFromReasoningEffort(effort string, budgets map[string]int) map[string]interface{} {
	normalized := normalizeRuntimeReasoningEffort(effort)
	if normalized == "" {
		return nil
	}

	budget, ok := resolveConfiguredReasoningEffortBudget(effort, budgets)
	if ok && budget > 0 {
		return map[string]interface{}{
			"includeThoughts": true,
			"thinkingBudget":  budget,
		}
	}

	// Gemini models/gateways that expose thinkingLevel accept the provider
	// effort string directly. Use this fallback instead of dropping an effort
	// that is newer or more specific than the local budget catalog.
	return map[string]interface{}{
		"includeThoughts": true,
		"thinkingLevel":   normalized,
	}
}

func buildGeminiThinkingConfigFromThinking(thinking *anthropictypes.Thinking, budgets map[string]int) map[string]interface{} {
	if thinking == nil {
		return nil
	}
	switch normalizeAnthropicThinkingType(thinking.Type) {
	case "", "disabled", "none":
		return nil
	}
	if thinking.BudgetTokens != nil {
		return map[string]interface{}{
			"includeThoughts": true,
			"thinkingBudget":  *thinking.BudgetTokens,
		}
	}
	if effort := normalizeRuntimeReasoningEffort(thinking.Effort); effort != "" {
		return buildGeminiThinkingConfigFromReasoningEffort(effort, budgets)
	}
	return nil
}
