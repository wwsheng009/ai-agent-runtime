package adapter

import (
	"testing"

	anthropictypes "github.com/ai-gateway/ai-agent-runtime/internal/types/anthropic"
)

func TestAnthropicBuildRequest_UsesExplicitThinkingConfig(t *testing.T) {
	a := &AnthropicAdapter{}
	budget := 8192

	req := a.BuildRequest(RequestConfig{
		Model:    "claude-sonnet-4-6",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Thinking: &anthropictypes.Thinking{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	})

	rawThinking, ok := req["thinking"]
	if !ok {
		t.Fatalf("expected thinking to be included in anthropic request")
	}
	thinking, ok := rawThinking.(*anthropictypes.Thinking)
	if !ok {
		t.Fatalf("expected anthropic thinking struct, got %T", rawThinking)
	}
	if thinking.Type != "enabled" {
		t.Fatalf("expected thinking type enabled, got %q", thinking.Type)
	}
	if thinking.BudgetTokens == nil || *thinking.BudgetTokens != 8192 {
		t.Fatalf("expected thinking budget 8192, got %#v", thinking.BudgetTokens)
	}
}

func TestAnthropicBuildRequest_MapsReasoningEffortToAdaptiveThinkingFor46Model(t *testing.T) {
	a := &AnthropicAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:           "claude-sonnet-4-6",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "high",
	})

	rawThinking, ok := req["thinking"]
	if !ok {
		t.Fatalf("expected thinking to be derived from reasoning effort")
	}
	thinking, ok := rawThinking.(*anthropictypes.Thinking)
	if !ok {
		t.Fatalf("expected anthropic thinking struct, got %T", rawThinking)
	}
	if thinking.Type != "adaptive" {
		t.Fatalf("expected adaptive thinking for sonnet 4.6, got %q", thinking.Type)
	}
	if thinking.Effort != "high" {
		t.Fatalf("expected adaptive effort high, got %q", thinking.Effort)
	}
}

func TestAnthropicBuildHeaders_InjectsInterleavedThinkingBetaForSonnet46ManualMode(t *testing.T) {
	a := &AnthropicAdapter{}

	headers := a.BuildHeaders(AdapterConfig{
		APIKey: "test-key",
		Model:  "claude-sonnet-4-6",
		RequestBody: map[string]interface{}{
			"model": "claude-sonnet-4-6",
			"thinking": map[string]interface{}{
				"type": "enabled",
			},
		},
	})

	if got := headers["x-api-key"]; got != "test-key" {
		t.Fatalf("expected x-api-key to be preserved, got %q", got)
	}
	if got := getHeaderValueCaseInsensitive(headers, "anthropic-beta"); got != anthropicInterleavedThinkingBeta {
		t.Fatalf("expected anthropic-beta %q, got %q", anthropicInterleavedThinkingBeta, got)
	}
}

func TestAnthropicBuildHeaders_DoesNotInjectInterleavedThinkingBetaForAdaptiveMode(t *testing.T) {
	a := &AnthropicAdapter{}

	headers := a.BuildHeaders(AdapterConfig{
		APIKey: "test-key",
		Model:  "claude-sonnet-4-6",
		RequestBody: map[string]interface{}{
			"thinking": map[string]interface{}{
				"type": "adaptive",
			},
		},
	})

	if got := getHeaderValueCaseInsensitive(headers, "anthropic-beta"); got != "" {
		t.Fatalf("expected no anthropic-beta header for adaptive mode, got %q", got)
	}
}

func TestAnthropicBuildHeaders_MergesExistingAnthropicBetaHeader(t *testing.T) {
	a := &AnthropicAdapter{}

	headers := a.BuildHeaders(AdapterConfig{
		APIKey: "test-key",
		Model:  "claude-sonnet-4-6",
		RequestBody: map[string]interface{}{
			"thinking": map[string]interface{}{
				"type": "enabled",
			},
		},
		Headers: map[string]string{
			"Anthropic-Beta": "foo-beta",
			"X-Trace-ID":     "trace-123",
		},
	})

	if got := getHeaderValueCaseInsensitive(headers, "anthropic-beta"); got != "foo-beta, "+anthropicInterleavedThinkingBeta {
		t.Fatalf("expected merged anthropic-beta header, got %q", got)
	}
	if got := getHeaderValueCaseInsensitive(headers, "x-trace-id"); got != "trace-123" {
		t.Fatalf("expected custom headers to be preserved, got %q", got)
	}
}

func TestOpenAIBuildHeaders_MergesCustomHeaders(t *testing.T) {
	a := &OpenAIAdapter{}

	headers := a.BuildHeaders(AdapterConfig{
		APIKey: "test-key",
		Headers: map[string]string{
			"Content-Type": "application/vnd.custom+json",
			"X-Trace-ID":   "trace-456",
		},
	})

	if got := headers["Content-Type"]; got != "application/vnd.custom+json" {
		t.Fatalf("expected custom content-type override, got %q", got)
	}
	if got := getHeaderValueCaseInsensitive(headers, "authorization"); got != "Bearer test-key" {
		t.Fatalf("expected authorization header, got %q", got)
	}
	if got := getHeaderValueCaseInsensitive(headers, "x-trace-id"); got != "trace-456" {
		t.Fatalf("expected custom trace header, got %q", got)
	}
}
