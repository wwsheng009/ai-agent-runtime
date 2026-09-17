package supervision

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWakeBudgetClassOf_ProgressSpellings pins the P0-2/ADR-2 classification:
// every host spelling of the opt-in progress check must land in the independent
// "progress" bucket, and the check runs before the failure/approval keyword
// matching so a descriptive host reason can never fall into the shared bounded
// budget by accident.
func TestWakeBudgetClassOf_ProgressSpellings(t *testing.T) {
	cases := []string{
		WakeReasonProgressCheck,      // "progress_check"（两宿主共用的常量）
		"supervision_progress_check", // API 宿主 reason 的旧拼写
		"ProgressCheck",              // 大小写不敏感
		"  progress_check  ",         // 前后空白被裁剪
		"supervision.progress.check", // 子串匹配，不要求下划线
	}
	for _, reason := range cases {
		require.Equalf(t, WakeBudgetClassProgress, WakeBudgetClassOf(reason), "reason=%q", reason)
		require.Truef(t, WakeReasonIsProgressCheck(reason), "reason=%q", reason)
	}

	// progress 先行判定：即便 reason 里同时出现 failure 关键词（历史上巡查
	// reason 曾被命名为 supervision_progress_check_failed 之类），也应归
	// progress，不能让 progress 流量反过来吃掉 failure 预算。
	require.Equal(t, WakeBudgetClassProgress, WakeBudgetClassOf("progress_check_failed"))

	// 非 progress reason 的分类保持不变。
	require.Equal(t, WakeBudgetClassFailure, WakeBudgetClassOf("child_failed"))
	require.Equal(t, WakeBudgetClassApproval, WakeBudgetClassOf("approval_required"))
	require.Equal(t, WakeBudgetClassOther, WakeBudgetClassOf("critical_lifecycle"))

	// progress 仍受自己的有界额度约束，而不是 approval 那样的不限流类别。
	require.True(t, WakeBudgetClassProgress.Bounded())
}

// TestWakeProgressBudget_DefaultAllowanceAndUnlimited pins the two configuration
// ends of MaxProgressWakePerWindow: 0 means the built-in default (6 per
// window), a negative value removes the cap for the class (accepted for
// symmetry, not recommended).
func TestWakeProgressBudget_DefaultAllowanceAndUnlimited(t *testing.T) {
	store := newTestStore(t, "wake-progress-limits")
	ctx := context.Background()
	now := time.Now().UTC()

	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{RateWindow: time.Hour})
	require.Equal(t, defaultMaxProgressWakePerWindow, scheduler.budgetLimit(WakeBudgetClassProgress))
	require.Equal(t, 6, scheduler.budgetLimit(WakeBudgetClassProgress),
		"the shipped default allowance is 6 progress wakes per window")

	state := scheduler.BudgetState(ctx, wakeBudgetTestRoot, WakeBudgetClassProgress)
	require.Equal(t, 6, state.Limit)
	require.False(t, state.Unlimited)

	// Spending the progress allowance rate-limits only progress: the
	// failure/other classes keep their own allowance in the same window.
	for i := 0; i < defaultMaxProgressWakePerWindow; i++ {
		scheduler.recordClaim(ctx, wakeBudgetTestRoot, wakeBudgetTestRoot, WakeBudgetClassProgress, WakeReasonProgressCheck, now)
	}
	require.False(t, scheduler.AllowAutoWake(ctx, wakeBudgetTestRoot, WakeBudgetClassProgress, now))
	require.True(t, scheduler.AllowAutoWake(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure, now))
	require.True(t, scheduler.AllowAutoWake(ctx, wakeBudgetTestRoot, WakeBudgetClassOther, now))

	unlimited := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:               time.Hour,
		MaxProgressWakePerWindow: -1,
	})
	unlimitedState := unlimited.BudgetState(ctx, wakeBudgetTestRoot, WakeBudgetClassProgress)
	require.True(t, unlimitedState.Unlimited, "a negative value removes the progress cap")
	require.Zero(t, unlimitedState.Limit)
	for i := 0; i < defaultMaxProgressWakePerWindow+2; i++ {
		unlimited.recordClaim(ctx, wakeBudgetTestRoot, wakeBudgetTestRoot, WakeBudgetClassProgress, WakeReasonProgressCheck, now)
	}
	require.True(t, unlimited.AllowAutoWake(ctx, wakeBudgetTestRoot, WakeBudgetClassProgress, now))
}

