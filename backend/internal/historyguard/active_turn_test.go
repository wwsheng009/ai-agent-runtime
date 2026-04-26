package historyguard

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestCompactActiveTurnReplay_NoCompactionWhenWithinBudget(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("current request"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "README.md"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_1", "README preview"),
	}

	got, compacted := CompactActiveTurnReplay(messages, 4096)
	if compacted {
		t.Fatalf("did not expect compaction, got %#v", got)
	}
	if len(got) != len(messages) {
		t.Fatalf("expected history size to remain unchanged, got %#v", got)
	}
}

func TestCompactActiveTurnReplay_CompactsEarlierReplayAndKeepsLatestBlock(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 80)
	messages := []types.Message{
		*types.NewUserMessage("continue analysis"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "README.md"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_1", "README "+large),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_2", Name: "view", Args: map[string]interface{}{"file_path": "AGENTS.md"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_2", "AGENTS "+large),
	}

	got, compacted := CompactActiveTurnReplay(messages, 2048)
	if !compacted {
		t.Fatalf("expected compaction, got %#v", got)
	}
	if len(got) != 4 {
		t.Fatalf("expected user + summary + latest assistant + latest tool, got %#v", got)
	}
	if got[1].Role != "assistant" || got[1].Metadata["active_turn_compaction"] != true {
		t.Fatalf("expected compacted assistant summary, got %#v", got[1])
	}
	if !strings.Contains(got[1].Content, "Compacted earlier tool replay in current turn") {
		t.Fatalf("expected compacted summary content, got %q", got[1].Content)
	}
	if got[2].Role != "assistant" || len(got[2].ToolCalls) != 1 || got[2].ToolCalls[0].ID != "call_2" {
		t.Fatalf("expected latest assistant tool call to be preserved, got %#v", got[2])
	}
	if got[3].Role != "tool" || got[3].ToolCallID != "call_2" {
		t.Fatalf("expected latest tool result to be preserved, got %#v", got[3])
	}
	if strings.Contains(got[3].Content, "README") {
		t.Fatalf("did not expect first tool output to remain in latest block, got %#v", got[3])
	}
}

func TestCompactActiveTurnReplayWithCounter_CompactsWhenTokenBudgetExceeded(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 40)
	messages := []types.Message{
		*types.NewUserMessage("continue analysis"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "view", Args: map[string]interface{}{"file_path": "README.md"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_1", "README "+large),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_2", Name: "view", Args: map[string]interface{}{"file_path": "AGENTS.md"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_2", "AGENTS "+large),
	}

	counter := func(messages []types.Message) int {
		total := 0
		for _, message := range messages {
			total += len(message.Content) / 4
		}
		return total
	}

	got, compacted := CompactActiveTurnReplayWithCounter(messages, 64*1024, 500, counter)
	if !compacted {
		t.Fatalf("expected token-budget compaction, got %#v", got)
	}
	if len(got) != 4 {
		t.Fatalf("expected user + summary + latest assistant + latest tool, got %#v", got)
	}
	if got[1].Metadata.GetBool("active_turn_compaction", false) != true {
		t.Fatalf("expected active turn compaction metadata, got %#v", got[1].Metadata)
	}
	if got[1].Metadata.GetString("active_turn_compaction_reason", "") == "" {
		t.Fatalf("expected compaction reason metadata, got %#v", got[1].Metadata)
	}
	if got[2].Role != "assistant" || len(got[2].ToolCalls) != 1 || got[2].ToolCalls[0].ID != "call_2" {
		t.Fatalf("expected latest assistant tool call to remain raw, got %#v", got[2])
	}
	if got[3].Role != "tool" || got[3].ToolCallID != "call_2" {
		t.Fatalf("expected latest tool result to remain raw, got %#v", got[3])
	}
}
