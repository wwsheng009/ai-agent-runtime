package chat

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestClonePendingToolInvocationCanExcludePersistedResult(t *testing.T) {
	pending := &PendingToolInvocation{
		ToolCallID:        "call-1",
		ToolName:          "shell",
		ArgsJSON:          []byte(`{"command":"build"}`),
		ResultMessageJSON: bytes.Repeat([]byte("result"), 128<<10),
		BatchToolCalls: []PendingToolCall{{
			ToolCallID: "call-2",
			ToolName:   "read",
			ArgsJSON:   []byte(`{"path":"next"}`),
		}},
	}

	recovery := clonePendingToolInvocation(pending, false)
	if recovery == nil {
		t.Fatal("expected recovery clone")
	}
	if len(recovery.ResultMessageJSON) != 0 {
		t.Fatalf("recovery clone retained %d result bytes", len(recovery.ResultMessageJSON))
	}
	if string(recovery.ArgsJSON) != string(pending.ArgsJSON) || string(recovery.BatchToolCalls[0].ArgsJSON) != string(pending.BatchToolCalls[0].ArgsJSON) {
		t.Fatal("recovery clone lost tool arguments")
	}
	recovery.ArgsJSON[0] = 'X'
	recovery.BatchToolCalls[0].ArgsJSON[0] = 'Y'
	if pending.ArgsJSON[0] == 'X' || pending.BatchToolCalls[0].ArgsJSON[0] == 'Y' {
		t.Fatal("recovery clone shares argument buffers")
	}

	stateClone := (&RuntimeState{SessionID: "session-1", PendingTool: pending}).Clone()
	if stateClone == nil || stateClone.PendingTool == nil {
		t.Fatal("expected runtime state clone")
	}
	if !bytes.Equal(stateClone.PendingTool.ResultMessageJSON, pending.ResultMessageJSON) {
		t.Fatal("runtime state clone must preserve recovery result")
	}
	stateClone.PendingTool.ResultMessageJSON[0] = 'Z'
	if pending.ResultMessageJSON[0] == 'Z' {
		t.Fatal("runtime state clone shares result buffer")
	}
}

func TestRuntimeStateInspectionCloneOmitsLargePersistedPayloads(t *testing.T) {
	state := &RuntimeState{
		SessionID: "session-inspection",
		Status:    SessionWaitingApproval,
		StableToolSurface: []types.ToolDefinition{{
			Name:        "large_tool",
			Description: string(bytes.Repeat([]byte("d"), 256<<10)),
		}},
		StableToolSurfaceSet: true,
		FrozenTurnTools:      []types.ToolDefinition{{Name: "large_tool"}},
		FrozenTurnToolsSet:   true,
		PendingTool: &PendingToolInvocation{
			ToolCallID:        "call-large",
			ToolName:          "large_tool",
			ArgsJSON:          []byte(`{"path":"input"}`),
			ResultMessageJSON: bytes.Repeat([]byte("r"), 512<<10),
		},
		PendingApproval: &ApprovalRequest{ID: "approval-large"},
	}

	inspection := state.CloneForInspection()
	if inspection == nil || inspection.PendingTool == nil {
		t.Fatal("expected inspection state with pending tool metadata")
	}
	if inspection.StableToolSurfaceSet || inspection.FrozenTurnToolsSet || len(inspection.StableToolSurface) != 0 || len(inspection.FrozenTurnTools) != 0 {
		t.Fatal("inspection clone retained tool surfaces")
	}
	if len(inspection.PendingTool.ResultMessageJSON) != 0 {
		t.Fatalf("inspection clone retained %d result bytes", len(inspection.PendingTool.ResultMessageJSON))
	}
	if string(inspection.PendingTool.ArgsJSON) != string(state.PendingTool.ArgsJSON) {
		t.Fatal("inspection clone lost pending tool arguments")
	}
	inspection.PendingTool.ArgsJSON[0] = 'X'
	if state.PendingTool.ArgsJSON[0] == 'X' {
		t.Fatal("inspection clone shares pending tool arguments")
	}

	summary := state.Summary()
	if !summary.Busy() || !summary.PendingTool || !summary.PendingApproval {
		t.Fatal("summary lost runtime status flags")
	}
	if summary.PendingToolCallID != "call-large" || summary.PendingToolName != "large_tool" {
		t.Fatal("summary lost pending tool identity")
	}
}

