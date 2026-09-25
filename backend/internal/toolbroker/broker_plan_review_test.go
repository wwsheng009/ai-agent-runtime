package toolbroker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type capturingPlanReviewController struct {
	calls    []PlanReviewArgs
	sessions []string
	result   *PlanReviewResult
	err      error
}

func (c *capturingPlanReviewController) ReviewPlan(_ context.Context, sessionID string, args PlanReviewArgs) (*PlanReviewResult, error) {
	c.calls = append(c.calls, args)
	c.sessions = append(c.sessions, sessionID)
	if c.err != nil {
		return nil, c.err
	}
	return c.result, nil
}

// The plan_review tool is advertised (and routed) only when the host wires a
// review controller, and it carries no write capability.
func TestBrokerPlanReviewDefinitionAndRouting(t *testing.T) {
	without := (&Broker{}).Definitions()
	for _, def := range without {
		require.NotEqual(t, ToolPlanReview, def.Name, "plan_review must stay hidden without a controller")
	}

	ctrl := &capturingPlanReviewController{result: &PlanReviewResult{
		Active:      true,
		Status:      "active",
		PlanID:      "proj/plan",
		PlanPath:    "docs/plan.md",
		Version:     2,
		ReviewRound: 1,
		Source:      "session",
		Content:     "# plan",
		ContentSize: 6,
	}}
	broker := &Broker{PlanReview: ctrl}

	found := false
	for _, def := range broker.Definitions() {
		if def.Name == ToolPlanReview {
			found = true
			props, _ := def.Parameters["properties"].(map[string]interface{})
			require.Contains(t, props, "plan_id")
			require.Contains(t, props, "plan_path")
			require.Contains(t, props, "version")
		}
	}
	require.True(t, found, "plan_review definition missing")
	assert.True(t, broker.IsBrokerTool(ToolPlanReview))
	assert.True(t, broker.IsBrokerTool("plan-review"))
	assert.Equal(t, ToolPlanReview, normalizeToolName("PlanReview"))
	assert.Equal(t, ToolPlanReview, normalizeToolName("plan-review"))

	raw, meta, err := broker.ExecuteToolCall(context.Background(), "session-review", types.ToolCall{
		ID:   "call_plan_review",
		Name: ToolPlanReview,
		Args: map[string]interface{}{
			"plan_id":   " proj/plan ",
			"plan_path": " docs/plan.md ",
			"version":   float64(2),
		},
	})
	require.NoError(t, err)
	result, ok := raw.(*PlanReviewResult)
	require.True(t, ok, "got %T", raw)
	assert.True(t, result.Active)
	assert.Equal(t, toolresult.KindStructured, meta[toolresult.MetadataKey])
	assert.Equal(t, "proj/plan", meta["plan_id"])
	assert.Equal(t, 2, meta["version"])
	require.Len(t, ctrl.calls, 1)
	assert.Equal(t, PlanReviewArgs{PlanID: "proj/plan", PlanPath: "docs/plan.md", Version: 2}, ctrl.calls[0])
	assert.Equal(t, []string{"session-review"}, ctrl.sessions)
}

func TestBrokerPlanReviewRequiresController(t *testing.T) {
	broker := &Broker{}
	_, _, err := broker.ExecuteToolCall(context.Background(), "session-review", types.ToolCall{
		ID:   "call_plan_review_missing",
		Name: ToolPlanReview,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plan review controller is not configured")
}

func TestBrokerPlanReviewPropagatesControllerError(t *testing.T) {
	ctrl := &capturingPlanReviewController{err: errors.New("plan not found in the plan archive")}
	broker := &Broker{PlanReview: ctrl}
	_, _, err := broker.ExecuteToolCall(context.Background(), "session-review", types.ToolCall{
		ID:   "call_plan_review_err",
		Name: ToolPlanReview,
		Args: map[string]interface{}{"plan_id": "missing/plan"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plan not found")
}

// Argument keys the broker consumes must be declared, otherwise a typo is
// reported back to the model instead of silently doing nothing.
func TestBrokerPlanReviewArgKeysDeclared(t *testing.T) {
	declared := brokerToolArgKeys[ToolPlanReview]
	for _, key := range []string{"plan_id", "plan_path", "version"} {
		assert.Contains(t, declared, key)
	}
	kinds := brokerToolArgKinds[ToolPlanReview]
	assert.Equal(t, toolArgFieldString, kinds["plan_id"])
	assert.Equal(t, toolArgFieldString, kinds["plan_path"])
	assert.Equal(t, toolArgFieldNumber, kinds["version"])
}
