package toolbroker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P2-12 方案 3 的模型入口测试：宿主能力门控、参数解析、payload 与审计字段。

type fakeSupervisionController struct {
	digest       *supervision.Digest
	snapshot     *supervision.Snapshot
	notification *supervision.Notification
	record       supervision.ActionRecord
	err          error

	parentID       string
	snapshotReq    SupervisionSnapshotArgs
	descendantsReq SupervisionDescendantsArgs
	ackReq         AckLifecycleArgs
	controlReq     ControlDescendantArgs
	calls          int
}

func (f *fakeSupervisionController) SupervisionSnapshot(ctx context.Context, parentSessionID string, args SupervisionSnapshotArgs) (*supervision.Digest, error) {
	f.calls++
	f.parentID = parentSessionID
	f.snapshotReq = args
	return f.digest, f.err
}

func (f *fakeSupervisionController) SupervisionDescendants(ctx context.Context, parentSessionID string, args SupervisionDescendantsArgs) (*supervision.Snapshot, error) {
	f.calls++
	f.parentID = parentSessionID
	f.descendantsReq = args
	return f.snapshot, f.err
}

func (f *fakeSupervisionController) AckLifecycle(ctx context.Context, parentSessionID string, args AckLifecycleArgs) (*supervision.Notification, error) {
	f.calls++
	f.parentID = parentSessionID
	f.ackReq = args
	return f.notification, f.err
}

func (f *fakeSupervisionController) ControlDescendant(ctx context.Context, parentSessionID string, args ControlDescendantArgs) (supervision.ActionRecord, error) {
	f.calls++
	f.parentID = parentSessionID
	f.controlReq = args
	return f.record, f.err
}

func supervisionDefinition(t *testing.T, defs []types.ToolDefinition, name string) types.ToolDefinition {
	t.Helper()
	for _, def := range defs {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("tool definition %s not found", name)
	return types.ToolDefinition{}
}

func TestBroker_Definitions_GateSupervisionToolsOnHostCapability(t *testing.T) {
	// A host without durable supervision must not advertise the tools: a
	// declared-but-unreachable tool is worse than a missing one.
	without := toolDefinitionNames((&Broker{}).Definitions())
	require.NotContains(t, without, ToolSupervisionSnapshot)
	require.NotContains(t, without, ToolSupervisionDescendants)
	require.NotContains(t, without, ToolAckLifecycle)
	require.NotContains(t, without, ToolControlDescendant)

	broker := &Broker{Supervision: &fakeSupervisionController{}}
	defs := broker.Definitions()
	names := toolDefinitionNames(defs)
	require.Contains(t, names, ToolSupervisionSnapshot)
	require.Contains(t, names, ToolSupervisionDescendants)
	require.Contains(t, names, ToolAckLifecycle)
	require.Contains(t, names, ToolControlDescendant)

	snapshot := supervisionDefinition(t, defs, ToolSupervisionSnapshot)
	require.NotNil(t, snapshot.Metadata)
	require.Equal(t, false, snapshot.Metadata[types.ToolMetadataEmptyReplayCacheKey],
		"snapshot is a polling tool: an empty result must not be cached as a negative answer")
	require.Contains(t, snapshot.Description, "use supervision_descendants",
		"the digest description must point routine matrix inspection at supervision_descendants")

	descendants := supervisionDefinition(t, defs, ToolSupervisionDescendants)
	require.Equal(t, false, descendants.Metadata[types.ToolMetadataEmptyReplayCacheKey],
		"descendants is an inspection tool: an empty matrix must not be cached as a negative answer")
	require.Contains(t, descendants.Description, "anti-polling",
		"the description must tell the model that repeated inspection is the intended primitive")
	require.Contains(t, descendants.Description, "do not poll wait_agent",
		"the description must steer routine supervision away from repeated wait_agent polling")
	for _, field := range []string{"recommended_action", "allowed_actions", "next_action"} {
		require.Contains(t, descendants.Description, field,
			"the description must explain the decision field %q returned per row", field)
	}
	descendantParams, ok := descendants.Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	for _, key := range []string{"mode", "health", "include_terminal", "limit", "after_seq"} {
		require.Contains(t, descendantParams, key)
	}
	healthParam, ok := descendantParams["health"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, healthParam["description"], "action_required",
		"the health parameter must document the action_required filter")
	terminalParam, ok := descendantParams["include_terminal"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, terminalParam["description"], "default false",
		"the include_terminal parameter must document its default")
	limitParam, ok := descendantParams["limit"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, limitParam["description"], "truncated=true",
		"the limit parameter must document how truncation is reported")

	ack := supervisionDefinition(t, defs, ToolAckLifecycle)
	require.NotEqual(t, false, ack.Metadata[types.ToolMetadataEmptyReplayCacheKey])

	params, ok := ack.Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, params, "expected_version")
	require.Contains(t, params, "note")
	ackNotification, ok := params["notification_id"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, ackNotification["description"], "verbatim",
		"ack_lifecycle must require a notification_id copied from the read models")
	controlParams, ok := supervisionDefinition(t, defs, ToolControlDescendant).Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, controlParams, "cascade")
	controlNotification, ok := controlParams["notification_id"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, controlNotification["description"], "verbatim",
		"control_descendant must require a notification_id copied from the read models")
}

