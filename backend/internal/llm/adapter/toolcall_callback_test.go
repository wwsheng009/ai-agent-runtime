package adapter

import (
	"strings"
	"testing"
)

// 纯 tool-call 流没有文本/思考/图片增量，首字时间只能由"工具调用参数开始
// 流出"这一信号证明。四个协议的适配器都必须在首个工具增量时触发 OnToolCall。
func TestStreamCallbacks_EmitToolCallOnFirstToolDelta(t *testing.T) {
	cases := []struct {
		name string
		run  func(callbacks StreamCallbacks) (map[string]interface{}, error)
	}{
		{
			name: "openai chat completions modern tool_calls",
			run: func(callbacks StreamCallbacks) (map[string]interface{}, error) {
				sse := strings.Join([]string{
					`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"git status\"}"}}]}}]}`,
					"",
					`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
					"",
					"data: [DONE]",
					"",
				}, "\n")
				return (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(sse), callbacks)
			},
		},
		{
			name: "openai chat completions legacy function_call",
			run: func(callbacks StreamCallbacks) (map[string]interface{}, error) {
				sse := strings.Join([]string{
					`data: {"choices":[{"delta":{"function_call":{"name":"shell","arguments":"{\"command\":\"git status\"}"}}}]}`,
					"",
					`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
					"",
					"data: [DONE]",
					"",
				}, "\n")
				return (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(sse), callbacks)
			},
		},
		{
			name: "codex responses function_call",
			run: func(callbacks StreamCallbacks) (map[string]interface{}, error) {
				sse := strings.Join([]string{
					"event: response.output_item.added",
					`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_ttft","call_id":"call_ttft","name":"shell","arguments":""}}`,
					"",
					"event: response.function_call_arguments.delta",
					`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"git status\"}"}`,
					"",
					"event: response.function_call_arguments.done",
					`data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_ttft","call_id":"call_ttft","name":"shell","arguments":"{\"command\":\"git status\"}"}`,
					"",
					"event: response.output_item.done",
					`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_ttft","call_id":"call_ttft","name":"shell","arguments":"{\"command\":\"git status\"}"}}`,
					"",
					"event: response.completed",
					`data: {"type":"response.completed","response":{"id":"resp_ttft","status":"completed","stop_reason":"tool_call"}}`,
					"",
				}, "\n")
				return (&CodexAdapter{}).HandleResponse(true, strings.NewReader(sse), callbacks)
			},
		},
		{
			name: "anthropic messages tool_use",
			run: func(callbacks StreamCallbacks) (map[string]interface{}, error) {
				sse := strings.Join([]string{
					"event: message_start",
					`data: {"type":"message_start","message":{"id":"msg_ttft","type":"message","role":"assistant","model":"m","content":[]}}`,
					"",
					"event: content_block_start",
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_ttft","name":"shell","input":{}}}`,
					"",
					"event: content_block_delta",
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"git status\"}"}}`,
					"",
					"event: content_block_stop",
					`data: {"type":"content_block_stop","index":0}`,
					"",
					"event: message_delta",
					`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
					"",
					"event: message_stop",
					`data: {"type":"message_stop"}`,
					"",
				}, "\n")
				return (&AnthropicAdapter{}).HandleResponse(true, strings.NewReader(sse), callbacks)
			},
		},
		{
			name: "gemini functionCall",
			run: func(callbacks StreamCallbacks) (map[string]interface{}, error) {
				sse := strings.Join([]string{
					`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"shell","args":{"command":"git status"}}}]}}]}`,
					"",
					`data: {"candidates":[{"finishReason":"STOP"}]}`,
					"",
				}, "\n")
				return (&GeminiAdapter{}).HandleResponse(true, strings.NewReader(sse), callbacks)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var toolCallSignals int
			msg, err := tc.run(StreamCallbacks{OnToolCall: func() { toolCallSignals++ }})
			if err != nil {
				t.Fatalf("HandleResponse failed: %v", err)
			}
			if toolCallSignals == 0 {
				t.Fatal("OnToolCall was never emitted for a tool-call-only stream")
			}
			if msg == nil {
				t.Fatal("expected assistant message")
			}
		})
	}
}

// 文本回复不得触发工具调用信号，避免首字时间被非工具内容错误归类。
func TestStreamCallbacks_TextOnlyStreamDoesNotEmitToolCall(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hello"}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var toolCallSignals, textSignals int
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(sse), StreamCallbacks{
		OnText:     func(string) { textSignals++ },
		OnToolCall: func() { toolCallSignals++ },
	})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if textSignals == 0 {
		t.Fatal("expected text signals for a text stream")
	}
	if toolCallSignals != 0 {
		t.Fatalf("text-only stream emitted %d tool-call signals", toolCallSignals)
	}
}
