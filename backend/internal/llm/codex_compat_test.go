package llm

import "testing"

func TestProviderSupportsCodexMaxOutputTokens_UsesChatGPTBackendHeuristic(t *testing.T) {
	if providerSupportsCodexMaxOutputTokens("https://chatgpt.com/backend-api/codex/responses", nil) {
		t.Fatalf("expected chatgpt codex backend to disable max_output_tokens by default")
	}
	if !providerSupportsCodexMaxOutputTokens("https://api.openai.com/v1/responses", nil) {
		t.Fatalf("expected official responses endpoint to keep max_output_tokens enabled")
	}
}

func TestProviderSupportsCodexMaxOutputTokens_ExplicitOverrideWins(t *testing.T) {
	enabled := true
	if !providerSupportsCodexMaxOutputTokens("https://chatgpt.com/backend-api/codex/responses", &enabled) {
		t.Fatalf("expected explicit true to override heuristic")
	}
}
