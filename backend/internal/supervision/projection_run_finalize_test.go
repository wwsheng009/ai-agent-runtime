package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// notificationOnlyStore hides the ExecutionRunStore capability so tests can
// assert the optional-store contract of the projection path.
type notificationOnlyStore struct {
	Store
}

// TestProjectAgentCompletionFinalizesChildRun is the regression guard for the
// dangling spawn_agent run alert observed on 2026-09-13: children p0-verify /
// p1-5-verify / p1-67-verify / p2-verify finished, but their registration runs
// stayed `queued`, so the watchdog kept reporting progress_stalled at the
// parent for work that was already done.
func TestProjectAgentCompletionFinalizesChildRun(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		childState string
		wantStatus string
		wantEvent  string
	}{
		{name: "completed", childState: "idle", wantStatus: RunStatusSucceeded, wantEvent: "agent_completed"},
		{name: "failed", childState: "failed", wantStatus: RunStatusFailed, wantEvent: "agent_failed"},
		{name: "interrupted", childState: "stopped", wantStatus: RunStatusCanceled, wantEvent: "agent_interrupted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testExecutionRunStore(t, "projection-run-"+tc.name)
			run := sampleExecutionRun("run_child_" + tc.name)
			run.SessionID = "child-" + tc.name
			run.AgentID = run.SessionID
			created, err := store.CreateExecutionRun(ctx, run)
			require.NoError(t, err)
			require.True(t, created)

			notification, err := ProjectAgentCompletion(ctx, store, nil, "root-1", "parent-1", run.SessionID, tc.childState, "session_end")
			require.NoError(t, err)
			require.Equal(t, tc.wantEvent, notification.EventType)

			got, err := store.GetExecutionRun(ctx, run.RunID)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, got.Status)
			require.True(t, got.Terminal())
			require.NotNil(t, got.FinishedAt)
		})
	}
}

// TestProjectAgentCompletionKeepsTerminalRun verifies the finalizer only
// touches active runs: a run that already reached a terminal state (for
// example an enforced cancel) must not be rewritten by a later replay of the
// child lifecycle event.
func TestProjectAgentCompletionKeepsTerminalRun(t *testing.T) {
	ctx := context.Background()
	store := testExecutionRunStore(t, "projection-run-terminal")
	run := sampleExecutionRun("run_child_terminal")
	run.SessionID = "child-terminal"
	run.Status = RunStatusRunning
	_, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)
	ok, err := store.MarkExecutionRunTerminal(ctx, run.RunID, RunStatusTimedOut, "progress_stalled", "", time.Now().UTC())
	require.NoError(t, err)
	require.True(t, ok)

	_, err = ProjectAgentCompletion(ctx, store, nil, "root-1", "parent-1", run.SessionID, "idle", "session_end")
	require.NoError(t, err)

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusTimedOut, got.Status)
	require.Equal(t, "progress_stalled", got.ErrorCode)
}

// TestFinalizeChildExecutionRunsOptionalStore keeps the notification-only
// contract: stores without execution-run persistence (and blank child ids)
// degrade to a no-op instead of failing the projection.
func TestFinalizeChildExecutionRunsOptionalStore(t *testing.T) {
	ctx := context.Background()
	store := testExecutionRunStore(t, "projection-run-optional")
	require.NoError(t, finalizeChildExecutionRuns(ctx, notificationOnlyStore{Store: store}, "child-x", "idle", time.Now().UTC()))
	require.NoError(t, finalizeChildExecutionRuns(ctx, store, "   ", "idle", time.Now().UTC()))

	run := sampleExecutionRun("run_child_optional")
	run.SessionID = "child-optional"
	run.Status = RunStatusRunning
	_, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)
	require.NoError(t, finalizeChildExecutionRuns(ctx, notificationOnlyStore{Store: store}, run.SessionID, "idle", time.Now().UTC()))
	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusRunning, got.Status, "an unknown store capability must not touch runs")
}
