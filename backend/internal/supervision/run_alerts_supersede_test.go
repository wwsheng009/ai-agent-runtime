package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSupersedeRunAlerts_KeepsOnlyNewestCondition is the 建议稿 §2.5 guard: a
// run that escalates (stalled → timed_out) must leave the parent inbox with
// exactly one current condition instead of one critical row per rung.
func TestSupersedeRunAlerts_KeepsOnlyNewestCondition(t *testing.T) {
	ctx := context.Background()
	supervisor, store := newTestExecutionSupervisor(t, "supersede-newest", ExecutionSupervisorConfig{
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
	projectStalledAlert(t, supervisor, run, "execution_timed_out")

	rows, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session", IncludeResolved: true})
	require.NoError(t, err)
	require.Len(t, rows, 2, "each rung keeps its own row (idempotency key includes event_type)")
	byType := map[string]Notification{}
	for _, row := range rows {
		byType[row.EventType] = row
	}
	require.Equal(t, ResolutionClosed, byType["progress_stalled"].ResolutionState)
	require.NotNil(t, byType["progress_stalled"].ResolvedAt)
	require.True(t, byType["execution_timed_out"].Unresolved())

	digest, err := BuildDigest(ctx, store, DigestRequest{RootScopeID: "root-session", TargetParentSessionID: "parent-session"})
	require.NoError(t, err)
	require.Equal(t, 1, digest.CriticalUnresolved, "only the current condition stays critical")
	require.Equal(t, 1, digest.ActionRequired)
}

// TestSupersedeRunAlerts_InformationalProjectionDoesNotSupersede: only the
// run-alert decisions supersede. The observe-mode auto_extended projection is
// informational (severity info) and must leave the run's live condition
// unresolved — it does not describe a newer condition.
func TestSupersedeRunAlerts_InformationalProjectionDoesNotSupersede(t *testing.T) {
	ctx := context.Background()
	supervisor, store := newTestExecutionSupervisor(t, "supersede-info", ExecutionSupervisorConfig{
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
	projectStalledAlert(t, supervisor, run, "auto_extended")

	rows, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session", IncludeResolved: true})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		if row.EventType == "progress_stalled" {
			require.True(t, row.Unresolved(), "informational projection must not resolve the live condition")
		}
	}
}

// TestSupersedeRunAlerts_DegradesToNoopAndSparesForeignRows: a nil store or a
// blank scope is a no-op (notification-only hosts), and another run's rows are
// never touched.
func TestSupersedeRunAlerts_DegradesToNoopAndSparesForeignRows(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	superseded, err := SupersedeRunAlerts(ctx, nil, "root-session", "run-x", "execution_timed_out", now)
	require.NoError(t, err)
	require.Zero(t, superseded)

	supervisor, store := newTestExecutionSupervisor(t, "supersede-foreign", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 30 * time.Minute,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, nil, nil)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{RootSessionID: "root-session", ParentSessionID: "parent-session", SessionID: "child-1"})
	require.NoError(t, err)
	other, err := supervisor.StartRun(ctx, RunSpec{RootSessionID: "root-session", ParentSessionID: "parent-session", SessionID: "child-2"})
	require.NoError(t, err)
	projectStalledAlert(t, supervisor, other, "progress_stalled")
	projectStalledAlert(t, supervisor, run, "progress_stalled")

	superseded, err = SupersedeRunAlerts(ctx, store, " ", run.RunID, "execution_timed_out", now)
	require.NoError(t, err)
	require.Zero(t, superseded, "blank scope degrades to a no-op")

	superseded, err = SupersedeRunAlerts(ctx, store, "root-session", run.RunID, "execution_timed_out", now)
	require.NoError(t, err)
	require.Equal(t, 1, superseded)

	rows, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session", IncludeResolved: true})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		if row.SubjectID == run.RunID {
			require.Equal(t, ResolutionClosed, row.ResolutionState)
			continue
		}
		require.Equal(t, other.RunID, row.SubjectID)
		require.True(t, row.Unresolved(), "another run's row must be untouched")
	}
}
