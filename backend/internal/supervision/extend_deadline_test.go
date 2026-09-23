package supervision

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// extendClockNow is the deterministic "now" shared by the extend_deadline
// tests: deadline arithmetic must be exact instead of wall-clock dependent.
var extendClockNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func newExtendTestEnv(t *testing.T, name string) (*SQLiteSupervisionStore, *ActionService, *fakeActionExecutor) {
	t.Helper()
	store := newTestStore(t, name)
	executor := &fakeActionExecutor{}
	svc := NewActionService(store, executor, nil)
	svc.now = func() time.Time { return extendClockNow }
	return store, svc, executor
}

// extendRunFixture is an active agent_run with the declared 30m budget and the
// doc 6.5 deadline pair (execution 30m / progress 10m) the I5 ratios are
// computed against.
func extendRunFixture(runID string) ExecutionRun {
	executionDeadline := extendClockNow.Add(30 * time.Minute)
	progressDeadline := extendClockNow.Add(10 * time.Minute)
	return ExecutionRun{
		RunID:               runID,
		Kind:                RunKindAgentRun,
		Workflow:            RunWorkflowSpawnAgent,
		RootSessionID:       "root-session-1",
		ParentSessionID:     "root-session-1",
		SessionID:           "child-" + runID,
		AgentID:             "child-" + runID,
		Attempt:             1,
		Status:              RunStatusRunning,
		OwnerID:             "host-1",
		StartedAt:           extendClockNow,
		LastHeartbeatAt:     extendClockNow,
		LastProgressAt:      extendClockNow,
		ProgressSeq:         1,
		ExecutionDeadlineAt: &executionDeadline,
		ProgressDeadlineAt:  &progressDeadline,
		MaxAttempts:         1,
		FencingToken:        1,
		DeclaredBudget:      30 * time.Minute,
		Version:             1,
		CreatedAt:           extendClockNow,
		UpdatedAt:           extendClockNow,
	}
}

func extendSeedRun(t *testing.T, store *SQLiteSupervisionStore, run ExecutionRun) ExecutionRun {
	t.Helper()
	created, err := store.CreateExecutionRun(context.Background(), run)
	require.NoError(t, err)
	require.True(t, created)
	got, err := store.GetExecutionRun(context.Background(), run.RunID)
	require.NoError(t, err)
	require.NotNil(t, got)
	return *got
}

func extendRequestAction(t *testing.T, svc *ActionService, runID string, req ActionRequest) ActionRecord {
	t.Helper()
	req.RootScopeID = "root-session-1"
	req.RequestedByKind = "parent_session"
	req.RequestedByID = "root-session-1"
	req.TargetKind = SubjectAgentRun
	req.TargetID = runID
	req.Action = ActionExtendDeadline
	if strings.TrimSpace(req.Reason) == "" {
		req.Reason = "child still making progress"
	}
	record, err := svc.RequestAction(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, ActionRequested, record.Status)
	return record
}

// extendExecuteAction drives requested -> accepted -> terminal. ExecuteAction
// folds executor/validation failures into the terminal record (status=failed)
// instead of returning them, so callers assert on the record.
func extendExecuteAction(t *testing.T, svc *ActionService, actionID string) ActionRecord {
	t.Helper()
	accepted, err := svc.AcceptAction(context.Background(), actionID)
	require.NoError(t, err)
	require.Equal(t, ActionAccepted, accepted.Status)
	record, err := svc.ExecuteAction(context.Background(), actionID)
	require.NoError(t, err)
	return record
}

func extendLifecycleEvent(t *testing.T, store Store, runID, eventType string) Notification {
	t.Helper()
	list, err := store.ListNotifications(context.Background(), NotificationFilter{
		RootScopeID:     "root-session-1",
		SubjectKind:     SubjectAgentRun,
		SubjectID:       runID,
		IncludeResolved: true,
	})
	require.NoError(t, err)
	for _, n := range list {
		if n.EventType == eventType {
			return n
		}
	}
	require.Failf(t, "lifecycle event missing", "no %s notification for run %s", eventType, runID)
	return Notification{}
}

