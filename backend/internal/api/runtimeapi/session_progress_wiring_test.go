package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestProgressRecorderForSessionStampsActiveRun pins the P0-1 API wiring: the
// recorder returned for a session must stamp LastProgressAt/progress_seq on
// the session's active execution run (the same store write the supervision
// ladder reads), and must stay a nil no-op when durable supervision is not
// configured.
func TestProgressRecorderForSessionStampsActiveRun(t *testing.T) {
	ctx := context.Background()
	handler, store, _ := newAPIWakeTestHandler(t, "api-progress-wiring")

	supervisor := handler.getExecutionSupervisor()
	require.NotNil(t, supervisor)
	run, err := supervisor.StartRun(ctx, supervision.RunSpec{
		SessionID:       "child-progress-1",
		ParentSessionID: "parent-progress-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, run.RunID)

	recorder := handler.progressRecorderForSession("child-progress-1")
	require.NotNil(t, recorder)
	recorder("llm_response")

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.False(t, got.LastProgressAt.IsZero(),
		"progress recorder must stamp LastProgressAt on the active run")
	require.NotZero(t, got.ProgressSeq)
}

// TestProgressRecorderForSessionNilWithoutSupervision keeps the "no store, no
// hook" contract: without a supervision store the factory returns nil so the
// ReAct loop keeps its previous behavior byte-for-byte.
func TestProgressRecorderForSessionNilWithoutSupervision(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	require.Nil(t, handler.progressRecorderForSession("child-progress-2"))
}

// TestProgressRecorderForSessionDropsUnknownSession pins the drop path: a
// session without any execution run resolves to "" and must not panic or write.
func TestProgressRecorderForSessionDropsUnknownSession(t *testing.T) {
	handler, _, _ := newAPIWakeTestHandler(t, "api-progress-no-run")
	recorder := handler.progressRecorderForSession("child-without-run")
	require.NotNil(t, recorder)
	recorder("llm_response") // resolves to no run and drops; must not panic
}

// TestBudgetRecorderForSessionStampsWatermark pins the §4.2 API wiring: the
// budget recorder returned for a session must stamp the live turn-budget
// watermark (level/line/ratio) on the session's active execution run, and must
// stay a nil no-op when durable supervision is not configured.
func TestBudgetRecorderForSessionStampsWatermark(t *testing.T) {
	ctx := context.Background()
	handler, store, _ := newAPIWakeTestHandler(t, "api-budget-wiring")

	supervisor := handler.getExecutionSupervisor()
	require.NotNil(t, supervisor)
	run, err := supervisor.StartRun(ctx, supervision.RunSpec{
		SessionID:       "child-budget-1",
		ParentSessionID: "parent-budget-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, run.RunID)

	recorder := handler.budgetRecorderForSession("child-budget-1")
	require.NotNil(t, recorder)
	recorder(agent.TurnBudgetState{
		Level: "soft",
		Line:  "turn budget: step 2/10 · tokens 84%",
		Ratio: 0.84,
	})

	got, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, "soft", got.BudgetLevel, "budget recorder must stamp the live watermark")
	require.Equal(t, "turn budget: step 2/10 · tokens 84%", got.BudgetLine)
	require.InDelta(t, 0.84, got.BudgetRatio, 0.0001)

	// 无水位读数的回调不得写空值覆盖既有水位。
	recorder(agent.TurnBudgetState{})
	after, err := store.GetExecutionRun(ctx, run.RunID)
	require.NoError(t, err)
	require.Equal(t, "soft", after.BudgetLevel)

	require.Nil(t, NewHandler(skill.NewRegistry(nil), nil, nil).budgetRecorderForSession("child-budget-2"))
}
