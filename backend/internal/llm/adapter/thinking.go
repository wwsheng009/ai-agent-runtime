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
	return strings.ToLower(strings.TrimSpace(effort))
}

func normalizeRuntimeReasoningEffort(effort string) string {
	return strings.ToLower(strings.TrimSpace(effort))
}

func buildAnthropicThinkingFromReasoningEffort(effort string, budgets map[string]int) *anthropictypes.Thinking {
	switch normalizeRuntimeReasoningEffort(effort) {
	case "":
		return nil
	case "none":
		return &anthropictypes.Thinking{Type: "disabled"}
	}

	budget, ok := resolveConfiguredReasoningEffortBudget(effort, budgets)
	if !ok || budget <= 0 {
		return nil
	}

	return &anthropictypes.Thinking{
		Type:         "enabled",
		Effort:       normalizeRuntimeReasoningEffort(effort),
		BudgetTokens: &budget,
	}
}

// resolveConfiguredReasoningEffortBudget only accepts budgets that are
// explicitly declared in config (exact match, "*" or "default").
func resolveConfiguredReasoningEffortBudget(effort string, budgets map[string]int) (int, bool) {
	normalized := normalizeRuntimeReasoningEffort(effort)
	if normalized == "" {
		return 0, false
	}

	for _, key := range []string{normalized, "*", "default"} {
		if budget, ok := budgets[key]; ok && budget > 0 {
			return budget, true
		}
	}

	return 0, false
}

func buildGeminiThinkingConfigFromReasoningEffort(effort string, budgets map[string]int) map[string]interface{} {
	switch normalizeRuntimeReasoningEffort(effort) {
	case "":
		return nil
	case "none":
		return nil
	}

	budget, ok := resolveConfiguredReasoningEffortBudget(effort, budgets)
	if !ok || budget <= 0 {
		return nil
	}

	return map[string]interface{}{
		"includeThoughts": true,
		"thinkingBudget":  budget,
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
