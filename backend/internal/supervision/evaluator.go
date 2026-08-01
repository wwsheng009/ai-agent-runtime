package supervision

import (
	"strings"
	"time"
)

// timeNow is a variable so tests can override the clock.
var timeNow = func() time.Time { return time.Now() }

// Evaluator computes recommended_action and allowed_actions from a
// notification's state plus the caller scope (doc 6.2 rules 5-6, 6.6
// constraints). The server always recomputes allowed actions; a model-passed
// action is never implicit authorization.
type Evaluator struct{}

// EvaluateAllowedActions returns the action kinds permitted for the given
// notification. Rules:
//   - acknowledged / actioned / resolved items only allow inspect.
//   - a deferred-but-not-due item allows inspect and acknowledge.
//   - an in-progress auto action forbids duplicate cancel/close but still
//     allows inspect + acknowledge (doc 6.6 rule 6: no unsafe duplicate).
//   - otherwise the subject kind decides between cancel/close and
//     cancel_subtree.
func (e Evaluator) EvaluateAllowedActions(n Notification) []string {
	allowed := []string{string(ActionInspect)}
	if n.DecisionState == DecisionAcknowledged || n.DecisionState == DecisionActioned {
		return dedupeAllowed(allowed)
	}
	if n.ResolutionState != "" && n.ResolutionState != ResolutionUnresolved {
		return dedupeAllowed(allowed)
	}
	if n.DecisionState == DecisionDeferred && n.DeferUntil != nil && !timeNow().After(*n.DeferUntil) {
		return dedupeAllowed(append(allowed, string(ActionAcknowledge)))
	}
	if strings.TrimSpace(n.AutoActionID) != "" && strings.TrimSpace(n.RecommendedAction) != "" {
		// Runtime is already acting (or acted): allow acknowledging the state
		// and inspecting the result, but not a duplicate cancel/close.
		return dedupeAllowed(append(allowed, string(ActionAcknowledge)))
	}
	switch n.SubjectKind {
	case SubjectTeam:
		allowed = append(allowed, string(ActionAcknowledge), string(ActionDefer), string(ActionCancel), string(ActionClose))
		if n.SupervisionState == SupervisionOrphaned || n.SupervisionState == SupervisionInvalid || n.SupervisionState == SupervisionTimedOut {
			allowed = append(allowed, string(ActionCancelSubtree))
		}
	case SubjectAgentSession, SubjectAgentRun, SubjectTeamTask:
		allowed = append(allowed, string(ActionAcknowledge), string(ActionDefer), string(ActionCancel), string(ActionClose))
	default:
		allowed = append(allowed, string(ActionAcknowledge), string(ActionDefer))
	}
	return dedupeAllowed(allowed)
}

// EvaluateRecommendedAction suggests the safest next step. It is a policy
// hint, not an approved action.
func (e Evaluator) EvaluateRecommendedAction(n Notification) string {
	if n.DecisionState == DecisionAcknowledged || n.DecisionState == DecisionActioned {
		return "inspect"
	}
	if n.ResolutionState != "" && n.ResolutionState != ResolutionUnresolved {
		return "inspect"
	}
	if strings.TrimSpace(n.RecommendedAction) != "" {
		return strings.TrimSpace(n.RecommendedAction)
	}
	switch n.SupervisionState {
	case SupervisionTimedOut, SupervisionStalled:
		return string(ActionCancel)
	case SupervisionOrphaned, SupervisionOrphanSuspected:
		if n.SubjectKind == SubjectTeam {
			return string(ActionCancelSubtree)
		}
		return string(ActionClose)
	case SupervisionInvalid:
		return string(ActionClose)
	case SupervisionBlocked, SupervisionCancelRequested, SupervisionCanceling:
		return "inspect_cancel_result"
	default:
		return string(ActionInspect)
	}
}

// AllowedAction returns true when the action kind is present in the list.
func AllowedAction(allowed []string, action ActionKind) bool {
	target := strings.TrimSpace(string(action))
	for _, value := range allowed {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func dedupeAllowed(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
