package supervision

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeInterrupter struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeInterrupter) InterruptRun(ctx context.Context, run ExecutionRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, run.RunID)
	return nil
}

func (f *fakeInterrupter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeDispatcher struct {
	mu        sync.Mutex
	seq       int64
	failFirst map[string]bool
	delivered []string
}

func (f *fakeDispatcher) DispatchCompletion(ctx context.Context, entry CompletionOutboxEntry) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFirst[entry.OutboxID] {
		delete(f.failFirst, entry.OutboxID)
		return 0, errors.New("parent mailbox temporarily unavailable")
	}
	f.seq++
	f.delivered = append(f.delivered, entry.OutboxID)
	return f.seq, nil
}

func newTestExecutionSupervisor(t *testing.T, name string, cfg ExecutionSupervisorConfig, interrupter RunInterrupter, dispatcher CompletionDispatcher) (*ExecutionSupervisor, *SQLiteSupervisionStore) {
	t.Helper()
	store := testExecutionRunStore(t, name)
	supervisor := &ExecutionSupervisor{
		Store:       store,
		StoreFull:   store,
		Config:      cfg,
		Interrupter: interrupter,
		Dispatcher:  dispatcher,
	}
	return supervisor, store
}

func TestExecutionSupervisor_StartRunResolvesDeadlines(t *testing.T) {
	supervisor, _ := newTestExecutionSupervisor(t, "sup-start", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 30 * time.Minute,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, nil, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:      RunWorkflowSpawnAgent,
		RootSessionID: "root-session",
		ParentSessionID: "parent-session",
		SessionID:     "child-1",
		AgentID:       "child-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, run.RunID)
	require.True(t, len(run.RunID) > 4 && run.RunID[:4] == "run_")
	require.Equal(t, RunStatusQueued, run.Status)
	require.Equal(t, int64(1), run.FencingToken)
	require.NotNil(t, run.ExecutionDeadlineAt)
	require.WithinDuration(t, now.Add(30*time.Minute), *run.ExecutionDeadlineAt, time.Second)
	require.WithinDuration(t, now.Add(5*time.Minute), *run.ProgressDeadlineAt, time.Second)
	require.WithinDuration(t, now.Add(1*time.Hour), *run.ApprovalDeadlineAt, time.Second)

	// Explicit per-run timeout wins over the default.
	explicit, err := supervisor.StartRun(ctx, RunSpec{
		SessionID:        "child-2",
		ExecutionTimeout: 90 * time.Second,
	})
	require.NoError(t, err)
	require.WithinDuration(t, now.Add(90*time.Second), *explicit.ExecutionDeadlineAt, time.Second)
	// Progress/approval still use defaults.
	require.WithinDuration(t, now.Add(5*time.Minute), *explicit.ProgressDeadlineAt, time.Second)

	// allow_unbounded=true + explicit zero -> no deadline.
	unboundedSupervisor, _ := newTestExecutionSupervisor(t, "sup-unbounded", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		AllowUnbounded:          true,
		DefaultExecutionTimeout: 30 * time.Minute,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, nil, nil)
	unboundedSupervisor.Now = func() time.Time { return now }
	unbounded, err := unboundedSupervisor.StartRun(ctx, RunSpec{
		SessionID:        "child-3",
		ExecutionTimeout: 0,
	})
	require.NoError(t, err)
	require.Nil(t, unbounded.ExecutionDeadlineAt)

	// allow_unbounded=false + explicit zero -> operator default, not forever.
	runZero, err := supervisor.StartRun(ctx, RunSpec{SessionID: "child-4"})
	require.NoError(t, err)
	require.NotNil(t, runZero.ExecutionDeadlineAt)
}

func TestExecutionSupervisor_DeadlineInterruptsBlockingRun(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-deadline", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 10 * time.Second,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		Workflow:         RunWorkflowSpawnAgent,
		RootSessionID:    "root-session",
		ParentSessionID:  "parent-session",
		SessionID:        "child-1",
		ExecutionTimeout: 10 * time.Second,
	})
	require.NoError(t, err)

	// Run starts (progress recorded), then the provider blocks.
	_, err = supervisor.RecordProgress(ctx, RunProgressEvent{RunID: run.RunID, Kind: "session_start"})
	require.NoError(t, err)

	// Before deadline: healthy, no decision.
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, decisions)
	require.Equal(t, 0, interrupter.count())

	// Past deadline: enforce requests cancel and interrupts.
	supervisor.Now = func() time.Time { return now.Add(11 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "execution_timed_out", decisions[0].Decision)
	require.Equal(t, "interrupted", decisions[0].ActionTaken)
	require.Equal(t, 1, interrupter.count())

	// Run is now cancel_requested with cancel deadline persisted.
	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, got.Status)
	require.Equal(t, "execution_timed_out", got.CancelSource)
	require.NotNil(t, got.CancelDeadlineAt)

	// Critical lifecycle notification was projected.
	notifications, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, SeverityCritical, notifications[0].Severity)
	require.Equal(t, "execution_timed_out", notifications[0].EventType)
}

