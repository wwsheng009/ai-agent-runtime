package supervision

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvaluator_AcknowledgedAndResolved_OnlyInspect(t *testing.T) {
	e := Evaluator{}

	acked := testNotification("agent-ack", 1)
	acked.DecisionState = DecisionAcknowledged
	require.Equal(t, []string{"inspect"}, e.EvaluateAllowedActions(acked))

	actioned := testNotification("agent-actioned", 1)
	actioned.DecisionState = DecisionActioned
	require.Equal(t, []string{"inspect"}, e.EvaluateAllowedActions(actioned))

	resolved := testNotification("agent-resolved", 1)
	resolved.ResolutionState = ResolutionClosed
	require.Equal(t, []string{"inspect"}, e.EvaluateAllowedActions(resolved))
}

// TestEvaluator_DeferredNotDue verifies a deferred-but-not-due item may only
// be inspected or acknowledged; cancel/close stay blocked.
func TestEvaluator_DeferredNotDue(t *testing.T) {
	e := Evaluator{}
	n := testNotification("agent-defer", 1)
	future := timeNow().Add(24 * time.Hour)
	n.DecisionState = DecisionDeferred
	n.DeferUntil = &future

	allowed := e.EvaluateAllowedActions(n)
	require.True(t, AllowedAction(allowed, ActionInspect))
	require.True(t, AllowedAction(allowed, ActionAcknowledge))
	require.False(t, AllowedAction(allowed, ActionCancel))
	require.False(t, AllowedAction(allowed, ActionClose))
}

// TestEvaluator_AutoActionInFlight_NoDuplicate verifies doc 6.2 rule 6:
// when the runtime already accepted/started a control action, the parent may
// acknowledge and inspect but must not issue a duplicate cancel/close.
func TestEvaluator_AutoActionInFlight_NoDuplicate(t *testing.T) {
	e := Evaluator{}
	n := testNotification("agent-auto", 1)
	n.AutoActionID = "act_runtime_1"
	n.RecommendedAction = "cancel"
	n.SupervisionState = SupervisionCanceling

	allowed := e.EvaluateAllowedActions(n)
	require.True(t, AllowedAction(allowed, ActionInspect))
	require.True(t, AllowedAction(allowed, ActionAcknowledge))
	require.False(t, AllowedAction(allowed, ActionCancel), "duplicate cancel while runtime action in flight")
	require.False(t, AllowedAction(allowed, ActionClose))
	require.False(t, AllowedAction(allowed, ActionCancelSubtree))
}

// TestEvaluator_TeamOrphaned_CancelSubtree verifies the team-specific
// cancel_subtree escalation (doc 6.2 rule 5).
func TestEvaluator_TeamOrphaned_CancelSubtree(t *testing.T) {
	e := Evaluator{}
	for _, state := range []SupervisionState{SupervisionOrphaned, SupervisionInvalid, SupervisionTimedOut} {
		n := testNotification("team-1", 1)
		n.SubjectKind = SubjectTeam
		n.SupervisionState = state
		allowed := e.EvaluateAllowedActions(n)
		require.True(t, AllowedAction(allowed, ActionCancelSubtree), "state=%s", state)
		require.True(t, AllowedAction(allowed, ActionCancel))
		require.True(t, AllowedAction(allowed, ActionClose))
	}

	// A merely stalled team does not escalate to subtree cancel.
	n := testNotification("team-2", 1)
	n.SubjectKind = SubjectTeam
	n.SupervisionState = SupervisionStalled
	require.False(t, AllowedAction(e.EvaluateAllowedActions(n), ActionCancelSubtree))
}

// TestEvaluator_AgentTimeout_CancelClose verifies per-subject action sets.
func TestEvaluator_AgentTimeout_CancelClose(t *testing.T) {
	e := Evaluator{}
	for _, kind := range []SubjectKind{SubjectAgentSession, SubjectAgentRun, SubjectTeamTask} {
		n := testNotification("subject-1", 1)
		n.SubjectKind = kind
		n.SupervisionState = SupervisionTimedOut
		allowed := e.EvaluateAllowedActions(n)
		require.True(t, AllowedAction(allowed, ActionCancel), "kind=%s", kind)
		require.True(t, AllowedAction(allowed, ActionClose), "kind=%s", kind)
		require.False(t, AllowedAction(allowed, ActionCancelSubtree), "kind=%s", kind)
	}
}

// TestEvaluator_RecommendedAction verifies the policy hints per state.
func TestEvaluator_RecommendedAction(t *testing.T) {
	e := Evaluator{}

	n := testNotification("agent-1", 1)
	n.SupervisionState = SupervisionTimedOut
	require.Equal(t, string(ActionCancel), e.EvaluateRecommendedAction(n))

	n.SupervisionState = SupervisionStalled
	require.Equal(t, string(ActionCancel), e.EvaluateRecommendedAction(n))

	n.SupervisionState = SupervisionOrphaned
	require.Equal(t, string(ActionClose), e.EvaluateRecommendedAction(n))

	n.SupervisionState = SupervisionOrphaned
	n.SubjectKind = SubjectTeam
	require.Equal(t, string(ActionCancelSubtree), e.EvaluateRecommendedAction(n))

	n.SupervisionState = SupervisionBlocked
	require.Equal(t, "inspect_cancel_result", e.EvaluateRecommendedAction(n))

	n.SupervisionState = SupervisionHealthy
	require.Equal(t, string(ActionInspect), e.EvaluateRecommendedAction(n))

	// Explicit recommended action wins.
	n = testNotification("agent-2", 1)
	n.RecommendedAction = "reassign"
	require.Equal(t, "reassign", e.EvaluateRecommendedAction(n))
}
