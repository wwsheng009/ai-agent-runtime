package adapter

import (
	"strings"

	anthropictypes "github.com/ai-gateway/ai-agent-runtime/internal/types/anthropic"
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

func supportsAnthropicAdaptiveThinking(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "claude-opus-4-6") || strings.Contains(model, "claude-sonnet-4-6")
}

func mapAnthropicThinkingToReasoningEffort(model string, thinking *anthropictypes.Thinking) string {
	if thinking == nil {
		return ""
	}

	if effort := anthropicAdaptiveEffortToReasoningEffort(thinking.Effort); effort != "" {
		return effort
	}

	switch normalizeAnthropicThinkingType(thinking.Type) {
	case "":
		return ""
	case "disabled":
		return "none"
	case "adaptive":
		if supportsAnthropicAdaptiveThinking(model) {
			return "xhigh"
		}
		return "high"
	case "low", "medium", "high":
		return normalizeAnthropicThinkingType(thinking.Type)
	case "minimal":
		return "low"
	case "enabled":
		return mapThinkingBudgetToReasoningEffort(thinking.BudgetTokens)
	default:
		return "medium"
	}
}

func mapReasoningEffortToAnthropicThinking(model, effort string) *anthropictypes.Thinking {
	switch normalizeRuntimeReasoningEffort(effort) {
	case "", "none":
		return &anthropictypes.Thinking{Type: "disabled"}
	case "low", "medium", "high", "xhigh":
		if supportsAnthropicAdaptiveThinking(model) {
			thinking := &anthropictypes.Thinking{Type: "adaptive"}
			if adaptiveEffort := reasoningEffortToAnthropicAdaptive(effort); adaptiveEffort != "" {
				thinking.Effort = adaptiveEffort
			}
			return thinking
		}
		return &anthropictypes.Thinking{
			Type:         "enabled",
			BudgetTokens: mapReasoningEffortToThinkingBudget(effort),
		}
	default:
		return &anthropictypes.Thinking{
			Type:         "enabled",
			BudgetTokens: mapReasoningEffortToThinkingBudget("medium"),
		}
	}
}

func mapThinkingBudgetToReasoningEffort(budget *int) string {
	if budget == nil {
		return "medium"
	}

	switch {
	case *budget <= 0:
		return "medium"
	case *budget <= 1024:
		return "low"
	case *budget <= 8192:
		return "medium"
	case *budget <= 16384:
		return "high"
	default:
		return "xhigh"
	}
}

func mapReasoningEffortToThinkingBudget(effort string) *int {
	var budget int
	switch normalizeRuntimeReasoningEffort(effort) {
	case "low":
		budget = 1024
	case "medium":
		budget = 8192
	case "high":
		budget = 16384
	case "xhigh":
		budget = 32000
	default:
		budget = 8192
	}
	return &budget
}

func anthropicAdaptiveEffortToReasoningEffort(effort string) string {
	switch normalizeAnthropicThinkingEffort(effort) {
	case "":
		return ""
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "max", "xhigh":
		return "xhigh"
	case "disabled", "none":
		return "none"
	default:
		return ""
	}
}

func reasoningEffortToAnthropicAdaptive(effort string) string {
	switch normalizeRuntimeReasoningEffort(effort) {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "max"
	default:
		return ""
	}
}

func normalizeOpenAIReasoningEffort(effort string) string {
	switch normalizeRuntimeReasoningEffort(effort) {
	case "", "none":
		return ""
	case "low", "medium", "high":
		return normalizeRuntimeReasoningEffort(effort)
	case "xhigh":
		return "high"
	default:
		return "medium"
	}
}

func deriveOpenAIReasoningEffort(model, explicit string, thinking *anthropictypes.Thinking) string {
	if !isReasoningModelPrefix(strings.ToLower(strings.TrimSpace(model))) {
		return ""
	}

	if normalized := normalizeOpenAIReasoningEffort(explicit); normalized != "" {
		return normalized
	}
	if thinking == nil {
		return ""
	}
	return normalizeOpenAIReasoningEffort(mapAnthropicThinkingToReasoningEffort(model, thinking))
}