func TestExecutionSupervisor_WaitingApprovalNotKilledByProgressTimeout(t *testing.T) {
	supervisor, _ := newTestExecutionSupervisor(t, "sup-approval", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 30 * time.Minute,
		DefaultProgressTimeout:  1 * time.Second,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, &fakeInterrupter{}, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID: "root-session",
		SessionID:     "child-1",
	})
	require.NoError(t, err)
	// Run transitions to waiting_approval (its own deadline dimension).
	run.Status = RunStatusWaitingApproval
	ok, err := supervisor.Store.UpdateExecutionRunCAS(ctx, *run, run.Version)
	require.NoError(t, err)
	require.True(t, ok)

	// Progress deadline long past, approval deadline still ahead: healthy.
	supervisor.Now = func() time.Time { return now.Add(30 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, decisions)

	// Approval deadline passes: independent timeout fires.
	supervisor.Now = func() time.Time { return now.Add(2 * time.Hour) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "approval_timeout", decisions[0].Decision)
}

func TestExecutionSupervisor_ObserveModeRecordsWithoutCancel(t *testing.T) {
	supervisor, store := newTestExecutionSupervisor(t, "sup-observe", ExecutionSupervisorConfig{
		Mode:                    "observe",
		DefaultExecutionTimeout: 10 * time.Second,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, &fakeInterrupter{}, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID: "root-session",
		SessionID:     "child-1",
	})
	require.NoError(t, err)
	// Host marks the run started (session_start) before work begins.
	run.Status = RunStatusRunning
	ok, err := supervisor.Store.UpdateExecutionRunCAS(ctx, *run, run.Version)
	require.NoError(t, err)
	require.True(t, ok)

	supervisor.Now = func() time.Time { return now.Add(30 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "none_observe", decisions[0].ActionTaken)

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusRunning, got.Status)

	// The decision is still durably visible to the parent control plane.
	notifications, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, "execution_timed_out", notifications[0].EventType)
}

func TestExecutionSupervisor_CancelGraceExpiryFencesOrphaned(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-orphan", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 10 * time.Second,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      5 * time.Second,
	}, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-1",
	})
	require.NoError(t, err)

	// Deadline fires, interrupt sent.
	supervisor.Now = func() time.Time { return now.Add(11 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, 1, interrupter.count())

	// Actor never exits: past cancel grace the run is fenced orphaned and the
	// fencing token bumps so late writes lose.
	supervisor.Now = func() time.Time { return now.Add(20 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "cancel_grace_expired", decisions[0].Decision)
	require.Equal(t, "orphaned", decisions[0].ActionTaken)

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusOrphaned, got.Status)
	require.Equal(t, int64(2), got.FencingToken)
}

func TestExecutionSupervisor_CompleteRunIdempotentOutbox(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-complete", ExecutionSupervisorConfig{
		Mode: "enforce",
	}, nil, dispatcher)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-1",
	})
	require.NoError(t, err)

	payload := map[string]interface{}{"agent_id": "child-1", "result": "ok"}
	err = supervisor.CompleteRun(ctx, run.RunID, RunStatusSucceeded, "", "result-ref", payload)
	require.NoError(t, err)

	// Second completion with the same status is idempotent (no duplicate outbox).
	err = supervisor.CompleteRun(ctx, run.RunID, RunStatusSucceeded, "", "result-ref", payload)
	require.NoError(t, err)

	pending, err := store.ListUndeliveredOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	firstKey := pending[0].IdempotencyKey
	require.Contains(t, firstKey, "subagent_completion:"+run.RunID+":")

	// Dispatch delivers once.
	err = supervisor.DispatchPendingOutbox(ctx)
	require.NoError(t, err)
	dispatcher.mu.Lock()
	require.Len(t, dispatcher.delivered, 1)
	dispatcher.mu.Unlock()
	pending, err = store.ListUndeliveredOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 0)
}

