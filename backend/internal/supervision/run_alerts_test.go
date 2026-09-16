package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// bumpingStore simulates a concurrent preflight write landing between the
// convergence lookup and its CAS: ListNotifications hands back rows whose
// version is already stale because the row was marked delivered in between.
type bumpingStore struct {
	Store
	bumped bool
}

func (s *bumpingStore) ListNotifications(ctx context.Context, filter NotificationFilter) ([]Notification, error) {
	rows, err := s.Store.ListNotifications(ctx, filter)
	if err != nil {
		return nil, err
	}
	if !s.bumped {
		s.bumped = true
		for _, row := range rows {
			if isRunAlertEventType(row.EventType) {
				_ = s.Store.MarkNotificationDelivered(ctx, row.NotificationID, time.Now().UTC())
			}
		}
	}
	return rows, nil
}

// projectStalledAlert drives the supervisor's own alert projection so the test
// exercises the real event type/severity the watchdog writes.
func projectStalledAlert(t *testing.T, supervisor *ExecutionSupervisor, run *ExecutionRun, decision string) {
	t.Helper()
	supervisor.projectDecision(context.Background(), run, &RunDecision{
		Decision: decision,
		Reason:   "no meaningful progress since progress deadline",
	})
}

// TestConvergeRunAlerts_StalledThenSucceeded is the plan §4.3 acceptance guard:
// the 2026-09-16 audit found `progress_stalled` rows still critical/unresolved
// after their run had finished, so the parent kept re-injecting action-required
// rows for completed work.
func TestConvergeRunAlerts_StalledThenSucceeded(t *testing.T) {
	ctx := context.Background()
	supervisor, store := newTestExecutionSupervisor(t, "converge-stalled-success", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 30 * time.Minute,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, nil, nil)
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{RootSessionID: "root-session", ParentSessionID: "parent-session", SessionID: "child-1"})
	require.NoError(t, err)
	projectStalledAlert(t, supervisor, run, "progress_stalled")

	alerts, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session"})
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.Equal(t, "progress_stalled", alerts[0].EventType)
	require.Equal(t, SeverityCritical, alerts[0].Severity)
	require.True(t, alerts[0].Unresolved())

	// Before convergence the digest must surface it as action-required: that is
	// the pre-fix failure mode, not a vacuous assertion.
	before, err := BuildDigest(ctx, store, DigestRequest{RootScopeID: "root-session", TargetParentSessionID: "parent-session"})
	require.NoError(t, err)
	require.Equal(t, 1, before.CriticalUnresolved)
	require.Equal(t, 1, before.ActionRequired)

	require.NoError(t, supervisor.CompleteRun(ctx, run.RunID, RunStatusSucceeded, "", "", nil))

	converged, err := store.GetNotification(ctx, alerts[0].NotificationID)
	require.NoError(t, err)
	require.Equal(t, ResolutionRecovered, converged.ResolutionState)
	require.NotNil(t, converged.ResolvedAt)

	after, err := BuildDigest(ctx, store, DigestRequest{RootScopeID: "root-session", TargetParentSessionID: "parent-session"})
	require.NoError(t, err)
	require.Equal(t, 0, after.CriticalUnresolved)
	require.Equal(t, 0, after.ActionRequired)
	// Only the terminal outcome is left; the stalled alert must be gone.
	require.Len(t, after.Items, 1)
	require.Equal(t, SupervisionTerminated, after.Items[0].SupervisionState)
	require.False(t, after.Items[0].ActionRequired)
}

// TestConvergeRunAlerts_FailedRunKeepsOutcomeVisible pins the status mapping of
// plan §4.3: a run that did not succeed resolves its stale alerts as failed,
// while the terminal row itself stays unresolved so the parent still learns the
// outcome.
func TestConvergeRunAlerts_FailedRunKeepsOutcomeVisible(t *testing.T) {
	ctx := context.Background()
	supervisor, store := newTestExecutionSupervisor(t, "converge-failed", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 30 * time.Minute,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, nil, nil)
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{RootSessionID: "root-session", ParentSessionID: "parent-session", SessionID: "child-1"})
	require.NoError(t, err)
	projectStalledAlert(t, supervisor, run, "execution_timed_out")

	require.NoError(t, supervisor.CompleteRun(ctx, run.RunID, RunStatusFailed, "spawn_failed", "", nil))

	rows, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "run_failed", rows[0].EventType)

	all, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session", IncludeResolved: true})
	require.NoError(t, err)
	byEvent := map[string]Notification{}
	for _, row := range all {
		byEvent[row.EventType] = row
	}
	require.Equal(t, ResolutionFailed, byEvent["execution_timed_out"].ResolutionState)
	require.True(t, byEvent["run_failed"].Unresolved(), "the terminal outcome must stay unresolved")
}

