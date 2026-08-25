package adapter

import (
	"encoding/json"
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

func TestOpenAIHandleResponse_PreservesExplicitEmptyReasoningContentFromStream(t *testing.T) {
	adapter := &OpenAIAdapter{}
	var reasoningParts []string

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"我来检查代码。"},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")), StreamCallbacks{
		OnReasoning: func(reasoning string) {
			reasoningParts = append(reasoningParts, reasoning)
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse: %v", err)
	}
	got, exists := msg["reasoning_content"]
	if !exists || got != "" {
		t.Fatalf("expected explicit empty reasoning_content, got exists=%v value=%#v", exists, got)
	}
	if got, _ := msg["content"].(string); got != "我来检查代码。" {
		t.Fatalf("unexpected content: %q", got)
	}
	if len(reasoningParts) != 0 {
		t.Fatalf("expected no non-empty reasoning deltas, got %#v", reasoningParts)
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

// Reproduces the real grok/codex stream shape that previously tripled reasoning:
// delta keeps a trailing '\n', done/recover snapshots restate the same body, and
// appendMissingCodexText used to treat the whitespace-only mismatch as missing.
func TestCodexHandleResponseDoesNotDuplicateReasoningWithTrailingNewline(t *testing.T) {
	adapter := &CodexAdapter{}
	const reasoning = "The user wants me to continue implementing the plan. Phase 0-2 are done. Next is Phase 3: Typed cell, tool ANSI, and Diff.\n"
	var reasoningParts []string

	reasoningJSON, err := jsonQuote(reasoning)
	if err != nil {
		t.Fatalf("quote reasoning: %v", err)
	}

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_dup","model":"grok-4.5"}}`,
		"",
		"event: response.reasoning_summary_part.added",
		`data: {"type":"response.reasoning_summary_part.added","summary_index":0}`,
		"",
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","summary_index":0,"delta":` + reasoningJSON + `}`,
		"",
		"event: response.reasoning_summary_text.done",
		`data: {"type":"response.reasoning_summary_text.done","summary_index":0,"text":` + reasoningJSON + `}`,
		"",
		"event: response.reasoning_summary_part.done",
		`data: {"type":"response.reasoning_summary_part.done","summary_index":0}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","summary":[]}}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[{"type":"summary_text","text":` + reasoningJSON + `}]}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","content":[]}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":1,"delta":"ok"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_dup","status":"completed","stop_reason":"end_turn","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":` + reasoningJSON + `}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`,
		"",
	}, "\n")), StreamCallbacks{
		OnReasoning: func(part string) {
			reasoningParts = append(reasoningParts, part)
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse: %v", err)
	}

	// Final assistant message trims display text via ReasoningBlock.Summary; the
	// important contract is no duplicated body after done/item/completed recovery.
	wantStored := strings.TrimSpace(reasoning)
	got, _ := msg["reasoning_content"].(string)
	if got != wantStored {
		t.Fatalf("unexpected reasoning_content:\n got: %q\nwant: %q", got, wantStored)
	}
	if count := strings.Count(got, "Phase 0-2 are done"); count != 1 {
		t.Fatalf("reasoning_content duplicated phrase %d times: %q", count, got)
	}
	joined := strings.Join(reasoningParts, "")
	if joined != reasoning {
		t.Fatalf("unexpected streamed reasoning:\n got: %q\nwant: %q\nparts: %#v", joined, reasoning, reasoningParts)
	}
	if count := strings.Count(joined, "Phase 0-2 are done"); count != 1 {
		t.Fatalf("streamed reasoning duplicated phrase %d times: %#v", count, reasoningParts)
	}
	if len(reasoningParts) != 1 {
		t.Fatalf("expected a single reasoning emit from deltas, got %#v", reasoningParts)
	}
}

