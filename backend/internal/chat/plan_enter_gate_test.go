package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// The plan-entry confirmation must reuse the ordinary approval channel: the
// engine asks, the actor publishes approval_requested (same reason key hosts map
// to copy), and the user's answer decides the tool call.
func TestEnterPlanModeGateUsesActorApprovalChannel(t *testing.T) {
	ctx := context.Background()
	actor, _, store := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, t.TempDir())
	engine := actor.agent.GetPermissionEngine()
	require.NotNil(t, engine)
	require.NotNil(t, engine.AskHandler, "actor must be the engine's approval handler")

	type outcome struct {
		decision runtimepolicy.Decision
		err      error
	}
	evaluate := func(callID string) chan outcome {
		out := make(chan outcome, 1)
		go func() {
			decision, err := engine.Evaluate(ctx, runtimepolicy.EvalRequest{
				ToolName:   runtimepolicy.PlanEnterToolName,
				SessionID:  actor.id,
				ToolCallID: callID,
				Args:       map[string]interface{}{"plan_path": "docs/plan.md"},
			})
			out <- outcome{decision: decision, err: err}
		}()
		return out
	}

	var pendingOutcome chan outcome
	waitPending := func(id string) *ApprovalRequest {
		t.Helper()
		var pending *ApprovalRequest
		require.Eventually(t, func() bool {
			pending = actor.PendingApproval()
			return pending != nil && pending.ID == id
		}, 5*time.Second, 10*time.Millisecond, "pending approval %s not surfaced", id)
		return pending
	}
	approve := func(pending *ApprovalRequest, allowed bool) runtimepolicy.Decision {
		require.True(t, actor.resolveApproval(pending.ID, runtimepolicy.ApprovalResponse{Allowed: allowed}),
			"pending approval %s must be resolvable", pending.ID)
		select {
		case result := <-pendingOutcome:
			require.NoError(t, result.err)
			return result.decision
		case <-time.After(5 * time.Second):
			t.Fatal("engine evaluation did not finish after the approval was resolved")
			return runtimepolicy.Decision{}
		}
	}

	pendingOutcome = evaluate("call_enter_gate_1")
	pending := waitPending("call_enter_gate_1")
	require.Equal(t, runtimepolicy.PlanEnterToolName, pending.ToolName)
	require.Equal(t, runtimepolicy.PlanAutoEnterApprovalReason, pending.Reason)

	events, err := store.ListEvents(ctx, actor.id, 0, 0)
	require.NoError(t, err)
	var requested *runtimeevents.Event
	for i := range events {
		if events[i].Type == EventApprovalRequested && events[i].Payload["request_id"] == pending.ID {
			requested = &events[i]
		}
	}
	require.NotNil(t, requested, "approval_requested event missing for the plan-entry gate")
	require.Equal(t, runtimepolicy.PlanAutoEnterApprovalReason, requested.Payload["reason"],
		"hosts match on the exact reason key to render the confirmation copy")

	decision := approve(pending, true)
	require.Equal(t, runtimepolicy.DecisionAllow, decision.Type)

	// Denying the prompt denies the tool call; plan mode never changes.
	pendingOutcome = evaluate("call_enter_gate_2")
	pending = waitPending("call_enter_gate_2")
	decision = approve(pending, false)
	require.Equal(t, runtimepolicy.DecisionDeny, decision.Type)

	reloaded, err := actor.loadSession(ctx)
	require.NoError(t, err)
	require.False(t, planmode.IsActive(planmode.Load(reloaded)),
		"refusing the confirmation must leave plan mode untouched")
}
