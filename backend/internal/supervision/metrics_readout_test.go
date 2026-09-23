package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// C0-E（§7.3 度量基线）读数用例：口径、窗口与 store 读路。
// 线上采样（/supervision/metrics）与这里调用同一批函数，用例锁住的就是线上数字。

// seedMetricsRun 写一条 run 行：terminal 为空表示活体 run；给了 cancelSource 就
// 走生产两步（RequestExecutionCancel → MarkExecutionRunTerminal），因为
// CancelSource 只有取消请求会写。
func seedMetricsRun(t *testing.T, store *SQLiteSupervisionStore, runID, rootSession, childSession string, createdAt time.Time, terminal, cancelSource string, finishedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	deadline := createdAt.Add(30 * time.Minute)
	progressDeadline := createdAt.Add(5 * time.Minute)
	run := ExecutionRun{
		RunID:               runID,
		Kind:                RunKindAgentRun,
		Workflow:            RunWorkflowSpawnAgent,
		RootSessionID:       rootSession,
		ParentSessionID:     rootSession,
		SessionID:           childSession,
		AgentID:             childSession,
		Attempt:             1,
		Status:              RunStatusRunning,
		OwnerID:             "host-1",
		StartedAt:           createdAt,
		LastHeartbeatAt:     createdAt,
		LastProgressAt:      createdAt,
		ProgressSeq:         1,
		ExecutionDeadlineAt: &deadline,
		ProgressDeadlineAt:  &progressDeadline,
		MaxAttempts:         1,
		FencingToken:        1,
		Version:             1,
		CreatedAt:           createdAt,
		UpdatedAt:           createdAt,
	}
	created, err := store.CreateExecutionRun(ctx, run)
	require.NoError(t, err)
	require.True(t, created)
	if terminal == "" {
		return
	}
	if cancelSource != "" {
		ok, err := store.RequestExecutionCancel(ctx, runID, cancelSource, time.Minute, finishedAt.Add(-time.Minute))
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := store.MarkExecutionRunTerminal(ctx, runID, terminal, cancelSource, "", finishedAt)
	require.NoError(t, err)
	require.True(t, ok)
}

func seedMetricsOutbox(t *testing.T, store *SQLiteSupervisionStore, outboxID, runID, sessionID string, createdAt time.Time, deliveredAt time.Time, mailboxSeq int64) {
	t.Helper()
	ctx := context.Background()
	ok, err := store.EnqueueCompletionOutbox(ctx, CompletionOutboxEntry{
		OutboxID:        outboxID,
		RunID:           runID,
		SessionID:       sessionID,
		ParentSessionID: "root-a",
		RootSessionID:   "root-a",
		Status:          RunStatusCompleted,
		IdempotencyKey:  "subagent_completion:" + runID + ":" + outboxID,
		PayloadJSON:     "{}",
		CreatedAt:       createdAt,
	})
	require.NoError(t, err)
	require.True(t, ok)
	if deliveredAt.IsZero() {
		return
	}
	ok, err = store.MarkOutboxDelivered(ctx, outboxID, mailboxSeq, deliveredAt)
	require.NoError(t, err)
	require.True(t, ok)
}

func metricsRunIDs(runs []ExecutionRun) []string {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.RunID)
	}
	return ids
}

