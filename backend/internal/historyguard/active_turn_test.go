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

func TestCompactActiveTurnReplay_KeepsLatestBlockBeforeTrailingContext(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 80)
	recall := types.NewAssistantMessage("Relevant recalled artifacts:\n- artifact=art_1 tool=view previous plan")
	recall.Metadata["context_stage"] = "recall"
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
				{ID: "call_2", Name: "ls", Args: map[string]interface{}{"path": "docs/plan"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_2", "docs/plan latest listing"),
		*recall,
	}

	got, compacted := CompactActiveTurnReplay(messages, 2048)
	if !compacted {
		t.Fatalf("expected compaction, got %#v", got)
	}
	if len(got) != 5 {
		t.Fatalf("expected user + summary + latest assistant + latest tool + recall, got %#v", got)
	}
	if got[2].Role != "assistant" || len(got[2].ToolCalls) != 1 || got[2].ToolCalls[0].ID != "call_2" {
		t.Fatalf("expected latest assistant tool call before recall to be preserved, got %#v", got[2])
	}
	if got[3].Role != "tool" || got[3].ToolCallID != "call_2" {
		t.Fatalf("expected latest tool result before recall to be preserved, got %#v", got[3])
	}
	if got[4].Metadata.GetString("context_stage", "") != "recall" {
		t.Fatalf("expected trailing recall context to remain after latest replay, got %#v", got[4])
	}
}

func TestCompactActiveTurnReplay_ReducesLatestReplayToolResultWithoutBreakingToolPair(t *testing.T) {
	large := strings.Repeat("line with artifact_refs: runtime-http/request.json\n", 200)
	messages := []types.Message{
		*types.NewUserMessage("continue analysis"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "read_file", Args: map[string]interface{}{"path": "debug.log"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_1", large),
	}

	got, compacted := CompactActiveTurnReplay(messages, 2048)
	if !compacted {
		t.Fatalf("expected latest replay tool result reduction, got %#v", got)
	}
	if len(got) != len(messages) {
		t.Fatalf("expected message shape to be preserved, got %#v", got)
	}
	if got[1].Role != "assistant" || got[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected assistant tool call to remain adjacent, got %#v", got[1])
	}
	if got[2].Role != "tool" || got[2].ToolCallID != "call_1" {
		t.Fatalf("expected tool result id to be preserved, got %#v", got[2])
	}
	if !got[2].Metadata.GetBool("active_turn_tool_result_reduced", false) {
		t.Fatalf("expected reduction metadata, got %#v", got[2].Metadata)
	}
	if !strings.Contains(got[2].Content, "Tool result content compacted for prompt budget.") {
		t.Fatalf("expected compacted tool result content, got %q", got[2].Content)
	}
	if !strings.Contains(got[2].Content, "artifact_refs") {
		t.Fatalf("expected artifact reference line to be preserved, got %q", got[2].Content)
	}
	if len(got[2].Content) >= len(large) {
		t.Fatalf("expected reduced content to be shorter")
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

func TestCompactActiveTurnReplay_PreservesEarlierCompactionSummaryAnchor(t *testing.T) {
	large := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 70)
	anchor := types.NewAssistantMessage("Compacted earlier tool replay in current turn:\n- assistant requested tools: view\n- tool outcomes: prior context anchor")
	anchor.Metadata["active_turn_compaction"] = true
	anchor.Metadata["compacted_messages"] = 3
	anchor.Metadata["compacted_tool_calls"] = 1

	messages := []types.Message{
		*types.NewUserMessage("continue analysis"),
		*anchor,
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_2", Name: "view", Args: map[string]interface{}{"file_path": "AGENTS.md"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_2", "AGENTS "+large),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_3", Name: "grep", Args: map[string]interface{}{"pattern": "Decision ledger"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_3", "grep "+large),
	}

	got, compacted := CompactActiveTurnReplay(messages, 2048)
	if !compacted {
		t.Fatalf("expected compaction, got %#v", got)
	}
	if len(got) != 5 {
		t.Fatalf("expected user + preserved anchor + summary + latest assistant + latest tool, got %#v", got)
	}
	if got[1].Metadata.GetBool("active_turn_compaction", false) != true {
		t.Fatalf("expected earlier compaction summary to be preserved, got %#v", got[1].Metadata)
	}
	if got[1].Content != anchor.Content {
		t.Fatalf("expected earlier compaction summary content to be preserved, got %#v", got[1].Content)
	}
	if got[2].Metadata.GetBool("active_turn_compaction", false) != true {
		t.Fatalf("expected new compacted summary at index 2, got %#v", got[2].Metadata)
	}
	if strings.Contains(got[2].Content, "prior context anchor") {
		t.Fatalf("did not expect preserved summary text to be re-summarized, got %q", got[2].Content)
	}
	if !strings.Contains(got[2].Content, "assistant requested tools: view") {
		t.Fatalf("expected new summary to cover later replay, got %q", got[2].Content)
	}
	if got[3].Role != "assistant" || got[3].ToolCalls[0].ID != "call_3" {
		t.Fatalf("expected latest assistant tool call to be preserved, got %#v", got[3])
	}
	if got[4].Role != "tool" || got[4].ToolCallID != "call_3" {
		t.Fatalf("expected latest tool result to be preserved, got %#v", got[4])
	}
}
