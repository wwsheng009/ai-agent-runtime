package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// C4-3 / AC-P3-3b：宿主重启时的"恢复 or orphaned"决策。
//
// 判据是 **worker 存活**（心跳 / 更新时间 + 重启宽限期）：
//   - 宽限期内仍有心跳 ⇒ 保留（恢复分支：另一个宿主可能还在跑，或本进程刚接手）；
//   - 宽限期外仍无心跳 ⇒ 不可恢复 ⇒ `orphaned`（终态）+ fencing token 提升 +
//     critical 告警，晚到写入因 version 已前进而被 CAS 拒绝（EC-B9）；
//   - 宿主显式声明可恢复（`Recoverable` 谓词，例如挂起 turn 的账本记录仍在）⇒ 保留。
//
// 决策时钟锚定在账本行自己的 liveness 上（`policy.Now` 显式传入），因此用例不依赖
// 测试机的墙钟。

// ageRunLiveness backdates a run's liveness columns. It writes the columns
// directly instead of going through UpdateExecutionRunCAS because the CAS stamps
// updated_at with the wall clock, which would defeat the fixed decision clock
// above (same raw-SQL pattern execution_store_test.go uses for rows the store API
// cannot express).
func ageRunLiveness(t *testing.T, store *SQLiteSupervisionStore, runID string, at time.Time) {
	t.Helper()
	db, err := store.dbOrErr()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`UPDATE supervision_execution_runs SET last_heartbeat_at=?, updated_at=? WHERE run_id=?`,
		formatRunTime(at), formatRunTime(at), runID)
	require.NoError(t, err)
}

func TestReconcileRestartFencesUnrecoverableRunsAndBlocksLateWrites(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	supervisor, store := newTestExecutionSupervisor(t, "restart-reconcile", ExecutionSupervisorConfig{Mode: "enforce"}, nil, nil)
	supervisor.Now = func() time.Time { return now }

	// 上一次进程留下的两条 run：一条心跳停摆（不可恢复），一条仍在宽限期内。
	stale, err := supervisor.StartRun(ctx, RunSpec{
		SessionID:        "child-stale",
		ParentSessionID:  "parent-1",
		RootSessionID:    "parent-1",
		TurnID:           "turn_parked",
		ExecutionTimeout: time.Hour,
	})
	require.NoError(t, err)
	fresh, err := supervisor.StartRun(ctx, RunSpec{
		SessionID:        "child-fresh",
		ParentSessionID:  "parent-1",
		RootSessionID:    "parent-1",
		ExecutionTimeout: time.Hour,
	})
	require.NoError(t, err)

	staleRow, err := store.GetExecutionRun(ctx, stale.RunID)
	require.NoError(t, err)
	freshRow, err := store.GetExecutionRun(ctx, fresh.RunID)
	require.NoError(t, err)
	// fresh 行"刚写过心跳"，把它当作重启对账的决策时刻。
	decisionNow := freshRow.UpdatedAt
	ageRunLiveness(t, store, stale.RunID, decisionNow.Add(-30*time.Minute))

	report, err := supervisor.ReconcileRestart(ctx, RestartReconcilePolicy{Now: decisionNow, StaleAfter: 5 * time.Minute})
	require.NoError(t, err)
	require.Equal(t, 2, report.Scanned)
	require.Equal(t, 1, report.Orphaned)
	require.Equal(t, 1, report.Recovered)
	require.Zero(t, report.Failed)
	require.Equal(t, []string{stale.RunID}, report.OrphanedRunIDs)

	fenced, err := store.GetExecutionRun(ctx, stale.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusOrphaned, fenced.Status)
	require.True(t, fenced.Terminal())
	require.Equal(t, RunCancelSourceRestartUnrecoverable, fenced.CancelSource)
	require.Equal(t, staleRow.FencingToken+1, fenced.FencingToken, "fencing token must advance")
	require.NotNil(t, fenced.FinishedAt)

	// 晚到写入：旧持有者拿着围栏之前的 version 回来写 ⇒ 被 CAS 拒绝。
	late := *fenced
	late.Status = RunStatusRunning
	late.CancelSource = "late_owner_write"
	written, err := store.UpdateExecutionRunCAS(ctx, late, staleRow.Version)
	require.ErrorIs(t, err, ErrRunConflict)
	require.False(t, written, "a write from the fenced owner must lose its CAS")
	reloaded, err := store.GetExecutionRun(ctx, stale.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusOrphaned, reloaded.Status, "the fenced verdict must survive the late write")

	// 围栏同时投影为 critical 告警（不是只躺在账本里）。
	notifications, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "parent-1"})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	require.Equal(t, "run_orphaned", notifications[0].EventType)
	require.Equal(t, SeverityCritical, notifications[0].Severity)
	require.Equal(t, stale.RunID, notifications[0].SubjectID)

	// 宽限期内的行按"可恢复"保留，一字未改。
	kept, err := store.GetExecutionRun(ctx, fresh.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, kept.Status)
	require.Equal(t, freshRow.FencingToken, kept.FencingToken)
	require.Equal(t, freshRow.Version, kept.Version)
}