// TestComputeRuntimeCancelMetricsFromRuns_CountsWindowAndForcedSources 锁住 §7.3
// 指标 1–2 的口径：分母是窗口内的 run 行（终态按 finished_at、活体按 created_at
// 记账），分子只认 runtime 自己判的取消来源。
func TestComputeRuntimeCancelMetricsFromRuns_CountsWindowAndForcedSources(t *testing.T) {
	now := time.Now().UTC()
	windowStart := now.Add(-24 * time.Hour)
	finished := now.Add(-time.Hour)
	older := windowStart.Add(-time.Hour)

	require.True(t, IsRuntimeForcedCancelSource(" "+CancelSourceDecisionWindowExpired+" "))
	require.False(t, IsRuntimeForcedCancelSource("operator_cancel"), "操作者主动取消不进分子")

	runs := []ExecutionRun{
		{RunID: "run-forced", Status: RunStatusTimedOut, CancelSource: CancelSourceDecisionWindowExpired, FinishedAt: &finished, CreatedAt: now.Add(-3 * time.Hour)},
		{RunID: "run-operator", Status: RunStatusCanceled, CancelSource: "operator_cancel", FinishedAt: &finished, CreatedAt: now.Add(-3 * time.Hour)},
		{RunID: "run-live", Status: RunStatusRunning, CreatedAt: now.Add(-2 * time.Hour)},
		{RunID: "run-old", Status: RunStatusCanceled, CancelSource: CancelSourceProgressStalled, FinishedAt: &older, CreatedAt: older.Add(-time.Hour)},
	}
	opts := RuntimeCancelMetricsOptions{Since: windowStart, Until: now.Add(time.Minute)}
	metrics := ComputeRuntimeCancelMetricsFromRuns(runs, opts)

	require.Equal(t, 3, metrics.Total, "窗口内的 run 行：终态 2 + 活体 1")
	require.Equal(t, 1, metrics.ForcedCancel)
	require.Equal(t, 1, metrics.DecisionWindowExpired)
	require.Equal(t, map[string]int{
		CancelSourceDecisionWindowExpired: 1,
		"operator_cancel":                 1,
	}, metrics.BySource)
	require.InDelta(t, 1.0/3.0, metrics.ForcedCancelRatio(), 1e-9)
	require.InDelta(t, 1.0/3.0, metrics.DecisionWindowExpiredRatio(), 1e-9)

	// 空窗口不产生假数字：0/0 报 0 而不是 NaN。
	empty := ComputeRuntimeCancelMetricsFromRuns(nil, opts)
	require.Zero(t, empty.Total)
	require.Zero(t, empty.ForcedCancelRatio())
}

// TestSQLiteSupervisionStore_ListExecutionRunsInWindow 锁住部署基线读路：全库
// （或一个 root scope）按记账时刻倒序，窗口外与更早的行被排除。
func TestSQLiteSupervisionStore_ListExecutionRunsInWindow(t *testing.T) {
	store := testExecutionRunStore(t, "metrics-window")
	ctx := context.Background()
	now := time.Now().UTC()

	seedMetricsRun(t, store, "run-a", "root-a", "child-a", now.Add(-3*time.Hour), RunStatusTimedOut, CancelSourceProgressStalled, now.Add(-time.Hour))
	seedMetricsRun(t, store, "run-b", "root-a", "child-b", now.Add(-2*time.Hour), "", "", time.Time{})
	seedMetricsRun(t, store, "run-c", "root-b", "child-c", now.Add(-90*time.Minute), RunStatusCompleted, "", now.Add(-80*time.Minute))
	seedMetricsRun(t, store, "run-old", "root-a", "child-old", now.Add(-48*time.Hour), RunStatusCompleted, "", now.Add(-47*time.Hour))

	all, err := store.ListExecutionRunsInWindow(ctx, ExecutionRunWindowFilter{})
	require.NoError(t, err)
	require.Equal(t, []string{"run-a", "run-c", "run-b", "run-old"}, metricsRunIDs(all), "按记账时刻倒序（无窗口 = 全库）")

	windowed, err := store.ListExecutionRunsInWindow(ctx, ExecutionRunWindowFilter{Since: now.Add(-24 * time.Hour)})
	require.NoError(t, err)
	require.Equal(t, []string{"run-a", "run-c", "run-b"}, metricsRunIDs(windowed))

	scoped, err := store.ListExecutionRunsInWindow(ctx, ExecutionRunWindowFilter{RootSessionID: "root-a", Since: now.Add(-24 * time.Hour)})
	require.NoError(t, err)
	require.Equal(t, []string{"run-a", "run-b"}, metricsRunIDs(scoped))

	limited, err := store.ListExecutionRunsInWindow(ctx, ExecutionRunWindowFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, limited, 2)
}

