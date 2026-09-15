package dedupe

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testBase is the reference timestamp of the reproduced defect window
// (2026-09-15T09:24Z), written in the same encoding the runtime uses.
var testBase = time.Date(2026, 9, 15, 9, 24, 0, 0, time.UTC)

func testStamp(offset time.Duration) string {
	return testBase.Add(offset).Format(time.RFC3339Nano)
}

func testPayload(t *testing.T, role, content string, toolCalls ...ToolCall) []byte {
	t.Helper()
	body := map[string]interface{}{"role": role, "content": content}
	if len(toolCalls) > 0 {
		calls := make([]map[string]interface{}, 0, len(toolCalls))
		for _, call := range toolCalls {
			calls = append(calls, map[string]interface{}{"id": call.ID, "name": call.Name})
		}
		body["tool_calls"] = calls
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return encoded
}

// TestBuildCollapsesThreeIdenticalRows is the primary defect shape: the same
// logical message appended three times in a row keeps exactly one row.
func TestBuildCollapsesThreeIdenticalRows(t *testing.T) {
	payload := testPayload(t, "assistant", "same answer")
	rows := []Row{
		{Seq: 10, CreatedAt: testStamp(0), Payload: payload},
		{Seq: 11, CreatedAt: testStamp(time.Second), Payload: payload},
		{Seq: 12, CreatedAt: testStamp(2 * time.Second), Payload: payload},
	}

	plan := Build(rows, time.Hour)

	require.Equal(t, 3, plan.Scanned)
	require.Equal(t, 0, plan.Skipped)
	require.Len(t, plan.Groups, 1)
	require.Equal(t, int64(10), plan.Groups[0].KeepSeq)
	require.Equal(t, []int64{11, 12}, plan.Groups[0].DropSeqs)
	require.Equal(t, []int64{11, 12}, plan.SurplusSeqs)
	require.Equal(t, 2, plan.SurplusCount())
}

// TestBuildKeepsSameSubstanceOutsideWindow: identical substance separated by
// more than the window is a legitimate repetition and must survive.
func TestBuildKeepsSameSubstanceOutsideWindow(t *testing.T) {
	payload := testPayload(t, "assistant", "repeated prompt")
	window := 10 * time.Second
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: payload},
		{Seq: 2, CreatedAt: testStamp(5 * time.Second), Payload: payload},
		{Seq: 3, CreatedAt: testStamp(time.Hour), Payload: payload},
	}

	plan := Build(rows, window)

	require.Equal(t, []int64{2}, plan.SurplusSeqs)
	require.Len(t, plan.Groups, 2)
	require.Equal(t, int64(1), plan.Groups[0].KeepSeq)
	require.Equal(t, []int64{2}, plan.Groups[0].DropSeqs)
	require.Equal(t, int64(3), plan.Groups[1].KeepSeq)
	require.Empty(t, plan.Groups[1].DropSeqs)
}

// TestBuildKeepsRowsWithDifferentToolCalls: a different tool-call signature is a
// different message even when role and content match.
func TestBuildKeepsRowsWithDifferentToolCalls(t *testing.T) {
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: testPayload(t, "assistant", "", ToolCall{ID: "call_a", Name: "shell"})},
		{Seq: 2, CreatedAt: testStamp(time.Second), Payload: testPayload(t, "assistant", "", ToolCall{ID: "call_b", Name: "shell"})},
		{Seq: 3, CreatedAt: testStamp(2 * time.Second), Payload: testPayload(t, "assistant", "", ToolCall{ID: "call_a", Name: "view"})},
		{Seq: 4, CreatedAt: testStamp(3 * time.Second), Payload: testPayload(t, "assistant", "")},
	}

	plan := Build(rows, time.Hour)

	require.Empty(t, plan.SurplusSeqs)
	require.Equal(t, 0, plan.SurplusCount())
	require.Len(t, plan.Groups, 4)
}

// TestBuildIgnoresContextStageAndMessageIdentity: metadata (message_id,
// turn_id, context_stage) is not part of the substance rule, so re-issued ids
// with a different stage still collapse.
func TestBuildIgnoresContextStageAndMessageIdentity(t *testing.T) {
	first := []byte(`{"role":"assistant","content":"final text","metadata":{"message_id":"msg-1","turn_id":"turn-1","context_stage":"tool_use"}}`)
	second := []byte(`{"role":"assistant","content":"final text","metadata":{"message_id":"msg-2","turn_id":"turn-1","context_stage":"final"}}`)
	rows := []Row{
		{Seq: 20, CreatedAt: testStamp(0), Payload: first},
		{Seq: 21, CreatedAt: testStamp(30 * time.Second), Payload: second},
	}

	plan := Build(rows, time.Hour)

	require.Equal(t, 0, plan.Skipped)
	require.Equal(t, []int64{21}, plan.SurplusSeqs)
	require.Len(t, plan.Groups, 1)
	require.Equal(t, int64(20), plan.Groups[0].KeepSeq)
}

