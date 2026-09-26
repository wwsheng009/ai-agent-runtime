package policy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// TestDecisionTableModes pins the permission ladder of
// docs/analysis/commandcode-permissions-design-borrowing-20260926.md §6 as one
// matrix, so every later milestone can extend it instead of re-testing modes
// case by case.
func TestDecisionTableModes(t *testing.T) {
	root := t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), root)

	type row struct {
		name                 string
		mode                 Mode
		tool                 string
		args                 map[string]interface{}
		wantType             DecisionType
		wantStage            string
		wantCalls            int
		wantReasonIn         string
		wantApprovalReasonIn string
	}
	rows := []row{
		{"default allows reads", ModeDefault, "view", map[string]interface{}{"file_path": "README.md"}, DecisionAllow, StageReadonlyAuto, 0, "", ""},
		{"default allows read-only shell", ModeDefault, "shell", map[string]interface{}{"command": "git status"}, DecisionAllow, StageReadonlyAuto, 0, "", ""},
		{"default asks for writes", ModeDefault, "write", map[string]interface{}{"file_path": "README.md"}, DecisionAllow, StageAsk, 1, "", "permission_mode_requires_approval"},
		{"default asks for secret reads", ModeDefault, "shell", map[string]interface{}{"command": "cat .env"}, DecisionAllow, StageAsk, 1, "", "permission_mode_requires_approval"},
		{"accept_edits allows writes", ModeAcceptEdits, "write", map[string]interface{}{"file_path": "README.md"}, DecisionAllow, StageMode, 0, "", ""},
		{"accept_edits allows safe file commands", ModeAcceptEdits, "shell", map[string]interface{}{"command": "mkdir dist"}, DecisionAllow, StageMode, 0, "accept_edits_safe_file", ""},
		{"accept_edits still asks for recursive deletes", ModeAcceptEdits, "shell", map[string]interface{}{"command": "rm -rf dist"}, DecisionAllow, StageAsk, 1, "", "permission_mode_requires_approval"},
		{"plan allows reads", ModePlan, "view", map[string]interface{}{"file_path": "README.md"}, DecisionAllow, StageReadonlyAuto, 0, "", ""},
		{"plan denies writes", ModePlan, "write", map[string]interface{}{"file_path": "README.md"}, DecisionDeny, StageMode, 0, "plan_denies_non_readonly", ""},
		{"bypass allows writes", ModeBypassPermissions, "write", map[string]interface{}{"file_path": "README.md"}, DecisionAllow, StageMode, 0, "bypass_permissions", ""},
		{"bypass still asks for the breaker", ModeBypassPermissions, "shell", map[string]interface{}{"command": "rm -rf /"}, DecisionAllow, StageAsk, 1, "", "shell_breaker"},
		{"dont_ask allows read-only shell", ModeDontAsk, "shell", map[string]interface{}{"command": "git status"}, DecisionAllow, StageReadonlyAuto, 0, "", ""},
		{"dont_ask denies writes", ModeDontAsk, "write", map[string]interface{}{"file_path": "README.md"}, DecisionDeny, StageMode, 0, "dont_ask_denies_unapproved", ""},
		{"dont_ask denies the breaker", ModeDontAsk, "shell", map[string]interface{}{"command": "rm -rf /"}, DecisionDeny, StageShellBreaker, 0, "shell_breaker", ""},
		{"dont_ask denies secret reads", ModeDontAsk, "shell", map[string]interface{}{"command": "cat .env"}, DecisionDeny, StageMode, 0, "dont_ask_denies_unapproved", ""},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
			engine := &Engine{
				Mode:       tc.mode,
				AskHandler: handler,
				Policy:     NewToolExecutionPolicy(nil, false),
			}
			decision, err := engine.Evaluate(ctx, EvalRequest{
				ToolName: tc.tool,
				Args:     tc.args,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.wantType, decision.Type, "reason=%s stage=%s", decision.Reason, decision.Stage)
			assert.Equal(t, tc.wantStage, decision.Stage)
			assert.Equal(t, tc.wantCalls, handler.calls)
			if tc.wantReasonIn != "" {
				assert.Contains(t, decision.Reason, tc.wantReasonIn)
			}
			if tc.wantApprovalReasonIn != "" {
				require.NotEmpty(t, handler.requests)
				assert.Contains(t, handler.requests[0].Reason, tc.wantApprovalReasonIn)
			}
		})
	}
}

// TestDecisionTableHeadlessAndHardAsk pins the two ask-resolution paths that do
// not depend on a host: a missing AskHandler denies, and the root/home breaker
// is not waivable by bypass.
func TestDecisionTableHeadlessAndHardAsk(t *testing.T) {
	headless := &Engine{Mode: ModeDefault, Policy: NewToolExecutionPolicy(nil, false)}
	decision, err := headless.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "README.md"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageHeadlessDeny, decision.Stage)
	assert.Contains(t, decision.Reason, "approval_required")

	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true, Remember: true}}
	bypass := &Engine{Mode: ModeBypassPermissions, AskHandler: handler, Policy: NewToolExecutionPolicy(nil, false), Grants: &MemoryGrantStore{}}
	decision, err = bypass.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		Args:     map[string]interface{}{"command": "rm -rf /"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, 1, handler.calls)
	assert.Empty(t, bypass.Grants.(*MemoryGrantStore).List(), "a hard ask must never be remembered")
}

// TestEngineDisableBypassDowngradesToDefault pins §4.9: a disable_bypass policy
// turns bypass-mode requests back into default mode.
func TestEngineDisableBypassDowngradesToDefault(t *testing.T) {
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	engine := &Engine{
		Mode:          ModeBypassPermissions,
		DisableBypass: true,
		AskHandler:    handler,
		Policy:        NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Mode:     ModeBypassPermissions,
		Args:     map[string]interface{}{"file_path": "README.md"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageAsk, decision.Stage, "disable_bypass must not skip the approval flow")
	assert.Equal(t, 1, handler.calls)

	headless := &Engine{Mode: ModeBypassPermissions, DisableBypass: true, Policy: NewToolExecutionPolicy(nil, false)}
	decision, err = headless.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "README.md"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageHeadlessDeny, decision.Stage)
}
