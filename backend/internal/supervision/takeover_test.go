package supervision

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// takeoverRequestAction submits one control action from an explicit actor, so a
// test can act as a session that does not own the run (§6.11).
func takeoverRequestAction(t *testing.T, svc *ActionService, actor string, req ActionRequest) ActionRecord {
	t.Helper()
	req.RootScopeID = "root-session-1"
	req.RequestedByKind = "parent_session"
	req.RequestedByID = actor
	req.TargetKind = SubjectAgentRun
	if strings.TrimSpace(req.Reason) == "" {
		req.Reason = "owner host-1 stopped heartbeating"
	}
	record, err := svc.RequestAction(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, ActionRequested, record.Status)
	return record
}

// takeoverFreshDecision projects the next supervision cycle's decision for the
// run. Every terminal mutation is folded into an "action_*_resolution" row
// (doc 6.6 rule 7) whose AutoActionID narrows the subject to
// inspect/acknowledge, so a later decision row is what re-opens it for
// mutation; the tests seed one instead of assuming the consumed row stays
// actionable.
func takeoverFreshDecision(t *testing.T, store Store, runID, eventType string, seq int64) Notification {
	t.Helper()
	n := testNotification(runID, seq)
	n.EventType = eventType
	n.Reason = "run stopped reporting progress"
	n.SupervisionState = SupervisionStalled
	created, err := store.UpsertNotification(context.Background(), n)
	require.NoError(t, err)
	return created
}

// AC-P3-2c: takeover is the explicit, audited ownership override — it moves
// owner_id to the acting session, advances the fencing token (EC-B9) and leaves
// both the durable action row and the parent-visible lifecycle event carrying
// the actor and the reason (I4).
func TestTakeover_ClaimsOwnershipWithAuditTrail(t *testing.T) {
	store, svc, executor := newExtendTestEnv(t, "supervision-takeover-a")
	ctx := context.Background()

	lease := extendClockNow.Add(5 * time.Minute)
	run := extendRunFixture("run_takeover_a")
	run.OwnerID = "host-1"
	run.OwnerLeaseUntil = &lease
	seeded := extendSeedRun(t, store, run)
	seededTimeoutNotification(t, store, seeded.RunID)

	record := takeoverRequestAction(t, svc, "session-2", ActionRequest{
		TargetID: seeded.RunID,
		Action:   ActionTakeover,
		Reason:   "owner host-1 stopped heartbeating",
	})
	terminal := extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionCompleted, terminal.Status)
	require.Contains(t, terminal.Result, "host-1 -> session-2")
	require.Contains(t, terminal.Result, "fencing_token=2")
	require.False(t, executor.called,
		"takeover is a control-plane mutation, not a host-executor action")

	claimed, err := store.GetExecutionRun(ctx, seeded.RunID)
	require.NoError(t, err)
	require.Equal(t, "session-2", claimed.OwnerID)
	require.EqualValues(t, 2, claimed.FencingToken,
		"the fencing token advances so the previous owner's in-flight writes lose their CAS")
	require.EqualValues(t, 2, claimed.Version)
	require.NotNil(t, claimed.OwnerLeaseUntil)
	require.True(t, claimed.OwnerLeaseUntil.After(extendClockNow), "the new owner holds a live lease")

	audit, err := svc.GetAction(ctx, record.ActionID)
	require.NoError(t, err)
	require.Equal(t, "session-2", audit.RequestedByID, "the action row records who took over")
	require.Equal(t, "owner host-1 stopped heartbeating", audit.Reason)

	event := extendLifecycleEvent(t, store, seeded.RunID, "obligation.ownership.taken_over")
	require.Equal(t, SeverityWarning, event.Severity)
	require.Contains(t, event.Reason, "host-1 -> session-2")
	require.Contains(t, event.Reason, "reason=owner host-1 stopped heartbeating")

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Limit:                 20,
		IncludeResolvedSince:  true,
	})
	require.NoError(t, err)
	require.Contains(t, digest.Text, "host-1 -> session-2",
		"the ownership change must be visible on the parent-facing digest")
}

