package adapter

import (
	"strings"
	"testing"
)

func TestAnthropicProcessResponseExtractsThinkingBlock(t *testing.T) {
	adapter := &AnthropicAdapter{}
	result := adapter.ProcessResponse(map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{
				"type":      "thinking",
				"thinking":  "先读取目录，再决定是否需要搜索。",
				"signature": "sig-123",
			},
			map[string]interface{}{
				"type": "text",
				"text": "我会先查看当前目录。",
			},
		},
	})

	if result.Reasoning != "先读取目录，再决定是否需要搜索。" {
		t.Fatalf("unexpected reasoning: %q", result.Reasoning)
	}
	if result.ReasoningBlock == nil {
		t.Fatal("expected anthropic reasoning block")
	}
	if result.ReasoningBlock.OpaqueState != "sig-123" || !result.ReasoningBlock.ReplayRequired {
		t.Fatalf("unexpected anthropic reasoning block: %+v", result.ReasoningBlock)
	}
}

func TestGeminiProcessResponseExtractsThoughtSummaryAndSignature(t *testing.T) {
	adapter := &GeminiAdapter{}
	result := adapter.ProcessResponse(map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"parts": []interface{}{
						map[string]interface{}{
							"text":             "先确认用户要的是最新数据。",
							"thought":          true,
							"thoughtSignature": "thought-sig-1",
						},
						map[string]interface{}{
							"text": "我来检查最新状态。",
						},
					},
				},
			},
		},
	})

	if result.Reasoning != "先确认用户要的是最新数据。" {
		t.Fatalf("unexpected reasoning: %q", result.Reasoning)
	}
	if result.Content != "我来检查最新状态。" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	if result.ReasoningBlock == nil {
		t.Fatal("expected gemini reasoning block")
	}
	if result.ReasoningBlock.OpaqueState != "thought-sig-1" || !result.ReasoningBlock.ReplayRequired {
		t.Fatalf("unexpected gemini reasoning block: %+v", result.ReasoningBlock)
	}
}

func TestOpenAIHandleResponseStreamsReasoningDelta(t *testing.T) {
	adapter := &OpenAIAdapter{}
	var reasoningParts []string
	var textParts []string

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"先看目录。"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"我来查看目录"},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")), StreamCallbacks{
		OnText: func(text string) {
			textParts = append(textParts, text)
		},
		OnReasoning: func(reasoning string) {
			reasoningParts = append(reasoningParts, reasoning)
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse: %v", err)
	}
	if got, _ := msg["reasoning_content"].(string); got != "先看目录。" {
		t.Fatalf("unexpected reasoning_content: %q", got)
	}
	if got, _ := msg["content"].(string); got != "我来查看目录" {
		t.Fatalf("unexpected content: %q", got)
	}
	if strings.Join(reasoningParts, "") != "先看目录。" {
		t.Fatalf("unexpected reasoning deltas: %#v", reasoningParts)
	}
	if strings.Join(textParts, "") != "我来查看目录" {
		t.Fatalf("unexpected text deltas: %#v", textParts)
	}
}

func TestOpenAIHandleResponse_EmitsReasoningBeforeTextWhenChunkContainsBoth(t *testing.T) {
	adapter := &OpenAIAdapter{}
	var order []string

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"先确认问题。","content":"Hello"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")), StreamCallbacks{
		OnText: func(text string) {
			order = append(order, "text:"+text)
		},
		OnReasoning: func(reasoning string) {
			order = append(order, "reasoning:"+reasoning)
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse: %v", err)
	}
	if got, _ := msg["reasoning_content"].(string); got != "先确认问题。" {
		t.Fatalf("unexpected reasoning_content: %q", got)
	}
	if got, _ := msg["content"].(string); got != "Hello!" {
		t.Fatalf("unexpected content: %q", got)
	}
	expected := []string{"reasoning:先确认问题。", "text:Hello", "text:!"}
	if strings.Join(order, "|") != strings.Join(expected, "|") {
		t.Fatalf("unexpected callback order: %#v", order)
	}
}

