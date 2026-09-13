package supervision

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const wakeBudgetTestRoot = "root-session-1"

// projectTestWake persists one critical lifecycle notification and its wake
// request, mirroring how hosts call ProjectLifecycle for a child transition.
func projectTestWake(t *testing.T, store Store, scheduler *WakeScheduler, subjectID, eventType string, severity Severity) {
	t.Helper()
	_, err := ProjectLifecycle(context.Background(), store, scheduler, LifecycleProjection{
		RootScopeID:           wakeBudgetTestRoot,
		TargetParentSessionID: wakeBudgetTestRoot,
		SubjectKind:           SubjectAgentRun,
		SubjectID:             subjectID,
		EventType:             eventType,
		Severity:              severity,
		SupervisionState:      SupervisionBlocked,
	})
	require.NoError(t, err)
}

func drainTestWakes(scheduler *WakeScheduler) ([]WakePending, *Digest, error) {
	return scheduler.DrainRunnable(context.Background(), wakeBudgetTestRoot, "", wakeBudgetTestRoot, nil)
}

// drainAndResolve mirrors the production WakeConsumer: after delivering the
// digest the claims are released so a later event for the same
// root+parent+reason can be persisted again (the dedup key is unique while a
// claimed row exists).
func drainAndResolve(scheduler *WakeScheduler) ([]WakePending, *Digest, error) {
	claimed, digest, err := drainTestWakes(scheduler)
	if err != nil {
		return claimed, digest, err
	}
	for _, w := range claimed {
		_ = scheduler.ResolveWake(context.Background(), w.WakeID)
	}
	return claimed, digest, nil
}

// TestWakeBudgetClassOf_ClassifiesWakeReasons locks the class mapping used by
// the tiered budget (plan P1-6 方案 1).
func TestWakeBudgetClassOf_ClassifiesWakeReasons(t *testing.T) {
	cases := map[string]WakeBudgetClass{
		WakeReasonApprovalRequired: WakeBudgetClassApproval,
		WakeReasonQuestionAsked:    WakeBudgetClassApproval,
		"permission_requested":     WakeBudgetClassApproval,
		WakeReasonExecutionFailed:  WakeBudgetClassFailure,
		WakeReasonExecutionTimeout: WakeBudgetClassFailure,
		WakeReasonLifecycleFailed:  WakeBudgetClassFailure,
		"agent_failed":             WakeBudgetClassFailure,
		"child stalled":            WakeBudgetClassFailure,
		"":                         WakeBudgetClassOther,
		"critical_lifecycle":       WakeBudgetClassOther,
		"agent_interrupted":        WakeBudgetClassOther,
	}
	for reason, want := range cases {
		require.Equalf(t, want, WakeBudgetClassOf(reason), "reason=%q", reason)
	}
	require.False(t, WakeBudgetClassApproval.Bounded())
	require.True(t, WakeBudgetClassFailure.Bounded())
	require.True(t, WakeBudgetClassOther.Bounded())
}

// TestWakeBudget_ApprovalNotStarvedByFailureBudget verifies the core P1-6
// rule: an exhausted failure budget must not delay a blocking approval, and
// the disallowed failure wake stays durable instead of being dropped.
func TestWakeBudget_ApprovalNotStarvedByFailureBudget(t *testing.T) {
	store := newTestStore(t, "wake-budget-approval")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
	})

	// First failure consumes the bounded budget.
	projectTestWake(t, store, scheduler, "child-1", WakeReasonExecutionFailed, SeverityCritical)
	claimed, digest, err := drainAndResolve(scheduler)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NotNil(t, digest)

	// A second failure and a blocking approval arrive while the failure
	// budget is exhausted.
	projectTestWake(t, store, scheduler, "child-2", WakeReasonExecutionFailed, SeverityCritical)
	projectTestWake(t, store, scheduler, "child-3", WakeReasonApprovalRequired, SeverityCritical)

	claimed, digest, err = drainAndResolve(scheduler)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "only the approval may drain while the failure budget is exhausted")
	require.Equal(t, WakeReasonApprovalRequired, claimed[0].WakeReason)
	require.NotNil(t, digest)

	// The failure wake remains durable for the next window.
	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:   wakeBudgetTestRoot,
		UnclaimedOnly: true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, WakeReasonExecutionFailed, pending[0].WakeReason)

	// With only bounded-class wakes left, the scheduler still rate limits.
	_, _, err = drainTestWakes(scheduler)
	require.ErrorIs(t, err, ErrWakeRateLimited)
}

