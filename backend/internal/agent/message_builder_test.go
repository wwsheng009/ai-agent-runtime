package agent

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestMessageBuilder_AssignsToolCallIDsAndRepairsMissingResults(t *testing.T) {
	builder := NewMessageBuilder(nil)

	toolCalls := builder.AppendAssistantAction("I will inspect the repository.", []types.ToolCall{
		{
			Name: "search_repo",
			Args: map[string]interface{}{"query": "gateway"},
		},
	})
	if len(toolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].ID == "" {
		t.Fatal("expected tool call id to be auto-generated")
	}

	builder.AppendToolResults(toolCalls, nil)

	messages := builder.Messages()
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "assistant" {
		t.Fatalf("expected first message to be assistant, got %s", messages[0].Role)
	}
	if messages[1].Role != "tool" {
		t.Fatalf("expected second message to be tool, got %s", messages[1].Role)
	}
	if messages[1].ToolCallID != toolCalls[0].ID {
		t.Fatalf("expected tool call id %s, got %s", toolCalls[0].ID, messages[1].ToolCallID)
	}
	if got := messages[1].Metadata.GetBool("auto_repaired", false); !got {
		t.Fatal("expected repaired tool result to be marked auto_repaired")
	}
	if got := messages[1].Metadata.GetBool("missing", false); !got {
		t.Fatal("expected repaired tool result to be marked missing")
	}
}

func TestMessageBuilder_AlignsResultsByOrderWhenIDsMissing(t *testing.T) {
	builder := NewMessageBuilder(nil)

	toolCalls := builder.AppendAssistantAction("Run both tools.", []types.ToolCall{
		{Name: "tool_a"},
		{Name: "tool_b"},
	})
	builder.AppendToolResults(toolCalls, []ToolResultPayload{
		{Content: "result A"},
		{Content: "result B"},
	})

	messages := builder.Messages()
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if messages[1].ToolCallID != toolCalls[0].ID {
		t.Fatalf("expected first tool result id %s, got %s", toolCalls[0].ID, messages[1].ToolCallID)
	}
	if messages[2].ToolCallID != toolCalls[1].ID {
		t.Fatalf("expected second tool result id %s, got %s", toolCalls[1].ID, messages[2].ToolCallID)
	}
}
