package policy

import (
	"os"
	"strings"
)

// PlanEnterToolName is the model-facing tool that switches the session into
// plan mode. It is the only tool the model plan-entry confirmation gate applies
// to (see Engine.PlanAutoEnterWithoutApproval).
const PlanEnterToolName = "enter_plan_mode"

// PlanAutoEnterApprovalReason is the approval reason carried by the
// confirmation request the engine emits when the model calls enter_plan_mode
// without an explicit autonomy policy. Hosts reuse the existing approval
// channel / pending-approval card for it; no separate mechanism is introduced.
const PlanAutoEnterApprovalReason = "plan_mode:model_auto_enter"

// planModelAutonomyEnv is the process-wide policy that restores the legacy
// behavior where the model may switch into (and decide the exit of) plan mode
// on its own. It is read only here so plan entry and exit share one switch.
const planModelAutonomyEnv = "AICLI_PLAN_MODE_MODEL_AUTONOMY"

// PlanModelAutonomyEnabled reports whether the process policy allows the model
// to drive plan-mode transitions without a user confirmation.
//
// AICLI_PLAN_MODE_MODEL_AUTONOMY is parsed case-insensitively with surrounding
// whitespace ignored: 1/true/yes/on enable autonomy; every other value —
// including empty, false, off, no, and unrecognized text — keeps the default
// confirm-first behavior. internal/planmode.ModelMayDecideExit delegates here,
// so plan entry (this gate) and plan exit (model verdicts) cannot diverge.
func PlanModelAutonomyEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(planModelAutonomyEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// planEnterGateArmed reports whether the model plan-entry confirmation gate is
// in force for this request: the request targets enter_plan_mode and neither
// opt-out is set —
//
//   - Engine.PlanAutoEnterWithoutApproval (host/engine field), or
//   - AICLI_PLAN_MODE_MODEL_AUTONOMY (process policy, see
//     PlanModelAutonomyEnabled).
//
// It deliberately ignores the requested mode: callers use it both to keep the
// read-only auto-allow stage from swallowing the tool (so the request can reach
// the gate) and, together with planEnterNeedsApproval, to decide whether to
// emit the confirmation ask.
func (e *Engine) planEnterGateArmed(req EvalRequest) bool {
	if normalizeToolName(req.ToolName) != PlanEnterToolName {
		return false
	}
	if e != nil && e.PlanAutoEnterWithoutApproval {
		return false
	}
	return !PlanModelAutonomyEnabled()
}

// planEnterNeedsApproval reports whether this request must be confirmed by the
// user before the mode switch: the gate is armed and plan mode is not already
// in effect. A nested re-entry while plan mode is active is a no-op transition,
// so it must not prompt again; that request falls through to the ordinary mode
// decision instead.
//
// Explicit user actions never reach this helper: `/plan enter` and
// `--permission-mode plan` switch the mode in the host directly, without a
// model tool call.
func (e *Engine) planEnterNeedsApproval(req EvalRequest, mode Mode) bool {
	return e.planEnterGateArmed(req) && normalizeMode(mode) != ModePlan
}