func TestBroker_IsBrokerTool_RecognizesSupervisionTools(t *testing.T) {
	broker := &Broker{}
	for _, name := range []string{ToolSupervisionSnapshot, ToolSupervisionDescendants, ToolAckLifecycle, ToolControlDescendant, "supervisionSnapshot", "supervisionDescendants", "ackLifecycle", "controlDescendant"} {
		require.Truef(t, broker.IsBrokerTool(name), "%s must be recognized as a broker tool", name)
	}
}

func TestBroker_Execute_SupervisionSnapshot(t *testing.T) {
	controller := &fakeSupervisionController{
		digest: &supervision.Digest{
			CriticalUnresolved: 2,
			ActionRequired:     1,
			StaleSubjects:      1,
			NextSeq:            7,
			Text:               "critical: 2",
			Items:              []supervision.DigestItem{{NotificationID: "n-1", SubjectID: "agent-1"}},
		},
	}
	broker := &Broker{Supervision: controller}

	raw, meta, err := broker.Execute(context.Background(), "parent-session", ToolSupervisionSnapshot, map[string]interface{}{
		"after_seq":        float64(5),
		"include_resolved": true,
		"limit":            float64(10),
	})
	require.NoError(t, err)
	digest, ok := raw.(*supervision.Digest)
	require.True(t, ok)
	require.Equal(t, 2, digest.CriticalUnresolved)
	require.Equal(t, "parent-session", controller.parentID, "the host derives scope from the caller session")
	require.Equal(t, int64(5), controller.snapshotReq.AfterSeq)
	require.True(t, controller.snapshotReq.IncludeResolved)
	require.Equal(t, 10, controller.snapshotReq.Limit)

	require.Equal(t, 2, meta["critical_unresolved"])
	require.Equal(t, 1, meta["action_required"])
	require.Equal(t, 1, meta["stale_subjects"])
	require.Equal(t, int64(7), meta["next_seq"])
	require.Contains(t, meta["next_action"], "ack_lifecycle")
}

// TestBroker_Execute_SupervisionDescendants pins the business 巡查 entry: the
// typed filters reach the host controller, the payload is the snapshot itself
// (so the model reads rows, not a pre-rendered string) and the summary rollup
// is cache-safe (repeat inspections must not be replayed from a negative cache).
func TestBroker_Execute_SupervisionDescendants(t *testing.T) {
	controller := &fakeSupervisionController{
		snapshot: &supervision.Snapshot{
			Summary: supervision.SnapshotSummary{Running: 2, Stalled: 1, ActionRequired: 1},
			Descendants: []supervision.SnapshotItem{
				{Kind: supervision.SubjectAgentSession, ID: "child-1", SupervisionState: supervision.SupervisionRunning},
				{Kind: supervision.SubjectAgentSession, ID: "child-2", SupervisionState: supervision.SupervisionRunning},
				{Kind: supervision.SubjectAgentSession, ID: "child-3", SupervisionState: supervision.SupervisionStalled, NotificationID: "n-3", ActionRequired: true},
			},
			Truncated: true,
			NextSeq:   9,
		},
	}
	broker := &Broker{Supervision: controller}

	raw, meta, err := broker.Execute(context.Background(), "parent-session", ToolSupervisionDescendants, map[string]interface{}{
		"mode":             "CHILDREN",
		"health":           "Action_Required",
		"include_terminal": true,
		"after_seq":        float64(4),
		"limit":            float64(50),
	})
	require.NoError(t, err)
	snapshot, ok := raw.(*supervision.Snapshot)
	require.True(t, ok)
	require.Len(t, snapshot.Descendants, 3)
	require.Equal(t, "parent-session", controller.parentID, "the host derives scope from the caller session")
	require.Equal(t, "children", controller.descendantsReq.Mode, "mode is a closed lowercase vocabulary")
	require.Equal(t, "action_required", controller.descendantsReq.Health)
	require.True(t, controller.descendantsReq.IncludeTerminal)
	require.Equal(t, int64(4), controller.descendantsReq.AfterSeq)
	require.Equal(t, 50, controller.descendantsReq.Limit)

	require.Equal(t, 3, meta["row_count"])
	require.Equal(t, 2, meta["running"])
	require.Equal(t, 1, meta["stalled"])
	require.Equal(t, 1, meta["action_required"])
	require.Equal(t, true, meta["truncated"])
	require.Equal(t, int64(9), meta["next_seq"])
	require.Contains(t, meta["next_action"], "control_descendant")

	// Unknown modes/health values are rejected before the host is called: a typo
	// must not silently widen the read to the whole scope.
	for _, bad := range []map[string]interface{}{{"mode": "everything"}, {"health": "slow"}} {
		_, _, err := broker.Execute(context.Background(), "parent-session", ToolSupervisionDescendants, bad)
		require.Error(t, err)
	}
	require.Equal(t, 1, controller.calls, "invalid filters never reach the controller")
}

