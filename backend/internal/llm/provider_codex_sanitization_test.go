package llm

import "testing"

func TestProviderWrapperConvertRequest_SanitizesCodexOrphanToolReplayMessages(t *testing.T) {
	provider := &ProviderWrapper{
		config: &ProviderConfig{
			Type: "codex",
		},
	}

	request := ChatRequest{
		Messages: []Message{
			{
				Role:       "tool",
				Content:    "old result 1",
				ToolCallID: "call_old_1",
			},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_view",
						Type: "function",
						Function: ToolCallFunc{
							Name:      "view",
							Arguments: `{"file_path":"AGENTS.md"}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				Content:    "view result",
				ToolCallID: "call_view",
			},
		},
	}

	converted := provider.convertRequest(request)
	if len(converted.Messages) != 2 {
		t.Fatalf("expected orphan tool replay message to be dropped, got %d messages: %#v", len(converted.Messages), converted.Messages)
	}
	if got := converted.Messages[0]["role"]; got != "assistant" {
		t.Fatalf("expected assistant message first after sanitization, got %#v", converted.Messages[0])
	}
	if got := converted.Messages[1]["tool_call_id"]; got != "call_view" {
		t.Fatalf("expected only valid tool replay to remain, got %#v", converted.Messages[1])
	}
}
