package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// gcNow is the deterministic clock for the retention tests: the "turn 终局 + N
// 天" cutoff arithmetic must be exact instead of wall-clock dependent.
var gcNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func gcPolicy(limit int) ExecutionRunPrunePolicy {
	return ExecutionRunPrunePolicy{Now: gcNow, Retention: DefaultExecutionRunRetention, Limit: limit}
}

// terminalRunFixture builds a terminal obligation whose retention clock is
// controlled by the caller: finishedAgo decides which side of the cutoff the
// row lands on. The ownership/deadline columns are cleared because a finished
// obligation no longer has a live owner.
func terminalRunFixture(runID string, finishedAgo time.Duration) ExecutionRun {
	run := sampleExecutionRun(runID)
	finished := gcNow.Add(-finishedAgo)
	run.Status = RunStatusSucceeded
	run.FinishedAt = &finished
	run.OwnerID = ""
	run.OwnerLeaseUntil = nil
	run.ExecutionDeadlineAt = nil
	run.ProgressDeadlineAt = nil
	run.CreatedAt = finished.Add(-time.Minute)
	run.UpdatedAt = finished
	return run
}

func gcSeedRun(t *testing.T, store *SQLiteSupervisionStore, run ExecutionRun) {
	t.Helper()
	created, err := store.CreateExecutionRun(context.Background(), run)
	require.NoError(t, err)
	require.True(t, created, "fixture run %s must be newly inserted", run.RunID)
}

func gcRunExists(t *testing.T, store *SQLiteSupervisionStore, runID string) bool {
	t.Helper()
	_, err := store.GetExecutionRun(context.Background(), runID)
	if err == nil {
		return true
	}
	require.ErrorIs(t, err, ErrRunNotFound, "unexpected read error for %s", runID)
	return false
}

// AC-P3-2a: GC removes only rows that are simultaneously retention-expired,
// free of artifact references and free of pending resumes; every row on the
// "不得影响" list survives the same statement.
func TestPruneExecutionRuns_RemovesOnlyEligibleRows(t *testing.T) {
	store := testExecutionRunStore(t, "run-prune-eligible")
	ctx := context.Background()

	eligible := terminalRunFixture("run_gc_eligible", 8*24*time.Hour)
	fresh := terminalRunFixture("run_gc_fresh", time.Hour)
	artifact := terminalRunFixture("run_gc_artifact", 8*24*time.Hour)
	artifact.ResultRef = "artifact://reports/run_gc_artifact"
	wakePending := terminalRunFixture("run_gc_wake", 8*24*time.Hour)
	outboxPending := terminalRunFixture("run_gc_outbox", 8*24*time.Hour)
	window := terminalRunFixture("run_gc_window", 8*24*time.Hour)
	decisionWindow := gcNow.Add(5 * time.Minute)
	window.DecisionWindowUntil = &decisionWindow
	// orphan_suspected is a supervision state, not a terminal run status: the
	// ledger row behind it (a live run whose owner lease expired) is still
	// non-terminal, so the status filter is what keeps a row that is still
	// awaiting disposition in the ledger.
	orphan := sampleExecutionRun("run_gc_orphan")
	expiredLease := gcNow.Add(-time.Hour)
	orphan.OwnerLeaseUntil = &expiredLease

	for _, run := range []ExecutionRun{eligible, fresh, artifact, wakePending, outboxPending, window, orphan} {
		gcSeedRun(t, store, run)
	}

	require.NoError(t, store.InsertWakePending(ctx, WakePending{
		RootScopeID:           "root-session",
		TargetParentSessionID: "parent-session",
		WakeReason:            "terminal",
		ObligationID:          wakePending.RunID,
	}))
	enqueued, err := store.EnqueueCompletionOutbox(ctx, CompletionOutboxEntry{
		OutboxID:        "outbox_gc_1",
		RunID:           outboxPending.RunID,
		SessionID:       outboxPending.SessionID,
		ParentSessionID: outboxPending.ParentSessionID,
		RootSessionID:   outboxPending.RootSessionID,
		Status:          RunStatusSucceeded,
		IdempotencyKey:  "subagent_completion:" + outboxPending.RunID + ":1",
	})
	require.NoError(t, err)
	require.True(t, enqueued)

	removed, err := store.PruneExecutionRuns(ctx, gcPolicy(DefaultExecutionRunPruneLimit))
	require.NoError(t, err)
	require.EqualValues(t, 1, removed, "only the fully eligible row may be evicted")

	require.False(t, gcRunExists(t, store, eligible.RunID))
	for _, kept := range []ExecutionRun{fresh, artifact, wakePending, outboxPending, window, orphan} {
		require.True(t, gcRunExists(t, store, kept.RunID), "%s must survive the prune", kept.RunID)
	}
}

