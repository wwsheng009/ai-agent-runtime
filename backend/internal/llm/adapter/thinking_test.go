package adapter

import (
	"testing"

	anthropictypes "github.com/wwsheng009/ai-agent-runtime/internal/types/anthropic"
)

func TestBuildAnthropicThinkingFromReasoningEffortPreservesUnmappedValues(t *testing.T) {
	// When no budgets are configured, default to adaptive thinking and preserve
	// the provider-defined effort for output_config.
	adaptive := buildAnthropicThinkingFromReasoningEffort("high", nil)
	if adaptive == nil {
		t.Fatal("expected adaptive thinking when no budgets configured")
	}
	if adaptive.Type != "adaptive" {
		t.Fatalf("expected adaptive thinking, got %q", adaptive.Type)
	}
	if adaptive.Effort != "high" {
		t.Fatalf("expected effort high, got %q", adaptive.Effort)
	}

	thinking := buildAnthropicThinkingFromReasoningEffort("high", map[string]int{
		"high": 16384,
	})
	if thinking == nil {
		t.Fatal("expected thinking with explicit budget config")
	}
	if thinking.Type != "enabled" {
		t.Fatalf("expected enabled thinking, got %q", thinking.Type)
	}
	if thinking.Effort != "high" {
		t.Fatalf("expected raw effort to be preserved, got %q", thinking.Effort)
	}
	if thinking.BudgetTokens == nil || *thinking.BudgetTokens != 16384 {
		t.Fatalf("expected budget_tokens 16384, got %#v", thinking.BudgetTokens)
	}

	custom := buildAnthropicThinkingFromReasoningEffort(" Provider-Custom ", map[string]int{
		"high": 16384,
	})
	if custom == nil || custom.Type != "adaptive" {
		t.Fatalf("expected custom effort to use adaptive thinking, got %#v", custom)
	}
	if custom.Effort != "Provider-Custom" {
		t.Fatalf("expected custom effort to be trimmed without rewriting, got %q", custom.Effort)
	}

	none := buildAnthropicThinkingFromReasoningEffort("none", nil)
	if none == nil || none.Type != "adaptive" || none.Effort != "none" {
		t.Fatalf("expected provider-specific none effort to be preserved, got %#v", none)
	}
}

func TestBuildGeminiThinkingConfigFromReasoningEffortPreservesUnmappedValues(t *testing.T) {
	fallback := buildGeminiThinkingConfigFromReasoningEffort("Provider-Custom", nil)
	if got := fallback["thinkingLevel"]; got != "Provider-Custom" {
		t.Fatalf("expected custom thinkingLevel to be preserved, got %#v", got)
	}
	if _, exists := fallback["thinkingBudget"]; exists {
		t.Fatalf("expected custom effort not to be rewritten as a budget, got %#v", fallback)
	}

	thinkingConfig := buildGeminiThinkingConfigFromReasoningEffort("high", map[string]int{
		"high": 16384,
	})
	if len(thinkingConfig) == 0 {
		t.Fatal("expected thinkingConfig with explicit budget config")
	}
	if got := thinkingConfig["includeThoughts"]; got != true {
		t.Fatalf("expected includeThoughts true, got %#v", got)
	}
	if got := thinkingConfig["thinkingBudget"]; got != 16384 {
		t.Fatalf("expected thinkingBudget 16384, got %#v", got)
	}

	unmapped := buildGeminiThinkingConfigFromReasoningEffort(" custom-level ", map[string]int{
		"high": 16384,
	})
	if got := unmapped["thinkingLevel"]; got != "custom-level" {
		t.Fatalf("expected unmapped effort to reach thinkingLevel, got %#v", got)
	}

	if got := buildGeminiThinkingConfigFromThinking(&anthropictypes.Thinking{Type: "enabled"}, nil); got != nil {
		t.Fatalf("expected nil thinkingConfig for enabled thinking without budget config, got %#v", got)
	}
}