func TestExecutionSupervisor_OutboxRedeliveryAfterCrashWindow(t *testing.T) {
	dispatcher := &fakeDispatcher{failFirst: map[string]bool{}}
	supervisor, store := newTestExecutionSupervisor(t, "sup-redeliver", ExecutionSupervisorConfig{
		Mode: "enforce",
	}, nil, dispatcher)
	ctx := context.Background()

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-1",
	})
	require.NoError(t, err)
	err = supervisor.CompleteRun(ctx, run.RunID, RunStatusSucceeded, "", "", nil)
	require.NoError(t, err)

	// First delivery attempt fails (simulates parent mailbox append crash
	// window), outbox entry must remain pending.
	dispatcher.failFirst["outbox_"+run.RunID+"_"+RunStatusSucceeded] = true
	err = supervisor.DispatchPendingOutbox(ctx)
	require.NoError(t, err)
	pending, err := store.ListUndeliveredOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, 1, pending[0].Attempts)
	require.NotEmpty(t, pending[0].LastError)

	// Restart: the scanner retries and delivers exactly once.
	err = supervisor.DispatchPendingOutbox(ctx)
	require.NoError(t, err)
	dispatcher.mu.Lock()
	require.Len(t, dispatcher.delivered, 1)
	dispatcher.mu.Unlock()
	pending, err = store.ListUndeliveredOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 0)
}

// Change #1 / AC-P0-1a + AC-P0-1c: crossing the soft threshold must report to
// the parent (critical + action_required) instead of cancelling the child, and
// only an exhausted decision window may fire the fallback cancel.
func TestExecutionSupervisor_ProgressStallEscalatesBeforeCancelling(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-escalate-first", ExecutionSupervisorConfig{
		Mode:                      "enforce",
		DefaultExecutionTimeout:   1 * time.Hour,
		DefaultProgressTimeout:    5 * time.Second,
		DefaultApprovalTimeout:    1 * time.Hour,
		DefaultCancelGrace:        15 * time.Second,
		StallEscalationMultiplier: 2,
		DecisionWindow:            10 * time.Second,
		DecisionWindowMax:         20 * time.Second,
	}, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-escalate",
	})
	require.NoError(t, err)

	// Soft threshold crossed (progress deadline at +5s) but still inside the
	// piggyback band (+5s..+10s): nothing happens, the run keeps running.
	supervisor.Now = func() time.Time { return now.Add(6 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, decisions)
	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, got.Status)
	require.Nil(t, got.DecisionWindowUntil)

	// Escalation instant (deadline + 1x soft = +10s): report, never cancel.
	supervisor.Now = func() time.Time { return now.Add(10 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "progress_stalled", decisions[0].Decision)
	require.Equal(t, "escalated", decisions[0].ActionTaken)
	require.Zero(t, interrupter.count(), "escalate-first never interrupts on report")

	got, err = store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, got.Status, "the run keeps running while the parent decides")
	require.NotNil(t, got.DecisionWindowUntil)
	require.WithinDuration(t, now.Add(20*time.Second), *got.DecisionWindowUntil, time.Second)

	notifications, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, SeverityCritical, notifications[0].Severity)
	require.Equal(t, "progress_stalled", notifications[0].EventType)
	require.True(t, notifications[0].ActionRequired())

	// Inside the decision window the ladder stays quiet: no re-report, no cancel.
	supervisor.Now = func() time.Time { return now.Add(15 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, decisions)
	require.Zero(t, interrupter.count())

	// Window exhausted without a decision -> the fallback fires.
	supervisor.Now = func() time.Time { return now.Add(21 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "progress_stalled", decisions[0].Decision)
	require.Equal(t, "interrupted", decisions[0].ActionTaken)
	require.Equal(t, 1, interrupter.count())

	got, err = store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, got.Status)
	require.Equal(t, "decision_window_expired", got.CancelSource)
}

