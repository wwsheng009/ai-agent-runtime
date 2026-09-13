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
	notification *supervision.Notification
	record       supervision.ActionRecord
	err          error

	parentID    string
	snapshotReq SupervisionSnapshotArgs
	ackReq      AckLifecycleArgs
	controlReq  ControlDescendantArgs
	calls       int
}

func (f *fakeSupervisionController) SupervisionSnapshot(ctx context.Context, parentSessionID string, args SupervisionSnapshotArgs) (*supervision.Digest, error) {
	f.calls++
	f.parentID = parentSessionID
	f.snapshotReq = args
	return f.digest, f.err
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
	require.NotContains(t, without, ToolAckLifecycle)
	require.NotContains(t, without, ToolControlDescendant)

	broker := &Broker{Supervision: &fakeSupervisionController{}}
	defs := broker.Definitions()
	names := toolDefinitionNames(defs)
	require.Contains(t, names, ToolSupervisionSnapshot)
	require.Contains(t, names, ToolAckLifecycle)
	require.Contains(t, names, ToolControlDescendant)

	snapshot := supervisionDefinition(t, defs, ToolSupervisionSnapshot)
	require.NotNil(t, snapshot.Metadata)
	require.Equal(t, false, snapshot.Metadata[types.ToolMetadataEmptyReplayCacheKey],
		"snapshot is a polling tool: an empty result must not be cached as a negative answer")

	ack := supervisionDefinition(t, defs, ToolAckLifecycle)
	require.NotEqual(t, false, ack.Metadata[types.ToolMetadataEmptyReplayCacheKey])

	params, ok := ack.Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, params, "expected_version")
	require.Contains(t, params, "note")
	controlParams, ok := supervisionDefinition(t, defs, ToolControlDescendant).Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, controlParams, "cascade")
}

func TestBroker_IsBrokerTool_RecognizesSupervisionTools(t *testing.T) {
	broker := &Broker{}
	for _, name := range []string{ToolSupervisionSnapshot, ToolAckLifecycle, ToolControlDescendant, "supervisionSnapshot", "ackLifecycle", "controlDescendant"} {
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
