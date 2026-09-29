package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// §4.2 运行中水位：携带预算的进度 tick 落账本三列（level/line/ratio），而普通
// 进度 tick（空水位）必须保留既有读数——高频进度写与低频水位写共用同一入口，
// 后者不得把前者抹掉。
func TestRecordExecutionProgressBudgetWatermark(t *testing.T) {
	store := newTestStore(t, "run-budget-watermark")
	ctx := context.Background()
	now := time.Now().UTC()

	seeded := ExecutionRun{
		RunID:           "run-budget-1",
		Kind:            RunKindAgentRun,
		Workflow:        RunWorkflowSpawnAgent,
		RootSessionID:   "root-budget",
		ParentSessionID: "root-budget",
		SessionID:       "child-budget",
		Status:          RunStatusRunning,
		StartedAt:       now.Add(-time.Minute),
		LastHeartbeatAt: now,
		LastProgressAt:  now,
		CreatedAt:       now.Add(-time.Minute),
		UpdatedAt:       now,
	}
	created, err := store.CreateExecutionRun(ctx, seeded)
	require.NoError(t, err)
	require.True(t, created)

	// 水位 tick：三列落库。
	ok, err := store.RecordExecutionProgress(ctx, RunProgressEvent{
		RunID:       "run-budget-1",
		Kind:        "turn_budget",
		BudgetLevel: "soft",
		BudgetLine:  "turn budget: step 2/10 · tokens 84%",
		BudgetRatio: 0.84,
	}, now)
	require.NoError(t, err)
	require.True(t, ok)

	got, err := store.GetExecutionRun(ctx, "run-budget-1")
	require.NoError(t, err)
	require.Equal(t, "soft", got.BudgetLevel)
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", got.BudgetLine)
	require.InDelta(t, 0.84, got.BudgetRatio, 0.0001)

	// 普通进度 tick：水位保持不变，进度照常推进。
	ok, err = store.RecordExecutionProgress(ctx, RunProgressEvent{
		RunID: "run-budget-1",
		Kind:  "llm_response",
	}, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)

	after, err := store.GetExecutionRun(ctx, "run-budget-1")
	require.NoError(t, err)
	require.Equal(t, "soft", after.BudgetLevel, "普通进度 tick 不得抹掉既有水位")
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", after.BudgetLine)
	require.InDelta(t, 0.84, after.BudgetRatio, 0.0001)
	require.Greater(t, after.ProgressSeq, got.ProgressSeq, "普通进度 tick 仍须推进 progress_seq")

	// 新 turn 的水位覆盖旧读数（水位是"本 turn 的最新读数"）。
	ok, err = store.RecordExecutionProgress(ctx, RunProgressEvent{
		RunID:       "run-budget-1",
		Kind:        "turn_budget",
		BudgetLevel: "ok",
		BudgetLine:  "turn budget: step 1/10 · tokens 4%",
		BudgetRatio: 0.04,
	}, now.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, ok)

	latest, err := store.GetExecutionRun(ctx, "run-budget-1")
	require.NoError(t, err)
	require.Equal(t, "ok", latest.BudgetLevel)
	require.InDelta(t, 0.04, latest.BudgetRatio, 0.0001)
}
