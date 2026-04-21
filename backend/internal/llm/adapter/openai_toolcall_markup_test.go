package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIHandleResponse_NonStreamParsesToolCallMarkupFromContent(t *testing.T) {
	adapter := &OpenAIAdapter{}
	jsonData := `{"choices":[{"message":{"role":"assistant","content":"<tool_call>ls<arg_key>depth</arg_key><arg_value>1</arg_value><arg_key>path</arg_key><arg_value>E:\\projects\\ai\\ai-agent-runtime\\frontend\\src</arg_value></tool_call>"}}]}`

	msg, err := adapter.HandleResponse(false, strings.NewReader(jsonData), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if got, _ := msg["content"].(string); got != "" {
		t.Fatalf("expected empty content after parsing markup, got %q", got)
	}

	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}

	fn, ok := toolCalls[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected function payload, got %#v", toolCalls[0]["function"])
	}
	if name, _ := fn["name"].(string); name != "ls" {
		t.Fatalf("expected tool name ls, got %q", name)
	}

	args := decodeToolCallMarkupArguments(t, fn["arguments"])
	if args["depth"] != float64(1) {
		t.Fatalf("expected depth=1, got %#v", args["depth"])
	}
	if args["path"] != `E:\projects\ai\ai-agent-runtime\frontend\src` {
		t.Fatalf("unexpected path: %#v", args["path"])
	}
}

func TestOpenAIHandleResponse_StreamSuppressesToolCallMarkupText(t *testing.T) {
	adapter := &OpenAIAdapter{}
	var textParts []string

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"先查看目录。<to"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"ol_call>ls<arg_key>depth</arg_key><arg_value>1</arg_value>"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"<arg_key>path</arg_key><arg_value>E:\\projects\\ai\\ai-agent-runtime\\frontend\\src</arg_value></tool_call>"},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")), StreamCallbacks{
		OnText: func(text string) {
			textParts = append(textParts, text)
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if got, _ := msg["content"].(string); got != "先查看目录。" {
		t.Fatalf("expected cleaned content, got %q", got)
	}
	if joined := strings.Join(textParts, ""); joined != "先查看目录。" {
		t.Fatalf("expected streamed text without markup, got %q", joined)
	}

	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}

	fn, ok := toolCalls[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected function payload, got %#v", toolCalls[0]["function"])
	}
	args := decodeToolCallMarkupArguments(t, fn["arguments"])
	if args["depth"] != float64(1) {
		t.Fatalf("expected depth=1, got %#v", args["depth"])
	}
	if args["path"] != `E:\projects\ai\ai-agent-runtime\frontend\src` {
		t.Fatalf("unexpected path: %#v", args["path"])
	}
}

func decodeToolCallMarkupArguments(t *testing.T, raw interface{}) map[string]interface{} {
	t.Helper()

	argsJSON, ok := raw.(string)
	if !ok {
		t.Fatalf("expected string arguments, got %T", raw)
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("failed to decode arguments: %v", err)
	}
	return args
}
