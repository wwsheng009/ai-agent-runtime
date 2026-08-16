package adapter

import (
	"strings"
	"testing"
)

// TestMalformedToolCallError_StreamCarriesCallInfo 验证流式路径：
// invalid_tool_arguments 以 MalformedToolCallError 返回且携带调用原文。
func TestMalformedToolCallError_StreamCarriesCallInfo(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"content\":\"truncated"}}]},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil {
		t.Fatalf("expected error")
	}
	malformed, ok := err.(*MalformedToolCallError)
	if !ok {
		t.Fatalf("expected *MalformedToolCallError, got %T: %v", err, err)
	}
	if malformed.Code != "invalid_tool_arguments" {
		t.Fatalf("unexpected code: %q", malformed.Code)
	}
	if len(malformed.ToolCalls) != 1 {
		t.Fatalf("expected 1 malformed tool call, got %d", len(malformed.ToolCalls))
	}
	call := malformed.ToolCalls[0]
	if call.Name != "write" || call.Arguments != `{"content":"truncated` {
		t.Fatalf("unexpected tool call info: %#v", call)
	}
	// 消息格式保持与旧 openAIProtocolError 一致（retry policy / 诊断按消息匹配）。
	if !strings.Contains(err.Error(), "openai_stream_protocol_error") || !strings.Contains(err.Error(), "invalid_tool_arguments") {
		t.Fatalf("message format regressed: %v", err)
	}
	if malformed.RetryErrorCode() != "invalid_tool_arguments" {
		t.Fatalf("unexpected retry code: %q", malformed.RetryErrorCode())
	}
}

// TestMalformedToolCallError_NonStreamCarriesCallInfo 验证非流式路径。
func TestMalformedToolCallError_NonStreamCarriesCallInfo(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"",
		"tool_calls":[{"id":"call_9","type":"function","function":{"name":"lookup","arguments":"{\"timeout\": 60s}"}}]},
		"finish_reason":"tool_calls"}]}`
	_, err := (&OpenAIAdapter{}).HandleResponse(false, strings.NewReader(body), StreamCallbacks{})
	if err == nil {
		t.Fatalf("expected error")
	}
	malformed, ok := err.(*MalformedToolCallError)
	if !ok {
		t.Fatalf("expected *MalformedToolCallError, got %T: %v", err, err)
	}
	if len(malformed.ToolCalls) != 1 {
		t.Fatalf("expected 1 malformed tool call, got %d", len(malformed.ToolCalls))
	}
	if malformed.ToolCalls[0].Name != "lookup" || malformed.ToolCalls[0].Arguments != `{"timeout": 60s}` {
		t.Fatalf("unexpected tool call info: %#v", malformed.ToolCalls[0])
	}
}

// TestMalformedToolCallError_CollectsAll 验证一次响应中多条非法调用全部收集。
func TestMalformedToolCallError_CollectsAll(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"",
		"tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":"}},
			{"id":"call_2","type":"function","function":{"name":"read","arguments":"[1,2,"}}
		]},
		"finish_reason":"tool_calls"}]}`
	_, err := (&OpenAIAdapter{}).HandleResponse(false, strings.NewReader(body), StreamCallbacks{})
	if err == nil {
		t.Fatalf("expected error")
	}
	malformed, ok := err.(*MalformedToolCallError)
	if !ok {
		t.Fatalf("expected *MalformedToolCallError, got %T: %v", err, err)
	}
	if len(malformed.ToolCalls) != 2 {
		t.Fatalf("expected 2 malformed tool calls, got %d", len(malformed.ToolCalls))
	}
	if malformed.ToolCalls[0].Name != "write" || malformed.ToolCalls[1].Name != "read" {
		t.Fatalf("unexpected calls: %#v", malformed.ToolCalls)
	}
}

// TestMalformedToolCallError_CodexPath 验证 codex 协议路径同样携带调用信息。
func TestMalformedToolCallError_CodexPath(t *testing.T) {
	body := `{"output":[{"type":"function_call","id":"fc_1","name":"lookup","arguments":"{\"timeout\": 60s}"}],"result":"completed"}`
	_, err := (&CodexAdapter{}).HandleResponse(false, strings.NewReader(body), StreamCallbacks{})
	if err == nil {
		t.Fatalf("expected error")
	}
	malformed, ok := err.(*MalformedToolCallError)
	if !ok {
		t.Fatalf("expected *MalformedToolCallError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "codex response invalid") {
		t.Fatalf("message format regressed: %v", err)
	}
	if len(malformed.ToolCalls) != 1 || malformed.ToolCalls[0].Name != "lookup" {
		t.Fatalf("unexpected tool call info: %#v", malformed.ToolCalls)
	}
}