// TestWakeBudget_DurableBudgetSharedBySchedulerInstances verifies P1-6 方案 2:
// with WakeBudgetModeDurable the window is shared through the store, so a
// second scheduler instance (another process on the same database) sees the
// claim instead of granting a fresh budget.
func TestWakeBudget_DurableBudgetSharedBySchedulerInstances(t *testing.T) {
	store := newTestStore(t, "wake-budget-durable")
	ctx := context.Background()
	config := WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
		BudgetMode:           WakeBudgetModeDurable,
	}
	first := NewWakeScheduler(store, config)

	projectTestWake(t, store, first, "child-1", WakeReasonExecutionFailed, SeverityCritical)
	claimed, _, err := drainAndResolve(first)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	claims, err := store.CountWakeClaims(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, claims)

	projectTestWake(t, store, first, "child-2", WakeReasonExecutionFailed, SeverityCritical)
	second := NewWakeScheduler(store, config)
	_, _, err = drainTestWakes(second)
	require.ErrorIs(t, err, ErrWakeRateLimited, "durable claims must be visible to other scheduler instances")

	state := second.BudgetState(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure)
	require.Equal(t, 1, state.Limit)
	require.Equal(t, 1, state.Used)
	require.False(t, state.Unlimited)
}

// TestWakeBudget_DurableSurvivesStoreReopen verifies the restart half of
// P1-6 方案 2: claims live in the supervision database, not process memory.
func TestWakeBudget_DurableSurvivesStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervision.db")
	ctx := context.Background()
	config := WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
		BudgetMode:           WakeBudgetModeDurable,
	}

	store, err := NewSQLiteSupervisionStore(&StoreConfig{Path: path})
	require.NoError(t, err)
	scheduler := NewWakeScheduler(store, config)
	projectTestWake(t, store, scheduler, "child-1", WakeReasonExecutionFailed, SeverityCritical)
	claimed, _, err := drainAndResolve(scheduler)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, store.Close())

	reopened, err := NewSQLiteSupervisionStore(&StoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := NewWakeScheduler(reopened, config)

	state := restarted.BudgetState(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure)
	require.Equal(t, 1, state.Used, "a restart must not reset the durable budget")

	projectTestWake(t, reopened, restarted, "child-2", WakeReasonExecutionFailed, SeverityCritical)
	_, _, err = drainTestWakes(restarted)
	require.ErrorIs(t, err, ErrWakeRateLimited)
}

// TestWakeBudget_MemoryBudgetResetsWithScheduler documents the default mode
// (and the N4 trade-off the durable mode removes).
func TestWakeBudget_MemoryBudgetResetsWithScheduler(t *testing.T) {
	store := newTestStore(t, "wake-budget-memory")
	config := WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
		BudgetMode:           WakeBudgetModeMemory,
	}
	first := NewWakeScheduler(store, config)
	projectTestWake(t, store, first, "child-1", WakeReasonExecutionFailed, SeverityCritical)
	claimed, _, err := drainAndResolve(first)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	projectTestWake(t, store, first, "child-2", WakeReasonExecutionFailed, SeverityCritical)
	second := NewWakeScheduler(store, config)
	claimed, _, err = drainAndResolve(second)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "memory mode gives every process its own window")
}

// TestWakeBudget_UnlimitedBoundedClass verifies the negative-value escape
// hatch used by hosts that want the old behavior disabled entirely.
func TestWakeBudget_UnlimitedBoundedClass(t *testing.T) {
	store := newTestStore(t, "wake-budget-unlimited")
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: -1,
	})
	for i := 0; i < 3; i++ {
		projectTestWake(t, store, scheduler, "child-"+string(rune('a'+i)), WakeReasonExecutionFailed, SeverityCritical)
		claimed, digest, err := drainAndResolve(scheduler)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		require.NotNil(t, digest)
	}
	state := scheduler.BudgetState(context.Background(), wakeBudgetTestRoot, WakeBudgetClassFailure)
	require.True(t, state.Unlimited)
}

