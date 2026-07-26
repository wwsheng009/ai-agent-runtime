package llm

import (
	"os"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestResolveRequestMaxTokens_CapsDefaultTo8k(t *testing.T) {
	t.Setenv(EnvDisableMaxTokensCap, "")
	t.Setenv(EnvMaxOutputTokens, "")
	t.Setenv(EnvAICLIMaxOutputTokens, "")

	resolved := ResolveRequestMaxTokens(
		"anthropic",
		"claude-opus-4-7",
		0,
		agentconfig.ModelCapabilitySpec{MaxTokens: 128000},
		true,
		131072,
	)
	if resolved.Default != CappedDefaultMaxTokens {
		t.Fatalf("expected capped default %d, got %d (source=%s)", CappedDefaultMaxTokens, resolved.Default, resolved.Source)
	}
	if !resolved.Capped {
		t.Fatalf("expected Capped=true, got %#v", resolved)
	}
	if resolved.UpperLimit != 128000 {
		t.Fatalf("expected upperLimit 128000 from capability, got %d", resolved.UpperLimit)
	}
}

func TestResolveRequestMaxTokens_ExplicitBudgetNotCappedTo8k(t *testing.T) {
	resolved := ResolveRequestMaxTokens(
		"anthropic",
		"claude-opus-4-7",
		64000,
		agentconfig.ModelCapabilitySpec{MaxTokens: 128000},
		true,
		131072,
	)
	if resolved.Default != 64000 {
		t.Fatalf("expected explicit 64000, got %d", resolved.Default)
	}
	if resolved.Source != "explicit" {
		t.Fatalf("expected source=explicit, got %s", resolved.Source)
	}
}

func TestResolveRequestMaxTokens_ClampsOversizeExplicitBudget(t *testing.T) {
	// Claude family without capability still knows a 128k ceiling.
	resolved := ResolveRequestMaxTokens(
		"anthropic",
		"claude-fable-5",
		131072,
		agentconfig.ModelCapabilitySpec{},
		false,
		0,
	)
	if resolved.Default != defaultClaudeMaxOutputTokens {
		t.Fatalf("expected clamp to %d, got %d", defaultClaudeMaxOutputTokens, resolved.Default)
	}
	if resolved.Source != "explicit_clamped" {
		t.Fatalf("expected source=explicit_clamped, got %s", resolved.Source)
	}

	// Unknown non-Claude model without capability: preserve explicit budget.
	// Provider recovery will lower it if the upstream rejects the value.
	resolved = ResolveRequestMaxTokens(
		"anthropic",
		"mimo-v2.5-pro",
		131072,
		agentconfig.ModelCapabilitySpec{},
		false,
		0,
	)
	if resolved.Default != 131072 {
		t.Fatalf("expected unknown model explicit budget preserved, got %d", resolved.Default)
	}
}

func TestResolveRequestMaxTokens_EnvOverrideWins(t *testing.T) {
	t.Setenv(EnvMaxOutputTokens, "12000")
	t.Setenv(EnvAICLIMaxOutputTokens, "")
	t.Setenv(EnvDisableMaxTokensCap, "")

	resolved := ResolveRequestMaxTokens(
		"anthropic",
		"claude-sonnet-4-6",
		0,
		agentconfig.ModelCapabilitySpec{MaxTokens: 64000},
		true,
		0,
	)
	if resolved.Default != 12000 {
		t.Fatalf("expected env override 12000, got %d", resolved.Default)
	}
	if !resolved.EnvOverride {
		t.Fatalf("expected EnvOverride=true")
	}
	if resolved.Capped {
		t.Fatalf("env override should clear capped flag")
	}
}

func TestResolveRequestMaxTokens_EnvOverrideClampedToUpperLimit(t *testing.T) {
	t.Setenv(EnvAICLIMaxOutputTokens, "999999")
	t.Setenv(EnvMaxOutputTokens, "")

	resolved := ResolveRequestMaxTokens(
		"openai",
		"gpt-4.1-mini",
		0,
		agentconfig.ModelCapabilitySpec{MaxTokens: 16000},
		true,
		0,
	)
	if resolved.Default != 16000 {
		t.Fatalf("expected env override clamped to upperLimit 16000, got %d", resolved.Default)
	}
}

func TestCapRequestMaxTokens_OnlyClampsCeiling(t *testing.T) {
	got := CapRequestMaxTokens(
		"anthropic",
		"claude-fable-5",
		131072,
		agentconfig.ModelCapabilitySpec{},
		false,
		0,
	)
	if got != defaultClaudeMaxOutputTokens {
		t.Fatalf("expected cap to %d, got %d", defaultClaudeMaxOutputTokens, got)
	}
	got = CapRequestMaxTokens(
		"anthropic",
		"claude-opus-4-7",
		8192,
		agentconfig.ModelCapabilitySpec{MaxTokens: 128000},
		true,
		0,
	)
	if got != 8192 {
		t.Fatalf("expected in-limit budget preserved, got %d", got)
	}
}

func TestEscalatedRequestMaxTokens(t *testing.T) {
	t.Setenv(EnvDisableMaxTokensCap, "")
	t.Setenv(EnvMaxOutputTokens, "")
	t.Setenv(EnvAICLIMaxOutputTokens, "")

	if got := EscalatedRequestMaxTokens(CappedDefaultMaxTokens, 128000); got != EscalatedMaxTokens {
		t.Fatalf("expected escalate to %d, got %d", EscalatedMaxTokens, got)
	}
	if got := EscalatedRequestMaxTokens(CappedDefaultMaxTokens, 32000); got != 32000 {
		t.Fatalf("expected escalate clamped to upperLimit 32000, got %d", got)
	}
	if got := EscalatedRequestMaxTokens(EscalatedMaxTokens, 128000); got != 0 {
		t.Fatalf("expected no escalate when already at escalated budget, got %d", got)
	}

	t.Setenv(EnvMaxOutputTokens, "8000")
	if got := EscalatedRequestMaxTokens(CappedDefaultMaxTokens, 128000); got != 0 {
		t.Fatalf("expected no escalate when env override present, got %d", got)
	}
}

func TestIsMaxOutputTokensStop(t *testing.T) {
	for _, reason := range []string{"length", "max_tokens", "MAX_TOKENS", "max_output_tokens"} {
		if !IsMaxOutputTokensStop(reason) {
			t.Fatalf("expected %q to be max-output stop", reason)
		}
	}
	if IsMaxOutputTokensStop("stop") || IsMaxOutputTokensStop("tool_calls") {
		t.Fatalf("did not expect non-length reasons to match")
	}
}

func TestResolveModelMaxOutputTokens_ProviderLimitDoesNotBecomeDefault(t *testing.T) {
	// Clear env so defaults are stable even if developer shell exports overrides.
	_ = os.Unsetenv(EnvMaxOutputTokens)
	_ = os.Unsetenv(EnvAICLIMaxOutputTokens)

	resolved := ResolveModelMaxOutputTokens(
		"anthropic",
		"mimo-v2.5-pro",
		agentconfig.ModelCapabilitySpec{MaxTokens: 131072},
		true,
		131072,
	)
	if resolved.Default >= 131072 {
		t.Fatalf("provider/capability ceiling must not become default request budget, got %#v", resolved)
	}
	if resolved.UpperLimit != 131072 {
		t.Fatalf("expected upperLimit 131072, got %d", resolved.UpperLimit)
	}
}

func TestResolveModelMaxOutputTokens_ClaudeFamilyHeuristics(t *testing.T) {
	cases := []struct {
		model       string
		wantDefault int
		wantUpper   int
		wantSource  string
	}{
		{"claude-fable-5", 64000, defaultClaudeMaxOutputTokens, "claude_adaptive_recent"},
		{"claude-mythos-preview", 64000, defaultClaudeMaxOutputTokens, "claude_adaptive_recent"},
		{"claude-opus-5", 64000, defaultClaudeMaxOutputTokens, "claude_adaptive_recent"},
		{"claude-sonnet-5", 64000, defaultClaudeMaxOutputTokens, "claude_adaptive_recent"},
		{"claude-opus-4-8", 64000, defaultClaudeMaxOutputTokens, "claude_adaptive_recent"},
		{"claude-opus-4-7", 64000, defaultClaudeMaxOutputTokens, "claude_adaptive_recent"},
		{"claude-sonnet-4-6", 32000, defaultClaudeMaxOutputTokens, "claude_sonnet_recent"},
		{"claude-haiku-4-5", 32000, 64000, "claude_4_family"},
	}
	for _, tc := range cases {
		resolved := ResolveModelMaxOutputTokens("anthropic", tc.model, agentconfig.ModelCapabilitySpec{}, false, 0)
		if resolved.Default != tc.wantDefault || resolved.UpperLimit != tc.wantUpper || resolved.Source != tc.wantSource {
			t.Fatalf("%s: got default=%d upper=%d source=%s, want default=%d upper=%d source=%s",
				tc.model, resolved.Default, resolved.UpperLimit, resolved.Source, tc.wantDefault, tc.wantUpper, tc.wantSource)
		}
	}
}
