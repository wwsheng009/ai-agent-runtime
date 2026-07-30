package policy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type staticHookDispatcher struct {
	decision runtimehooks.Decision
	err      error
}

type captureApprovalHandler struct {
	request ApprovalRequest
}

func (h *captureApprovalHandler) RequestApproval(_ context.Context, req ApprovalRequest) (ApprovalResponse, error) {
	h.request = req
	return ApprovalResponse{Allowed: true}, nil
}

func (d staticHookDispatcher) Dispatch(ctx context.Context, event runtimehooks.Event, payload map[string]interface{}) (runtimehooks.Decision, error) {
	return d.decision, d.err
}

func TestEngineEvaluatePreservesHookNotifyAndEnrichMetadata(t *testing.T) {
	engine := &Engine{
		Hooks: staticHookDispatcher{
			decision: runtimehooks.Decision{
				Action:  runtimehooks.DecisionEnrich,
				Message: "approval context",
				ExtraContext: map[string]string{
					"ticket": "GW-123",
				},
			},
		},
	}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "read_task_spec",
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, "approval context", decision.HookMessage)
	assert.Equal(t, map[string]string{"ticket": "GW-123"}, decision.HookContext)
}

func TestEngineHookModifyContinuesThroughReadOnlyShellValidation(t *testing.T) {
	engine := &Engine{
		Mode: ModeDefault,
		Hooks: staticHookDispatcher{decision: runtimehooks.Decision{
			Action:         runtimehooks.DecisionModify,
			PatchedPayload: json.RawMessage(`{"command":"rm -rf /"}`),
		}},
		Policy: NewToolExecutionPolicy(nil, true),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		ToolInfo: &skill.ToolInfo{Name: "shell", MCPTrustLevel: "local", ExecutionMode: "local_mcp"},
		Args:     map[string]interface{}{"command": "git status"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Contains(t, decision.Reason, "non-readonly shell command")
	assert.Equal(t, StagePolicy, decision.Stage)
}

func TestEngineCallbackPatchRevalidatesSandboxConstraints(t *testing.T) {
	root := t.TempDir()
	engine := &Engine{
		Mode:   ModeBypassPermissions,
		Policy: NewToolExecutionPolicy(nil, false),
		Callback: func(_ context.Context, _ EvalRequest) (Decision, string, error) {
			return Decision{PatchedArgs: json.RawMessage(`{"path":"outside.txt"}`)}, "callback patch", nil
		},
	}
	engine.Policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: []string{filepath.Join(root, "inside")},
	})
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "read_file",
		ToolInfo: &skill.ToolInfo{Name: "read_file", MCPTrustLevel: "local", ExecutionMode: "local_mcp"},
		Args:     map[string]interface{}{"path": filepath.Join(root, "inside", "ok.txt")},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Contains(t, decision.Reason, "outside sandbox")
}

func TestEngineResolveAskAssignsApprovalExpiry(t *testing.T) {
	handler := &captureApprovalHandler{}
	engine := &Engine{
		AskHandler:      handler,
		ApprovalTimeout: 2 * time.Minute,
	}
	startedAt := time.Now().UTC()

	decision, err := engine.resolveAsk(context.Background(), Decision{Type: DecisionAsk}, EvalRequest{
		SessionID:  "session-expiry",
		ToolCallID: "tool-expiry",
		ToolName:   "write_file",
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.WithinDuration(t, startedAt.Add(2*time.Minute), handler.request.ExpiresAt, time.Second)
}

func TestEngineUsesCapabilityScopeBeforeApprovalDecision(t *testing.T) {
	engine := &Engine{
		Mode:   ModeBypassPermissions,
		Policy: NewCapabilityScopedToolExecutionPolicy(nil, []Capability{CapReadOnly}),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: "spawn_agent"})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Contains(t, decision.Reason, "agent_management")

	decision, err = engine.Evaluate(context.Background(), EvalRequest{ToolName: "list_agents"})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
}

func TestEnginePlanModeWriteAllowPaths(t *testing.T) {
	t.Parallel()

	t.Run("empty paths keep legacy plan deny for CapWriteFS", func(t *testing.T) {
		engine := &Engine{Mode: ModePlan}
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "write",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"file_path": "plan.md", "content": "x"},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionDeny, decision.Type)
		assert.Equal(t, "mode:plan_denies_non_readonly", decision.Reason)
	})

	t.Run("allow matching plan.md path", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "write",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"file_path": "plan.md", "content": "# plan"},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionAllow, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_allowed", decision.Reason)
		assert.Equal(t, StageMode, decision.Stage)
	})

	t.Run("allow basename match under nested path", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: []string{"plan.md"},
		}
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "edit",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"file_path": "docs/plan.md", "old_string": "a", "new_string": "b"},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionAllow, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_allowed", decision.Reason)
	})

	t.Run("deny non-matching write path", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "write",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"file_path": "main.go", "content": "package main"},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionDeny, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_not_allowed", decision.Reason)
	})

	t.Run("allow apply_patch mentioning plan path", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		patchText := "Update File: plan.md\n@@\n+# step\n"
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "apply_patch",
			Mode:     ModePlan,
			Args: map[string]interface{}{
				"patch": patchText,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionAllow, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_allowed", decision.Reason)
	})

	t.Run("read tools still allow under plan", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "view",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"file_path": "main.go"},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionAllow, decision.Type)
	})
}

func TestEnsureAndSetPlanWriteAllowPaths(t *testing.T) {
	t.Parallel()

	engine := &Engine{}
	EnsurePlanWriteAllowPaths(engine)
	assert.Equal(t, []string{DefaultPlanFileName}, engine.PlanWriteAllowPaths)

	// second ensure is a no-op when already set
	engine.PlanWriteAllowPaths = []string{"custom-plan.md"}
	EnsurePlanWriteAllowPaths(engine)
	assert.Equal(t, []string{"custom-plan.md"}, engine.PlanWriteAllowPaths)

	SetPlanWriteAllowPaths(engine)
	assert.Equal(t, []string{DefaultPlanFileName}, engine.PlanWriteAllowPaths)

	SetPlanWriteAllowPaths(engine, " docs/plan.md ", "docs/plan.md", "")
	assert.Equal(t, []string{"docs/plan.md"}, engine.PlanWriteAllowPaths)
}

func TestEnginePlanModeAllowsEnterExitPlanModeTools(t *testing.T) {
	t.Parallel()

	engine := &Engine{
		Mode:                ModePlan,
		PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
	}
	for _, toolName := range []string{"enter_plan_mode", "exit_plan_mode"} {
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: toolName,
			Mode:     ModePlan,
		})
		require.NoError(t, err, toolName)
		assert.Equal(t, DecisionAllow, decision.Type, toolName)
	}
}