// TestSQLiteSupervisionStore_ListDeliveredOutboxSince 锁住指标 3 的读路：只取已
// 投递的出件，按投递时刻倒序，since 之前的投递被排除。
func TestSQLiteSupervisionStore_ListDeliveredOutboxSince(t *testing.T) {
	store := testExecutionRunStore(t, "metrics-outbox")
	ctx := context.Background()
	now := time.Now().UTC()

	seedMetricsRun(t, store, "run-ok", "root-a", "child-a", now.Add(-3*time.Hour), RunStatusCompleted, "", now.Add(-2*time.Hour))
	seedMetricsOutbox(t, store, "ob-recent", "run-ok", "child-a", now.Add(-110*time.Minute), now.Add(-100*time.Minute), 1)
	seedMetricsOutbox(t, store, "ob-old", "run-ok", "child-a", now.Add(-30*time.Hour), now.Add(-29*time.Hour), 2)
	seedMetricsOutbox(t, store, "ob-pending", "run-ok", "child-a", now.Add(-100*time.Minute), time.Time{}, 0)

	entries, err := store.ListDeliveredOutboxSince(ctx, now.Add(-24*time.Hour), 50)
	require.NoError(t, err)
	require.Len(t, entries, 1, "未投递与窗口外的投递都不算样本")
	require.Equal(t, "ob-recent", entries[0].OutboxID)
	require.NotNil(t, entries[0].DeliveredAt)

	all, err := store.ListDeliveredOutboxSince(ctx, time.Time{}, 50)
	require.NoError(t, err)
	require.Equal(t, []string{"ob-recent", "ob-old"}, []string{all[0].OutboxID, all[1].OutboxID}, "投递时刻倒序")
}

// TestComputeReportLatencyMetrics_JoinsSuccessRuns 锁住指标 3 的投影：只有
// 「成功终态 + 已投递」的配对构成样本，其余条目被跳过而不是记成 0ms。
func TestComputeReportLatencyMetrics_JoinsSuccessRuns(t *testing.T) {
	finished := time.Now().UTC().Add(-2 * time.Hour)
	delivered := finished.Add(45 * time.Second)
	runs := []ExecutionRun{
		{RunID: "run-ok", Status: RunStatusCompleted, FinishedAt: &finished},
		{RunID: "run-failed", Status: RunStatusFailed, FinishedAt: &finished},
		{RunID: "run-no-finish", Status: RunStatusSucceeded},
	}
	entries := []CompletionOutboxEntry{
		{RunID: "run-ok", DeliveredAt: &delivered},
		{RunID: "run-failed", DeliveredAt: &delivered},
		{RunID: "run-missing", DeliveredAt: &delivered},
		{RunID: "run-no-finish", DeliveredAt: &delivered},
		{RunID: "run-ok"},
	}

	metrics := ComputeReportLatencyMetrics(entries, runs)
	require.Equal(t, 1, metrics.Samples)
	require.Equal(t, int64(45000), metrics.P50Millis)
	require.Equal(t, int64(45000), metrics.P95Millis)
	require.Equal(t, int64(45000), metrics.MaxMillis)
	require.False(t, metrics.Unavailable)
}

// TestMisKillCandidatesFromRuns_NewestFirstAndCapped 锁住指标 5 的候选清单：
// 只列 runtime 强制取消的 run，按完成时刻倒序，条数受上限约束。
func TestMisKillCandidatesFromRuns_NewestFirstAndCapped(t *testing.T) {
	now := time.Now().UTC()
	older := now.Add(-3 * time.Hour)
	middle := now.Add(-2 * time.Hour)
	newest := now.Add(-time.Hour)
	runs := []ExecutionRun{
		{RunID: "run-old", Status: RunStatusTimedOut, CancelSource: CancelSourceProgressStalled, FinishedAt: &older, SessionID: "child-a", TurnID: "turn-1", ExtensionCount: 2, ProgressSeq: 7},
		{RunID: "run-mid", Status: RunStatusTimedOut, CancelSource: CancelSourceExecutionDeadline, FinishedAt: &middle, SessionID: "child-b"},
		{RunID: "run-new", Status: RunStatusTimedOut, CancelSource: CancelSourceDecisionWindowExpired, FinishedAt: &newest, SessionID: "child-c"},
		{RunID: "run-operator", Status: RunStatusCanceled, CancelSource: "operator_cancel", FinishedAt: &newest},
	}

	candidates := MisKillCandidatesFromRuns(runs, RuntimeCancelMetricsOptions{}, 2)
	require.Len(t, candidates, 2)
	require.Equal(t, "run-new", candidates[0].RunID)
	require.Equal(t, "run-mid", candidates[1].RunID)

	all := MisKillCandidatesFromRuns(runs, RuntimeCancelMetricsOptions{}, 0)
	require.Len(t, all, 3, "操作者取消不进复核清单")
	require.Equal(t, "run-old", all[2].RunID)
	require.Equal(t, "turn-1", all[2].TurnID)
	require.Equal(t, 2, all[2].ExtensionCount)
	require.Equal(t, int64(7), all[2].ProgressSeq)
	require.Equal(t, older.UTC().Format(time.RFC3339), all[2].FinishedAt)
}

