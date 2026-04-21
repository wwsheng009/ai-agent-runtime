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