// Change #1 / AC-P0-1d: the hard execution deadline keeps its own branch above
// the escalation ladder - it fires immediately, never waits for a decision, and
// still projects the critical notification.
func TestExecutionSupervisor_HardDeadlineBeatsEscalation(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-hard-deadline-first", ExecutionSupervisorConfig{
		Mode:                      "enforce",
		DefaultExecutionTimeout:   6 * time.Second,
		DefaultProgressTimeout:    5 * time.Second,
		DefaultApprovalTimeout:    1 * time.Hour,
		DefaultCancelGrace:        15 * time.Second,
		StallEscalationMultiplier: 2,
		DecisionWindow:            10 * time.Second,
		DecisionWindowMax:         20 * time.Second,
	}, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-hard-deadline",
	})
	require.NoError(t, err)

	// Past the hard deadline (and therefore also past the soft one): the hard
	// branch wins and the run is cancelled in this very scan.
	supervisor.Now = func() time.Time { return now.Add(7 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "execution_timed_out", decisions[0].Decision)
	require.Equal(t, "interrupted", decisions[0].ActionTaken)
	require.Equal(t, 1, interrupter.count())

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, got.Status)
	require.Equal(t, "execution_timed_out", got.CancelSource)
	require.Nil(t, got.DecisionWindowUntil, "the hard branch never opens a decision window")

	notifications, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, SeverityCritical, notifications[0].Severity)
	require.Equal(t, "execution_timed_out", notifications[0].EventType)
}

// Change #1 / AC-P0-1e (I8, design doc EC-A3): the decision window is measured
// on the runnable clock. While the parent cannot be woken the window does not
// burn down, but the wall-clock cap (progress deadline + DecisionWindowMax)
// still bounds the deferral so the fallback stays decidable.
func TestExecutionSupervisor_DecisionWindowUsesRunnableClock(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-runnable-clock", ExecutionSupervisorConfig{
		Mode:                      "enforce",
		DefaultExecutionTimeout:   1 * time.Hour,
		DefaultProgressTimeout:    5 * time.Second,
		DefaultApprovalTimeout:    1 * time.Hour,
		DefaultCancelGrace:        15 * time.Second,
		StallEscalationMultiplier: 2,
		DecisionWindow:            10 * time.Second,
		DecisionWindowMax:         20 * time.Second,
	}, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	// The parent is busy (user input / approval / compacting) for the whole test.
	checks := 0
	supervisor.ParentRunnable = func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool {
		checks++
		require.Equal(t, "root-session", rootScopeID)
		require.Equal(t, "parent-session", parentSessionID)
		return false
	}

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-runnable-clock",
	})
	require.NoError(t, err)

	// Escalate at +10s -> window until +20s.
	supervisor.Now = func() time.Time { return now.Add(10 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "escalated", decisions[0].ActionTaken)

	// The window expired, but the parent could never be woken: no fallback yet,
	// the window is pushed forward instead (bounded by the cap at +25s).
	supervisor.Now = func() time.Time { return now.Add(21 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, decisions)
	require.Zero(t, interrupter.count())

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, got.Status)
	require.NotNil(t, got.DecisionWindowUntil)
	require.WithinDuration(t, now.Add(25*time.Second), *got.DecisionWindowUntil, time.Second)
	require.Positive(t, checks, "the ladder must consult the runnable clock")

	// Past the wall-clock cap the deferral ends and the fallback fires, even
	// though the parent is still not runnable.
	supervisor.Now = func() time.Time { return now.Add(26 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "interrupted", decisions[0].ActionTaken)

	got, err = store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, got.Status)
	require.Equal(t, "decision_window_expired", got.CancelSource)
}

