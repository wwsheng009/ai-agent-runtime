package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// §4.2 只作观测：token 水位只出现在操作者 HTTP 读模型（快照/审计）；模型面工具
// （subagent_status → SupervisionDescendants）的行不得携带它——父代理不按
// "tokens 84%" 这类成本读数在运行中反应，决策输入是业务进度与结果。
func TestSupervisionBudgetWatermarkObservationOnly(t *testing.T) {
	handler, store := newAPISupervisionToolTestHandler(t, "api-budget-observation")
	ctx := context.Background()
	now := time.Now().UTC()

	created, err := store.CreateExecutionRun(ctx, supervision.ExecutionRun{
		RunID:           "run-obs-budget",
		Kind:            supervision.RunKindAgentRun,
		Workflow:        supervision.RunWorkflowSpawnAgent,
		RootSessionID:   "parent-session",
		ParentSessionID: "parent-session",
		SessionID:       "child-1",
		Status:          supervision.RunStatusRunning,
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

	handler.SetSupervisionDescendantProvider(&recordingAPIDescendantProvider{states: []supervision.DescendantState{
		{
			Kind:             supervision.SubjectAgentSession,
			ID:               "child-1",
			ParentPath:       []string{"parent-session"},
			ExecutionStatus:  "running",
			SupervisionState: supervision.SupervisionRunning,
		},
	}})

	// 观测面：HTTP 快照（操作者读模型）必须携带水位。
	rec := httptest.NewRecorder()
	handler.GetSupervisionSnapshot(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/runtime/supervision/snapshot?root_session_id=parent-session",
		nil,
	))
	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Snapshot struct {
			Descendants []struct {
				ID          string  `json:"id"`
				BudgetLevel string  `json:"budget_level"`
				BudgetLine  string  `json:"budget_line"`
				BudgetRatio float64 `json:"budget_ratio"`
			} `json:"descendants"`
		} `json:"snapshot"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Snapshot.Descendants, 1)
	require.Equal(t, "child-1", payload.Snapshot.Descendants[0].ID)
	require.Equal(t, "soft", payload.Snapshot.Descendants[0].BudgetLevel, "操作者读模型必须看到水位")
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", payload.Snapshot.Descendants[0].BudgetLine)
	require.InDelta(t, 0.84, payload.Snapshot.Descendants[0].BudgetRatio, 0.0001)

	// 模型面：同一 store、同一 run，subagent_status 的行不得携带水位。
	controller := newHandlerSupervisionToolController(handler)
	require.NotNil(t, controller)
	snapshot, err := controller.SupervisionDescendants(ctx, "parent-session", toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Descendants)
	for _, item := range snapshot.Descendants {
		require.Empty(t, item.BudgetLevel, "模型面不得携带 token 水位")
		require.Empty(t, item.BudgetLine)
		require.Zero(t, item.BudgetRatio)
	}
}
