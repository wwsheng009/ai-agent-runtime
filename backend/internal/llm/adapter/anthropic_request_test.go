package adapter

import "testing"

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