// Change #1 ladder x change #2 hand-off (AC-P0-1b / EC-A5): once extend_deadline
// moves the progress deadline the old window must not be allowed to cancel the
// run - it is retired, and a second stall escalates afresh instead of firing a
// fallback for a stall that was never reported.
func TestExecutionSupervisor_ExtendedDeadlineRetiresStaleDecisionWindow(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-extend-retires-window", ExecutionSupervisorConfig{
		Mode:                      "enforce",
		DefaultExecutionTimeout:   1 * time.Hour,
		DefaultProgressTimeout:    5 * time.Second,
		DefaultApprovalTimeout:    1 * time.Hour,
		DefaultCancelGrace:        15 * time.Second,
		StallEscalationMultiplier: 2,
		DecisionWindow:            10 * time.Second,
		DecisionWindowMax:         20 * time.Second,
	}, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-extend",
	})
	require.NoError(t, err)

	supervisor.Now = func() time.Time { return now.Add(10 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "escalated", decisions[0].ActionTaken)

	// The parent decides "keep waiting" and extends the progress deadline (the
	// change #2 write path): the window outlives its stall.
	extended, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	newDeadline := now.Add(1 * time.Hour)
	updated := *extended
	updated.ProgressDeadlineAt = &newDeadline
	updated.ExtensionCount++
	ok, err := store.UpdateExecutionRunCAS(ctx, updated, extended.Version)
	require.NoError(t, err)
	require.True(t, ok)

	// Past the old window: the run is no longer stalled, so the stale window is
	// retired instead of cancelling a run the parent just re-armed.
	supervisor.Now = func() time.Time { return now.Add(21 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, decisions)
	require.Zero(t, interrupter.count())

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, got.Status)
	require.Nil(t, got.DecisionWindowUntil)

	// Second stall: it escalates afresh (report + fresh window), never a silent
	// cancel inherited from the first episode.
	secondDeadline := now.Add(30 * time.Second)
	updated = *got
	updated.ProgressDeadlineAt = &secondDeadline
	ok, err = store.UpdateExecutionRunCAS(ctx, updated, got.Version)
	require.NoError(t, err)
	require.True(t, ok)

	// The second budget is 30s, so its own escalation instant is deadline + 1x
	// soft = +60s.
	supervisor.Now = func() time.Time { return now.Add(61 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "escalated", decisions[0].ActionTaken)
	require.Zero(t, interrupter.count())

	got, err = store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, got.Status)
	require.NotNil(t, got.DecisionWindowUntil)
	require.WithinDuration(t, now.Add(71*time.Second), *got.DecisionWindowUntil, time.Second)
}

// Change #1 rollback switch (#8 EscalateFirst=false): the pre-#1 forced branch
// is restored without rolling back the binary.
func TestExecutionSupervisor_EscalateFirstRollbackSwitchKeepsLegacyCancel(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-escalate-first-off", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 1 * time.Hour,
		DefaultProgressTimeout:  5 * time.Second,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
		EscalateFirst:           boolPtr(false),
	}, interrupter, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-legacy-cancel",
	})
	require.NoError(t, err)

	// Just past the soft threshold: the legacy branch cancels right away.
	supervisor.Now = func() time.Time { return now.Add(6 * time.Second) }
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "progress_stalled", decisions[0].Decision)
	require.Equal(t, "interrupted", decisions[0].ActionTaken)

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, got.Status)
	require.Equal(t, "progress_stalled", got.CancelSource)
	require.Nil(t, got.DecisionWindowUntil)
}

func TestExecutionSupervisor_ResolveDeadlineUnboundedDisabled(t *testing.T) {
	now := time.Now().UTC()
	// allowUnbounded=false: zero maps to operator default.
	d := resolveDeadline(0, 30*time.Minute, false, now)
	require.NotNil(t, d)
	require.WithinDuration(t, now.Add(30*time.Minute), *d, time.Second)
	// allowUnbounded=true: zero means no deadline.
	require.Nil(t, resolveDeadline(0, 30*time.Minute, true, now))
	// Explicit positive wins.
	d = resolveDeadline(5*time.Minute, 30*time.Minute, true, now)
	require.NotNil(t, d)
	require.WithinDuration(t, now.Add(5*time.Minute), *d, time.Second)
}

