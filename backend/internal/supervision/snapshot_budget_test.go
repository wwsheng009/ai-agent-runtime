package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// §4.2 运行中水位（读侧）：快照行必须把 run 记录的实时水位带给父代理，未上报
// 水位的行保持零值（omitempty ⇒ 未接线宿主的行输出字节不变）。
func TestAttachExecutionRunCarriesBudgetWatermark(t *testing.T) {
	store := newTestStore(t, "snapshot-budget-attach")
	ctx := context.Background()
	now := time.Now().UTC()

	created, err := store.CreateExecutionRun(ctx, ExecutionRun{
		RunID:           "run-snap-budget",
		Kind:            RunKindAgentRun,
		Workflow:        RunWorkflowSpawnAgent,
		RootSessionID:   "root-snap",
		ParentSessionID: "root-snap",
		SessionID:       "child-snap",
		Status:          RunStatusRunning,
		StartedAt:       now,
		LastHeartbeatAt: now,
		LastProgressAt:  now,
		CreatedAt:       now,
		UpdatedAt:       now,
		BudgetLevel:     "soft",
		BudgetLine:      "turn budget: step 2/10 · tokens 84%",
		BudgetRatio:     0.84,
	})
	require.NoError(t, err)
	require.True(t, created)

	item := SnapshotItem{Kind: SubjectAgentSession, ID: "child-snap"}
	attachExecutionRun(ctx, store, &item)
	require.Equal(t, "run-snap-budget", item.RunID)
	require.Equal(t, "soft", item.BudgetLevel, "巡检行必须携带运行中水位")
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", item.BudgetLine)
	require.InDelta(t, 0.84, item.BudgetRatio, 0.0001)

	// 未上报水位的行：零值（输出字节不变）。
	plain := SnapshotItem{Kind: SubjectAgentSession, ID: "child-none"}
	attachExecutionRun(ctx, store, &plain)
	require.Empty(t, plain.BudgetLevel)
	require.Zero(t, plain.BudgetRatio)
}
