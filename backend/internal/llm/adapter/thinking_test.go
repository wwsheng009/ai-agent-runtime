package adapter

import (
	"testing"

	anthropictypes "github.com/wwsheng009/ai-agent-runtime/internal/types/anthropic"
)

func TestBuildAnthropicThinkingFromReasoningEffortRequiresExplicitBudgetConfig(t *testing.T) {
	// When no budgets are configured, should default to adaptive thinking
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

	disabled := buildAnthropicThinkingFromReasoningEffort("none", nil)
	if disabled == nil || disabled.Type != "disabled" {
		t.Fatalf("expected disabled thinking for none, got %#v", disabled)
	}
}

func TestBuildGeminiThinkingConfigFromReasoningEffortRequiresExplicitBudgetConfig(t *testing.T) {
	if got := buildGeminiThinkingConfigFromReasoningEffort("high", nil); got != nil {
		t.Fatalf("expected nil thinkingConfig without explicit budget config, got %#v", got)
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

	if got := buildGeminiThinkingConfigFromThinking(&anthropictypes.Thinking{Type: "enabled"}, nil); got != nil {
		t.Fatalf("expected nil thinkingConfig for enabled thinking without budget config, got %#v", got)
	}
}