// C0-2 / AC-C0-2a + AC-C0-2b + AC-C0-2d: a run that is still active without a
// decidable deadline can never be judged terminal by the ordinary ladder, so
// the watchdog must judge it terminal in a single scan cycle, record the cancel
// source and project exactly one critical + action_required row.
func TestExecutionSupervisor_WatchdogForcesTerminalWhenDeadlineMissing(t *testing.T) {
	supervisor, store := newTestExecutionSupervisor(t, "sup-watchdog-missing-deadline", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		AllowUnbounded:          true,
		DefaultExecutionTimeout: 30 * time.Minute,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, nil, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	// Legacy admission path (RequireExecutionDeadline unset) can still persist a
	// run without an execution deadline; the watchdog is the safety net for it.
	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-1",
	})
	require.NoError(t, err)
	require.Nil(t, run.ExecutionDeadlineAt)

	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "deadline_missing", decisions[0].Decision)
	require.Equal(t, "forced_terminal", decisions[0].ActionTaken)

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusAbandoned, got.Status)
	require.True(t, got.Terminal())
	require.Equal(t, "deadline_missing", got.CancelSource)
	require.NotNil(t, got.FinishedAt)
	// Fencing token bumps so late writes from the lost run can no longer win.
	require.Equal(t, int64(2), got.FencingToken)

	notifications, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-session"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, SeverityCritical, notifications[0].Severity)
	require.True(t, notifications[0].ActionRequired())
	require.Equal(t, "deadline_missing", notifications[0].EventType)

	// AC-C0-2d: the judgment is idempotent. Re-running the force path neither
	// writes a second transition nor bumps the fencing token/version again.
	before := *got
	require.True(t, supervisor.forceTerminalRun(ctx, &before, RunStatusAbandoned, "deadline_missing", now.Add(time.Minute)))
	after, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, got.FencingToken, after.FencingToken)
	require.Equal(t, got.Version, after.Version)
	require.Equal(t, got.FinishedAt.UTC(), after.FinishedAt.UTC())

	// A terminal run is no longer scanned as active, so no second decision.
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, decisions)
}

// C0-2 / AC-C0-2a: a cancel_requested run whose cancel deadline was never
// persisted can never reach the ladder's cancel_grace_expired branch; the
// watchdog judges it terminal within one scan cycle.
func TestExecutionSupervisor_WatchdogForcesTerminalWhenCancelDeadlineMissing(t *testing.T) {
	supervisor, store := newTestExecutionSupervisor(t, "sup-watchdog-missing-cancel-deadline", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 10 * time.Second,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, nil, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-1",
	})
	require.NoError(t, err)

	requested, err := store.RequestExecutionCancel(ctx, run.RunID, "operator_cancel", 15*time.Second, now)
	require.NoError(t, err)
	require.True(t, requested)
	current, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, current.Status)
	current.CancelDeadlineAt = nil
	ok, err := store.UpdateExecutionRunCAS(ctx, *current, current.Version)
	require.NoError(t, err)
	require.True(t, ok)

	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "cancel_deadline_missing", decisions[0].Decision)
	require.Equal(t, "forced_terminal", decisions[0].ActionTaken)

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusOrphaned, got.Status)
	require.Equal(t, "cancel_deadline_missing", got.CancelSource)
	require.True(t, got.Terminal())
}

// C0-2 / AC-C0-2a: a waiting state governed by an approval deadline that was
// never persisted is equally undecidable and must be judged terminal.
func TestExecutionSupervisor_WatchdogForcesTerminalWhenApprovalDeadlineMissing(t *testing.T) {
	supervisor, store := newTestExecutionRunStoreForApprovalWatchdog(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:   "root-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-1",
	})
	require.NoError(t, err)
	run.Status = RunStatusWaitingApproval
	run.ApprovalDeadlineAt = nil
	ok, err := store.UpdateExecutionRunCAS(ctx, *run, run.Version)
	require.NoError(t, err)
	require.True(t, ok)

	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "approval_deadline_missing", decisions[0].Decision)

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusAbandoned, got.Status)
	require.Equal(t, "approval_deadline_missing", got.CancelSource)
}

func newTestExecutionRunStoreForApprovalWatchdog(t *testing.T) (*ExecutionSupervisor, *SQLiteSupervisionStore) {
	t.Helper()
	return newTestExecutionSupervisor(t, "sup-watchdog-missing-approval-deadline", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 10 * time.Second,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, nil, nil)
}

