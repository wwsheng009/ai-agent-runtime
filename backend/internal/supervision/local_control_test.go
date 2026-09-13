package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newLocalControlEnv(t *testing.T, name string) (*SQLiteSupervisionStore, *LocalControlService, *fakeActionExecutor) {
	t.Helper()
	store := newTestStore(t, name)
	executor := &fakeActionExecutor{result: ActionResult{Status: ActionCompleted, Result: "closed"}}
	svc := NewLocalControlService(store, NewActionService(store, executor, nil))
	return store, svc, executor
}

func seedLocalNotification(t *testing.T, store Store, subject string) Notification {
	t.Helper()
	created, err := store.UpsertNotification(context.Background(), testNotification(subject, 1))
	require.NoError(t, err)
	return created
}

// TestLocalControlService_Snapshot_ScopedAndStaleAware covers the read entry:
// the snapshot is bound to the caller's own root scope and honours the stale
// probe so a dead subject stops inflating critical_unresolved (P2-12).
func TestLocalControlService_Snapshot_ScopedAndStaleAware(t *testing.T) {
	store, svc, _ := newLocalControlEnv(t, "supervision-local-snapshot")
	ctx := context.Background()
	seeded := seedLocalNotification(t, store, "agent-1")

	digest, err := svc.Snapshot(ctx, LocalSnapshotRequest{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Equal(t, 1, digest.CriticalUnresolved)
	require.Equal(t, 1, digest.ActionRequired)
	require.Equal(t, seeded.NotificationID, digest.Items[0].NotificationID)

	// A foreign scope must see nothing: the read entry is scoped exactly like
	// the mutation entry.
	foreign, err := svc.Snapshot(ctx, LocalSnapshotRequest{RootScopeID: "someone-else"})
	require.NoError(t, err)
	require.Zero(t, foreign.CriticalUnresolved)
	require.Empty(t, foreign.Items)

	// Subject gone -> informational only, no action required.
	stale, err := svc.Snapshot(ctx, LocalSnapshotRequest{
		RootScopeID:     "root-session-1",
		SubjectPresence: func(context.Context, Notification) (bool, bool) { return false, true },
	})
	require.NoError(t, err)
	require.Equal(t, 1, stale.StaleSubjects)
	require.Zero(t, stale.CriticalUnresolved)
	require.Zero(t, stale.ActionRequired)
	require.True(t, stale.Items[0].Stale)

	// Unverifiable presence keeps the original severity (never hide a critical).
	unchecked, err := svc.Snapshot(ctx, LocalSnapshotRequest{
		RootScopeID:     "root-session-1",
		SubjectPresence: func(context.Context, Notification) (bool, bool) { return false, false },
	})
	require.NoError(t, err)
	require.Equal(t, 1, unchecked.CriticalUnresolved)
	require.Zero(t, unchecked.StaleSubjects)

	_, err = svc.Snapshot(ctx, LocalSnapshotRequest{})
	require.ErrorIs(t, err, ErrActionInvalid)
}

// TestLocalControlService_Acknowledge_NoteScopeAndVersion pins the three guards
// that keep the mutation auditable and scope-safe.
func TestLocalControlService_Acknowledge_NoteScopeAndVersion(t *testing.T) {
	store, svc, _ := newLocalControlEnv(t, "supervision-local-ack")
	ctx := context.Background()
	seeded := seedLocalNotification(t, store, "agent-1")
	scopes := []string{"root-session-1"}

	_, err := svc.Acknowledge(ctx, LifecycleDecisionRequest{NotificationID: seeded.NotificationID, Scopes: scopes})
	require.ErrorIs(t, err, ErrActionInvalid, "ack requires an audit note")

	_, err = svc.Acknowledge(ctx, LifecycleDecisionRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         []string{"foreign-scope"},
		Note:           "handled",
	})
	require.ErrorIs(t, err, ErrActionNotAllowed, "cross-scope acknowledge must be rejected")

	_, err = svc.Acknowledge(ctx, LifecycleDecisionRequest{
		NotificationID:     seeded.NotificationID,
		Scopes:             scopes,
		Note:               "handled",
		ExpectedVersion:    99,
		HasExpectedVersion: true,
	})
	require.ErrorIs(t, err, ErrActionConflict, "stale expected_version must fail loudly")

	updated, err := svc.Acknowledge(ctx, LifecycleDecisionRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         scopes,
		Note:           "parent handled the timeout",
	})
	require.NoError(t, err)
	require.Equal(t, DecisionAcknowledged, updated.DecisionState)
	require.Greater(t, updated.Version, seeded.Version)

	// Acknowledged rows leave the critical set.
	digest, err := svc.Snapshot(ctx, LocalSnapshotRequest{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Zero(t, digest.CriticalUnresolved)
}

// TestLocalControlService_DeferAndResolve covers the two remaining decisions.
func TestLocalControlService_DeferAndResolve(t *testing.T) {
	store, svc, _ := newLocalControlEnv(t, "supervision-local-defer-resolve")
	ctx := context.Background()
	seeded := seedLocalNotification(t, store, "agent-1")
	scopes := []string{"root-session-1"}

	_, err := svc.Defer(ctx, LifecycleDecisionRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         scopes,
		Reason:         "known noise",
		Until:          time.Now().UTC().Add(-time.Minute),
	})
	require.ErrorIs(t, err, ErrActionInvalid, "a past deadline is not a defer")

	deferred, err := svc.Defer(ctx, LifecycleDecisionRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         scopes,
		Reason:         "waiting for the rerun",
		Until:          time.Now().UTC().Add(30 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, DecisionDeferred, deferred.DecisionState)
	require.NotNil(t, deferred.DeferUntil)

	// Deferred (not yet due) is out of the preflight view but still durable.
	digest, err := svc.Snapshot(ctx, LocalSnapshotRequest{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Zero(t, digest.CriticalUnresolved)

	_, err = svc.Resolve(ctx, LifecycleDecisionRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         scopes,
		Resolution:     ResolutionState("bogus"),
	})
	require.ErrorIs(t, err, ErrActionInvalid)

	resolved, err := svc.Resolve(ctx, LifecycleDecisionRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         scopes,
		Resolution:     ResolutionClosed,
	})
	require.NoError(t, err)
	require.Equal(t, ResolutionClosed, resolved.ResolutionState)
}

// TestLocalControlService_Control_AuditedAndGuarded verifies the write entry
// keeps the request -> accept -> execute audit trail and re-validates the
// server-computed allowed_actions instead of trusting the model.
func TestLocalControlService_Control_AuditedAndGuarded(t *testing.T) {
	store, svc, executor := newLocalControlEnv(t, "supervision-local-control")
	ctx := context.Background()
	seeded := seedLocalNotification(t, store, "agent-1")
	scopes := []string{"root-session-1"}

	_, err := svc.Control(ctx, ControlRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         scopes,
		Action:         ActionCancel,
		RequestedByID:  "root-session-1",
	})
	require.ErrorIs(t, err, ErrActionInvalid, "mutating control requires a reason")

	_, err = svc.Control(ctx, ControlRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         scopes,
		Action:         ActionRetry,
		Reason:         "retry it",
	})
	require.ErrorIs(t, err, ErrActionNotAllowed, "retry is not allowed for a timeout row")
	require.False(t, executor.called)

	record, err := svc.Control(ctx, ControlRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         scopes,
		Action:         ActionCancel,
		Reason:         "deadline exceeded twice",
		RequestedByID:  "root-session-1",
	})
	require.NoError(t, err)
	require.True(t, executor.called)
	require.Equal(t, ActionCompleted, record.Status)
	require.Equal(t, "root-session-1", record.RootScopeID)
	require.Equal(t, SubjectAgentRun, record.TargetKind)
	require.Equal(t, "agent-1", record.TargetID)

	actions, err := svc.Actions().ListActions(ctx, ActionFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.Equal(t, ActionCancel, actions[0].Action)

	foreign, err := svc.Control(ctx, ControlRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         []string{"foreign-scope"},
		Action:         ActionCancel,
		Reason:         "not mine",
	})
	require.ErrorIs(t, err, ErrActionNotAllowed)
	require.Equal(t, ActionRecord{}, foreign)
}

// TestLocalControlService_Control_WithoutExecutor documents what happens when a
// host wires the store but no runtime executor: the durable action exists and
// fails loudly (no silent success).
func TestLocalControlService_Control_WithoutExecutor(t *testing.T) {
	store := newTestStore(t, "supervision-local-no-executor")
	svc := NewLocalControlService(store, nil)
	ctx := context.Background()
	seeded := seedLocalNotification(t, store, "agent-1")

	record, err := svc.Control(ctx, ControlRequest{
		NotificationID: seeded.NotificationID,
		Scopes:         []string{"root-session-1"},
		Action:         ActionCancel,
		Reason:         "no executor wired",
	})
	require.NoError(t, err)
	require.Equal(t, ActionFailed, record.Status)
	require.Contains(t, record.Result, "no executor configured")
}