func TestReconcileRestartKeepsRecoverableRunsAndRepeatsIdempotently(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	supervisor, store := newTestExecutionSupervisor(t, "restart-reconcile-recoverable", ExecutionSupervisorConfig{Mode: "enforce"}, nil, nil)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		SessionID:        "child-parked",
		ParentSessionID:  "parent-1",
		RootSessionID:    "parent-1",
		TurnID:           "turn_parked",
		ExecutionTimeout: time.Hour,
	})
	require.NoError(t, err)
	row, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	decisionNow := row.UpdatedAt
	ageRunLiveness(t, store, run.RunID, decisionNow.Add(-2*time.Hour))

	// 宿主声明这条 run 可恢复（挂起 turn 的账本记录仍在）⇒ 即使心跳陈旧也保留。
	report, err := supervisor.ReconcileRestart(ctx, RestartReconcilePolicy{
		Now:        decisionNow,
		StaleAfter: 5 * time.Minute,
		Recoverable: func(candidate *ExecutionRun) bool {
			return candidate.TurnID == "turn_parked"
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, report.Scanned)
	require.Equal(t, 1, report.Recovered)
	require.Zero(t, report.Orphaned)

	kept, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, kept.Status)
	require.Equal(t, row.FencingToken, kept.FencingToken)
	require.Equal(t, row.Version, kept.Version, "a recoverable verdict writes nothing")

	// 去掉谓词后同一条 run 不可恢复 ⇒ 围栏；再次运行同一 pass 幂等：
	// 已终态的行不再产生第二次转换。
	report, err = supervisor.ReconcileRestart(ctx, RestartReconcilePolicy{Now: decisionNow, StaleAfter: 5 * time.Minute})
	require.NoError(t, err)
	require.Equal(t, 1, report.Orphaned)
	require.Equal(t, []string{run.RunID}, report.OrphanedRunIDs)

	again, err := supervisor.ReconcileRestart(ctx, RestartReconcilePolicy{Now: decisionNow, StaleAfter: 5 * time.Minute})
	require.NoError(t, err)
	require.Zero(t, again.Scanned, "a fenced run is terminal and leaves the active set")
	require.Zero(t, again.Orphaned)

	fenced, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, row.FencingToken+1, fenced.FencingToken, "exactly one token bump")
	require.Equal(t, row.Version+1, fenced.Version, "exactly one version bump")
}

func TestReconcileRestartObserveModeRecordsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	supervisor, store := newTestExecutionSupervisor(t, "restart-reconcile-observe", ExecutionSupervisorConfig{Mode: "observe"}, nil, nil)
	supervisor.Now = func() time.Time { return now }

	run, err := supervisor.StartRun(ctx, RunSpec{
		SessionID:        "child-observe",
		ParentSessionID:  "parent-1",
		RootSessionID:    "parent-1",
		ExecutionTimeout: time.Hour,
	})
	require.NoError(t, err)
	row, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	decisionNow := row.UpdatedAt
	ageRunLiveness(t, store, run.RunID, decisionNow.Add(-time.Hour))

	report, err := supervisor.ReconcileRestart(ctx, RestartReconcilePolicy{Now: decisionNow, StaleAfter: time.Minute})
	require.NoError(t, err)
	require.Equal(t, 1, report.Scanned)
	require.Equal(t, 1, report.Observed, "observe mode reports the candidate without a transition")
	require.Zero(t, report.Orphaned)

	kept, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, RunStatusQueued, kept.Status, "observe mode must not fence")
	require.Equal(t, row.FencingToken, kept.FencingToken)
	require.Equal(t, row.Version, kept.Version)
}
