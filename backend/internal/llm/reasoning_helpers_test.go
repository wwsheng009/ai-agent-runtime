package llm

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRuntimeMessagesToProtocolMessages_OpenAINormalizesToolReplayPayload(t *testing.T) {
	assistant := types.Message{
		Role:    "assistant",
		Content: "我来查看目录。",
		ToolCalls: []types.ToolCall{
			{ID: "call_1", Name: "ls"},
		},
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_compatible",
		Summary:    "先看目录。",
		Visibility: types.ReasoningVisibilitySummary,
	})

	tool := types.Message{
		Role:       "tool",
		Content:    "目录: .",
		ToolCallID: "call_1",
		Metadata:   types.NewMetadata(),
	}
	tool.Metadata["artifact_refs"] = []string{"art_1"}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant, tool}, "openai")
	if len(messages) != 2 {
		t.Fatalf("expected 2 protocol messages, got %d", len(messages))
	}

	if _, exists := messages[0]["reasoning_content"]; exists {
		t.Fatalf("did not expect reasoning_content in openai request message: %#v", messages[0])
	}
	if _, exists := messages[0]["metadata"]; exists {
		t.Fatalf("did not expect metadata in openai request message: %#v", messages[0])
	}

	toolCalls, ok := messages[0]["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %#v", messages[0]["tool_calls"])
	}
	functionPayload, ok := toolCalls[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected function payload map, got %#v", toolCalls[0]["function"])
	}
	if got := functionPayload["arguments"]; got != "{}" {
		t.Fatalf("expected empty args to normalize to {}, got %#v", got)
	}

	if _, exists := messages[1]["metadata"]; exists {
		t.Fatalf("did not expect metadata in tool request message: %#v", messages[1])
	}
	if got := messages[1]["tool_call_id"]; got != "call_1" {
		t.Fatalf("unexpected tool_call_id: %#v", got)
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexDropsOrphanToolReplayPayload(t *testing.T) {
	assistant := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "call_view", Name: "view"},
		},
		Metadata: types.NewMetadata(),
	}
	validTool := types.Message{
		Role:       "tool",
		Content:    "view result",
		ToolCallID: "call_view",
		Metadata:   types.NewMetadata(),
	}

	messages := RuntimeMessagesToProtocolMessages([]types.Message{
		*types.NewToolMessage("call_old_1", "old result 1"),
		*types.NewToolMessage("call_old_2", "old result 2"),
		assistant,
		validTool,
	}, "codex")

	if len(messages) != 2 {
		t.Fatalf("expected orphan tool replay messages to be dropped, got %d messages: %#v", len(messages), messages)
	}

	if got := messages[0]["role"]; got != "assistant" {
		t.Fatalf("expected assistant message first after sanitization, got %#v", messages[0])
	}
	if got := messages[1]["tool_call_id"]; got != "call_view" {
		t.Fatalf("expected only valid tool replay to remain, got %#v", messages[1])
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexCanonicalizesAssistantReplayShape(t *testing.T) {
	assistant := types.Message{
		Role:    "assistant",
		Content: "我来查看目录。",
		ToolCalls: []types.ToolCall{
			{
				ID:   "call_ls",
				Name: "ls",
				Args: map[string]interface{}{"path": "."},
			},
		},
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_responses",
		Summary:    "先查看目录结构。",
		Visibility: types.ReasoningVisibilitySummary,
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "codex")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}

	if _, exists := messages[0]["tool_calls"]; exists {
		t.Fatalf("expected codex assistant replay to use response_output_items, got %#v", messages[0])
	}
	outputItems, ok := messages[0]["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 3 {
		t.Fatalf("expected 3 response_output_items, got %T %#v", messages[0]["response_output_items"], messages[0]["response_output_items"])
	}
	if outputItems[0]["type"] != "reasoning" {
		t.Fatalf("expected first output item reasoning, got %#v", outputItems[0])
	}
	if outputItems[1]["type"] != "message" {
		t.Fatalf("expected second output item message, got %#v", outputItems[1])
	}
	if outputItems[2]["type"] != "function_call" || outputItems[2]["call_id"] != "call_ls" {
		t.Fatalf("expected function_call output item, got %#v", outputItems[2])
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexStripsVolatileReplayFields(t *testing.T) {
	assistant := types.Message{
		Role:     "assistant",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_responses",
		Visibility: types.ReasoningVisibilityOpaque,
		Metadata: map[string]interface{}{
			"response_output_items": []map[string]interface{}{
				{
					"type":              "reasoning",
					"id":                "rs_123",
					"status":            "completed",
					"encrypted_content": "opaque-token",
					"summary": []map[string]interface{}{
						{
							"type":   "summary_text",
							"id":     "sum_1",
							"status": "completed",
							"text":   "先检查最近的工具输出。",
						},
					},
				},
				{
					"type":   "message",
					"id":     "msg_123",
					"phase":  "final_answer",
					"status": "completed",
					"role":   "assistant",
					"content": []map[string]interface{}{
						{
							"type":        "output_text",
							"text":        "我先整理已知信息。",
							"logprobs":    []interface{}{},
							"annotations": []map[string]interface{}{},
						},
					},
				},
				{
					"type":      "function_call",
					"id":        "call_123",
					"status":    "completed",
					"name":      "view",
					"arguments": `{"file_path":"README.md"}`,
				},
			},
		},
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "codex")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}

	outputItems, ok := messages[0]["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 3 {
		t.Fatalf("expected 3 response_output_items, got %#v", messages[0]["response_output_items"])
	}

	if _, exists := outputItems[0]["id"]; exists {
		t.Fatalf("did not expect reasoning item id after canonicalization: %#v", outputItems[0])
	}
	if _, exists := outputItems[0]["status"]; exists {
		t.Fatalf("did not expect reasoning item status after canonicalization: %#v", outputItems[0])
	}
	if outputItems[0]["encrypted_content"] != "opaque-token" {
		t.Fatalf("expected encrypted_content to be preserved, got %#v", outputItems[0]["encrypted_content"])
	}

	if _, exists := outputItems[1]["id"]; exists {
		t.Fatalf("did not expect message item id after canonicalization: %#v", outputItems[1])
	}
	if _, exists := outputItems[1]["phase"]; exists {
		t.Fatalf("did not expect message item phase after canonicalization: %#v", outputItems[1])
	}
	content, ok := outputItems[1]["content"].([]map[string]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("expected 1 message content part, got %#v", outputItems[1]["content"])
	}
	if _, exists := content[0]["logprobs"]; exists {
		t.Fatalf("did not expect logprobs after canonicalization: %#v", content[0])
	}
	if _, exists := content[0]["annotations"]; exists {
		t.Fatalf("did not expect empty annotations after canonicalization: %#v", content[0])
	}

	if outputItems[2]["call_id"] != "call_123" {
		t.Fatalf("expected function_call id to normalize into call_id, got %#v", outputItems[2])
	}
	if _, exists := outputItems[2]["id"]; exists {
		t.Fatalf("did not expect raw function_call id field after canonicalization: %#v", outputItems[2])
	}
	if _, exists := outputItems[2]["status"]; exists {
		t.Fatalf("did not expect function_call status after canonicalization: %#v", outputItems[2])
	}
}

func TestRuntimeMessagesToProtocolMessages_CodexPreservesEmptyReasoningSummaryForOpaqueReplay(t *testing.T) {
	assistant := types.Message{
		Role:     "assistant",
		Metadata: types.NewMetadata(),
	}
	types.SetReasoningBlock(assistant.Metadata, &types.ReasoningBlock{
		Format:     "openai_responses",
		Visibility: types.ReasoningVisibilityOpaque,
		Metadata: map[string]interface{}{
			"response_output_items": []map[string]interface{}{
				{
					"type":              "reasoning",
					"encrypted_content": "opaque-token",
					"summary":           []map[string]interface{}{},
				},
			},
		},
	})

	messages := RuntimeMessagesToProtocolMessages([]types.Message{assistant}, "codex")
	if len(messages) != 1 {
		t.Fatalf("expected 1 protocol message, got %d", len(messages))
	}

	outputItems, ok := messages[0]["response_output_items"].([]map[string]interface{})
	if !ok || len(outputItems) != 1 {
		t.Fatalf("expected 1 response_output_item, got %#v", messages[0]["response_output_items"])
	}
	if outputItems[0]["encrypted_content"] != "opaque-token" {
		t.Fatalf("expected encrypted_content to be preserved, got %#v", outputItems[0]["encrypted_content"])
	}

	summary, ok := outputItems[0]["summary"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected empty reasoning summary array to be preserved, got %#v", outputItems[0]["summary"])
	}
	if len(summary) != 0 {
		t.Fatalf("expected empty reasoning summary array, got %#v", summary)
	}
}
