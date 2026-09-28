package planmode

import "strings"

// A model-authored approve/quit on exit_plan_mode is a request, not a verdict:
// interactive hosts confirm it on the ordinary approval channel — the same one
// the enter_plan_mode gate uses (see policy.PlanAutoEnterApprovalReason) — so
// the user answers a native approval card instead of a dead-end pending marker
// that only `/plan approve` can clear.
//
// Hosts match on these reason keys to render their confirmation copy; the tool
// args additionally carry {"decision":"approve"|"quit","plan_path":...} for
// richer copy. request_changes has no reason: it never switches the mode, so it
// is applied directly (the model keeps revising in plan mode).
const (
	// ExitApprovalReasonApprove confirms a model-authored plan approval
	// ("start implementation").
	ExitApprovalReasonApprove = "plan_mode:model_auto_exit_approve"
	// ExitApprovalReasonQuit confirms a model-authored plan close without
	// execution.
	ExitApprovalReasonQuit = "plan_mode:model_auto_exit_quit"
)

// ExitApprovalReason maps a model-authored exit decision to the approval reason
// its interactive confirmation carries. Decisions that switch no mode (or are
// not model-gated) return "".
func ExitApprovalReason(decision ExitDecision) string {
	switch ExitDecision(strings.TrimSpace(string(decision))) {
	case ExitApprove:
		return ExitApprovalReasonApprove
	case ExitQuit:
		return ExitApprovalReasonQuit
	default:
		return ""
	}
}
