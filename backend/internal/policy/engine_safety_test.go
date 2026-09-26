package policy

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func shellToolInfo() *skill.ToolInfo {
	return &skill.ToolInfo{Name: "shell", MCPTrustLevel: "local", ExecutionMode: "local_mcp"}
}

func writeToolInfo(name string) *skill.ToolInfo {
	return &skill.ToolInfo{Name: name, MCPTrustLevel: "local", ExecutionMode: "local_mcp"}
}

func TestEngineShellBreakerRequiresApprovalEvenUnderBypass(t *testing.T) {
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	engine := &Engine{
		Mode:       ModeBypassPermissions,
		AskHandler: handler,
		Policy:     NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "rm -rf /"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls, "bypass must not auto-resolve the breaker")
	assert.Contains(t, handler.requests[0].Reason, "shell_breaker:root_home_removal")
	assert.Equal(t, DecisionAllow, decision.Type, "approved breaker request executes")
}

func TestEngineShellBreakerDeniedByUser(t *testing.T) {
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: false, Reason: "approval_denied"}}
	engine := &Engine{
		Mode:       ModeBypassPermissions,
		AskHandler: handler,
		Policy:     NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "rm -rf $HOME"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Contains(t, decision.Reason, "approval_denied")
}

func TestEngineShellBreakerHeadlessBypassDenies(t *testing.T) {
	engine := &Engine{
		Mode:   ModeBypassPermissions,
		Policy: NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "rm -rf /"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, "headless_deny:approval_required", decision.Reason)
	assert.Equal(t, StageHeadlessDeny, decision.Stage)
}

func TestEngineShellBreakerDeniesInPlanAndDontAsk(t *testing.T) {
	for _, mode := range []Mode{ModePlan, ModeDontAsk} {
		t.Run(string(mode), func(t *testing.T) {
			engine := &Engine{
				Mode:   mode,
				Policy: NewToolExecutionPolicy(nil, false),
			}
			decision, err := engine.Evaluate(context.Background(), EvalRequest{
				ToolName: "shell",
				ToolInfo: shellToolInfo(),
				Args:     map[string]interface{}{"command": "rm -rf /"},
			})
			require.NoError(t, err)
			assert.Equal(t, DecisionDeny, decision.Type)
			assert.Equal(t, StageShellBreaker, decision.Stage)
			assert.Contains(t, decision.Reason, "shell_breaker:root_home_removal")
		})
	}
}

func TestEngineShellBreakerIgnoresCleanCommands(t *testing.T) {
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	engine := &Engine{
		Mode:       ModeDefault,
		AskHandler: handler,
		Policy:     NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "rm -rf ./dist"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls, "ordinary rm must keep the normal mode ask")
	assert.NotContains(t, handler.requests[0].Reason, "shell_breaker")
	assert.Equal(t, DecisionAllow, decision.Type, "the user approved the ordinary ask")
}

func TestEngineShellBreakerCallbackCannotWaive(t *testing.T) {
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	engine := &Engine{
		Mode:       ModeDefault,
		AskHandler: handler,
		Policy:     NewToolExecutionPolicy(nil, false),
		Callback: func(context.Context, EvalRequest) (Decision, string, error) {
			return Decision{Type: DecisionAllow}, "callback_allows", nil
		},
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "rm -rf /"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls, "callback must not replace the breaker ask")
	assert.Equal(t, DecisionAllow, decision.Type, "user approval still resolves it")
}