func TestSessionActorStateSummaryAvoidsLargeStateCloneAllocations(t *testing.T) {
	actor := &SessionActor{state: &RuntimeState{
		SessionID: "session-summary",
		Status:    SessionWaitingApproval,
		StableToolSurface: []types.ToolDefinition{{
			Name: "large_tool",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"payload": map[string]interface{}{"type": "string"},
				},
			},
		}},
		StableToolSurfaceSet: true,
		PendingTool: &PendingToolInvocation{
			ToolCallID:        "call-summary",
			ToolName:          "large_tool",
			ResultMessageJSON: bytes.Repeat([]byte("r"), 512<<10),
		},
		PendingApproval: &ApprovalRequest{ID: "approval-summary"},
	}}

	var summary RuntimeStateSummary
	summaryAllocs := testing.AllocsPerRun(20, func() {
		summary, _ = actor.StateSummary()
	})
	var full *RuntimeState
	fullAllocs := testing.AllocsPerRun(20, func() {
		full = actor.State()
	})
	if summary.PendingToolCallID != "call-summary" || full == nil {
		t.Fatal("state snapshots were not produced")
	}
	if summaryAllocs != 0 {
		t.Fatalf("state summary allocated %.1f objects per poll", summaryAllocs)
	}
	if fullAllocs <= summaryAllocs+5 {
		t.Fatalf("full state clone allocations %.1f did not exceed summary %.1f", fullAllocs, summaryAllocs)
	}
}

// C4-3 / AC-P3-3c：挂起 turn（§6.7 `awaiting_obligations`）与 Busy() 语义一致 ——
// 托管 turn 在账本终态之前只是"没有在途 run"，不是空闲；同时同一 turn 的 resume
// episode（steer / wake resume / 巡检投递）必须仍然放行。
func TestRuntimeStateSummaryBusyCoversParkedTurn(t *testing.T) {
	parked := RuntimeStateSummary{SessionID: "session-parked", Status: SessionIdle, SuspendedTurnID: "turn_parked"}
	if !parked.AwaitingObligations() {
		t.Fatal("a suspended turn id must read as awaiting obligations")
	}
	if !parked.Busy() {
		t.Fatal("a parked managed turn must not read as idle")
	}
	if !parked.AcceptsResume() {
		t.Fatal("the resume episode of the same turn must still be accepted")
	}

	// 纯空闲：没有挂起 turn 时语义不变。
	idle := RuntimeStateSummary{SessionID: "session-idle", Status: SessionIdle}
	if idle.AwaitingObligations() || idle.Busy() || !idle.AcceptsResume() {
		t.Fatalf("an idle session without obligations must stay idle and resumable, got %+v", idle)
	}

	// 执行中：busy，且不接受任何投递（同一 turn 的 resume 也要排队）。
	running := RuntimeStateSummary{SessionID: "session-running", Status: SessionRunning, CurrentTurnID: "turn_live"}
	if !running.Busy() || running.AcceptsResume() {
		t.Fatalf("an executing turn must be busy and closed to delivery, got %+v", running)
	}

	// 重启现场：状态经 durable 存储往返（JSON）后，挂起标记与 busy 语义都必须还原。
	raw, err := json.Marshal(&RuntimeState{
		SessionID:       "session-restart",
		Status:          SessionIdle,
		SuspendedTurnID: "turn_parked",
	})
	if err != nil {
		t.Fatalf("marshal runtime state: %v", err)
	}
	var decoded RuntimeState
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal runtime state: %v", err)
	}
	summary := decoded.Summary()
	if summary.SuspendedTurnID != "turn_parked" || !summary.AwaitingObligations() || !summary.Busy() {
		t.Fatalf("restart read-back lost the parked turn: %+v", summary)
	}
	if !summary.AcceptsResume() {
		t.Fatalf("restart read-back must keep the parked turn resumable: %+v", summary)
	}
}
