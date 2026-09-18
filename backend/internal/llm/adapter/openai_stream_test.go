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

func TestOpenAIHandleResponse_StreamReturnsErrorForSSEErrorEvent(t *testing.T) {
	adapter := &OpenAIAdapter{}
	var content strings.Builder

	_, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
		"",
		"event: error",
		`data: {"error":{"message":"Upstream request failed","type":"upstream_error"}}`,
		"",
	}, "\n")), StreamCallbacks{
		OnText: func(delta string) {
			content.WriteString(delta)
		},
	})
	if err == nil {
		t.Fatal("expected SSE error event to fail the stream")
	}
	if got := err.Error(); !strings.Contains(got, "stream_interrupted") ||
		!strings.Contains(got, "upstream_error") ||
		!strings.Contains(got, "Upstream request failed") {
		t.Fatalf("unexpected stream error: %v", err)
	}
	streamErr, ok := err.(*openAIStreamError)
	if !ok {
		t.Fatalf("expected openAIStreamError, got %T", err)
	}
	if got := streamErr.RetryErrorCode(); got != "upstream_error" {
		t.Fatalf("expected upstream_error retry code, got %q", got)
	}
	if got := content.String(); got != "partial" {
		t.Fatalf("expected partial content callback before error, got %q", got)
	}
}

func TestOpenAIHandleResponse_StreamReturnsErrorForTopLevelErrorChunk(t *testing.T) {
	adapter := &OpenAIAdapter{}

	_, err := adapter.HandleResponse(true, strings.NewReader(
		`data:{"error":{"message":"provider unavailable","code":"server_error"}}`+"\n\n",
	), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected top-level error chunk to fail the stream")
	}
	streamErr, ok := err.(*openAIStreamError)
	if !ok {
		t.Fatalf("expected openAIStreamError, got %T", err)
	}
	if got := streamErr.RetryErrorCode(); got != "server_error" {
		t.Fatalf("expected server_error retry code, got %q", got)
	}
	if got := err.Error(); !strings.Contains(got, "provider unavailable") {
		t.Fatalf("unexpected stream error: %v", err)
	}
}

