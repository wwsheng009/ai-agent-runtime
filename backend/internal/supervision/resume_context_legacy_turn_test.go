package supervision

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// EC-G4 专用用例（P3 场景表 §6.3-G）：迁移期"历史生命周期行无 turn_id"。
//
// 设计口径（design §6.2 + EC-G4）：
//  1. 允许 turn_id 为空 —— 旧行照样被读进账本投影，不因缺字段被丢弃或报错；
//  2. 此类行走原有"独立 run"语义 —— 不归因于任何 turn，因此不参与账本清空
//     判定（I1 判据是 count(obligations where turn_id=? and not terminal) == 0），
//     既不阻塞 can_finalize，也不把 resume 拉进一个不存在的 turn；
//  3. 它们仍是可见读数 —— rollup 与 standalone_obligations 行都在 resume 文本里，
//     模型能看到"有一个不归因的活体 run"，但不会被它误判 I1。
//
// 反证方向同样锁住：真正归因于某 turn 的非终态行必须继续压住收尾（EC-G4 不放宽 I1）。

// TestBatchObligationSource_LegacyRowsWithoutTurnIDDoNotGateFinalize 覆盖 ①+②+③：
// 同一父会话里既有本 turn 的终态批次，又有一行迁移期遗留的无归因活体批次。
func TestBatchObligationSource_LegacyRowsWithoutTurnIDDoNotGateFinalize(t *testing.T) {
	store := &fakeBatchStore{batches: []subagentbatch.SubagentBatch{
		{
			BatchID: "batch-done", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
			Status: subagentbatch.BatchCompleted, TaskCount: 2, CompletedCount: 2,
			ResultSummaryRef: "art-batch-done",
		},
		{
			// 迁移期历史行：没有 turn_id（旧版本没有这一列）。
			BatchID: "batch-legacy", ParentSessionID: "root-session-1",
			Status: subagentbatch.BatchRunning, TaskCount: 3, CompletedCount: 1,
		},
	}}

	rc := BuildResumeContext(context.Background(), NewBatchObligationSource(store), nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
		RootScopeID:     "root-session-1",
		TurnID:          "turn-1",
	})

	require.Equal(t, "turn-1", rc.TurnID, "无归因行不能把 resume 拉进一个不存在的 turn")
	require.Equal(t, 2, rc.TotalCount, "历史行仍是账本读数（不丢弃）")
	require.Equal(t, 0, rc.PendingCount, "EC-G4：无 turn_id 的行不参与账本清空判定")
	require.Equal(t, 1, rc.StandaloneCount)
	require.True(t, rc.Terminal, "本 turn 的账本已全终态 ⇒ 允许收尾")

	// ③ 可见性：rollup 里仍有这一行，且被显式标记为 standalone。
	require.Len(t, rc.Obligations, 2)
	require.Contains(t, rc.Text, "obligations: total=2 pending=0 can_finalize=true")
	require.Contains(t, rc.Text, "standalone_obligations: 1 (no turn_id; legacy standalone runs, not gating can_finalize)")
	require.Contains(t, rc.Text, "batch batch-legacy: status=running total=3 completed=1 failed=0 (not terminal) standalone=true")
	require.Contains(t, AutoWakePromptFor(rc), "所有 obligation 均已终态：请直接产出终局报告")
}

// TestBatchObligationSource_LegacyRowsAloneKeepLegacyNewTurnFallback 覆盖"账本里
// 只剩历史行"的形态：没有可归因的 turn 时不编造 turn 身份，宿主按原有"新开 turn"
// 路径保底（I3 的降级分支）。
func TestBatchObligationSource_LegacyRowsAloneKeepLegacyNewTurnFallback(t *testing.T) {
	store := &fakeBatchStore{batches: []subagentbatch.SubagentBatch{
		{
			BatchID: "batch-legacy-done", ParentSessionID: "root-session-1",
			Status: subagentbatch.BatchCompleted, TaskCount: 1, CompletedCount: 1,
		},
		{
			BatchID: "batch-legacy-live", ParentSessionID: "root-session-1",
			Status: subagentbatch.BatchRunning, TaskCount: 1,
		},
	}}

	rc := BuildResumeContext(context.Background(), NewBatchObligationSource(store), nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
	})

	require.Equal(t, "", rc.TurnID, "没有任何可归因的 turn ⇒ 不编造 turn 身份")
	require.Equal(t, 0, rc.PendingCount)
	require.Equal(t, 1, rc.StandaloneCount)
	require.True(t, rc.Terminal)

	require.NotContains(t, rc.Text, "turn_id:", "无 turn 身份时文本不得写出空 turn_id 行")
	require.Contains(t, rc.Text, "standalone_obligations: 1")
	require.NotContains(t, AutoWakePromptFor(rc), "turn_id=", "宿主退回原有『新开 turn』路径")
}

// TestResumeContext_AttributedLiveRowsStillGateFinalize 是反证：EC-G4 只豁免无归因
// 行，真正归因于某 turn 的非终态行继续压住收尾，且其 turn 优先于调度提示（I1/I3）。
func TestResumeContext_AttributedLiveRowsStillGateFinalize(t *testing.T) {
	source := &staticObligationSource{obligations: []ObligationRef{
		{ID: "batch-live", Kind: "batch", ParentTurnID: "turn-2", State: ObligationStateRunning, Total: 2, Completed: 1},
		{ID: "batch-legacy", Kind: "batch", State: ObligationStateRunning, Total: 1},
	}}

	rc := BuildResumeContext(context.Background(), source, nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
		TurnID:          "turn-1",
	})

	require.Equal(t, "turn-2", rc.TurnID, "活体归因行所属的 turn 优先于调度提示（I3）")
	require.Equal(t, 1, rc.PendingCount, "I1 不因 EC-G4 放宽：归因的非终态行继续压住收尾")
	require.Equal(t, 1, rc.StandaloneCount)
	require.False(t, rc.Terminal)

	prompt := AutoWakePromptFor(rc)
	require.Contains(t, prompt, "仍有未终态 obligation：不得收尾（I1）")
	require.NotContains(t, prompt, "请直接产出终局报告")
}
