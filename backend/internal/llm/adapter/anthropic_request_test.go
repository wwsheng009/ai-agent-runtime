package adapter

import (
	"strings"
	"testing"

	anthropictypes "github.com/wwsheng009/ai-agent-runtime/internal/types/anthropic"
)

func TestAnthropicBuildRequest_MovesInstructionMessagesToSystem(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "claude-3-7-sonnet",
		Messages: []map[string]interface{}{
			{"role": "system", "content": "Base guardrail"},
			{"role": "developer", "content": "Tool guidance"},
			{"role": "user", "content": "hello"},
		},
		Stream: false,
	})

	if req["system"] != "Base guardrail\n\nTool guidance" {
		t.Fatalf("unexpected anthropic system instructions: %#v", req["system"])
	}
	messages, ok := req["messages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected anthropic messages array, got %#v", req["messages"])
	}
	if len(messages) != 1 {
		t.Fatalf("expected only user message after extraction, got %#v", messages)
	}
	if messages[0]["role"] != "user" {
		t.Fatalf("expected user role after extraction, got %#v", messages[0]["role"])
	}
}

func TestAnthropicBuildRequest_OmitsEmptySystemField(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model: "claude-3-7-sonnet",
		Messages: []map[string]interface{}{
			{"role": "user", "content": "hello"},
		},
		Stream: false,
	})

	if _, exists := req["system"]; exists {
		t.Fatalf("did not expect system field, got %#v", req["system"])
	}
}

func TestAnthropicBuildRequest_SetsTemperatureWhenNoThinking(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:       "claude-sonnet-4-6",
		Messages:    []map[string]interface{}{{"role": "user", "content": "hello"}},
		Temperature: 0.7,
	})

	temp, ok := req["temperature"].(float64)
	if !ok {
		t.Fatalf("expected temperature in request, got %#v", req["temperature"])
	}
	if temp != 0.7 {
		t.Fatalf("expected temperature 0.7, got %v", temp)
	}
}

func TestAnthropicBuildRequest_OmitsTemperatureWhenThinkingEnabled(t *testing.T) {
	a := &AnthropicAdapter{}
	budget := 8192
	req := a.BuildRequest(RequestConfig{
		Model:       "claude-sonnet-4-6",
		Messages:    []map[string]interface{}{{"role": "user", "content": "hello"}},
		Temperature: 0.7,
		Thinking: &anthropictypes.Thinking{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	})

	if _, exists := req["temperature"]; exists {
		t.Fatalf("expected temperature to be omitted when thinking is enabled, got %#v", req["temperature"])
	}
}

func TestAnthropicBuildRequest_OmitsTemperatureWhenZero(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:       "claude-sonnet-4-6",
		Messages:    []map[string]interface{}{{"role": "user", "content": "hello"}},
		Temperature: 0,
	})

	if _, exists := req["temperature"]; exists {
		t.Fatalf("expected temperature to be omitted when zero, got %#v", req["temperature"])
	}
}

func TestAnthropicBuildRequest_AdaptiveThinkingGeneratesCorrectBody(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:           "claude-opus-4-6",
		Messages:        []map[string]interface{}{{"role": "user", "content": "hello"}},
		ReasoningEffort: "high",
		ReasoningEffortBudgets: map[string]int{
			"high": 0, // 0 budget signals adaptive mode
		},
	})

	rawThinking, ok := req["thinking"]
	if !ok {
		t.Fatalf("expected thinking in request")
	}
	thinking, ok := rawThinking.(*anthropictypes.Thinking)
	if !ok {
		t.Fatalf("expected thinking struct, got %T", rawThinking)
	}
	if thinking.Type != "adaptive" {
		t.Fatalf("expected thinking type adaptive, got %q", thinking.Type)
	}
	if thinking.Effort != "high" {
		t.Fatalf("expected thinking effort high, got %q", thinking.Effort)
	}

	// Check output_config
	rawConfig, ok := req["output_config"]
	if !ok {
		t.Fatalf("expected output_config in request for adaptive thinking")
	}
	config, ok := rawConfig.(map[string]interface{})
	if !ok {
		t.Fatalf("expected output_config map, got %T", rawConfig)
	}
	if config["effort"] != "high" {
		t.Fatalf("expected output_config.effort high, got %v", config["effort"])
	}
}

func TestAnthropicBuildRequest_ToolChoiceIsPropagated(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:      "claude-sonnet-4-6",
		Messages:   []map[string]interface{}{{"role": "user", "content": "hello"}},
		ToolChoice: map[string]interface{}{"type": "auto"},
	})

	tc, ok := req["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_choice in request, got %#v", req["tool_choice"])
	}
	if tc["type"] != "auto" {
		t.Fatalf("expected tool_choice type auto, got %v", tc["type"])
	}
}

func TestAnthropicBuildAssistantMessage_NormalizesToolUseBlocks(t *testing.T) {
	a := &AnthropicAdapter{}
	msg := a.BuildAssistantMessage("", []map[string]interface{}{
		{
			"type": "tool_use",
			"id":   "call-1",
			"name": "view",
			"input": map[string]interface{}{
				"file_path": "README.md",
			},
		},
	}, "")

	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected normalized tool_calls slice, got %T", msg["tool_calls"])
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	fn, _ := toolCalls[0]["function"].(map[string]interface{})
	if fn["name"] != "view" {
		t.Fatalf("unexpected function name: %#v", fn["name"])
	}
	args, _ := fn["arguments"].(string)
	if !strings.Contains(args, `"file_path":"README.md"`) {
		t.Fatalf("expected file_path in normalized arguments, got %q", args)
	}
}

func TestAnthropicBuildRequest_StopSequencesArePropagated(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:         "claude-sonnet-4-6",
		Messages:      []map[string]interface{}{{"role": "user", "content": "hello"}},
		StopSequences: []string{"STOP", "END"},
	})

	ss, ok := req["stop_sequences"].([]string)
	if !ok {
		t.Fatalf("expected stop_sequences in request, got %#v", req["stop_sequences"])
	}
	if len(ss) != 2 || ss[0] != "STOP" || ss[1] != "END" {
		t.Fatalf("expected [STOP, END], got %v", ss)
	}
}

func TestAnthropicBuildRequest_StopSequencesFromMetadata(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "claude-sonnet-4-6",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
		Metadata: map[string]interface{}{
			"stop_sequences": []interface{}{"HALT"},
		},
	})

	ss, ok := req["stop_sequences"].([]interface{})
	if !ok {
		t.Fatalf("expected stop_sequences from metadata, got %#v", req["stop_sequences"])
	}
	if len(ss) != 1 || ss[0] != "HALT" {
		t.Fatalf("expected [HALT], got %v", ss)
	}
}

func TestAnthropicBuildRequest_DefaultMaxTokens16384(t *testing.T) {
	a := &AnthropicAdapter{}
	req := a.BuildRequest(RequestConfig{
		Model:    "claude-sonnet-4-6",
		Messages: []map[string]interface{}{{"role": "user", "content": "hello"}},
	})

	if req["max_tokens"] != 16384 {
		t.Fatalf("expected default max_tokens 16384, got %v", req["max_tokens"])
	}
}
