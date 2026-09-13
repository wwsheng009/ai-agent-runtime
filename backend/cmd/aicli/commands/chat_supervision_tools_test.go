package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// P2-12 方案 3 的宿主接线测试：模型入口复用 /debug supervision 的 scope 规则
// 与 LocalControlService 的审计/CAS 语义。

type recordingSupervisionExecutor struct {
	calls []supervision.ActionRecord
}

func (r *recordingSupervisionExecutor) Execute(ctx context.Context, a supervision.ActionRecord) (supervision.ActionResult, error) {
	r.calls = append(r.calls, a)
	return supervision.ActionResult{Status: supervision.ActionCompleted, Result: "canceled"}, nil
}

// seedSupervisionExecutionRun keeps the notification subject alive in the
// control plane, so the snapshot's stale probe reports it as existing (the
// opposite case — a dead subject — is asserted by the supervision package).
func seedSupervisionExecutionRun(t *testing.T, host *localChatRuntimeHost, runID string) {
	t.Helper()
	runStore, ok := host.Supervision.Store.(supervision.ExecutionRunStore)
	require.True(t, ok)
	_, err := runStore.CreateExecutionRun(context.Background(), supervision.ExecutionRun{
		RunID:     runID,
		Kind:      supervision.RunKindAgentRun,
		Workflow:  supervision.RunWorkflowSpawnAgent,
		SessionID: runID,
		AgentID:   runID,
		Status:    supervision.RunStatusRunning,
		OwnerID:   "parent-session",
		StartedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
}

// upsertTeamScopedSupervisionNotification seeds a row the way team-scope
// producers do: root scope and team target are the team id while the addressed
// parent stays the lead session (chat_actor_host.go:2155-2165).
func upsertTeamScopedSupervisionNotification(t *testing.T, host *localChatRuntimeHost, teamID, leadSessionID, subjectID string) supervision.Notification {
	t.Helper()
	now := time.Now().UTC()
	record, err := host.Supervision.Store.UpsertNotification(context.Background(), supervision.Notification{
		NotificationID:        "n-" + subjectID,
		RootScopeID:           teamID,
		TargetParentSessionID: leadSessionID,
		TargetParentTeamID:    teamID,
		SubjectKind:           supervision.SubjectAgentRun,
		SubjectID:             subjectID,
		SubjectVersion:        1,
		EventSeq:              1,
		EventType:             "subagent.batch.failed",
		Severity:              supervision.SeverityCritical,
		SupervisionState:      supervision.SupervisionBlocked,
		Reason:                "batch failed",
		ResolutionState:       supervision.ResolutionUnresolved,
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	require.NoError(t, err)
	return record
}

func TestLocalSupervisionToolController_SnapshotThenAckConverges(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	controller := newLocalSupervisionToolController(host, session)
	require.NotNil(t, controller)

	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-tool-1", supervision.SeverityCritical, 1)
	seedSupervisionExecutionRun(t, host, "batch-tool-1")

	digest, err := controller.SupervisionSnapshot(context.Background(), "parent-session", toolbroker.SupervisionSnapshotArgs{})
	require.NoError(t, err)
	require.Equal(t, 1, digest.CriticalUnresolved)

	// The audit note is the same hard requirement as the CLI command.
	_, err = controller.AckLifecycle(context.Background(), "parent-session", toolbroker.AckLifecycleArgs{
		NotificationID: notification.NotificationID,
		Decision:       "acknowledge",
	})
	require.ErrorIs(t, err, supervision.ErrActionInvalid)

	updated, err := controller.AckLifecycle(context.Background(), "parent-session", toolbroker.AckLifecycleArgs{
		NotificationID: notification.NotificationID,
		Decision:       "acknowledge",
		Note:           "reviewed the dead batch in the tool path",
	})
	require.NoError(t, err)
	require.Equal(t, supervision.DecisionAcknowledged, updated.DecisionState)

	digest, err = controller.SupervisionSnapshot(context.Background(), "parent-session", toolbroker.SupervisionSnapshotArgs{})
	require.NoError(t, err)
	require.Zero(t, digest.CriticalUnresolved, "acknowledged row must leave the model-facing digest")
}

func TestLocalSupervisionToolController_RejectsForeignScope(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := newChatDebugSupervisionSession(host, "parent-session")
	controller := newLocalSupervisionToolController(host, session)
	foreign := upsertChatDebugSupervisionNotification(t, host, "other-session", "batch-foreign-1", supervision.SeverityCritical, 1)

	_, err := controller.AckLifecycle(context.Background(), "parent-session", toolbroker.AckLifecycleArgs{
		NotificationID: foreign.NotificationID,
		Decision:       "acknowledge",
		Note:           "not mine",
	})
	require.ErrorIs(t, err, supervision.ErrActionNotAllowed)

	record, err := controller.ControlDescendant(context.Background(), "parent-session", toolbroker.ControlDescendantArgs{
		NotificationID: foreign.NotificationID,
		Action:         "cancel",
		Reason:         "not mine",
	})
	require.ErrorIs(t, err, supervision.ErrActionNotAllowed)
	require.Equal(t, supervision.ActionRecord{}, record)
}

func TestLocalSupervisionToolController_ControlRunsDurableAction(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	executor := &recordingSupervisionExecutor{}
	host.Supervision.SetActionExecutor(executor)
	session := newChatDebugSupervisionSession(host, "parent-session")
	controller := newLocalSupervisionToolController(host, session)
	notification := upsertChatDebugSupervisionNotification(t, host, "parent-session", "batch-control-1", supervision.SeverityCritical, 1)

	record, err := controller.ControlDescendant(context.Background(), "parent-session", toolbroker.ControlDescendantArgs{
		NotificationID: notification.NotificationID,
		Action:         "cancel",
		Reason:         "deadline exceeded twice",
	})
	require.NoError(t, err)
	require.Equal(t, supervision.ActionCompleted, record.Status)
	require.Len(t, executor.calls, 1)
	require.Equal(t, supervision.ActionCancel, executor.calls[0].Action)
	require.Equal(t, "parent-session", executor.calls[0].RootScopeID)
	require.Equal(t, notification.SubjectID, executor.calls[0].TargetID)

	// Unknown actions never reach the executor (the shared service validates the
	// action set; disallowed-but-known actions are covered by the service tests).
	_, err = controller.ControlDescendant(context.Background(), "parent-session", toolbroker.ControlDescendantArgs{
		NotificationID: notification.NotificationID,
		Action:         "explode",
		Reason:         "nope",
	})
	require.ErrorIs(t, err, supervision.ErrActionInvalid)
	require.Len(t, executor.calls, 1)
}

func TestLocalSupervisionToolController_TeamLeadUsesTeamScope(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	t.Cleanup(func() { _ = host.TeamStore.Close() })
	teamID, err := host.TeamStore.CreateTeam(context.Background(), team.Team{
		ID:            "team-supervision",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)

	session := newChatDebugSupervisionSession(host, "lead-session")
	session.ActiveTeam = &chatTeamBinding{TeamID: teamID, AgentID: "lead"}
	controller := newLocalSupervisionToolController(host, session)

	// Rows addressed at the team scope are visible to the lead...
	teamNotification := upsertTeamScopedSupervisionNotification(t, host, teamID, "lead-session", "batch-team-1")
	seedSupervisionExecutionRun(t, host, "batch-team-1")
	digest, err := controller.SupervisionSnapshot(context.Background(), "lead-session", toolbroker.SupervisionSnapshotArgs{})
	require.NoError(t, err)
	require.Equal(t, 1, digest.CriticalUnresolved)
	require.Equal(t, teamNotification.NotificationID, digest.Items[0].NotificationID)

	// ...and the lead may converge them.
	updated, err := controller.AckLifecycle(context.Background(), "lead-session", toolbroker.AckLifecycleArgs{
		NotificationID: teamNotification.NotificationID,
		Decision:       "acknowledge",
		Note:           "lead handled the team row",
	})
	require.NoError(t, err)
	require.Equal(t, supervision.DecisionAcknowledged, updated.DecisionState)
}

func TestLocalSupervisionToolController_RequiresStore(t *testing.T) {
	host := &localChatRuntimeHost{}
	require.Nil(t, newLocalSupervisionToolController(host, nil))
}
