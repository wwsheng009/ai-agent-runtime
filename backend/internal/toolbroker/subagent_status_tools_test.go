package toolbroker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P2 C3-1（改动 #7）的模型入口测试：subagent_status / subagent_inspect_task。
//
// 两个名字与 supervision_descendants / read_agent_result 共用同一数据面，
// 这里只 pin 新增的三件事：账本 rollup（pending/terminal/terminal_delta）、
// 空账本 finalize（AC-P2-1c）与"观测失败不升级为工具失败"（AC-P2-1d）。

func supervisionStatusRow(id string, state supervision.SupervisionState, changeSeq int64) supervision.SnapshotItem {
	return supervision.SnapshotItem{
		Kind:             supervision.SubjectAgentSession,
		ID:               id,
		SupervisionState: state,
		LastChangeSeq:    changeSeq,
	}
}

// subagentPayloadOf unwraps the Execute payload: both new tools answer with a
// structured map, and a wrong shape must fail loudly instead of silently
// passing every downstream Contains check.
func subagentPayloadOf(t *testing.T, raw interface{}) map[string]interface{} {
	t.Helper()
	payload, ok := raw.(map[string]interface{})
	require.True(t, ok, "the payload must be a structured map, got %#v", raw)
	return payload
}

// failingDescendantsController fails only the matrix read: the deep look must
// still return the durable result instead of turning into a tool failure.
type failingDescendantsController struct {
	*fakeSupervisionController
	err error
}

func (f *failingDescendantsController) SupervisionDescendants(ctx context.Context, parentSessionID string, args SupervisionDescendantsArgs) (*supervision.Snapshot, error) {
	return nil, f.err
}

// countingDescendantsController counts matrix reads so a test can prove that
// include_status=false never pays for the extra read.
type countingDescendantsController struct {
	*fakeSupervisionController
	descendantCalls int
}

func (c *countingDescendantsController) SupervisionDescendants(ctx context.Context, parentSessionID string, args SupervisionDescendantsArgs) (*supervision.Snapshot, error) {
	c.descendantCalls++
	return c.fakeSupervisionController.SupervisionDescendants(ctx, parentSessionID, args)
}

func TestBroker_Execute_SubagentStatusLedgerRollup(t *testing.T) {
	fake := &fakeSupervisionController{snapshot: &supervision.Snapshot{
		Descendants: []supervision.SnapshotItem{
			supervisionStatusRow("child-running", supervision.SupervisionRunning, 5),
			supervisionStatusRow("child-done", supervision.SupervisionTerminated, 12),
			supervisionStatusRow("child-old", supervision.SupervisionTerminated, 3),
		},
		Summary: supervision.SnapshotSummary{Running: 1, TerminalUnacknowledged: 1},
		NextSeq: 12,
	}}
	broker := &Broker{Supervision: fake}

	raw, metadata, err := broker.Execute(context.Background(), "parent-1", ToolSubagentStatus, map[string]interface{}{
		"after_seq": float64(10),
	})
	require.NoError(t, err)
	payload := subagentPayloadOf(t, raw)
	require.Equal(t, "parent-1", fake.parentID, "the scope comes from the caller session, never from the model")
	require.Equal(t, int64(10), fake.descendantsReq.AfterSeq, "after_seq must reach the shared data plane unchanged")
	require.Equal(t, 1, payload["pending_count"])
	require.Equal(t, 2, payload["terminal_count"])
	require.Equal(t, []string{"child-done"}, payload["terminal_delta"],
		"only rows that reached terminal after the cursor are a delta")
	require.Equal(t, int64(12), payload["next_seq"])
	require.Equal(t, "report the finished rows to the user, then subagent_ack_lifecycle or close_agent them to converge the lifecycle",
		payload["next_action"], "an unacknowledged finished row must keep the shared matrix guidance")
	summary, ok := metadata[cacheSafeSummaryMetadataKey].(string)
	require.True(t, ok, "the ledger rollup must survive into the cache-safe summary")
	require.Contains(t, summary, "1 pending, 2 terminal")
	require.Contains(t, summary, "finished since cursor: child-done")
}