// TestWakeBudget_ExplicitApprovalCap verifies that a host can still bound the
// approval class explicitly.
func TestWakeBudget_ExplicitApprovalCap(t *testing.T) {
	store := newTestStore(t, "wake-budget-approval-cap")
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:               time.Hour,
		MaxAutoWakePerWindow:     5,
		MaxApprovalWakePerWindow: 1,
	})
	projectTestWake(t, store, scheduler, "child-1", WakeReasonApprovalRequired, SeverityCritical)
	claimed, _, err := drainAndResolve(scheduler)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	projectTestWake(t, store, scheduler, "child-2", WakeReasonQuestionAsked, SeverityCritical)
	_, _, err = drainTestWakes(scheduler)
	require.ErrorIs(t, err, ErrWakeRateLimited)
}

// TestWakeClaims_RecordCountAndPrune covers the durable ledger semantics:
// idempotent claim ids, per-class counting, window filtering and retention.
func TestWakeClaims_RecordCountAndPrune(t *testing.T) {
	store := newTestStore(t, "wake-claims-ledger")
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.RecordWakeClaim(ctx, WakeClaim{
		ClaimID:     "claim-old",
		RootScopeID: wakeBudgetTestRoot,
		BudgetClass: WakeBudgetClassFailure,
		WakeReason:  WakeReasonExecutionFailed,
		ClaimedAt:   now.Add(-3 * time.Hour),
	}))
	require.NoError(t, store.RecordWakeClaim(ctx, WakeClaim{
		ClaimID:     "claim-new",
		RootScopeID: wakeBudgetTestRoot,
		BudgetClass: WakeBudgetClassFailure,
		WakeReason:  WakeReasonExecutionFailed,
		ClaimedAt:   now,
	}))
	// Same claim id must not double count, even with a different timestamp.
	require.NoError(t, store.RecordWakeClaim(ctx, WakeClaim{
		ClaimID:     "claim-new",
		RootScopeID: wakeBudgetTestRoot,
		BudgetClass: WakeBudgetClassFailure,
		WakeReason:  WakeReasonExecutionFailed,
		ClaimedAt:   now,
	}))
	// A different class is counted independently.
	require.NoError(t, store.RecordWakeClaim(ctx, WakeClaim{
		ClaimID:     "claim-approval",
		RootScopeID: wakeBudgetTestRoot,
		BudgetClass: WakeBudgetClassApproval,
		WakeReason:  WakeReasonApprovalRequired,
		ClaimedAt:   now,
	}))

	inWindow, err := store.CountWakeClaims(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, inWindow)

	allTime, err := store.CountWakeClaims(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, allTime)

	approvals, err := store.CountWakeClaims(ctx, wakeBudgetTestRoot, WakeBudgetClassApproval, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, approvals)

	removed, err := store.PruneWakeClaims(ctx, now.Add(-2*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, removed)
	remaining, err := store.CountWakeClaims(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, remaining)
}

// TestWakeSchedulerConfig_DefaultsFromSupervisionConfig verifies the YAML
// surface stays backward compatible: zero values keep the historical budget
// and only an explicit durable mode changes the ledger.
func TestWakeSchedulerConfig_MapsFromSupervisionConfig(t *testing.T) {
	defaults := Config{}.WakeSchedulerConfig()
	require.Equal(t, time.Hour, defaults.RateWindow)
	require.Equal(t, defaultMaxAutoWakePerWindow, defaults.MaxAutoWakePerWindow)
	require.Equal(t, 0, defaults.MaxApprovalWakePerWindow)
	require.Equal(t, WakeBudgetModeMemory, defaults.BudgetMode)

	configured := Config{
		WakeRateWindow:      30 * time.Minute,
		WakeMaxAutoWake:     2,
		WakeMaxApprovalWake: 4,
		WakeBudgetMode:      "durable",
	}.WakeSchedulerConfig()
	require.Equal(t, 30*time.Minute, configured.RateWindow)
	require.Equal(t, 2, configured.MaxAutoWakePerWindow)
	require.Equal(t, 4, configured.MaxApprovalWakePerWindow)
	require.Equal(t, WakeBudgetModeDurable, configured.BudgetMode)

	unknown := Config{WakeBudgetMode: "postgres"}.WakeSchedulerConfig()
	require.Equal(t, WakeBudgetModeMemory, unknown.BudgetMode)
}