func TestBroker_Execute_AckLifecycle_ValidatesAndForwards(t *testing.T) {
	controller := &fakeSupervisionController{
		notification: &supervision.Notification{
			NotificationID:   "n-1",
			SubjectKind:      supervision.SubjectAgentRun,
			SubjectID:        "agent-1",
			SupervisionState: supervision.SupervisionTimedOut,
			DecisionState:    supervision.DecisionAcknowledged,
			ResolutionState:  supervision.ResolutionUnresolved,
			Version:          3,
		},
	}
	broker := &Broker{Supervision: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolAckLifecycle, map[string]interface{}{
		"decision": "acknowledge",
		"note":     "handled",
	})
	require.Error(t, err, "notification_id is required")
	require.Equal(t, 0, controller.calls)

	_, _, err = broker.Execute(context.Background(), "parent-session", ToolAckLifecycle, map[string]interface{}{
		"notification_id": "n-1",
		"decision":        "shrug",
	})
	require.Error(t, err)
	require.Equal(t, 0, controller.calls)

	_, _, err = broker.Execute(context.Background(), "parent-session", ToolAckLifecycle, map[string]interface{}{
		"notification_id":  "n-1",
		"decision":         "defer",
		"reason":           "waiting for rerun",
		"until":            "30m",
		"expected_version": float64(2),
	})
	require.NoError(t, err, "30m must be accepted as a duration")
	require.Equal(t, "defer", controller.ackReq.Decision)
	require.Equal(t, int64(2), controller.ackReq.ExpectedVersion)
	require.True(t, controller.ackReq.HasExpectedVersion)
	require.False(t, controller.ackReq.Until.IsZero())

	raw, meta, err := broker.Execute(context.Background(), "parent-session", ToolAckLifecycle, map[string]interface{}{
		"notification_id": "n-1",
		"decision":        "acknowledge",
		"note":            "parent handled the timeout",
	})
	require.NoError(t, err)
	payload, ok := raw.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "acknowledged", payload["decision_state"])
	require.Equal(t, int64(3), payload["version"])
	require.Contains(t, meta[cacheSafeSummaryMetadataKey], "not re-injected")
}

func TestBroker_Execute_AckLifecycle_RejectsBadDeadline(t *testing.T) {
	broker := &Broker{Supervision: &fakeSupervisionController{}}
	_, _, err := broker.Execute(context.Background(), "parent-session", ToolAckLifecycle, map[string]interface{}{
		"notification_id": "n-1",
		"decision":        "defer",
		"reason":          "later",
		"until":           "sometime tomorrow",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "RFC3339")
}

func TestBroker_Execute_ControlDescendant(t *testing.T) {
	controller := &fakeSupervisionController{
		record: supervision.ActionRecord{
			ActionID:   "act-1",
			Action:     supervision.ActionCancel,
			Status:     supervision.ActionCompleted,
			TargetKind: supervision.SubjectAgentRun,
			TargetID:   "agent-1",
			Result:     "canceled",
		},
	}
	broker := &Broker{Supervision: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolControlDescendant, map[string]interface{}{
		"notification_id": "n-1",
		"action":          "explode",
		"reason":          "nope",
	})
	require.Error(t, err)
	require.Equal(t, 0, controller.calls)

	_, _, err = broker.Execute(context.Background(), "parent-session", ToolControlDescendant, map[string]interface{}{
		"notification_id": "n-1",
		"action":          "cancel",
		"reason":          "deadline exceeded",
		"cascade":         "everywhere",
	})
	require.Error(t, err)
	require.Equal(t, 0, controller.calls)

	raw, meta, err := broker.Execute(context.Background(), "parent-session", ToolControlDescendant, map[string]interface{}{
		"notification_id":  "n-1",
		"action":           "cancel_subtree",
		"reason":           "deadline exceeded",
		"cascade":          "descendants",
		"expected_version": float64(4),
	})
	require.NoError(t, err)
	require.Equal(t, "cancel_subtree", controller.controlReq.Action)
	require.Equal(t, "descendants", controller.controlReq.Cascade)
	require.Equal(t, int64(4), controller.controlReq.ExpectedVersion)
	payload, ok := raw.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "act-1", payload["action_id"])
	require.Equal(t, "completed", payload["status"])
	require.Contains(t, meta[cacheSafeSummaryMetadataKey], "supervision_snapshot")
}

func TestBroker_Execute_SupervisionWithoutHostCapability(t *testing.T) {
	broker := &Broker{}
	_, _, err := broker.Execute(context.Background(), "parent-session", ToolSupervisionSnapshot, map[string]interface{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "supervision controller is not configured")
	require.False(t, errors.Is(err, supervision.ErrActionConflict))
}