func TestBroker_Execute_SubagentStatusEmptyLedgerFinalizes(t *testing.T) {
	// AC-P2-1c: an empty ledger returns immediately with next_action=finalize
	// instead of leaving the parent to wait on work that does not exist.
	broker := &Broker{Supervision: &fakeSupervisionController{snapshot: &supervision.Snapshot{}}}

	raw, _, err := broker.Execute(context.Background(), "parent-1", ToolSubagentStatus, map[string]interface{}{})
	require.NoError(t, err)
	payload := subagentPayloadOf(t, raw)
	require.Equal(t, 0, payload["pending_count"])
	require.Equal(t, 0, payload["terminal_count"])
	require.Equal(t, "finalize", payload["next_action"])
}

func TestBroker_Execute_SubagentStatusWithholdsFinalizeWhilePending(t *testing.T) {
	// The conservative direction of the fail-open rule: while any row may still
	// be in flight - including a stalled row that no summary counter reports as
	// running - the tool must never claim the parent may finalize.
	broker := &Broker{Supervision: &fakeSupervisionController{snapshot: &supervision.Snapshot{
		Descendants: []supervision.SnapshotItem{supervisionStatusRow("child-stuck", supervision.SupervisionStalled, 7)},
		Summary:     supervision.SnapshotSummary{Stalled: 1},
	}}}

	raw, _, err := broker.Execute(context.Background(), "parent-1", ToolSubagentStatus, map[string]interface{}{})
	require.NoError(t, err)
	payload := subagentPayloadOf(t, raw)
	require.Equal(t, 1, payload["pending_count"])
	require.NotEqual(t, "finalize", payload["next_action"],
		"a stalled row is not finalizable even though the shared helper has no counter for it")

	running := &Broker{Supervision: &fakeSupervisionController{snapshot: &supervision.Snapshot{
		Descendants: []supervision.SnapshotItem{supervisionStatusRow("child-running", supervision.SupervisionRunning, 2)},
		Summary:     supervision.SnapshotSummary{Running: 1},
	}}}
	raw, _, err = running.Execute(context.Background(), "parent-1", ToolSubagentStatus, map[string]interface{}{})
	require.NoError(t, err)
	payload = subagentPayloadOf(t, raw)
	require.Contains(t, payload["next_action"], "still running")
}

func TestBroker_Execute_SubagentStatusRejectsUnknownVocabulary(t *testing.T) {
	broker := &Broker{Supervision: &fakeSupervisionController{}}

	_, _, err := broker.Execute(context.Background(), "parent-1", ToolSubagentStatus, map[string]interface{}{
		"mode": "siblings",
	})
	require.Error(t, err, "a typo must not widen the read to the whole scope")
	require.Contains(t, err.Error(), "children or descendants")
}

func TestBroker_Execute_SubagentInspectTaskComposesStatusAndResult(t *testing.T) {
	fake := &fakeSupervisionController{
		result: supervision.ReadResultPayload{
			Source:  supervision.ResultSourceTaskResult,
			Status:  "completed",
			Summary: "deliverable body",
		},
		snapshot: &supervision.Snapshot{
			Descendants: []supervision.SnapshotItem{
				supervisionStatusRow("other-child", supervision.SupervisionRunning, 4),
				supervisionStatusRow("child-1", supervision.SupervisionTerminated, 9),
			},
		},
	}
	broker := &Broker{Supervision: fake}

	raw, metadata, err := broker.Execute(context.Background(), "parent-1", ToolSubagentInspectTask, map[string]interface{}{
		"id": "child-1",
	})
	require.NoError(t, err)
	payload := subagentPayloadOf(t, raw)
	require.Equal(t, "child-1", fake.resultReq.SessionID, "the deep look must address the requested subject")
	require.Equal(t, "matrix", payload["status_source"])
	subject, ok := payload["subject"].(supervision.SnapshotItem)
	require.True(t, ok, "the subject row must be attached as a typed snapshot row")
	require.Equal(t, "child-1", subject.ID, "the row must be the subject, not the first sibling")
	require.Equal(t, supervision.ResultSourceTaskResult, payload["result"].(supervision.ReadResultPayload).Source)
	summary, ok := metadata[cacheSafeSummaryMetadataKey].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(summary, "inspect task:"), "got summary %q", summary)
	require.NotContains(t, summary, "matrix_unavailable", "a served matrix row needs no degradation note")
}