// TestWakeClaims_SameInstantClaimsStayDistinct guards the claim id sequence:
// with a stubbed clock every delivered wake shares one timestamp, and a
// colliding id would be swallowed by the unique index while the ledger still
// looked healthy.
func TestWakeClaims_SameInstantClaimsStayDistinct(t *testing.T) {
	store := newTestStore(t, "wake-claims-same-instant")
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:               time.Hour,
		MaxAutoWakePerWindow:     5,
		MaxApprovalWakePerWindow: 5,
		BudgetMode:               WakeBudgetModeDurable,
	})
	frozen := time.Now().UTC().Add(-time.Minute)
	scheduler.now = func() time.Time { return frozen }

	reasons := []string{WakeReasonExecutionFailed, WakeReasonApprovalRequired, "critical_lifecycle"}
	classes := []WakeBudgetClass{WakeBudgetClassFailure, WakeBudgetClassApproval, WakeBudgetClassOther}
	for i, reason := range reasons {
		projectTestWake(t, store, scheduler, fmt.Sprintf("child-%d", i), reason, SeverityCritical)
		claimed, _, err := drainAndResolve(scheduler)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
	}

	ctx := context.Background()
	for _, class := range classes {
		count, err := store.CountWakeClaims(ctx, wakeBudgetTestRoot, class, frozen.Add(-time.Hour))
		require.NoError(t, err)
		require.Equal(t, 1, count, "class %s must book its own claim", class)
		state := scheduler.BudgetState(ctx, wakeBudgetTestRoot, class)
		require.Equal(t, 1, state.Used, "same-instant claims must not be deduplicated away")
	}
}

// TestWakeClaims_RetentionStaysBoundedUnderRepeatedClaims is the small-scale
// write-amplification check requested in the plan appendix (B.3): retained
// rows stay proportional to the rolling window instead of the lifetime claim
// count, and the active window still counts accurately.
func TestWakeClaims_RetentionStaysBoundedUnderRepeatedClaims(t *testing.T) {
	store := newTestStore(t, "wake-claims-retention")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: -1,
		BudgetMode:           WakeBudgetModeDurable,
	})
	const claimCount = 200
	base := time.Now().UTC().Truncate(time.Second)
	current := base
	scheduler.now = func() time.Time { return current }

	started := time.Now()
	for i := 0; i < claimCount; i++ {
		scheduler.recordClaim(ctx, wakeBudgetTestRoot, wakeBudgetTestRoot, WakeBudgetClassOther, "critical_lifecycle", current)
		current = current.Add(time.Minute)
	}
	elapsed := time.Since(started)
	t.Logf("%d durable claims + opportunistic prunes in %s (%s per claim)", claimCount, elapsed, elapsed/claimCount)

	remaining, err := store.CountWakeClaims(ctx, wakeBudgetTestRoot, WakeBudgetClassOther, time.Time{})
	require.NoError(t, err)
	// Retention keeps roughly two windows (2h) of minute-spaced claims.
	require.Equal(t, 121, remaining, "ledger must stay bounded by the retention window")
	require.Less(t, remaining, claimCount)

	state := scheduler.BudgetState(ctx, wakeBudgetTestRoot, WakeBudgetClassOther)
	require.True(t, state.Unlimited)
	require.Equal(t, 60, state.Used, "the active window still counts every claim inside it")

	lastCutoff := base.Add(time.Duration(claimCount-1) * time.Minute).Add(-2 * time.Hour)
	pruned, err := store.PruneWakeClaims(ctx, lastCutoff)
	require.NoError(t, err)
	require.EqualValues(t, 0, pruned, "replaying the last retention cutoff is a no-op")

	pruned, err = store.PruneWakeClaims(ctx, lastCutoff.Add(time.Minute))
	require.NoError(t, err)
	require.EqualValues(t, 1, pruned, "retention advances one row per minute of claims")
}