func TestCodexHandleResponsePreservesReasoningSummaryPartBoundaries(t *testing.T) {
	adapter := &CodexAdapter{}
	const (
		first  = "Assessing terminal lifecycle callback timing"
		second = "Reevaluating host adapter projection order"
	)
	want := first + "\n" + second
	var reasoningParts []string

	msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_parts","model":"gpt-5.4"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","summary":[]}}`,
		"",
		"event: response.reasoning_summary_part.added",
		`data: {"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0}`,
		"",
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"Assessing terminal "}`,
		"",
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"lifecycle callback timing"}`,
		"",
		"event: response.reasoning_summary_text.done",
		`data: {"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0,"text":"Assessing terminal lifecycle callback timing"}`,
		"",
		"event: response.reasoning_summary_part.done",
		`data: {"type":"response.reasoning_summary_part.done","output_index":0,"summary_index":0}`,
		"",
		"event: response.reasoning_summary_part.added",
		`data: {"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":1}`,
		"",
		// A replayed lifecycle event must not create another separator.
		"event: response.reasoning_summary_part.added",
		`data: {"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":1}`,
		"",
		"event: response.reasoning_summary_text.delta",
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"Reevaluating host adapter "}`,
		"",
		"event: response.reasoning_summary_text.done",
		`data: {"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":1,"text":"Reevaluating host adapter projection order"}`,
		"",
		"event: response.reasoning_summary_part.done",
		`data: {"type":"response.reasoning_summary_part.done","output_index":0,"summary_index":1}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[{"type":"summary_text","text":"Assessing terminal lifecycle callback timing"},{"type":"summary_text","text":"Reevaluating host adapter projection order"}]}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","content":[]}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":1,"delta":"ok"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_parts","status":"completed","stop_reason":"end_turn","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"Assessing terminal lifecycle callback timing"},{"type":"summary_text","text":"Reevaluating host adapter projection order"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`,
		"",
	}, "\n")), StreamCallbacks{
		OnReasoning: func(part string) {
			reasoningParts = append(reasoningParts, part)
		},
	})
	if err != nil {
		t.Fatalf("HandleResponse: %v", err)
	}

	if got, _ := msg["reasoning_content"].(string); got != want {
		t.Fatalf("unexpected reasoning_content:\n got: %q\nwant: %q", got, want)
	}
	if got := strings.Join(reasoningParts, ""); got != want {
		t.Fatalf("unexpected streamed reasoning:\n got: %q\nwant: %q\nparts: %#v", got, want, reasoningParts)
	}
	wantParts := []string{
		"Assessing terminal ",
		"lifecycle callback timing",
		"\nReevaluating host adapter ",
		"projection order",
	}
	if len(reasoningParts) != len(wantParts) {
		t.Fatalf("done/item/completed snapshots replayed reasoning: got %#v, want %#v", reasoningParts, wantParts)
	}
	for index := range wantParts {
		if reasoningParts[index] != wantParts[index] {
			t.Fatalf("reasoning part %d = %q, want %q (all parts: %#v)", index, reasoningParts[index], wantParts[index], reasoningParts)
		}
	}
	if count := strings.Count(strings.Join(reasoningParts, ""), first); count != 1 {
		t.Fatalf("first summary part was replayed %d times: %#v", count, reasoningParts)
	}
	if count := strings.Count(strings.Join(reasoningParts, ""), second); count != 1 {
		t.Fatalf("second summary part was replayed %d times: %#v", count, reasoningParts)
	}
}

func TestCodexHandleResponseNonStreamPreservesReasoningSummaryPartBoundaries(t *testing.T) {
	adapter := &CodexAdapter{}
	const want = "Assessing lifecycle\nReevaluating projection"
	msg, err := adapter.HandleResponse(false, strings.NewReader(`{
		"id":"resp_parts",
		"status":"completed",
		"output":[
			{"type":"reasoning","summary":[
				{"type":"summary_text","text":"Assessing lifecycle"},
				{"type":"summary_text","text":"   "},
				{"type":"summary_text","text":"Reevaluating projection"}
			]}
		]
	}`), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse: %v", err)
	}
	if got, _ := msg["reasoning_content"].(string); got != want {
		t.Fatalf("unexpected reasoning_content: got %q, want %q", got, want)
	}
}

func TestCodexHandleResponseRecoversReasoningSummaryPartBoundariesFromSnapshots(t *testing.T) {
	const (
		summary = `[{"type":"summary_text","text":"Assessing lifecycle"},{"type":"summary_text","text":"Reevaluating projection"}]`
		want    = "Assessing lifecycle\nReevaluating projection"
	)
	tests := []struct {
		name   string
		events []string
	}{
		{
			name: "output item done then completed",
			events: []string{
				"event: response.output_item.done",
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":` + summary + `}}`,
				"",
				"event: response.completed",
				`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","summary":` + summary + `}]}}`,
				"",
			},
		},
		{
			name: "completed only",
			events: []string{
				"event: response.completed",
				`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","summary":` + summary + `}]}}`,
				"",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &CodexAdapter{}
			var emitted []string
			msg, err := adapter.HandleResponse(true, strings.NewReader(strings.Join(test.events, "\n")), StreamCallbacks{
				OnReasoning: func(part string) {
					emitted = append(emitted, part)
				},
			})
			if err != nil {
				t.Fatalf("HandleResponse: %v", err)
			}
			if got, _ := msg["reasoning_content"].(string); got != want {
				t.Fatalf("unexpected reasoning_content: got %q, want %q", got, want)
			}
			if got := strings.Join(emitted, ""); got != want {
				t.Fatalf("unexpected recovered reasoning: got %q, want %q (parts: %#v)", got, want, emitted)
			}
			if len(emitted) != 1 {
				t.Fatalf("snapshot lifecycle replayed reasoning: %#v", emitted)
			}
		})
	}
}

func TestAppendMissingCodexTextIgnoresTrailingWhitespaceMismatch(t *testing.T) {
	var builder strings.Builder
	var emitted []string
	emit := func(s string) { emitted = append(emitted, s) }

	builder.WriteString("hello\n")
	appendMissingCodexText(&builder, "hello", emit)
	if builder.String() != "hello\n" {
		t.Fatalf("trimmed done text re-appended body: %q", builder.String())
	}
	if len(emitted) != 0 {
		t.Fatalf("expected no emit on whitespace-only mismatch, got %#v", emitted)
	}

	appendMissingCodexText(&builder, "hello\n", emit)
	if builder.String() != "hello\n" {
		t.Fatalf("exact snapshot re-appended body: %q", builder.String())
	}

	appendMissingCodexText(&builder, "hello\nworld", emit)
	if builder.String() != "hello\nworld" {
		t.Fatalf("expected remainder append, got %q", builder.String())
	}
	if strings.Join(emitted, "") != "world" && strings.Join(emitted, "") != "\nworld" {
		// remainder may be "world" (trimmed path) depending on prefix matching
		if got := strings.Join(emitted, ""); !strings.HasSuffix(got, "world") {
			t.Fatalf("unexpected emit remainder: %#v", emitted)
		}
	}
}

func jsonQuote(s string) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