func TestBroker_Execute_SubagentInspectTaskDegradesWhenMatrixFails(t *testing.T) {
	// AC-P2-1d: an observation failure is reported as an observation, not as a
	// tool failure - the durable result is already in hand.
	fake := &fakeSupervisionController{result: supervision.ReadResultPayload{
		Source:     supervision.ResultSourceTaskResult,
		Status:     "completed",
		NextAction: "converge the finished row",
	}}
	broker := &Broker{Supervision: &failingDescendantsController{
		fakeSupervisionController: fake,
		err:                       errors.New("matrix store unavailable"),
	}}

	raw, metadata, err := broker.Execute(context.Background(), "parent-1", ToolSubagentInspectTask, map[string]interface{}{
		"id": "child-1",
	})
	require.NoError(t, err, "a failed status read must not fail the deep look")
	payload := subagentPayloadOf(t, raw)
	require.Equal(t, "matrix_unavailable", payload["status_source"])
	require.Contains(t, payload["status_error"], "matrix store unavailable")
	require.NotContains(t, payload, "subject")
	require.Equal(t, supervision.ResultSourceTaskResult, payload["result"].(supervision.ReadResultPayload).Source)
	require.Equal(t, "converge the finished row", payload["next_action"])
	summary, ok := metadata[cacheSafeSummaryMetadataKey].(string)
	require.True(t, ok)
	require.Contains(t, summary, "subject state matrix_unavailable")
}

func TestBroker_Execute_SubagentInspectTaskHandleAliasesAndStatusOptOut(t *testing.T) {
	fake := &fakeSupervisionController{}
	counting := &countingDescendantsController{fakeSupervisionController: fake}
	broker := &Broker{Supervision: counting}

	raw, _, err := broker.Execute(context.Background(), "parent-1", ToolSubagentInspectTask, map[string]interface{}{
		"session_id":     "child-1",
		"include_status": false,
	})
	require.NoError(t, err)
	payload := subagentPayloadOf(t, raw)
	require.Equal(t, "child-1", fake.resultReq.SessionID,
		"the sibling tools' handle alias must resolve to the same subject")
	require.NotContains(t, payload, "subject")
	require.Equal(t, 0, counting.descendantCalls,
		"include_status=false must not pay for the matrix read")

	_, _, err = broker.Execute(context.Background(), "parent-1", ToolSubagentInspectTask, map[string]interface{}{})
	require.Error(t, err, "a deep look without a subject must be rejected, not widened")
	require.Contains(t, err.Error(), "id is required")

	_, _, err = broker.Execute(context.Background(), "parent-1", ToolSubagentInspectTask, map[string]interface{}{
		"id":       "child-1",
		"sections": []interface{}{"bogus"},
	})
	require.Error(t, err, "an unknown section must be rejected instead of widening the read")
}

func TestSubagentStatusToolsAreRegisteredAndNormalized(t *testing.T) {
	without := toolDefinitionNames((&Broker{}).Definitions())
	require.NotContains(t, without, ToolSubagentStatus,
		"a host without durable supervision must not advertise the tool")
	require.NotContains(t, without, ToolSubagentInspectTask)

	broker := &Broker{Supervision: &fakeSupervisionController{}}
	defs := broker.Definitions()

	status := supervisionDefinition(t, defs, ToolSubagentStatus)
	require.Equal(t, false, status.Metadata[types.ToolMetadataEmptyReplayCacheKey],
		"an empty ledger is a valid answer, so it must not be cached as a negative replay")
	require.Contains(t, status.Description, "pending_count")
	require.Contains(t, status.Description, "terminal_delta")
	require.Contains(t, status.Description, "finalize")

	inspect := supervisionDefinition(t, defs, ToolSubagentInspectTask)
	require.Equal(t, false, inspect.Metadata[types.ToolMetadataEmptyReplayCacheKey],
		"a missing durable record is an observation, so it must not be cached as a negative replay")
	require.Contains(t, inspect.Description, "max_chars")
	require.Contains(t, inspect.Description, "matrix_unavailable")

	// A model that drops the underscore still reaches the tool.
	require.Equal(t, ToolSubagentStatus, normalizeToolName("subagentstatus"))
	require.Equal(t, ToolSubagentStatus, normalizeToolName("agent_status"))
	require.Equal(t, ToolSubagentInspectTask, normalizeToolName("inspecttask"))
	require.Equal(t, ToolSubagentInspectTask, normalizeToolName("subagent_inspect"))
	require.Equal(t, ToolSubagentStatus, normalizeToolName("subagent-status"))
	require.True(t, broker.IsBrokerTool("subagent_status"))
	require.True(t, broker.IsBrokerTool("subagent_inspect_task"))
}