// TestBuildMeasuresWindowFromRetainedRow pins the "gap to the last retained
// row" rule: the third row is compared with the kept row, not with the folded
// one, so it is no longer inside the window.
func TestBuildMeasuresWindowFromRetainedRow(t *testing.T) {
	payload := testPayload(t, "assistant", "streamed")
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: payload},
		{Seq: 2, CreatedAt: testStamp(50 * time.Minute), Payload: payload},
		{Seq: 3, CreatedAt: testStamp(100 * time.Minute), Payload: payload},
	}

	plan := Build(rows, time.Hour)

	require.Equal(t, []int64{2}, plan.SurplusSeqs)
	require.Len(t, plan.Groups, 2)
	require.Equal(t, int64(1), plan.Groups[0].KeepSeq)
	require.Equal(t, int64(3), plan.Groups[1].KeepSeq)
}

// TestBuildSeparatesNonContiguousGroups: only consecutive duplicates collapse;
// an intervening different message ends the group.
func TestBuildSeparatesNonContiguousGroups(t *testing.T) {
	repeated := testPayload(t, "user", "ping")
	other := testPayload(t, "assistant", "pong")
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: repeated},
		{Seq: 2, CreatedAt: testStamp(time.Second), Payload: repeated},
		{Seq: 3, CreatedAt: testStamp(2 * time.Second), Payload: other},
		{Seq: 4, CreatedAt: testStamp(3 * time.Second), Payload: repeated},
		{Seq: 5, CreatedAt: testStamp(4 * time.Second), Payload: repeated},
	}

	plan := Build(rows, time.Hour)

	require.Equal(t, []int64{2, 5}, plan.SurplusSeqs)
	require.Len(t, plan.Groups, 3)
	require.Equal(t, []int64{2}, plan.Groups[0].DropSeqs)
	require.Empty(t, plan.Groups[1].DropSeqs)
	require.Equal(t, []int64{5}, plan.Groups[2].DropSeqs)
}

// TestBuildKeepsUnreadableRowsAndBreaksChain: an undecodable payload is never
// deleted and does not let its neighbours fold across it.
func TestBuildKeepsUnreadableRowsAndBreaksChain(t *testing.T) {
	payload := testPayload(t, "assistant", "same")
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: payload},
		{Seq: 2, CreatedAt: testStamp(time.Second), Payload: []byte("{not json")},
		{Seq: 3, CreatedAt: testStamp(2 * time.Second), Payload: payload},
		{Seq: 4, CreatedAt: testStamp(3 * time.Second)},
		{Seq: 5, CreatedAt: testStamp(4 * time.Second), Payload: payload},
	}

	plan := Build(rows, time.Hour)

	require.Equal(t, 5, plan.Scanned)
	require.Equal(t, 2, plan.Skipped)
	require.Empty(t, plan.SurplusSeqs)
	require.Len(t, plan.Groups, 3)
	require.Equal(t, int64(1), plan.Groups[0].KeepSeq)
	require.Equal(t, int64(3), plan.Groups[1].KeepSeq)
	require.Equal(t, int64(5), plan.Groups[2].KeepSeq)
}

// TestBuildSkipsUnparsableTimestamps: a row without a usable created_at cannot
// be time-windowed, so it is kept instead of being folded.
func TestBuildSkipsUnparsableTimestamps(t *testing.T) {
	payload := testPayload(t, "assistant", "same")
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: payload},
		{Seq: 2, CreatedAt: "not-a-timestamp", Payload: payload},
	}

	plan := Build(rows, time.Hour)

	require.Empty(t, plan.SurplusSeqs)
	require.Equal(t, 1, plan.Skipped)
	require.Len(t, plan.Groups, 1)
}

// TestBuildWindowBoundary: the window is inclusive, and one nanosecond beyond it
// keeps the row.
func TestBuildWindowBoundary(t *testing.T) {
	payload := testPayload(t, "assistant", "boundary")
	window := 10 * time.Second
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: payload},
		{Seq: 2, CreatedAt: testStamp(window), Payload: payload},
		{Seq: 3, CreatedAt: testStamp(2*window + time.Nanosecond), Payload: payload},
	}

	plan := Build(rows, window)

	require.Equal(t, []int64{2}, plan.SurplusSeqs)
}

// TestBuildIgnoresToolCallArgs documents the inherited rule: the reference
// signature only uses call id/name, so different arguments alone do not split a
// group. This is a known limitation, not an accident.
func TestBuildIgnoresToolCallArgs(t *testing.T) {
	first := []byte(`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","name":"shell","arguments":{"command":"ls"}}]}`)
	second := []byte(`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","name":"shell","arguments":{"command":"dir"}}]}`)
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: first},
		{Seq: 2, CreatedAt: testStamp(time.Second), Payload: second},
	}

	plan := Build(rows, time.Hour)

	require.Equal(t, []int64{2}, plan.SurplusSeqs)
}