// TestCollectMetricsSnapshot_ReadsWindowFromStore 是 C0-E 的端到端读数：同一个
// store 上先落一条兜底取消与一条成功完成（含已投递出件），快照必须同时给出指标
// 1–3 与指标 5 的复核清单，并说明指标 4 的读数位置。
func TestCollectMetricsSnapshot_ReadsWindowFromStore(t *testing.T) {
	store := testExecutionRunStore(t, "metrics-snapshot")
	ctx := context.Background()
	now := time.Now().UTC()

	seedMetricsRun(t, store, "run-forced", "root-a", "child-a", now.Add(-4*time.Hour), RunStatusTimedOut, CancelSourceDecisionWindowExpired, now.Add(-3*time.Hour))
	seedMetricsRun(t, store, "run-ok", "root-a", "child-b", now.Add(-4*time.Hour), RunStatusCompleted, "", now.Add(-2*time.Hour))
	seedMetricsOutbox(t, store, "ob-ok", "run-ok", "child-b", now.Add(-2*time.Hour), now.Add(-2*time.Hour).Add(30*time.Second), 1)

	snapshot, err := CollectMetricsSnapshot(ctx, store, MetricsSnapshotOptions{Since: now.Add(-24 * time.Hour)})
	require.NoError(t, err)
	require.Equal(t, "window", snapshot.ReadPath)
	require.Equal(t, 2, snapshot.Cancel.Total)
	require.Equal(t, 1, snapshot.Cancel.ForcedCancel)
	require.Equal(t, 1, snapshot.Cancel.DecisionWindowExpired)
	require.InDelta(t, 0.5, snapshot.Cancel.ForcedCancelRatio(), 1e-9)
	require.False(t, snapshot.WindowTruncated)

	require.False(t, snapshot.ReportLatency.Unavailable)
	require.Equal(t, 1, snapshot.ReportLatency.Samples)
	require.Equal(t, int64(30000), snapshot.ReportLatency.P95Millis)

	require.Len(t, snapshot.MisKillCandidates, 1)
	require.Equal(t, "run-forced", snapshot.MisKillCandidates[0].RunID)
	require.Equal(t, CancelSourceDecisionWindowExpired, snapshot.MisKillCandidates[0].CancelSource)

	require.NotEmpty(t, snapshot.Notes, "指标 4/5 的读数边界必须随快照给出")
	require.NotEmpty(t, snapshot.WindowSince)
	require.NotEmpty(t, snapshot.GeneratedAt)

	// 无 store 时不报假数字：只给「不可读」的说明。
	empty, err := CollectMetricsSnapshot(ctx, nil, MetricsSnapshotOptions{})
	require.NoError(t, err)
	require.Zero(t, empty.Cancel.Total)
	require.NotEmpty(t, empty.Notes)
}

// TestParseMetricsWindowValue 锁住窗口参数解析：RFC3339 / 时长 / 天数 / 无界，
// 以及「非法输入必须报错」——HTTP 端点与离线采集命令共用这一份解析。
func TestParseMetricsWindowValue(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	unbounded, err := ParseMetricsWindowValue("", now)
	require.NoError(t, err)
	require.True(t, unbounded.IsZero())

	absolute, err := ParseMetricsWindowValue("2026-09-20T06:30:00Z", now)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 20, 6, 30, 0, 0, time.UTC), absolute)

	hours, err := ParseMetricsWindowValue("24h", now)
	require.NoError(t, err)
	require.Equal(t, now.Add(-24*time.Hour), hours)

	days, err := ParseMetricsWindowValue("7d", now)
	require.NoError(t, err)
	require.Equal(t, now.Add(-7*24*time.Hour), days)

	spaced, err := ParseMetricsWindowValue(" 7d ", now)
	require.NoError(t, err)
	require.Equal(t, now.Add(-7*24*time.Hour), spaced)

	for _, raw := range []string{"banana", "0d", "-1h", "0s", "d"} {
		_, err := ParseMetricsWindowValue(raw, now)
		require.Error(t, err, raw)
	}
}
