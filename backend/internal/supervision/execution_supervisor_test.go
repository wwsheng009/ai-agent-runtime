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

func TestExecutionSupervisor_ProgressStalledDecision(t *testing.T) {
	interrupter := &fakeInterrupter{}
	supervisor, store := newTestExecutionSupervisor(t, "sup-stalled", ExecutionSupervisorConfig{
		Mode:                    "enforce",
		DefaultExecutionTimeout: 1 * time.Hour,
		DefaultProgressTimeout:  5 * time.Second,
		DefaultApprovalTimeout:  1 * time.Hour,
		DefaultCancelGrace:      15 * time.Second,
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

	// Activity keeps the run healthy past the progress deadline.
	supervisor.Now = func() time.Time { return now.Add(4 * time.Second) }
	_, err = supervisor.RecordProgress(ctx, RunProgressEvent{RunID: run.RunID, Kind: "tool_call_end"})
	require.NoError(t, err)
	decisions, err := supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Empty(t, decisions)

	// No progress for > 5s -> stalled -> interrupt.
	supervisor.Now = func() time.Time { return now.Add(10 * time.Second) }
	decisions, err = supervisor.ScanOnce(ctx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "progress_stalled", decisions[0].Decision)
	require.Equal(t, 1, interrupter.count())

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusCancelRequested, got.Status)
	require.Equal(t, "progress_stalled", got.CancelSource)
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
