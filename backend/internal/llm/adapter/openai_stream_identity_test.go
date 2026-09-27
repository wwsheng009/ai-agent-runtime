package adapter

import (
	"strings"
	"testing"
)

// TestOpenAIStreamCountsArgumentFragments 锁定 P1-6：arguments 的增量片段数
// 随 malformed 调用落盘，离线可区分「单个大 delta」与「多片拼接」。
func TestOpenAIStreamCountsArgumentFragments(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"timeout\": "}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"60"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"s}"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected malformed-arguments error")
	}
	malformed, ok := err.(*MalformedToolCallError)
	if !ok {
		t.Fatalf("expected *MalformedToolCallError, got %T: %v", err, err)
	}
	if len(malformed.ToolCalls) != 1 {
		t.Fatalf("expected 1 malformed call, got %#v", malformed.ToolCalls)
	}
	call := malformed.ToolCalls[0]
	if call.ArgumentFragments != 3 {
		t.Fatalf("argument fragments = %d, want 3", call.ArgumentFragments)
	}
	if call.ParseClass != MalformedArgumentsParseBareLiteral {
		t.Fatalf("parse_class = %q, want bare_literal", call.ParseClass)
	}
	if !strings.Contains(err.Error(), "delta_fragments=3") {
		t.Fatalf("error message must carry fragment evidence, got %q", err.Error())
	}
}

// TestOpenAIStreamRejectsToolCallIDIndexConflict 锁定 P1-6：同一 id 出现在不同
// index 时不得把两次调用拼进同一身份，必须 fail-closed。
func TestOpenAIStreamRejectsToolCallIDIndexConflict(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"shell","arguments":"{}"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_x","function":{"arguments":"{}"}}]}}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected protocol error for a tool-call id reused at another index")
	}
	if !strings.Contains(err.Error(), "tool_call_id_index_conflict") {
		t.Fatalf("expected tool_call_id_index_conflict, got %v", err)
	}
}

// TestOpenAIStreamRejectsToolCallNameChange 锁定 P1-6：同一 index 的名称在流中
// 发生变化属于身份覆盖，必须拒绝。
func TestOpenAIStreamRejectsToolCallNameChange(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{}"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"write_file","arguments":"{}"}}]}}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected protocol error for a mid-stream name change")
	}
	if !strings.Contains(err.Error(), "tool_call_name_conflict") {
		t.Fatalf("expected tool_call_name_conflict, got %v", err)
	}
}

// TestOpenAIStreamRejectsMixedToolCallPayloads 锁定 P1-6：同一 delta 或同一流里
// modern tool_calls 与 legacy function_call 混写同一槽位必须拒绝（空占位除外）。
func TestOpenAIStreamRejectsMixedToolCallPayloads(t *testing.T) {
	t.Run("same delta", func(t *testing.T) {
		_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"shell","arguments":"{}"}}],"function_call":{"name":"shell","arguments":"{}"}}}]}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")), StreamCallbacks{})
		if err == nil || !strings.Contains(err.Error(), "mixed_tool_call_payload") {
			t.Fatalf("expected mixed_tool_call_payload for one mixed delta, got %v", err)
		}
	})

	t.Run("across chunks", func(t *testing.T) {
		_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"shell","arguments":"{}"}}]}}]}`,
			"",
			`data: {"choices":[{"delta":{"function_call":{"name":"shell","arguments":"{}"}}}]}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")), StreamCallbacks{})
		if err == nil || !strings.Contains(err.Error(), "mixed_tool_call_payload") {
			t.Fatalf("expected mixed_tool_call_payload across chunks, got %v", err)
		}
	})

	t.Run("empty legacy placeholder stays compatible", func(t *testing.T) {
		_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"hello"}}]}`,
			"",
			`data: {"choices":[{"delta":{"function_call":{"name":"","arguments":""}}}]}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")), StreamCallbacks{})
		if err != nil {
			t.Fatalf("empty legacy placeholder must not fail the stream: %v", err)
		}
	})
}
