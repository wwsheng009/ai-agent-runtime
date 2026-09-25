package policy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingApprovalHandler answers every approval request with a fixed response
// and records the calls, so a test can assert both the resulting decision and
// whether the user was prompted at all.
type recordingApprovalHandler struct {
	calls    int
	requests []ApprovalRequest
	response ApprovalResponse
}

func (h *recordingApprovalHandler) RequestApproval(_ context.Context, req ApprovalRequest) (ApprovalResponse, error) {
	h.calls++
	h.requests = append(h.requests, req)
	return h.response, nil
}

func TestPlanEnterGateContract(t *testing.T) {
	// The gate is a public contract: hosts match on these values, so pin them.
	assert.Equal(t, "enter_plan_mode", PlanEnterToolName)
	assert.Equal(t, "plan_mode:model_auto_enter", PlanAutoEnterApprovalReason)
}

func TestPlanModelAutonomyEnabledParsesPolicyValues(t *testing.T) {
	cases := map[string]bool{
		"1":        true,
		"true":     true,
		"TRUE":     true,
		"Yes":      true,
		" on ":     true,
		"":         false,
		"0":        false,
		"false":    false,
		"off":      false,
		"no":       false,
		"yolo":     false,
		"autonomy": false,
	}
	for raw, want := range cases {
		t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", raw)
		assert.Equal(t, want, PlanModelAutonomyEnabled(), "value %q", raw)
	}
}

// (a) Headless host: without an AskHandler the gate degrades to the pipeline's
// existing headless deny, so the model cannot silently switch modes.
func TestPlanEnterGateHeadlessDenyWithoutAskHandler(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "")
	engine := &Engine{Mode: ModeDefault}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: PlanEnterToolName})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageHeadlessDeny, decision.Stage)
	// resolveAsk keeps its existing headless semantics, whose reason is
	// stage-prefixed by withStage (same as every other headless deny).
	assert.Contains(t, decision.Reason, "approval_required")
}

// (b) Interactive host approves: the confirmation travels on the existing
// approval channel with the plan-entry reason.
func TestPlanEnterGateAsksThenAllows(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "")
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	engine := &Engine{Mode: ModeDefault, AskHandler: handler}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName:   PlanEnterToolName,
		ToolCallID: "call-plan-enter",
		SessionID:  "sess-1",
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageAsk, decision.Stage)
	require.Equal(t, 1, handler.calls)
	assert.Equal(t, PlanEnterToolName, handler.requests[0].ToolName)
	assert.Equal(t, PlanAutoEnterApprovalReason, handler.requests[0].Reason)
	assert.Equal(t, "call-plan-enter", handler.requests[0].ID)
	assert.Equal(t, "sess-1", handler.requests[0].SessionID)
}

// (c) Interactive host denies.
func TestPlanEnterGateAsksThenDenies(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "")
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: false, Reason: "user_declined"}}
	engine := &Engine{Mode: ModeDefault, AskHandler: handler}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: PlanEnterToolName})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageAsk, decision.Stage)
	assert.Equal(t, "ask:user_declined", decision.Reason)
	assert.Equal(t, 1, handler.calls)
}

// (d) Engine policy opts into autonomous entry: no prompt, legacy read-only
// auto-allow path unchanged.
func TestPlanEnterGateEnginePolicySkipsApproval(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "")
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: false}}
	engine := &Engine{
		Mode:                         ModeDefault,
		AskHandler:                   handler,
		PlanAutoEnterWithoutApproval: true,
	}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: PlanEnterToolName})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageReadonlyAuto, decision.Stage, "opt-out keeps the legacy auto-allow stage")
	assert.Equal(t, 0, handler.calls)
}

// (e) Process policy (env) opts into autonomous entry: no prompt.
func TestPlanEnterGateProcessPolicySkipsApproval(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "on")
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: false}}
	engine := &Engine{Mode: ModeDefault, AskHandler: handler}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: PlanEnterToolName})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, 0, handler.calls)
}

// (e') The env opt-out also recovers entry for a headless host without a
// handler.
func TestPlanEnterGateHeadlessAllowedWithAutonomyEnv(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", " 1 ")
	engine := &Engine{Mode: ModeDefault}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: PlanEnterToolName})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
}

// (f) Plan mode is already in effect: nested re-entry must not prompt again and
// is decided by the ordinary mode policy.
func TestPlanEnterGateSkippedWhilePlanModeActive(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "")
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: false}}
	engine := &Engine{
		Mode:                ModePlan,
		PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		AskHandler:          handler,
	}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: PlanEnterToolName, Mode: ModePlan})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageMode, decision.Stage, "decided by the ordinary mode policy, not the gate")
	assert.Equal(t, 0, handler.calls)
}

// (g) An explicit allow rule for the tool wins over the gate: rules decided
// first, so no confirmation is stacked on top.
func TestPlanEnterGateExplicitAllowRuleWins(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "")
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: false}}
	engine := &Engine{
		Mode: ModeDefault,
		Rules: []Rule{{
			Tools:    []string{PlanEnterToolName},
			Decision: DecisionAllow,
			Reason:   "explicit_plan_enter_allow",
		}},
		AskHandler: handler,
	}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: PlanEnterToolName})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageRules, decision.Stage)
	assert.Equal(t, "rules:explicit_plan_enter_allow", decision.Reason)
	assert.Equal(t, 0, handler.calls)
}

// (h) Every other tool keeps its previous behavior: read-only tools still
// auto-allow, and an ordinary write still asks with the ordinary mode reason.
func TestPlanEnterGateLeavesOtherToolsUnchanged(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "")
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	engine := &Engine{Mode: ModeDefault, AskHandler: handler}

	viewDecision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: "view"})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, viewDecision.Type)
	assert.Equal(t, StageReadonlyAuto, viewDecision.Stage)

	writeDecision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "a.go", "content": "x"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, writeDecision.Type)
	require.Equal(t, 1, handler.calls)
	assert.Equal(t, "write", handler.requests[0].ToolName)
	assert.Equal(t, "mode:permission_mode_requires_approval", handler.requests[0].Reason)
}

// bypass_permissions adds no gate-specific special case: the ask flows into
// resolveAsk, which keeps its existing bypass → allow semantics.
func TestPlanEnterGateBypassResolvesToAllow(t *testing.T) {
	t.Setenv("AICLI_PLAN_MODE_MODEL_AUTONOMY", "")
	engine := &Engine{Mode: ModeBypassPermissions}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: PlanEnterToolName,
		Mode:     ModeBypassPermissions,
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageMode, decision.Stage)
	assert.Equal(t, "mode:bypass_permissions", decision.Reason)
}
