package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIHandleResponse_StreamPreservesToolIdentityAcrossEmptyDeltas(t *testing.T) {
	adapter := &OpenAIAdapter{}

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"execute_shell_command","arguments":""}}]}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"","function":{"name":"","arguments":"{\"command\":\"git status\""}}]}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"","function":{"name":"","arguments":",\"workdir\":\"E:/projects/ai/ai-gateway\"}"}}]},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}

	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}

	if got, _ := toolCalls[0]["id"].(string); got != "call_1" {
		t.Fatalf("expected tool call id call_1, got %q", got)
	}
	if got, _ := msg["finish_reason"].(string); got != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %#v", msg["finish_reason"])
	}
	if got, _ := toolCalls[0]["type"].(string); got != "function" {
		t.Fatalf("expected tool call type function, got %q", got)
	}

	fn, ok := toolCalls[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected function payload, got %#v", toolCalls[0]["function"])
	}
	if got, _ := fn["name"].(string); got != "execute_shell_command" {
		t.Fatalf("expected tool name execute_shell_command, got %q", got)
	}

	argsJSON, ok := fn["arguments"].(string)
	if !ok {
		t.Fatalf("expected string arguments, got %T", fn["arguments"])
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("failed to decode arguments: %v", err)
	}
	if got, _ := args["command"].(string); got != "git status" {
		t.Fatalf("expected command git status, got %q", got)
	}
	if got, _ := args["workdir"].(string); got != "E:/projects/ai/ai-gateway" {
		t.Fatalf("expected workdir E:/projects/ai/ai-gateway, got %q", got)
	}
}
