package supervision

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// §4.2 运行中水位（只作观测）：token 水位只允许出现在**显式请求**的操作者
// 读模型里；模型面（默认请求）的行必须与未接线宿主字节一致——父代理不得在
// 运行中按 "tokens 84%" 反应。
func TestAttachExecutionRunBudgetWatermarkObservationOnly(t *testing.T) {
	store := newTestStore(t, "snapshot-budget-observation")
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

	// 模型面（默认）：run 字段照常附着，但 token 水位不可见。
	modelRow := SnapshotItem{Kind: SubjectAgentSession, ID: "child-snap"}
	attachExecutionRun(ctx, store, &modelRow, false)
	require.Equal(t, "run-snap-budget", modelRow.RunID, "模型面仍须携带 run 观测字段")
	require.Empty(t, modelRow.BudgetLevel, "模型面不得携带 token 水位")
	require.Empty(t, modelRow.BudgetLine)
	require.Zero(t, modelRow.BudgetRatio)
	modelJSON, err := json.Marshal(modelRow)
	require.NoError(t, err)
	require.NotContains(t, string(modelJSON), `"budget_level"`, "模型面行输出不得出现 token 水位字段")
	require.NotContains(t, string(modelJSON), `"budget_line"`)
	require.NotContains(t, string(modelJSON), `"budget_ratio"`)
	require.NotContains(t, string(modelJSON), "84%")

	// 观测面（显式请求）：水位可见。
	operatorRow := SnapshotItem{Kind: SubjectAgentSession, ID: "child-snap"}
	attachExecutionRun(ctx, store, &operatorRow, true)
	require.Equal(t, "soft", operatorRow.BudgetLevel)
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", operatorRow.BudgetLine)
	require.InDelta(t, 0.84, operatorRow.BudgetRatio, 0.0001)
}