// AC-P0-2a: a legal extension moves exactly the deadlines named by extend_which,
// bumps the I5 counters, clears the decision window and leaves a parent-visible
// "已延长 ×N, +时长" lifecycle event.
func TestExtendDeadline_ExtendsSelectedDeadlinesAndProjectsEvent(t *testing.T) {
	store, svc, executor := newExtendTestEnv(t, "supervision-extend-a")
	ctx := context.Background()

	window := extendClockNow.Add(2 * time.Minute)
	run := extendRunFixture("run_extend_both")
	run.DecisionWindowUntil = &window
	seeded := extendSeedRun(t, store, run)
	seededTimeoutNotification(t, store, seeded.RunID)

	record := extendRequestAction(t, svc, seeded.RunID, ActionRequest{
		ExtendBy:    5 * time.Minute,
		ExtendWhich: "both",
	})
	terminal := extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionCompleted, terminal.Status)
	require.Contains(t, terminal.Result, "已延长 ×1")
	require.Contains(t, terminal.Result, "+5m0s")
	require.False(t, executor.called,
		"extend_deadline is a control-plane mutation, not a host-executor action")

	got, err := store.GetExecutionRun(ctx, seeded.RunID)
	require.NoError(t, err)
	require.True(t, extendClockNow.Add(35*time.Minute).Equal(*got.ExecutionDeadlineAt))
	require.True(t, extendClockNow.Add(15*time.Minute).Equal(*got.ProgressDeadlineAt))
	require.Equal(t, 1, got.ExtensionCount)
	require.Equal(t, 5*time.Minute, got.ExtendedTotal)
	require.Nil(t, got.DecisionWindowUntil,
		"the decision that triggered the extension spends the escalate-first window")

	extended := extendLifecycleEvent(t, store, seeded.RunID, "obligation.deadline.extended")
	require.Contains(t, extended.Reason, "已延长 ×1")
	require.Contains(t, extended.Reason, "+5m0s")
	require.Contains(t, extended.Reason, "child still making progress")

	// The event is visible on both parent-facing surfaces: the "resolved since
	// last turn" digest report carries the extension reason, and the row stays
	// durable for the UI/snapshot cursors (see the IncludeResolved lookup above).
	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Limit:                 20,
		IncludeResolvedSince:  true,
	})
	require.NoError(t, err)
	visible := 0
	for _, item := range digest.Items {
		if item.SubjectID == seeded.RunID && strings.Contains(item.Reason, "deadline extended (已延长 ×1, +5m0s)") {
			visible++
		}
	}
	require.Equal(t, 1, visible, "the extension must be visible in the parent digest")
	require.Contains(t, digest.Text, "已延长 ×1, +5m0s")

	// extend_which=progress with an absolute new_deadline moves only the soft
	// deadline: the hard (execution) deadline stays untouched.
	progressRun := extendRunFixture("run_extend_progress")
	seededProgress := extendSeedRun(t, store, progressRun)
	seededTimeoutNotification(t, store, seededProgress.RunID)
	newDeadline := extendClockNow.Add(25 * time.Minute)
	record = extendRequestAction(t, svc, seededProgress.RunID, ActionRequest{
		NewDeadline: &newDeadline,
		ExtendWhich: "progress",
	})
	terminal = extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionCompleted, terminal.Status)
	require.Contains(t, terminal.Result, "+15m0s")

	got, err = store.GetExecutionRun(ctx, seededProgress.RunID)
	require.NoError(t, err)
	require.True(t, extendClockNow.Add(30*time.Minute).Equal(*got.ExecutionDeadlineAt),
		"extend_which=progress must not move the execution deadline")
	require.True(t, newDeadline.Equal(*got.ProgressDeadlineAt))
	require.Equal(t, 1, got.ExtensionCount)
	require.Equal(t, 15*time.Minute, got.ExtendedTotal)
}