// AC-P3-2c / §6.11: while another session holds a live owner lease a mutation
// is refused with the takeover hint instead of writing behind the owner's back;
// after the audited takeover the very same mutation goes through.
func TestTakeover_UnblocksCrossOwnerMutation(t *testing.T) {
	store, svc, _ := newExtendTestEnv(t, "supervision-takeover-b")
	ctx := context.Background()

	lease := extendClockNow.Add(5 * time.Minute)
	run := extendRunFixture("run_takeover_b")
	run.OwnerID = "host-1"
	run.OwnerLeaseUntil = &lease
	seeded := extendSeedRun(t, store, run)
	seededTimeoutNotification(t, store, seeded.RunID)

	// A non-owner mutation is refused: the owner's live lease is the gate.
	record := takeoverRequestAction(t, svc, "session-2", ActionRequest{
		TargetID:    seeded.RunID,
		Action:      ActionExtendDeadline,
		ExtendBy:    5 * time.Minute,
		ExtendWhich: "progress",
		Reason:      "child still making progress",
	})
	blocked := extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionFailed, blocked.Status)
	require.Contains(t, blocked.Result, HintOwnerLeaseHeld)
	require.Contains(t, blocked.Result, "requires ownership of run")

	untouched, err := store.GetExecutionRun(ctx, seeded.RunID)
	require.NoError(t, err)
	require.Equal(t, 0, untouched.ExtensionCount, "the refused mutation must not touch the ledger")
	require.Equal(t, "host-1", untouched.OwnerID)

	// The explicit override is the named path out of the deadlock; the attempt
	// above consumed the decision row, so the parent's next cycle supplies a
	// fresh one (in production takeover is announced in allowed_actions and is
	// taken before a mutation is attempted at all).
	takeoverFreshDecision(t, store, seeded.RunID, "progress_stalled", 5)
	record = takeoverRequestAction(t, svc, "session-2", ActionRequest{
		TargetID: seeded.RunID,
		Action:   ActionTakeover,
	})
	done := extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionCompleted, done.Status)

	// Now that session-2 owns the run, the same mutation is authorized.
	takeoverFreshDecision(t, store, seeded.RunID, "decision_window_elapsed", 8)
	record = takeoverRequestAction(t, svc, "session-2", ActionRequest{
		TargetID:    seeded.RunID,
		Action:      ActionExtendDeadline,
		ExtendBy:    5 * time.Minute,
		ExtendWhich: "progress",
		Reason:      "child still making progress",
	})
	extended := extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionCompleted, extended.Status)
	after, err := store.GetExecutionRun(ctx, seeded.RunID)
	require.NoError(t, err)
	require.Equal(t, 1, after.ExtensionCount)
	require.True(t, extendClockNow.Add(15*time.Minute).Equal(*after.ProgressDeadlineAt))
}

