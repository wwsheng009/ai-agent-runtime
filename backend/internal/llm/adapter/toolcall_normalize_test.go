package adapter

import (
	"reflect"
	"testing"
)

// TestNormalizeToolCalls_Shapes 覆盖共享归一化函数支持的全部输入形状。
func TestNormalizeToolCalls_Shapes(t *testing.T) {
	t.Run("openai_nested_is_idempotent", func(t *testing.T) {
		in := []map[string]interface{}{{
			"id":   "call_1",
			"type": "function",
			"function": map[string]interface{}{
				"name":      "search",
				"arguments": `{"q":"go"}`,
			},
		}}
		out := NormalizeToolCalls(in)
		if !reflect.DeepEqual(out, in) {
			t.Fatalf("nested shape must pass through unchanged, got %#v", out)
		}
	})

	t.Run("codex_flat_function_call", func(t *testing.T) {
		// Responses/codex 协议扁平形状：type 为 function_call，无 function 嵌套。
		in := []map[string]interface{}{{
			"id":        "call_1",
			"type":      "function_call",
			"name":      "search",
			"arguments": `{"q":"go"}`,
		}}
		out := NormalizeToolCalls(in)
		fn, ok := out[0]["function"].(map[string]interface{})
		if !ok || out[0]["type"] != "function" || fn["name"] != "search" || fn["arguments"] != `{"q":"go"}` {
			t.Fatalf("flat function_call must become nested function, got %#v", out[0])
		}
		if out[0]["id"] != "call_1" {
			t.Fatalf("id must be preserved, got %#v", out[0])
		}
	})

	t.Run("codex_flat_without_type", func(t *testing.T) {
		// codex 非流式出口的 function_call 没有 type 字段。
		in := []map[string]interface{}{{
			"id":        "call_1",
			"name":      "view",
			"arguments": `{"file_path":"a.go"}`,
		}}
		out := NormalizeToolCalls(in)
		fn, ok := out[0]["function"].(map[string]interface{})
		if !ok || out[0]["type"] != "function" || fn["name"] != "view" {
			t.Fatalf("missing type must default to function, got %#v", out[0])
		}
	})

	t.Run("codex_custom_preserved", func(t *testing.T) {
		in := []map[string]interface{}{{
			"id":    "call_patch",
			"type":  "custom_tool_call",
			"name":  "apply_patch",
			"input": "*** Begin Patch\n*** End Patch",
		}}
		out := NormalizeToolCalls(in)
		if out[0]["type"] != "custom_tool_call" || out[0]["name"] != "apply_patch" || out[0]["input"] != "*** Begin Patch\n*** End Patch" {
			t.Fatalf("custom_tool_call must be preserved, got %#v", out[0])
		}
		if _, hasFn := out[0]["function"]; hasFn {
			t.Fatalf("custom_tool_call must stay flat, got %#v", out[0])
		}
	})

	t.Run("anthropic_tool_use_input_is_arguments", func(t *testing.T) {
		in := []map[string]interface{}{{
			"type":  "tool_use",
			"id":    "toolu_1",
			"name":  "edit_file",
			"input": map[string]interface{}{"file_path": "a.go", "old_string": "old", "new_string": "new"},
		}}
		out := NormalizeToolCalls(in)
		fn, ok := out[0]["function"].(map[string]interface{})
		if !ok || out[0]["type"] != "function" || fn["name"] != "edit_file" {
			t.Fatalf("tool_use must become nested function, got %#v", out[0])
		}
		if fn["arguments"] != `{"file_path":"a.go","new_string":"new","old_string":"old"}` {
			t.Fatalf("tool_use input must become arguments JSON, got %#v", fn["arguments"])
		}
	})

	t.Run("gemini_native_generates_id", func(t *testing.T) {
		in := []map[string]interface{}{{
			"name": "search",
			"args": map[string]interface{}{"q": "go"},
		}}
		out := NormalizeToolCalls(in)
		fn, ok := out[0]["function"].(map[string]interface{})
		if !ok || out[0]["id"] != "call_0" || out[0]["type"] != "function" || fn["name"] != "search" {
			t.Fatalf("gemini native shape must get generated id and nested function, got %#v", out[0])
		}
		if fn["arguments"] != `{"q":"go"}` {
			t.Fatalf("gemini args map must be serialized, got %#v", fn["arguments"])
		}
	})

	t.Run("empty_arguments_become_object", func(t *testing.T) {
		in := []map[string]interface{}{{
			"id":   "call_1",
			"name": "noop",
		}}
		out := NormalizeToolCalls(in)
		fn := out[0]["function"].(map[string]interface{})
		if fn["arguments"] != "{}" {
			t.Fatalf("missing arguments must become {}, got %#v", fn["arguments"])
		}
	})

	t.Run("raw_wrapper_unwrapped", func(t *testing.T) {
		in := []map[string]interface{}{{
			"id":   "call_1",
			"name": "edit_file",
			"function": map[string]interface{}{
				"name":      "edit_file",
				"arguments": `{"_raw":"{\"file_path\":\"a.go\"}"}`,
			},
		}}
		out := NormalizeToolCalls(in)
		fn := out[0]["function"].(map[string]interface{})
		if fn["arguments"] != `{"file_path":"a.go"}` {
			t.Fatalf("_raw wrapper must be unwrapped, got %#v", fn["arguments"])
		}
	})

	t.Run("custom_input_freeform_not_parsed", func(t *testing.T) {
		in := []map[string]interface{}{{
			"id":    "call_patch",
			"type":  "custom_tool_call",
			"name":  "apply_patch",
			"input": "*** Begin Patch\n*** End Patch",
		}}
		out := NormalizeToolCalls(in)
		if out[0]["input"] != "*** Begin Patch\n*** End Patch" {
			t.Fatalf("custom input must stay freeform, got %#v", out[0])
		}
	})

	t.Run("empty_input_returns_nil", func(t *testing.T) {
		if out := NormalizeToolCalls(nil); out != nil {
			t.Fatalf("nil input must return nil, got %#v", out)
		}
		if out := NormalizeToolCalls([]map[string]interface{}{}); out != nil {
			t.Fatalf("empty input must return nil, got %#v", out)
		}
		if out := NormalizeToolCalls([]map[string]interface{}{{}, {}}); out != nil {
			t.Fatalf("all-empty maps must return nil, got %#v", out)
		}
	})
}

