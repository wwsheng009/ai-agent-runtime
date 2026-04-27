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
		ReasoningModel:  true,
	})

	if got := req["reasoning_effort"]; got != "high" {
		t.Fatalf("expected reasoning_effort high, got %#v", got)
	}
	if _, exists := req["temperature"]; exists {
		t.Fatalf("did not expect temperature for reasoning model request")
	}
}

func TestOpenAIBuildRequest_PreservesXHighReasoningEffort(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:           "gpt-5.4",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "xhigh",
		ReasoningModel:  true,
	})

	if got := req["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("expected reasoning_effort xhigh, got %#v", got)
	}
}

func TestOpenAIBuildRequest_PreservesDeepSeekReasoningEffort(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:           "deepseek-v4-pro",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "max",
		ReasoningModel:  true,
	})

	if got := req["reasoning_effort"]; got != "max" {
		t.Fatalf("expected deepseek reasoning_effort max, got %#v", got)
	}
}

func TestOpenAIBuildRequest_PreservesModelScopeDeepSeekReasoningEffort(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:           "deepseek-ai/DeepSeek-V4-Pro",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "max",
		ReasoningModel:  true,
	})

	if got := req["reasoning_effort"]; got != "max" {
		t.Fatalf("expected modelscope deepseek reasoning_effort max, got %#v", got)
	}
	if _, exists := req["temperature"]; exists {
		t.Fatalf("did not expect temperature for deepseek modelscope reasoning request")
	}
}

func TestOpenAIBuildRequest_PropagatesExplicitThinkingWithoutReasoningEffortDerivation(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:    "deepseek-v4-pro",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Thinking: &anthropictypes.Thinking{
			Type: "enabled",
		},
	})

	if _, exists := req["reasoning_effort"]; exists {
		t.Fatalf("did not expect reasoning_effort to be derived from thinking: %#v", req["reasoning_effort"])
	}
	if got := req["thinking"]; got == nil {
		t.Fatal("expected explicit thinking to be propagated")
	}
}

func TestOpenAIBuildRequest_OmitsReasoningEffortForNonReasoningModel(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:           "gpt-4o-mini",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "high",
	})

	if got := req["reasoning_effort"]; got != "high" {
		t.Fatalf("expected explicit reasoning_effort to be preserved, got %#v", got)
	}
	if got := req["temperature"]; got != 0.0 {
		t.Fatalf("expected temperature to remain on non-reasoning model, got %#v", got)
	}
}

func TestOpenAIBuildRequest_PropagatesCompatibleMetadataOptions(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:    "deepseek-v4-pro",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Stream:   true,
		Metadata: map[string]interface{}{
			"thinking": map[string]interface{}{
				"type": "enabled",
			},
			"response_format": map[string]interface{}{
				"type": "json_object",
			},
			"stream_options": map[string]interface{}{
				"include_usage": true,
			},
			"stop":              []string{"END"},
			"top_p":             0.9,
			"frequency_penalty": 0.1,
			"presence_penalty":  0.2,
			"tool_choice":       "none",
			"extra_body": map[string]interface{}{
				"foo": "bar",
			},
		},
	})

	if got := req["thinking"]; got == nil {
		t.Fatal("expected thinking to be propagated")
	}
	if got := req["response_format"]; got == nil {
		t.Fatal("expected response_format to be propagated")
	}
	if got := req["stream_options"]; got == nil {
		t.Fatal("expected stream_options to be propagated for stream requests")
	}
	if got := req["stop"]; got == nil {
		t.Fatal("expected stop to be propagated")
	}
	if got := req["top_p"]; got != 0.9 {
		t.Fatalf("expected top_p 0.9, got %#v", got)
	}
	if got := req["frequency_penalty"]; got != 0.1 {
		t.Fatalf("expected frequency_penalty 0.1, got %#v", got)
	}
	if got := req["presence_penalty"]; got != 0.2 {
		t.Fatalf("expected presence_penalty 0.2, got %#v", got)
	}
	if got := req["tool_choice"]; got != "none" {
		t.Fatalf("expected explicit tool_choice none, got %#v", got)
	}
	if got := req["foo"]; got != "bar" {
		t.Fatalf("expected extra_body foo=bar, got %#v", got)
	}
}

func TestOpenAIBuildRequest_ExplicitToolChoiceOverridesAuto(t *testing.T) {
	a := &OpenAIAdapter{}

	req := a.BuildRequest(RequestConfig{
		Model:     "gpt-4o-mini",
		Messages:  []map[string]interface{}{{"role": "user", "content": "hello"}},
		Functions: []map[string]interface{}{{"type": "function"}},
		Metadata: map[string]interface{}{
			"tool_choice": map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": "ls",
				},
			},
		},
	})

	toolChoice, ok := req["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected structured tool_choice, got %T", req["tool_choice"])
	}
	if toolChoice["type"] != "function" {
		t.Fatalf("expected tool_choice.type=function, got %#v", toolChoice["type"])
	}
	function, ok := toolChoice["function"].(map[string]interface{})
	if !ok || function["name"] != "ls" {
		t.Fatalf("expected tool_choice.function.name=ls, got %#v", toolChoice["function"])
	}
}