func TestOpenAIHandleResponse_StreamIgnoresNullErrorField(t *testing.T) {
	adapter := &OpenAIAdapter{}

	msg, err := adapter.HandleResponse(true, strings.NewReader(
		`data: {"error":null,"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n",
	), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if got, _ := msg["content"].(string); got != "ok" {
		t.Fatalf("expected content ok, got %q", got)
	}
	if got, _ := msg["finish_reason"].(string); got != "stop" {
		t.Fatalf("expected finish_reason stop, got %q", got)
	}
}

func TestOpenAIHandleResponse_StreamReadsErrorAfterFinishReason(t *testing.T) {
	adapter := &OpenAIAdapter{}

	_, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"stop"}]}`,
		"",
		"event: error",
		`data: {"type":"error","code":"late_failure","message":"late upstream failure"}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected late SSE error to fail the stream")
	}
	if got := err.Error(); !strings.Contains(got, "late_failure") || !strings.Contains(got, "late upstream failure") {
		t.Fatalf("unexpected stream error: %v", err)
	}
}

func TestOpenAIHandleResponse_StreamHandlesMultilineData(t *testing.T) {
	adapter := &OpenAIAdapter{}

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,`,
		`data: "delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if got, _ := msg["content"].(string); got != "ok" {
		t.Fatalf("expected multiline data content ok, got %q", got)
	}
}

func TestOpenAIHandleResponse_StreamRejectsMalformedBusinessEvent(t *testing.T) {
	adapter := &OpenAIAdapter{}

	_, err := adapter.HandleResponse(true, strings.NewReader("data: {not-json}\n\n"), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "malformed_stream_event") {
		t.Fatalf("expected malformed_stream_event, got %v", err)
	}
}

func TestOpenAIHandleResponse_StreamNormalizesLegacyFunctionCall(t *testing.T) {
	adapter := &OpenAIAdapter{}

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"function_call":{"name":"get_weather","arguments":""}},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"function_call":{"arguments":"{\"city\":\"Hangzhou\"}"}},"finish_reason":"function_call"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected one normalized legacy tool call, got %T %#v", msg["tool_calls"], msg["tool_calls"])
	}
	fn, _ := toolCalls[0]["function"].(map[string]interface{})
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"Hangzhou"}` {
		t.Fatalf("unexpected legacy tool call: %#v", toolCalls[0])
	}
	if msg["finish_reason"] != "function_call" {
		t.Fatalf("unexpected finish reason: %#v", msg["finish_reason"])
	}
}

// 复现 unsee relay 的真实抓包：纯文本回复的收尾 chunk 携带空的 legacy
// function_call 占位对象（name/arguments 均为空），不应被判为无效工具调用。
func TestOpenAIHandleResponse_StreamIgnoresEmptyLegacyFunctionCallPlaceholder(t *testing.T) {
	adapter := &OpenAIAdapter{}
	var content strings.Builder

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"","reasoning_content":"","role":"assistant","tool_calls":[]},"finish_reason":null,"index":0,"logprobs":null}],"created":1789745567,"id":"cmb-1","model":"deepseek-v4.1-flash","object":"chat.completion.chunk"}`,
		"",
		`data: {"choices":[{"delta":{"content":"好的","tool_calls":[]},"finish_reason":null,"index":0}]}`,
		"",
		`data: {"choices":[{"delta":{"content":"","function_call":{"arguments":"","name":""},"reasoning_content":"","role":"assistant","tool_calls":[]},"finish_reason":"stop","index":0,"logprobs":null}],"created":1789745567,"id":"cmb-1","model":"deepseek-v4.1-flash","object":"chat.completion.chunk"}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")), StreamCallbacks{OnText: func(delta string) { content.WriteString(delta) }})
	if err != nil {
		t.Fatalf("empty legacy function_call placeholder must not fail the stream: %v", err)
	}
	if got := content.String(); got != "好的" {
		t.Fatalf("expected streamed content 好的, got %q", got)
	}
	if got, _ := msg["content"].(string); got != "好的" {
		t.Fatalf("expected assistant content 好的, got %#v", msg["content"])
	}
	if toolCalls, exists := msg["tool_calls"]; exists {
		t.Fatalf("expected no tool calls, got %#v", toolCalls)
	}
	if got, _ := msg["finish_reason"].(string); got != "stop" {
		t.Fatalf("expected finish_reason stop, got %#v", msg["finish_reason"])
	}
}

// 非空 legacy function_call 仍必须被识别为真实工具调用。
func TestOpenAIHandleResponse_StreamKeepsNamedLegacyFunctionCall(t *testing.T) {
	adapter := &OpenAIAdapter{}

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"function_call":{"name":"lookup","arguments":"{\"q\":\"x\"}"}},"finish_reason":"function_call","index":0}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("named legacy function_call must be accepted: %v", err)
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %#v", msg["tool_calls"])
	}
	fn, _ := toolCalls[0]["function"].(map[string]interface{})
	if got, _ := fn["name"].(string); got != "lookup" {
		t.Fatalf("expected tool name lookup, got %q", got)
	}
}

// 只有 arguments、缺 name 的 legacy function_call 仍是真实的协议错误。
func TestOpenAIHandleResponse_StreamRejectsNamelessLegacyFunctionCallWithArguments(t *testing.T) {
	adapter := &OpenAIAdapter{}

	_, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"function_call":{"name":"","arguments":"{\"q\":\"x\"}"}},"finish_reason":"function_call","index":0}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected nameless legacy function_call with arguments to fail")
	}
	if !strings.Contains(err.Error(), "invalid_tool_call") {
		t.Fatalf("expected invalid_tool_call, got %v", err)
	}
}