// TestWakeProgressBudget_MemoryClassesDoNotShareAllowance is the memory-mode
// half of the P0-2 split: progress and failure/other each keep their own
// rolling window, so a busy opt-in sweep can never defer a critical lifecycle
// wake and a burst of failures can never starve the progress report.
func TestWakeProgressBudget_MemoryClassesDoNotShareAllowance(t *testing.T) {
	store := newTestStore(t, "wake-progress-memory-split")
	ctx := context.Background()
	now := time.Now().UTC()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
	})

	// Direction 1: progress exhausts its own (larger) allowance first; the
	// single failure/other slot must still be available.
	for i := 0; i < defaultMaxProgressWakePerWindow; i++ {
		scheduler.recordClaim(ctx, "root-progress-first", "root-progress-first", WakeBudgetClassProgress, WakeReasonProgressCheck, now)
	}
	require.False(t, scheduler.AllowAutoWake(ctx, "root-progress-first", WakeBudgetClassProgress, now))
	require.True(t, scheduler.AllowAutoWake(ctx, "root-progress-first", WakeBudgetClassFailure, now),
		"progress must not consume the failure budget")
	require.True(t, scheduler.AllowAutoWake(ctx, "root-progress-first", WakeBudgetClassOther, now),
		"progress must not consume the other budget")

	// Direction 2: the shared bounded budget is already exhausted; progress
	// still has its full independent allowance.
	scheduler.recordClaim(ctx, "root-failure-first", "root-failure-first", WakeBudgetClassFailure, WakeReasonExecutionFailed, now)
	require.False(t, scheduler.AllowAutoWake(ctx, "root-failure-first", WakeBudgetClassFailure, now))
	require.True(t, scheduler.AllowAutoWake(ctx, "root-failure-first", WakeBudgetClassProgress, now),
		"an exhausted failure budget must not block the progress check")
	require.True(t, scheduler.AllowAutoWake(ctx, "root-failure-first", WakeBudgetClassApproval, now))
}

// TestWakeProgressBudget_MemorySplitSurvivesDrain drives the split through the
// real drain path (not just the ledger helpers): a delivered failure wake and a
// delivered progress-only wake each spend exactly their own class.
func TestWakeProgressBudget_MemorySplitSurvivesDrain(t *testing.T) {
	store := newTestStore(t, "wake-progress-memory-drain")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
	})
	scheduler.SetProgressSource(staticProgressSource{groups: []ProgressGroup{runningProgressGroup()}})

	// The single shared bounded slot is spent by a critical failure wake.
	projectTestWake(t, store, scheduler, "child-1", WakeReasonExecutionFailed, SeverityCritical)
	claimed, digest, err := drainAndResolve(scheduler)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NotNil(t, digest)

	// The progress-only wake still drains: its rollup is content, and it must
	// book a progress claim instead of consuming the (now exhausted) shared
	// failure/other slot.
	_, err = scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           wakeBudgetTestRoot,
		TargetParentSessionID: wakeBudgetTestRoot,
		WakeReason:            WakeReasonProgressCheck,
	})
	require.NoError(t, err)

	claimed, digest, err = drainAndResolve(scheduler)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, WakeBudgetClassProgress, WakeBudgetClassOf(claimed[0].WakeReason))
	require.NotNil(t, digest)
	require.NotEmpty(t, digest.Progress)

	failure := scheduler.BudgetState(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure)
	require.Equal(t, 1, failure.Used)
	require.Equal(t, 1, failure.Limit, "the progress turn must not add a failure claim")

	progress := scheduler.BudgetState(ctx, wakeBudgetTestRoot, WakeBudgetClassProgress)
	require.Equal(t, 1, progress.Used)
	require.Equal(t, 6, progress.Limit)
}