// AC-P0-2b: extend_deadline is a mutation action, so it goes through the same
// reason-required validation path as cancel/close and leaves no durable row when
// the reason is missing (I4).
func TestExtendDeadline_RequiresReason(t *testing.T) {
	store, svc, _ := newExtendTestEnv(t, "supervision-extend-reason")
	ctx := context.Background()
	run := extendSeedRun(t, store, extendRunFixture("run_extend_reason"))
	seededTimeoutNotification(t, store, run.RunID)

	_, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "root-session-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        run.RunID,
		Action:          ActionExtendDeadline,
		ExtendBy:        time.Minute,
	})
	require.ErrorIs(t, err, ErrActionInvalid)
	require.Contains(t, err.Error(), "reason is required")

	actions, err := store.ListActions(ctx, ActionFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Empty(t, actions, "a rejected request must not leave a durable action row")
}

// AC-P0-2c / I5: every cap (per-call 1×, ≤3 calls, ≤4× total) rejects with an
// explicit error carrying the stable next_action hint.
func TestExtendDeadline_RejectsBudgetOverrun(t *testing.T) {
	store, svc, _ := newExtendTestEnv(t, "supervision-extend-budget")
	ctx := context.Background()

	limits := DefaultExtensionLimits()
	require.Equal(t, 3, limits.MaxExtensions)
	require.Equal(t, 1.0, limits.MaxExtensionPerCall)
	require.Equal(t, 4.0, limits.MaxExtensionTotal)

	// Per-call cap: one call may not exceed 1× the declared 30m budget.
	perCall := extendSeedRun(t, store, extendRunFixture("run_extend_percall"))
	seededTimeoutNotification(t, store, perCall.RunID)
	record := extendRequestAction(t, svc, perCall.RunID, ActionRequest{ExtendBy: 45 * time.Minute})
	terminal := extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionFailed, terminal.Status)
	require.Contains(t, terminal.Result, "per-call cap")
	require.Contains(t, terminal.Result, HintExtensionBudgetExhausted)
	got, err := store.GetExecutionRun(ctx, perCall.RunID)
	require.NoError(t, err)
	require.Equal(t, 0, got.ExtensionCount)
	require.True(t, extendClockNow.Add(30*time.Minute).Equal(*got.ExecutionDeadlineAt),
		"a rejected extension must not move the deadline")

	// Call-count cap: the fourth call on one obligation is refused.
	counted := extendRunFixture("run_extend_count")
	counted.ExtensionCount = limits.MaxExtensions
	seededCounted := extendSeedRun(t, store, counted)
	seededTimeoutNotification(t, store, seededCounted.RunID)
	record = extendRequestAction(t, svc, seededCounted.RunID, ActionRequest{ExtendBy: time.Minute})
	terminal = extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionFailed, terminal.Status)
	require.Contains(t, terminal.Result, "extension budget exhausted")
	require.Contains(t, terminal.Result, HintExtensionBudgetExhausted)

	// Total cap: the obligation may not grow past 4× its original budget.
	total := extendRunFixture("run_extend_total")
	total.ExtensionCount = 2
	total.ExtendedTotal = time.Duration(limits.MaxExtensionTotal * float64(30*time.Minute))
	seededTotal := extendSeedRun(t, store, total)
	seededTimeoutNotification(t, store, seededTotal.RunID)
	record = extendRequestAction(t, svc, seededTotal.RunID, ActionRequest{ExtendBy: time.Minute})
	terminal = extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionFailed, terminal.Status)
	require.Contains(t, terminal.Result, "total cap")
	require.Contains(t, terminal.Result, HintExtensionBudgetExhausted)
}