// TestConvergeRunAlerts_LeavesForeignSubjectsAlone keeps the convergence narrow:
// only the run's own live-condition rows are touched, a resolution someone else
// recorded is never rewritten, and other scopes are not scanned.
func TestConvergeRunAlerts_LeavesForeignSubjectsAlone(t *testing.T) {
	ctx := context.Background()
	store := testExecutionRunStore(t, "converge-scope")
	now := time.Now().UTC()

	project := func(rootScope, runID, eventType string, resolution ResolutionState) Notification {
		t.Helper()
		n, err := ProjectLifecycle(ctx, store, nil, LifecycleProjection{
			RootScopeID:           rootScope,
			TargetParentSessionID: "parent-session",
			SubjectKind:           SubjectAgentRun,
			SubjectID:             runID,
			EventType:             eventType,
			Severity:              SeverityCritical,
			SupervisionState:      SupervisionStalled,
			ResolutionState:       resolution,
		})
		require.NoError(t, err)
		return n
	}

	target := project("root-session", "run_target", "progress_stalled", "")
	otherRun := project("root-session", "run_other", "progress_stalled", "")
	alreadyResolved := project("root-session", "run_target", "cancel_grace_expired", ResolutionRecovered)
	otherScope := project("root-other", "run_target", "progress_stalled", "")

	converged, err := ConvergeRunAlerts(ctx, store, "root-session", "run_target", RunStatusFailed, now)
	require.NoError(t, err)
	require.Equal(t, 1, converged, "only the unresolved run-scoped alert converges")

	got, err := store.GetNotification(ctx, target.NotificationID)
	require.NoError(t, err)
	require.Equal(t, ResolutionFailed, got.ResolutionState)

	untouched, err := store.GetNotification(ctx, otherRun.NotificationID)
	require.NoError(t, err)
	require.True(t, untouched.Unresolved())

	preserved, err := store.GetNotification(ctx, alreadyResolved.NotificationID)
	require.NoError(t, err)
	require.Equal(t, ResolutionRecovered, preserved.ResolutionState)

	foreign, err := store.GetNotification(ctx, otherScope.NotificationID)
	require.NoError(t, err)
	require.True(t, foreign.Unresolved())
}

// TestConvergeRunAlerts_RetriesStaleVersion covers the realistic race: preflight
// marks a row delivered while the convergence is in flight, so the first CAS
// fails. Without the bounded retry the stale alert would survive forever.
func TestConvergeRunAlerts_RetriesStaleVersion(t *testing.T) {
	ctx := context.Background()
	inner := testExecutionRunStore(t, "converge-cas")
	now := time.Now().UTC()
	alert, err := ProjectLifecycle(ctx, inner, nil, LifecycleProjection{
		RootScopeID:           "root-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           SubjectAgentRun,
		SubjectID:             "run_race",
		EventType:             "progress_stalled",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionStalled,
	})
	require.NoError(t, err)

	store := &bumpingStore{Store: inner}
	converged, err := ConvergeRunAlerts(ctx, store, "root-session", "run_race", RunStatusSucceeded, now)
	require.NoError(t, err)
	require.True(t, store.bumped, "the simulated concurrent write must have happened")
	require.Equal(t, 1, converged)

	got, err := inner.GetNotification(ctx, alert.NotificationID)
	require.NoError(t, err)
	require.Equal(t, ResolutionRecovered, got.ResolutionState)
}

// TestMarkDigestItemDelivered_ThrottlesRepeatWrites is the plan §4.3 churn
// guard: preflight used to re-mark every item on every turn, and the audit found
// a stale row churned past version 180 while nothing about it had changed.
func TestMarkDigestItemDelivered_ThrottlesRepeatWrites(t *testing.T) {
	ctx := context.Background()
	store := testExecutionRunStore(t, "delivery-throttle")
	notification, err := ProjectLifecycle(ctx, store, nil, LifecycleProjection{
		RootScopeID:           "root-session",
		TargetParentSessionID: "parent-session",
		SubjectKind:           SubjectAgentSession,
		SubjectID:             "child-1",
		EventType:             "agent_failed",
		Severity:              SeverityCritical,
		SupervisionState:      SupervisionTerminated,
		Reason:                "child session failed",
	})
	require.NoError(t, err)
	require.Equal(t, DeliveryPending, notification.DeliveryState)

	digestOf := func() Digest {
		t.Helper()
		digest, err := BuildDigest(ctx, store, DigestRequest{
			RootScopeID:           "root-session",
			TargetParentSessionID: "parent-session",
		})
		require.NoError(t, err)
		require.Len(t, digest.Items, 1)
		return *digest
	}

	first := digestOf()
	require.Equal(t, DeliveryPending, first.Items[0].DeliveryState)
	require.NoError(t, MarkDigestItemDelivered(ctx, store, first.Items[0], time.Now().UTC()))

	delivered, err := store.GetNotification(ctx, notification.NotificationID)
	require.NoError(t, err)
	require.Equal(t, DeliverySeen, delivered.DeliveryState)

	second := digestOf()
	require.Equal(t, DeliverySeen, second.Items[0].DeliveryState, "digest must expose the delivery state")
	require.NoError(t, MarkDigestItemDelivered(ctx, store, second.Items[0], time.Now().UTC()))

	unchanged, err := store.GetNotification(ctx, notification.NotificationID)
	require.NoError(t, err)
	require.Equal(t, delivered.Version, unchanged.Version, "an already-seen row must not be re-marked")
}