// TestSubstanceKeyMatchesRuntimeRule pins the byte-level key layout copied from
// chat.canonicalMessageSubstanceKey / chat.toolCallSignature.
func TestSubstanceKeyMatchesRuntimeRule(t *testing.T) {
	key := SubstanceKey(Message{
		Role:      "  Assistant ",
		Content:   "line\x00with nul",
		ToolCalls: []ToolCall{{ID: " call_1 ", Name: " shell "}, {ID: "call_2", Name: "view"}},
	})

	require.Equal(t, "assistant\x00line\x00with nul\x00call_1\x1fshell\ncall_2\x1fview\n", key)
	require.Equal(t, "assistant\x00text\x00", SubstanceKey(Message{Role: "ASSISTANT", Content: "text"}))
	require.Equal(t, "", ToolCallSignature(nil))
}

// TestBuildEmptyInput: an empty scan plans nothing.
func TestBuildEmptyInput(t *testing.T) {
	plan := Build(nil, time.Hour)

	require.Equal(t, 0, plan.Scanned)
	require.Empty(t, plan.SurplusSeqs)
	require.Empty(t, plan.Groups)
	require.Equal(t, 0, plan.SurplusCount())
}

// TestBuildNeverPlansTheSameSeqTwice guards the delete path: every seq appears
// at most once in the surplus list.
func TestBuildNeverPlansTheSameSeqTwice(t *testing.T) {
	payload := testPayload(t, "assistant", "dup")
	rows := make([]Row, 0, 5)
	for index := 0; index < 5; index++ {
		rows = append(rows, Row{
			Seq:       int64(100 + index),
			CreatedAt: testStamp(time.Duration(index) * time.Second),
			Payload:   payload,
		})
	}

	plan := Build(rows, time.Hour)

	seen := map[int64]bool{}
	for _, seq := range plan.SurplusSeqs {
		require.False(t, seen[seq], "seq %d planned twice", seq)
		seen[seq] = true
	}
	require.Equal(t, []int64{101, 102, 103, 104}, plan.SurplusSeqs)
}

// testPayloadWithTurn adds the write identity to a payload. Rows of the
// reproduced defect share one turn id; a prompt the user sent twice carries two.
func testPayloadWithTurn(t *testing.T, role, content, turnID string) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]interface{}{
		"role":     role,
		"content":  content,
		"metadata": map[string]interface{}{"turn_id": turnID},
	})
	require.NoError(t, err)
	return encoded
}

// TestBuildWithRuleFoldsReAppendInsideOneTurn is the shape the repair targets:
// one turn persisted the same message twice, seconds apart.
func TestBuildWithRuleFoldsReAppendInsideOneTurn(t *testing.T) {
	payload := testPayloadWithTurn(t, "user", "在 streaming output 过程中", "turn_a")
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: payload},
		{Seq: 2, CreatedAt: testStamp(100 * time.Millisecond), Payload: payload},
	}

	plan := BuildWithRule(rows, Rule{Window: time.Hour})

	require.Equal(t, []int64{2}, plan.SurplusSeqs)
	require.Len(t, plan.Groups, 1)
	require.Equal(t, int64(1), plan.Groups[0].KeepSeq)
}

// TestBuildWithRuleKeepsSameSubstanceFromDifferentTurns is why the rule exists:
// "继续" sent again six minutes later is a real user action, not a duplicate,
// so the default rule must leave both rows alone.
func TestBuildWithRuleKeepsSameSubstanceFromDifferentTurns(t *testing.T) {
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: testPayloadWithTurn(t, "user", "继续", "turn_a")},
		{Seq: 2, CreatedAt: testStamp(6 * time.Minute), Payload: testPayloadWithTurn(t, "user", "继续", "turn_b")},
	}

	require.Empty(t, BuildWithRule(rows, Rule{Window: time.Hour}).SurplusSeqs)
	require.Empty(t, BuildWithRule(rows, Rule{Window: 2 * time.Minute}).SurplusSeqs)
	require.Equal(t, []int64{2}, BuildWithRule(rows, Rule{Window: time.Hour, AnyTurn: true}).SurplusSeqs)
}

// TestBuildWithRuleKeepsRowsWithoutTurnIdentity: a row that cannot be attributed
// to a turn is never deleted, so the tool stays conservative on legacy data.
func TestBuildWithRuleKeepsRowsWithoutTurnIdentity(t *testing.T) {
	payload := testPayload(t, "assistant", "no identity")
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: payload},
		{Seq: 2, CreatedAt: testStamp(time.Second), Payload: payload},
	}

	require.Empty(t, BuildWithRule(rows, Rule{Window: time.Hour}).SurplusSeqs)
	require.Equal(t, []int64{2}, BuildWithRule(rows, Rule{Window: time.Hour, AnyTurn: true}).SurplusSeqs)
}

// TestBuildWithRuleStillHonoursTheWindow: rows of one turn that are further
// apart than the window are kept, so a turn that legitimately restates a
// message much later is not folded.
func TestBuildWithRuleStillHonoursTheWindow(t *testing.T) {
	payload := testPayloadWithTurn(t, "assistant", "same text", "turn_a")
	rows := []Row{
		{Seq: 1, CreatedAt: testStamp(0), Payload: payload},
		{Seq: 2, CreatedAt: testStamp(2 * time.Hour), Payload: payload},
	}

	require.Empty(t, BuildWithRule(rows, Rule{Window: time.Hour}).SurplusSeqs)
}