func TestEngineSensitiveWritePromptsInAcceptEdits(t *testing.T) {
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	engine := &Engine{
		Mode:       ModeAcceptEdits,
		AskHandler: handler,
		Policy:     NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		ToolInfo: writeToolInfo("write"),
		Args:     map[string]interface{}{"file_path": ".env", "content": "SECRET=1"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls)
	assert.Contains(t, handler.requests[0].Reason, "sensitive_write:secret")
	assert.Equal(t, DecisionAllow, decision.Type)
}

func TestEngineSensitiveWriteHeadlessDeniesWithoutHandler(t *testing.T) {
	engine := &Engine{
		Mode:   ModeDefault,
		Policy: NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		ToolInfo: writeToolInfo("write"),
		Args:     map[string]interface{}{"file_path": ".aicli/permissions.yaml", "content": "x"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Contains(t, decision.Reason, "approval_required")
}

func TestEngineSensitiveWriteDontAskDenies(t *testing.T) {
	engine := &Engine{
		Mode:   ModeDontAsk,
		Policy: NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		ToolInfo: writeToolInfo("write"),
		Args:     map[string]interface{}{"file_path": ".env", "content": "SECRET=1"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageSensitiveWrite, decision.Stage)
	assert.Contains(t, decision.Reason, "sensitive_write:secret")
}

func TestEngineSensitiveWriteBypassSkipsGate(t *testing.T) {
	engine := &Engine{
		Mode:   ModeBypassPermissions,
		Policy: NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		ToolInfo: writeToolInfo("write"),
		Args:     map[string]interface{}{"file_path": ".env", "content": "SECRET=1"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, "mode:bypass_permissions", decision.Reason)
}

func TestEngineSensitiveWriteCleanPathStillAutoApproved(t *testing.T) {
	engine := &Engine{
		Mode:   ModeAcceptEdits,
		Policy: NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		ToolInfo: writeToolInfo("write"),
		Args:     map[string]interface{}{"file_path": "docs/README.md", "content": "hello"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
}

func TestEngineReadOnlySecretArgumentFallsOutOfFastPath(t *testing.T) {
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: true}}
	engine := &Engine{
		Mode:       ModeDefault,
		AskHandler: handler,
		Policy:     NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "cat .env"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls, "cat .env must require an approval")
	assert.Contains(t, handler.requests[0].Reason, "permission_mode_requires_approval")
	assert.Equal(t, DecisionAllow, decision.Type, "the approving handler resolves the ask")

	clean, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "cat README.md"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, clean.Type)
	assert.Contains(t, clean.Reason, "shell_readonly")
}

func TestEngineReadOnlySecretArgumentDeniedInPlan(t *testing.T) {
	engine := &Engine{
		Mode:   ModePlan,
		Policy: NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "cat .env"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, "mode:plan_denies_non_readonly", decision.Reason)
}

func TestEngineDontAskDeniesAskRule(t *testing.T) {
	engine := &Engine{
		Mode:   ModeDontAsk,
		Policy: NewToolExecutionPolicy(nil, false),
		Rules: []Rule{{
			Name:     "ask-write",
			Tools:    []string{"write"},
			Decision: DecisionAsk,
			Reason:   "project_requires_confirmation",
		}},
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		ToolInfo: writeToolInfo("write"),
		Args:     map[string]interface{}{"file_path": "docs/README.md", "content": "hi"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, "mode:dont_ask_denies_unapproved", decision.Reason)
	assert.Equal(t, StageMode, decision.Stage)
}

func TestEngineDontAskDeniesPlanEntry(t *testing.T) {
	engine := &Engine{
		Mode:   ModeDontAsk,
		Policy: NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: PlanEnterToolName,
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, "mode:dont_ask_denies_unapproved", decision.Reason)
}

func TestEngineDontAskAllowsReadOnlyShell(t *testing.T) {
	engine := &Engine{
		Mode:   ModeDontAsk,
		Policy: NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "git status"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Contains(t, decision.Reason, "shell_readonly")
}

func TestEngineDontAskStillEnforcesHardDenyRules(t *testing.T) {
	engine := &Engine{
		Mode:   ModeDontAsk,
		Policy: NewToolExecutionPolicy(nil, false),
		Rules: []Rule{{
			Name:     "deny-shell",
			Tools:    []string{"shell"},
			Decision: DecisionDeny,
			Reason:   "shell_denied",
		}},
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args:     map[string]interface{}{"command": "git status"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageRules, decision.Stage)
}

func TestEngineShellBreakerBatchCommands(t *testing.T) {
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: false}}
	engine := &Engine{
		Mode:       ModeDefault,
		AskHandler: handler,
		Policy:     NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: shellToolInfo(),
		Args: map[string]interface{}{
			"commands": []interface{}{
				map[string]interface{}{"command": "git status"},
				map[string]interface{}{"command": "rm -rf ~"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls)
	assert.True(t, strings.Contains(handler.requests[0].Reason, "shell_breaker"))
	assert.Equal(t, DecisionDeny, decision.Type)
}