func TestAnthropicHandleResponseStreamsThinkingDelta(t *testing.T) {
	adapter := &AnthropicAdapter{}
	var reasoningParts []string
	var textParts []string

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先确认需求。"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"我来检查。"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")), StreamCallbacks{
		OnText: func(text string) {
			textParts = append(textParts, text)
		},
		OnReasoning: func(reasoning string) {
			reasoningParts = append(reasoningParts, reasoning)
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse: %v", err)
	}
	if got, _ := msg["reasoning_content"].(string); got != "先确认需求。" {
		t.Fatalf("unexpected reasoning_content: %q", got)
	}
	if got, _ := msg["content"].(string); got != "我来检查。" {
		t.Fatalf("unexpected content: %q", got)
	}
	if strings.Join(reasoningParts, "") != "先确认需求。" {
		t.Fatalf("unexpected reasoning deltas: %#v", reasoningParts)
	}
	if strings.Join(textParts, "") != "我来检查。" {
		t.Fatalf("unexpected text deltas: %#v", textParts)
	}
}

func TestGeminiHandleResponseStreamsThoughtDelta(t *testing.T) {
	adapter := &GeminiAdapter{}
	var reasoningParts []string
	var textParts []string

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"先检查上下文。","thought":true,"thoughtSignature":"sig-1"}]}}]}`,
		"",
		`data: {"candidates":[{"content":{"parts":[{"text":"我来继续处理。"}]}}]}`,
		"",
		`data: {"candidates":[{"finishReason":"STOP"}]}`,
		"",
	}, "\n")), StreamCallbacks{
		OnText: func(text string) {
			textParts = append(textParts, text)
		},
		OnReasoning: func(reasoning string) {
			reasoningParts = append(reasoningParts, reasoning)
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse: %v", err)
	}
	if got, _ := msg["reasoning_content"].(string); got != "先检查上下文。" {
		t.Fatalf("unexpected reasoning_content: %q", got)
	}
	if got, _ := msg["content"].(string); got != "我来继续处理。" {
		t.Fatalf("unexpected content: %q", got)
	}
	if strings.Join(reasoningParts, "") != "先检查上下文。" {
		t.Fatalf("unexpected reasoning deltas: %#v", reasoningParts)
	}
	if strings.Join(textParts, "") != "我来继续处理。" {
		t.Fatalf("unexpected text deltas: %#v", textParts)
	}
}

func TestCodexHandleResponseStreamsReasoningSummaryDelta(t *testing.T) {
	adapter := &CodexAdapter{}
	var reasoningParts []string
	var textParts []string

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`,
		"",
		"event: response.reasoning_summary_part.added",
		`data: {"type":"response.reasoning_summary_part.added","summary_index":0}`,
		"",
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","summary_index":0,"delta":"先确认文件结构。"}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[]}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"我来查看文件。"} `,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","stop_reason":"end_turn"}}`,
		"",
	}, "\n")), StreamCallbacks{
		OnText: func(text string) {
			textParts = append(textParts, text)
		},
		OnReasoning: func(reasoning string) {
			reasoningParts = append(reasoningParts, reasoning)
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse: %v", err)
	}
	if got, _ := msg["reasoning_content"].(string); got != "先确认文件结构。" {
		t.Fatalf("unexpected reasoning_content: %q", got)
	}
	if got, _ := msg["content"].(string); got != "我来查看文件。" {
		t.Fatalf("unexpected content: %q", got)
	}
	if strings.Join(reasoningParts, "") != "先确认文件结构。" {
		t.Fatalf("unexpected reasoning deltas: %#v", reasoningParts)
	}
	if strings.Join(textParts, "") != "我来查看文件。" {
		t.Fatalf("unexpected text deltas: %#v", textParts)
	}
}