// TestBuildAssistantMessage_NormalizesToolCalls 矩阵：四协议入口对"脏历史形状"的
// 入库归一化行为必须一致——非规范形状（扁平 function_call / gemini 原生 / tool_use）
// 一律收敛为规范形状后再进入会话历史。
func TestBuildAssistantMessage_NormalizesToolCalls(t *testing.T) {
	flatFunctionCall := []map[string]interface{}{{
		"id":        "call_1",
		"type":      "function_call", // Responses/codex 协议泄漏值
		"name":      "search",
		"arguments": `{"q":"go"}`,
	}}
	customCall := []map[string]interface{}{{
		"id":    "call_patch",
		"type":  "custom_tool_call",
		"name":  "apply_patch",
		"input": "*** Begin Patch\n*** End Patch",
	}}
	geminiNative := []map[string]interface{}{{
		"name": "search",
		"args": map[string]interface{}{"q": "go"},
	}}

	t.Run("openai_flat_function_call", func(t *testing.T) {
		msg := (&OpenAIAdapter{}).BuildAssistantMessage("ok", flatFunctionCall, "")
		calls := msg["tool_calls"].([]map[string]interface{})
		if len(calls) != 1 || calls[0]["type"] != "function" {
			t.Fatalf("expected nested function, got %#v", calls)
		}
		fn := calls[0]["function"].(map[string]interface{})
		if fn["name"] != "search" || fn["arguments"] != `{"q":"go"}` {
			t.Fatalf("unexpected tool call: %#v", calls[0])
		}
	})

	t.Run("openai_custom_preserved", func(t *testing.T) {
		msg := (&OpenAIAdapter{}).BuildAssistantMessage("ok", customCall, "")
		calls := msg["tool_calls"].([]map[string]interface{})
		if len(calls) != 1 || calls[0]["type"] != "custom_tool_call" || calls[0]["input"] != "*** Begin Patch\n*** End Patch" {
			t.Fatalf("custom_tool_call must be preserved, got %#v", calls)
		}
	})

	t.Run("gemini_native_shape", func(t *testing.T) {
		msg := (&GeminiAdapter{}).BuildAssistantMessage("ok", geminiNative, "")
		calls := msg["tool_calls"].([]map[string]interface{})
		if len(calls) != 1 || calls[0]["id"] == "" || calls[0]["type"] != "function" {
			t.Fatalf("gemini native shape must get id and nested function, got %#v", calls)
		}
		fn := calls[0]["function"].(map[string]interface{})
		if fn["name"] != "search" || fn["arguments"] != `{"q":"go"}` {
			t.Fatalf("unexpected tool call: %#v", calls[0])
		}
	})

	t.Run("codex_flat_function_call", func(t *testing.T) {
		msg := (&CodexAdapter{}).BuildAssistantMessage("ok", flatFunctionCall, "")
		calls := msg["tool_calls"].([]map[string]interface{})
		if len(calls) != 1 || calls[0]["type"] != "function" {
			t.Fatalf("expected nested function, got %#v", calls)
		}
		fn := calls[0]["function"].(map[string]interface{})
		if fn["name"] != "search" || fn["arguments"] != `{"q":"go"}` {
			t.Fatalf("unexpected tool call: %#v", calls[0])
		}
	})

	t.Run("codex_custom_preserved", func(t *testing.T) {
		msg := (&CodexAdapter{}).BuildAssistantMessage("ok", customCall, "")
		calls := msg["tool_calls"].([]map[string]interface{})
		if len(calls) != 1 || calls[0]["type"] != "custom_tool_call" || calls[0]["input"] != "*** Begin Patch\n*** End Patch" {
			t.Fatalf("custom_tool_call must be preserved, got %#v", calls)
		}
	})

	t.Run("anthropic_tool_use", func(t *testing.T) {
		toolUse := []map[string]interface{}{{
			"type":  "tool_use",
			"id":    "toolu_1",
			"name":  "edit_file",
			"input": map[string]interface{}{"file_path": "a.go", "old_string": "old", "new_string": "new"},
		}}
		msg := (&AnthropicAdapter{}).BuildAssistantMessage("ok", toolUse, "")
		calls := msg["tool_calls"].([]map[string]interface{})
		if len(calls) != 1 || calls[0]["type"] != "function" {
			t.Fatalf("tool_use must become nested function, got %#v", calls)
		}
		fn := calls[0]["function"].(map[string]interface{})
		if fn["name"] != "edit_file" {
			t.Fatalf("unexpected tool call: %#v", calls[0])
		}
	})

	t.Run("no_tool_calls_sets_no_key", func(t *testing.T) {
		msg := (&CodexAdapter{}).BuildAssistantMessage("ok", nil, "")
		if _, hasKey := msg["tool_calls"]; hasKey {
			t.Fatalf("empty tool calls must not set the key, got %#v", msg)
		}
	})
}