// AC-P3-2c: the override is only for a live run someone else holds — a terminal
// run, the current owner's own live lease and a missing reason are all refused
// with the stable next_action hint, while a stale lease is not a competing
// owner (§6.11 stops renewing it while the turn is suspended).
func TestTakeover_Rejections(t *testing.T) {
	store, svc, _ := newExtendTestEnv(t, "supervision-takeover-c")
	ctx := context.Background()

	finished := extendClockNow.Add(-time.Minute)
	terminal := extendRunFixture("run_takeover_terminal")
	terminal.Status = RunStatusSucceeded
	terminal.FinishedAt = &finished
	terminal.OwnerID = "host-1"
	seededTerminal := extendSeedRun(t, store, terminal)
	seededTimeoutNotification(t, store, seededTerminal.RunID)

	record := takeoverRequestAction(t, svc, "session-2", ActionRequest{
		TargetID: seededTerminal.RunID,
		Action:   ActionTakeover,
	})
	done := extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionFailed, done.Status)
	require.Contains(t, done.Result, HintTakeoverTerminal)

	live := extendClockNow.Add(5 * time.Minute)
	owned := extendRunFixture("run_takeover_owned")
	owned.OwnerID = "session-2"
	owned.OwnerLeaseUntil = &live
	seededOwned := extendSeedRun(t, store, owned)
	seededTimeoutNotification(t, store, seededOwned.RunID)

	record = takeoverRequestAction(t, svc, "session-2", ActionRequest{
		TargetID: seededOwned.RunID,
		Action:   ActionTakeover,
	})
	done = extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionFailed, done.Status)
	require.Contains(t, done.Result, HintTakeoverNotNeeded,
		"an owner with a live lease can issue the mutation directly")

	staleLease := extendClockNow.Add(-time.Minute)
	stale := extendRunFixture("run_takeover_stale")
	stale.OwnerID = "session-2"
	stale.OwnerLeaseUntil = &staleLease
	seededStale := extendSeedRun(t, store, stale)
	seededTimeoutNotification(t, store, seededStale.RunID)

	record = takeoverRequestAction(t, svc, "session-2", ActionRequest{
		TargetID: seededStale.RunID,
		Action:   ActionTakeover,
	})
	done = extendExecuteAction(t, svc, record.ActionID)
	require.Equal(t, ActionCompleted, done.Status)
	reclaimed, err := store.GetExecutionRun(ctx, seededStale.RunID)
	require.NoError(t, err)
	require.Equal(t, "session-2", reclaimed.OwnerID)
	require.True(t, reclaimed.OwnerLeaseUntil.After(extendClockNow))

	_, err = svc.RequestAction(ctx, ActionRequest{
		RootScopeID:     "root-session-1",
		RequestedByKind: "parent_session",
		RequestedByID:   "session-2",
		TargetKind:      SubjectAgentRun,
		TargetID:        seededStale.RunID,
		Action:          ActionTakeover,
	})
	require.ErrorIs(t, err, ErrActionInvalid)
	require.Contains(t, err.Error(), "reason is required")
}

// AC-P3-2c (durable half): the store-level takeover is a CAS on the fencing
// token the caller observed, so two concurrent claims cannot both win, and a
// terminal run can never be claimed.
func TestTakeoverExecutionRun_CASAndTerminalGuard(t *testing.T) {
	store := testExecutionRunStore(t, "run-takeover-cas")
	ctx := context.Background()
	lease := 30 * time.Second

	run := sampleExecutionRun("run_takeover_cas")
	created, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)
	require.True(t, created)

	ok, err := store.TakeoverExecutionRun(ctx, run.RunID, "session-2", 1, lease, gcNow)
	require.NoError(t, err)
	require.True(t, ok)
	claimed, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, "session-2", claimed.OwnerID)
	require.EqualValues(t, 2, claimed.FencingToken)
	require.NotNil(t, claimed.OwnerLeaseUntil)
	require.True(t, claimed.OwnerLeaseUntil.After(gcNow))

	// A claim holding the old token loses instead of silently overwriting.
	ok, err = store.TakeoverExecutionRun(ctx, run.RunID, "session-3", 1, lease, gcNow)
	require.NoError(t, err)
	require.False(t, ok)
	// The winner's token is the one that advances.
	ok, err = store.TakeoverExecutionRun(ctx, run.RunID, "session-3", 2, lease, gcNow)
	require.NoError(t, err)
	require.True(t, ok)

	marked, err := store.MarkExecutionRunTerminal(ctx, run.RunID, RunStatusSucceeded, "", "", gcNow)
	require.NoError(t, err)
	require.True(t, marked)
	ok, err = store.TakeoverExecutionRun(ctx, run.RunID, "session-4", 3, lease, gcNow)
	require.NoError(t, err)
	require.False(t, ok, "a terminal run has no live obligation left to own")

	_, err = store.TakeoverExecutionRun(ctx, run.RunID, "  ", 3, lease, gcNow)
	require.Error(t, err, "an empty new owner is a caller bug, not a silent no-op")
}
