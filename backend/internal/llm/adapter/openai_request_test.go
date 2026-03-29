package adapter

import (
	"testing"

	anthropictypes "github.com/wwsheng009/ai-agent-runtime/internal/types/anthropic"
)

func TestOpenAIBuildRequest_IncludesExplicitReasoningEffortForReasoningModel(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:           "gpt-5.4",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "high",
	})

	if got := req["reasoning_effort"]; got != "high" {
		t.Fatalf("expected reasoning_effort high, got %#v", got)
	}
	if _, exists := req["temperature"]; exists {
		t.Fatalf("did not expect temperature for reasoning model request")
	}
}

func TestOpenAIBuildRequest_NormalizesXHighReasoningEffort(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:           "gpt-5.4",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "xhigh",
	})

	if got := req["reasoning_effort"]; got != "high" {
		t.Fatalf("expected reasoning_effort high after normalization, got %#v", got)
	}
}

func TestOpenAIBuildRequest_DerivesReasoningEffortFromAnthropicThinking(t *testing.T) {
	a := &OpenAIAdapter{}
	budget := 32000

	req := a.BuildRequest(RequestConfig{
		Model:    "gpt-5.4",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Thinking: &anthropictypes.Thinking{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	})

	if got := req["reasoning_effort"]; got != "high" {
		t.Fatalf("expected derived reasoning_effort high, got %#v", got)
	}
}

func TestOpenAIBuildRequest_OmitsReasoningEffortForNonReasoningModel(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:           "gpt-4o-mini",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "high",
	})

	if _, exists := req["reasoning_effort"]; exists {
		t.Fatalf("did not expect reasoning_effort for non-reasoning model: %#v", req["reasoning_effort"])
	}
	if got := req["temperature"]; got != 0.0 {
		t.Fatalf("expected temperature to remain on non-reasoning model, got %#v", got)
	}
}