// TestWakeProgressBudget_DurableLedgerSeparatesClasses covers the durable
// ledger half of ADR-2: progress claims are written under their own
// (root_scope, class) key, so processes sharing a database keep the split and
// old failure/other keys are untouched.
func TestWakeProgressBudget_DurableLedgerSeparatesClasses(t *testing.T) {
	store := newTestStore(t, "wake-progress-durable-split")
	ctx := context.Background()
	now := time.Now().UTC()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
		BudgetMode:           WakeBudgetModeDurable,
	})
	scheduler.SetProgressSource(staticProgressSource{groups: []ProgressGroup{runningProgressGroup()}})

	_, err := scheduler.ScheduleWake(ctx, WakeRequest{
		RootScopeID:           wakeBudgetTestRoot,
		TargetParentSessionID: wakeBudgetTestRoot,
		WakeReason:            WakeReasonProgressCheck,
	})
	require.NoError(t, err)
	claimed, _, err := drainAndResolve(scheduler)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	progressClaims, err := store.CountWakeClaims(ctx, wakeBudgetTestRoot, WakeBudgetClassProgress, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, progressClaims)
	for _, class := range []WakeBudgetClass{WakeBudgetClassFailure, WakeBudgetClassOther} {
		count, err := store.CountWakeClaims(ctx, wakeBudgetTestRoot, class, now.Add(-time.Hour))
		require.NoError(t, err)
		require.Zero(t, count, "a progress turn must not book a %s claim", class)
	}
	require.True(t, scheduler.AllowAutoWake(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure, now),
		"the durable failure allowance must be untouched by the progress turn")
	require.True(t, scheduler.AllowAutoWake(ctx, wakeBudgetTestRoot, WakeBudgetClassOther, now))

	// Fill the durable progress allowance: only progress is rate-limited.
	for i := 1; i < defaultMaxProgressWakePerWindow; i++ {
		scheduler.recordClaim(ctx, wakeBudgetTestRoot, wakeBudgetTestRoot, WakeBudgetClassProgress, WakeReasonProgressCheck, now)
	}
	require.False(t, scheduler.AllowAutoWake(ctx, wakeBudgetTestRoot, WakeBudgetClassProgress, now))
	require.True(t, scheduler.AllowAutoWake(ctx, wakeBudgetTestRoot, WakeBudgetClassFailure, now))
	require.True(t, scheduler.AllowAutoWake(ctx, wakeBudgetTestRoot, WakeBudgetClassOther, now))
}

// TestFormatWakeBudgetLine_RendersProgressRow locks the model-visible ledger:
// the new class must appear as its own progress=used/limit row in the preflight
// digest line (plan §3.2 改动 1, ADR-2).
func TestFormatWakeBudgetLine_RendersProgressRow(t *testing.T) {
	line := FormatWakeBudgetLine([]WakeBudgetState{
		{BudgetClass: WakeBudgetClassFailure, Used: 5, Limit: 5},
		{BudgetClass: WakeBudgetClassProgress, Used: 2, Limit: 6},
	})
	require.Contains(t, line, "progress=2/6")
	require.Contains(t, line, "failure=5/5")
	require.Less(t, strings.Index(line, "failure="), strings.Index(line, "progress="),
		"rows keep the caller's class order")

	exhausted := FormatWakeBudgetLine([]WakeBudgetState{
		{BudgetClass: WakeBudgetClassProgress, Used: 6, Limit: 6},
	})
	require.Contains(t, exhausted, "progress=6/6")
	require.Contains(t, exhausted, "defer", "an exhausted progress class defers, it does not drop")
}