// AC-P0-2d / I6: past the irreversible point the deadline no longer governs the
// run, so every extension is refused with the retry/reassign hint and the ledger
// stays untouched.
func TestExtendDeadline_RejectsIrreversiblePoints(t *testing.T) {
	store, svc, _ := newExtendTestEnv(t, "supervision-extend-irreversible")
	ctx := context.Background()

	cancelRequestedAt := extendClockNow.Add(-time.Minute)
	cases := []struct {
		name   string
		mutate func(*ExecutionRun)
	}{
		{name: "cancel_requested", mutate: func(run *ExecutionRun) { run.Status = RunStatusCancelRequested }},
		{name: "canceling", mutate: func(run *ExecutionRun) { run.Status = RunStatusCanceling }},
		{name: "terminal", mutate: func(run *ExecutionRun) { run.Status = RunStatusCanceled }},
		{name: "cancel_requested_at", mutate: func(run *ExecutionRun) { run.CancelRequestedAt = &cancelRequestedAt }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := extendRunFixture("run_extend_irreversible_" + tc.name)
			tc.mutate(&run)
			seeded := extendSeedRun(t, store, run)
			seededTimeoutNotification(t, store, seeded.RunID)

			record := extendRequestAction(t, svc, seeded.RunID, ActionRequest{ExtendBy: time.Minute})
			terminal := extendExecuteAction(t, svc, record.ActionID)
			require.Equal(t, ActionFailed, terminal.Status)
			require.Contains(t, terminal.Result, ErrActionInvalid.Error())
			require.Contains(t, terminal.Result, HintExtensionIrreversible)

			got, err := store.GetExecutionRun(ctx, seeded.RunID)
			require.NoError(t, err)
			require.Equal(t, 0, got.ExtensionCount)
			require.True(t, extendClockNow.Add(30*time.Minute).Equal(*got.ExecutionDeadlineAt),
				"an irreversible run keeps its deadline")
		})
	}

	// Only agent_run subjects own a run row, so a session target never announces
	// extend_deadline: the request is refused before anything could be extended.
	sessionRun := extendSeedRun(t, store, extendRunFixture("run_extend_kind"))
	seededTimeoutNotification(t, store, sessionRun.RunID)
	_, err := svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "root-session-1",
		TargetKind:      SubjectAgentSession,
		TargetID:        sessionRun.RunID,
		Action:          ActionExtendDeadline,
		ExtendBy:        time.Minute,
		Reason:          "still working",
	})
	require.ErrorIs(t, err, ErrActionNotAllowed,
		"a session target has no run row, so the action is not even allowed")
}

// racingRunStore injects a concurrent writer between the extension's load and
// its CAS write: that is exactly the EC-B1 race the plan requires the CAS to
// catch instead of losing an update.
type racingRunStore struct {
	*SQLiteSupervisionStore
	raceOnce bool
}

func (r *racingRunStore) GetExecutionRun(ctx context.Context, runID string) (*ExecutionRun, error) {
	run, err := r.SQLiteSupervisionStore.GetExecutionRun(ctx, runID)
	if err != nil || run == nil {
		return run, err
	}
	if r.raceOnce {
		r.raceOnce = false
		// A second extension wins the row after this read: it applies the very
		// same +5m the losing attempt is about to apply.
		concurrent := *run
		deadline := run.ExecutionDeadlineAt.Add(5 * time.Minute)
		concurrent.ExecutionDeadlineAt = &deadline
		concurrent.ExtensionCount = run.ExtensionCount + 1
		concurrent.ExtendedTotal = run.ExtendedTotal + 5*time.Minute
		ok, err := r.SQLiteSupervisionStore.UpdateExecutionRunCAS(ctx, concurrent, run.Version)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrRunConflict
		}
	}
	return run, nil
}