// C0-2 / AC-C0-2c: every status of the §16.4 terminal enumeration must be
// judged terminal by join, and no live status may be mistaken for terminal.
func TestJoinTerminalStatusesCoverDesignDocEnumeration(t *testing.T) {
	docEnumeration := []string{
		"completed",
		"completed_with_failures",
		"succeeded", // legacy spelling, must stay terminal
		"failed",
		"canceled",
		"timed_out",
		"orphaned",
		"rejected",
		"superseded",
		"abandoned",
	}
	for _, status := range docEnumeration {
		require.Truef(t, IsJoinTerminalStatus(status), "status %q must be terminal for join", status)
		require.Truef(t, RunStatusTerminal(status), "status %q must be terminal", status)
		require.Contains(t, JoinTerminalStatuses(), status)
	}
	for _, status := range JoinTerminalStatuses() {
		require.Truef(t, RunStatusTerminal(status), "enumerated status %q must be terminal", status)
		require.Falsef(t, RunStatusActive(status), "enumerated status %q must not be active", status)
	}
	for _, status := range []string{
		RunStatusQueued, RunStatusRunning, RunStatusWaitingApproval,
		RunStatusWaitingInput, RunStatusCancelRequested, RunStatusCanceling,
	} {
		require.Falsef(t, IsJoinTerminalStatus(status), "live status %q must not be terminal", status)
	}
}

// C0-2 / AC-C0-2e: I2 admission validation rejects a dispatch that would be
// persisted without an execution deadline.
func TestExecutionSupervisor_RequireExecutionDeadlineRejectsDispatch(t *testing.T) {
	supervisor, store := newTestExecutionSupervisor(t, "sup-require-deadline", ExecutionSupervisorConfig{
		Mode:                     "enforce",
		AllowUnbounded:           true,
		RequireExecutionDeadline: true,
		DefaultCancelGrace:       15 * time.Second,
	}, nil, nil)
	ctx := context.Background()

	_, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:    "root-session",
		ParentSessionID:  "parent-session",
		SessionID:        "child-1",
		ExecutionTimeout: 0,
	})
	require.ErrorIs(t, err, ErrExecutionDeadlineRequired)

	runs, err := store.ListExecutionRunsBySession(ctx, "child-1", 10)
	require.NoError(t, err)
	require.Empty(t, runs, "a rejected dispatch must not leave a run row behind")

	allowed, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:    "root-session",
		ParentSessionID:  "parent-session",
		SessionID:        "child-2",
		ExecutionTimeout: 90 * time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, allowed.ExecutionDeadlineAt)
}

// AC-P0-3c: the dispatching turn, declared budget and decision window are
// persisted with the run record so same-turn resume and extend_deadline can key
// on them, while runs that never declare them stay zero-value compatible.
func TestExecutionSupervisor_StartRunPersistsTurnLedgerFields(t *testing.T) {
	supervisor, store := newTestExecutionSupervisor(t, "sup-turn-ledger", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 30 * time.Minute,
		DefaultProgressTimeout:  5 * time.Minute,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
	}, nil, nil)
	now := time.Now().UTC().Truncate(time.Second)
	supervisor.Now = func() time.Time { return now }
	ctx := context.Background()

	window := now.Add(10 * time.Minute)
	run, err := supervisor.StartRun(ctx, RunSpec{
		RootSessionID:       "root-session",
		ParentSessionID:     "parent-session",
		SessionID:           "child-turn",
		TurnID:              "  turn-9  ",
		DeclaredBudget:      20 * time.Minute,
		DecisionWindowUntil: &window,
	})
	require.NoError(t, err)

	stored, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, "turn-9", stored.TurnID, "turn id is trimmed before persisting")
	require.Equal(t, 20*time.Minute, stored.DeclaredBudget)
	require.NotNil(t, stored.DecisionWindowUntil)
	require.WithinDuration(t, window, *stored.DecisionWindowUntil, time.Second)
	require.Zero(t, stored.ExtensionCount)
	require.Zero(t, stored.ExtendedTotal)

	// Runs that do not declare turn-scoped fields keep the legacy zero values.
	plain, err := supervisor.StartRun(ctx, RunSpec{SessionID: "child-plain"})
	require.NoError(t, err)
	require.Empty(t, plain.TurnID)
	require.Zero(t, plain.DeclaredBudget)
	require.Nil(t, plain.DecisionWindowUntil)
}
