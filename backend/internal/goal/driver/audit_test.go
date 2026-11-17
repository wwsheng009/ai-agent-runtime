package driver

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

func TestBuildAuditPayloadIsMinimalAndStable(t *testing.T) {
	t.Parallel()
	active := &goal.SessionGoal{
		GoalID:        "goal-audit",
		Status:        goal.StatusActive,
		RoundsStarted: 2,
		MaxRounds:     5,
		TokensUsed:    100,
		TokenBudget:   200,
		Objective:     "sensitive objective",
	}
	view := View{
		Goal:                   active,
		HasGoal:                true,
		ContinuationLimit:      3,
		ContinuationCount:      2,
		NoProgressStrikes:      1,
		IterationToolAvailable: true,
	}
	decision := Decide(view)
	payload := BuildAuditPayload("run_terminal", view, decision)

	if payload["schema"] != "goal.driver.decision.v1" ||
		payload["trigger"] != "run_terminal" ||
		payload["action"] != string(ActionSubmitContinuation) ||
		payload["reason"] != "goal_active" ||
		payload["goal_id"] != "goal-audit" ||
		payload["goal_status"] != string(goal.StatusActive) ||
		payload["prompt_kind"] != "continuation" {
		t.Fatalf("unexpected audit payload: %#v", payload)
	}
	encoded := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%#v", payload)))
	if strings.Contains(encoded, "sensitive objective") || strings.Contains(encoded, "probe") {
		t.Fatalf("audit payload must not include objective/prompt text: %s", encoded)
	}
}