// AC-P0-2e: a concurrent writer makes the extension lose the CAS, and the losing
// attempt must be recorded as failed without half-applying (no lost update).
func TestExtendDeadline_ConcurrentExtensionLosesCASWithoutLostUpdate(t *testing.T) {
	store := newTestStore(t, "supervision-extend-cas")
	racing := &racingRunStore{SQLiteSupervisionStore: store, raceOnce: true}
	executor := &fakeActionExecutor{}
	svc := NewActionService(racing, executor, nil)
	svc.now = func() time.Time { return extendClockNow }
	ctx := context.Background()

	seeded := extendSeedRun(t, store, extendRunFixture("run_extend_cas"))
	seededTimeoutNotification(t, store, seeded.RunID)

	first := extendRequestAction(t, svc, seeded.RunID, ActionRequest{ExtendBy: 5 * time.Minute})
	raced := extendExecuteAction(t, svc, first.ActionID)
	require.Equal(t, ActionFailed, raced.Status)
	require.Contains(t, raced.Result, "changed while extending")

	// Exactly one extension won: the ledger carries the winner's single +5m,
	// not a double-applied +10m, and the loser left no partial write behind.
	got, err := store.GetExecutionRun(ctx, seeded.RunID)
	require.NoError(t, err)
	require.Equal(t, 1, got.ExtensionCount, "only the winning extension may count")
	require.Equal(t, 5*time.Minute, got.ExtendedTotal)
	require.True(t, extendClockNow.Add(35*time.Minute).Equal(*got.ExecutionDeadlineAt))
	require.Equal(t, seeded.Version+1, got.Version, "only the winner's version bump survives")

	// The losing attempt is durably recorded as a warning row (rule 7), which
	// narrows the subject's action set until the next escalation: re-extending
	// is a fresh decision after re-reading, never a blind replay of the CAS.
	_, err = svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "root-session-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        seeded.RunID,
		Action:          ActionExtendDeadline,
		ExtendBy:        5 * time.Minute,
		Reason:          "retry after re-read",
	})
	require.ErrorIs(t, err, ErrActionNotAllowed)
	require.Contains(t, err.Error(), "allowed=[inspect,acknowledge]")
}

// AC-P0-2f: the compatibility constraint (unknown actions keep the existing
// ErrActionInvalid path) still holds, and the new payload validation rides the
// same path.
func TestExtendDeadline_UnknownActionAndPayloadStayInvalid(t *testing.T) {
	store, svc, _ := newExtendTestEnv(t, "supervision-extend-unknown")
	ctx := context.Background()
	seeded := extendSeedRun(t, store, extendRunFixture("run_extend_unknown"))
	seededTimeoutNotification(t, store, seeded.RunID)

	base := ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "root-session-1",
		TargetKind:      SubjectAgentRun,
		TargetID:        seeded.RunID,
		Reason:          "still working",
	}

	unknown := base
	unknown.Action = ActionKind("teleport")
	_, err := svc.RequestAction(ctx, unknown)
	require.ErrorIs(t, err, ErrActionInvalid)

	missing := base
	missing.Action = ActionExtendDeadline
	_, err = svc.RequestAction(ctx, missing)
	require.ErrorIs(t, err, ErrActionInvalid)
	require.Contains(t, err.Error(), "requires extend_by or new_deadline")

	deadline := extendClockNow.Add(time.Hour)
	both := base
	both.Action = ActionExtendDeadline
	both.ExtendBy = time.Minute
	both.NewDeadline = &deadline
	_, err = svc.RequestAction(ctx, both)
	require.ErrorIs(t, err, ErrActionInvalid)
	require.Contains(t, err.Error(), "not both")

	badWhich := base
	badWhich.Action = ActionExtendDeadline
	badWhich.ExtendBy = time.Minute
	badWhich.ExtendWhich = "sometimes"
	_, err = svc.RequestAction(ctx, badWhich)
	require.ErrorIs(t, err, ErrActionInvalid)
	require.Contains(t, err.Error(), "extend_which")

	// Nothing above may leave a durable action behind.
	actions, err := store.ListActions(ctx, ActionFilter{RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Empty(t, actions)
}