// AC-P3-2a (保留窗口 = turn 终局 + N 天): the retention clock belongs to the
// turn, not to the individual row — a recently finished sibling holds the whole
// turn's rows until every sibling is past the cutoff.
func TestPruneExecutionRuns_AnchorsRetentionOnTurnFinalization(t *testing.T) {
	store := testExecutionRunStore(t, "run-prune-turn")
	ctx := context.Background()

	old := terminalRunFixture("run_gc_turn_old", 8*24*time.Hour)
	old.TurnID = "turn-1"
	recent := terminalRunFixture("run_gc_turn_recent", time.Hour)
	recent.TurnID = "turn-1"
	gcSeedRun(t, store, old)
	gcSeedRun(t, store, recent)

	removed, err := store.PruneExecutionRuns(ctx, gcPolicy(DefaultExecutionRunPruneLimit))
	require.NoError(t, err)
	require.Zero(t, removed, "the turn is not final long enough: its old sibling must stay readable")
	require.True(t, gcRunExists(t, store, old.RunID))

	// Once the whole turn is past the window its rows go together.
	settled := terminalRunFixture("run_gc_turn_settled", 9*24*time.Hour)
	settled.TurnID = "turn-2"
	settledPeer := terminalRunFixture("run_gc_turn_settled_peer", 8*24*time.Hour)
	settledPeer.TurnID = "turn-2"
	gcSeedRun(t, store, settled)
	gcSeedRun(t, store, settledPeer)

	removed, err = store.PruneExecutionRuns(ctx, gcPolicy(DefaultExecutionRunPruneLimit))
	require.NoError(t, err)
	require.EqualValues(t, 2, removed)
	require.False(t, gcRunExists(t, store, settled.RunID))
	require.False(t, gcRunExists(t, store, settledPeer.RunID))
	require.True(t, gcRunExists(t, store, old.RunID), "turn-1 still has a live sibling")
}

// AC-P3-2a (批量删除、单次有界): one call removes at most Limit rows, oldest
// finished_at first, and repeated calls drain the backlog without overshooting.
func TestPruneExecutionRuns_BoundedBatchesDrainOldestFirst(t *testing.T) {
	store := testExecutionRunStore(t, "run-prune-bounded")
	ctx := context.Background()

	oldest := terminalRunFixture("run_gc_batch_1", 30*24*time.Hour)
	middle := terminalRunFixture("run_gc_batch_2", 20*24*time.Hour)
	newest := terminalRunFixture("run_gc_batch_3", 8*24*time.Hour)
	for _, run := range []ExecutionRun{oldest, middle, newest} {
		gcSeedRun(t, store, run)
	}

	removed, err := store.PruneExecutionRuns(ctx, gcPolicy(2))
	require.NoError(t, err)
	require.EqualValues(t, 2, removed)
	require.False(t, gcRunExists(t, store, oldest.RunID), "the oldest rows are drained first")
	require.False(t, gcRunExists(t, store, middle.RunID))
	require.True(t, gcRunExists(t, store, newest.RunID))

	removed, err = store.PruneExecutionRuns(ctx, gcPolicy(2))
	require.NoError(t, err)
	require.EqualValues(t, 1, removed)
	removed, err = store.PruneExecutionRuns(ctx, gcPolicy(2))
	require.NoError(t, err)
	require.Zero(t, removed)
}

// The zero policy is the production default (7d window, default batch size),
// not "GC off": an omitted config field must never turn the ledger into a
// never-pruned table.
func TestPruneExecutionRuns_ZeroPolicyUsesDefaults(t *testing.T) {
	store := testExecutionRunStore(t, "run-prune-defaults")
	ctx := context.Background()

	expired := terminalRunFixture("run_gc_default_old", DefaultExecutionRunRetention+time.Hour)
	inside := terminalRunFixture("run_gc_default_inside", DefaultExecutionRunRetention-time.Hour)
	gcSeedRun(t, store, expired)
	gcSeedRun(t, store, inside)

	removed, err := store.PruneExecutionRuns(ctx, ExecutionRunPrunePolicy{Now: gcNow})
	require.NoError(t, err)
	require.EqualValues(t, 1, removed)
	require.False(t, gcRunExists(t, store, expired.RunID))
	require.True(t, gcRunExists(t, store, inside.RunID), "a row one hour inside the default window stays")
}

// AC-P3-2a (巡检侧接线): the GC rides the scan, is throttled to one bounded
// batch per interval, and reports through Stats without failing the scan.
func TestExecutionSupervisor_ScanOncePrunesExpiredRuns(t *testing.T) {
	ctx := context.Background()
	now := gcNow
	supervisor, store := newTestExecutionSupervisor(t, "sup-prune", ExecutionSupervisorConfig{
		Enabled: true,
		Mode:    "observe",
	}, nil, &fakeDispatcher{failFirst: map[string]bool{}})
	supervisor.Now = func() time.Time { return now }

	expired := terminalRunFixture("run_gc_scan_1", 8*24*time.Hour)
	gcSeedRun(t, store, expired)

	_, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.False(t, gcRunExists(t, store, expired.RunID), "the scan is the GC's carrier")

	stats := supervisor.Stats()
	require.Equal(t, DefaultExecutionRunRetention, stats.Retention)
	require.EqualValues(t, 1, stats.PrunedTotal)
	require.EqualValues(t, 1, stats.LastPruneRemoved)
	require.Equal(t, now, stats.LastPruneAt)
	require.Empty(t, stats.LastPruneError)

	// Throttled: a scan inside the interval must not run the DELETE again.
	second := terminalRunFixture("run_gc_scan_2", 8*24*time.Hour)
	gcSeedRun(t, store, second)
	now = now.Add(time.Minute)
	_, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.True(t, gcRunExists(t, store, second.RunID))
	require.EqualValues(t, 1, supervisor.Stats().PrunedTotal)

	// After the interval elapses the backlog is picked up again.
	now = now.Add(executionRunPruneInterval)
	_, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.False(t, gcRunExists(t, store, second.RunID))
	require.EqualValues(t, 2, supervisor.Stats().PrunedTotal)
}
