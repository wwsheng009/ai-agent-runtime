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
	}, nil, nil)
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
	}, nil, nil)
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

func TestMessageBuilder_AssignsDeterministicToolCallIDs(t *testing.T) {
	builderA := NewMessageBuilder(nil)
	builderB := NewMessageBuilder(nil)

	callsA := builderA.AppendAssistantAction("Run tool.", []types.ToolCall{
		{
			Name: "search_repo",
			Args: map[string]interface{}{"query": "gateway"},
		},
	}, nil, nil)
	callsB := builderB.AppendAssistantAction("Run tool.", []types.ToolCall{
		{
			Name: "search_repo",
			Args: map[string]interface{}{"query": "gateway"},
		},
	}, nil, nil)

	if len(callsA) != 1 || len(callsB) != 1 {
		t.Fatalf("expected one tool call in each builder, got %d and %d", len(callsA), len(callsB))
	}
	if callsA[0].ID == "" || callsB[0].ID == "" {
		t.Fatal("expected tool call ids to be auto-generated")
	}
	if callsA[0].ID != callsB[0].ID {
		t.Fatalf("expected deterministic tool call ids, got %s and %s", callsA[0].ID, callsB[0].ID)
	}
}

func TestMessageBuilder_PreservesAssistantReasoningMetadata(t *testing.T) {
	builder := NewMessageBuilder(nil)

	builder.AppendAssistantAction("answer", nil, &types.ReasoningBlock{
		Provider:       "gemini",
		Format:         "gemini_thought",
		Summary:        "先给出摘要，再继续正文。",
		OpaqueState:    "sig-1",
		ReplayRequired: true,
	}, nil)

	messages := builder.Messages()
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	reasoning := types.GetReasoningBlock(messages[0].Metadata)
	if reasoning == nil {
		t.Fatal("expected reasoning metadata to be preserved")
	}
	if reasoning.Provider != "gemini" || reasoning.OpaqueState != "sig-1" || !reasoning.ReplayRequired {
		t.Fatalf("unexpected reasoning metadata: %+v", reasoning)
	}
}

func TestMessageBuilder_PreservesExplicitEmptyReasoningContentMetadata(t *testing.T) {
	builder := NewMessageBuilder(nil)

	builder.AppendAssistantAction("answer", nil, nil, types.Metadata{
		"reasoning_content": "",
	})

	messages := builder.Messages()
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if got, exists := messages[0].Metadata["reasoning_content"]; !exists || got != "" {
		t.Fatalf("expected explicit empty reasoning_content metadata, got exists=%v value=%#v", exists, got)
	}
}
