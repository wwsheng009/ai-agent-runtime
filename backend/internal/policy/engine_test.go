package policy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// codexHeader renders a Codex apply_patch header at runtime. The marker is
// assembled from repeated stars so this source file never carries a literal
// patch header (the apply_patch repair heuristics treat an embedded marker as
// structure, even inside a hunk body).
func codexHeader(kind string) string {
	return strings.Repeat("*", 3) + " " + kind
}

// buildCodexPatch assembles a complete apply_patch body around the given
// operation lines.
func buildCodexPatch(lines ...string) string {
	all := make([]string, 0, len(lines)+2)
	all = append(all, codexHeader("Begin Patch"))
	all = append(all, lines...)
	all = append(all, codexHeader("End Patch"))
	return strings.Join(all, "\n")
}

var (
	codexUpdateFile = codexHeader("Update File: ")
	codexMoveTo     = codexHeader("Move to: ")
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

	t.Run("base name alone does not match under a nested path", func(t *testing.T) {
		// Regression for the old base-name rule: the allowlist entry "plan.md"
		// must not license "docs/plan.md" through filepath.Base equality.
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
		assert.Equal(t, DecisionDeny, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_not_allowed", decision.Reason)
	})

	t.Run("same base name in another directory is not allowed", func(t *testing.T) {
		// other/plan.md shares the base name of the allowlisted docs/plan.md but
		// is a different file, so it stays denied.
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: []string{"docs/plan.md"},
		}
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "write",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"file_path": "other/plan.md", "content": "# trap"},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionDeny, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_not_allowed", decision.Reason)
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

	t.Run("allow apply_patch whose only target is the plan file", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		patchText := buildCodexPatch(
			codexUpdateFile+"plan.md",
			"@@",
			"+# step",
		)
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

	t.Run("every patch header kind is parsed", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		allowedPatches := []string{
			buildCodexPatch(codexHeader("Add File: ")+"plan.md", "+# plan"),
			buildCodexPatch(codexHeader("Delete File: ") + "plan.md"),
			buildCodexPatch(codexUpdateFile+"plan.md", "@@", "-old", "+new"),
		}
		for _, patchText := range allowedPatches {
			decision, err := engine.Evaluate(context.Background(), EvalRequest{
				ToolName: "apply_patch",
				Mode:     ModePlan,
				Args:     map[string]interface{}{"patch": patchText},
			})
			require.NoError(t, err)
			assert.Equal(t, DecisionAllow, decision.Type, patchText)
		}

		added, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "apply_patch",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"patch": buildCodexPatch(codexHeader("Add File: ")+"notes.md", "+# notes")},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionDeny, added.Type)
	})

	t.Run("deny apply_patch that also touches a non-plan file", func(t *testing.T) {
		// A "plan.md" mention must not license the whole patch: every parsed
		// target of the call has to be inside the allowlist.
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		patchText := buildCodexPatch(
			codexUpdateFile+"plan.md",
			"@@",
			"+# step",
			codexUpdateFile+"main.go",
			"@@",
			"+package main",
		)
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "apply_patch",
			Mode:     ModePlan,
			Args: map[string]interface{}{
				"patch": patchText,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionDeny, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_not_allowed", decision.Reason)
	})

	t.Run("deny apply_patch whose body merely mentions the plan path", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		patchText := buildCodexPatch(
			codexUpdateFile+"main.go",
			"@@",
			"// see plan.md for the design",
		)
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "apply_patch",
			Mode:     ModePlan,
			Args: map[string]interface{}{
				"patch": patchText,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionDeny, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_not_allowed", decision.Reason)
	})

	t.Run("deny apply_patch moving the plan file outside the allowlist", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		patchText := buildCodexPatch(
			codexUpdateFile+"plan.md",
			codexMoveTo+"notes/final.md",
			"@@",
			"+# step",
		)
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "apply_patch",
			Mode:     ModePlan,
			Args: map[string]interface{}{
				"patch": patchText,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionDeny, decision.Type)
	})

	t.Run("deny apply_patch without a parseable target", func(t *testing.T) {
		engine := &Engine{
			Mode:                ModePlan,
			PlanWriteAllowPaths: DefaultPlanWriteAllowPaths(),
		}
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "apply_patch",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"patch": "plan.md\nnot a patch header\n"},
		})
		require.NoError(t, err)
		assert.Equal(t, DecisionDeny, decision.Type)
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

// TestEnginePlanModeMultiplePlanWritePaths pins the multi-path allowlist
// contract that enter_plan_mode now feeds (plan_path array / plan_write_paths):
// every listed plan file stays writable, and any other write is still denied.
func TestEnginePlanModeMultiplePlanWritePaths(t *testing.T) {
	t.Parallel()

	engine := &Engine{
		Mode: ModePlan,
		PlanWriteAllowPaths: []string{
			"docs/plan/primary.md",
			"docs/plan/child-a.md",
		},
	}

	for _, allowedPath := range []string{"docs/plan/primary.md", "docs/plan/child-a.md"} {
		decision, err := engine.Evaluate(context.Background(), EvalRequest{
			ToolName: "write",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"file_path": allowedPath, "content": "# plan"},
		})
		require.NoError(t, err, allowedPath)
		assert.Equal(t, DecisionAllow, decision.Type, allowedPath)
		assert.Equal(t, "mode:plan_mode_write_path_allowed", decision.Reason, allowedPath)
	}

	denied, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Mode:     ModePlan,
		Args:     map[string]interface{}{"file_path": "docs/plan/child-z.md", "content": "# stray"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, denied.Type)
	assert.Equal(t, "mode:plan_mode_write_path_not_allowed", denied.Reason)
}

// TestEnginePlanModeResolvedWriteAllowPaths pins the resolved-path semantics:
// a workspace-resolved absolute allowlist is authoritative (cleaned absolute
// equality or a separator-boundary directory prefix), relative targets are
// anchored to the session workspace root the executor uses, base-name equality
// never matches, and an empty resolved list falls back to the raw relative
// allowlist.
func TestEnginePlanModeResolvedWriteAllowPaths(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	resolvedPlan := filepath.Join(workspace, "docs", "plan.md")
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)

	engine := &Engine{
		Mode:                        ModePlan,
		PlanWriteAllowPaths:         []string{"docs/plan.md"},
		PlanWriteAllowPathsResolved: []string{resolvedPlan},
	}

	writeDecision := func(t *testing.T, e *Engine, evalCtx context.Context, target string) Decision {
		t.Helper()
		decision, err := e.Evaluate(evalCtx, EvalRequest{
			ToolName: "write",
			Mode:     ModePlan,
			Args:     map[string]interface{}{"file_path": target, "content": "# plan"},
		})
		require.NoError(t, err)
		return decision
	}

	t.Run("absolute target equal to the resolved entry", func(t *testing.T) {
		decision := writeDecision(t, engine, ctx, resolvedPlan)
		assert.Equal(t, DecisionAllow, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_allowed", decision.Reason)
	})

	t.Run("relative target anchored to the session workspace", func(t *testing.T) {
		decision := writeDecision(t, engine, ctx, "docs/plan.md")
		assert.Equal(t, DecisionAllow, decision.Type)
	})

	t.Run("same base name in another directory stays denied", func(t *testing.T) {
		decision := writeDecision(t, engine, ctx, "other/plan.md")
		assert.Equal(t, DecisionDeny, decision.Type)
		assert.Equal(t, "mode:plan_mode_write_path_not_allowed", decision.Reason)
	})

	t.Run("resolved entries win over a matching raw entry", func(t *testing.T) {
		// The raw entry "plan.md" would match the relative target exactly, but
		// resolved entries are authoritative: the target anchors to
		// <workspace>/plan.md, not to the allowlisted <workspace>/docs/plan.md.
		override := &Engine{
			Mode:                        ModePlan,
			PlanWriteAllowPaths:         []string{"plan.md"},
			PlanWriteAllowPathsResolved: []string{resolvedPlan},
		}
		decision := writeDecision(t, override, ctx, "plan.md")
		assert.Equal(t, DecisionDeny, decision.Type)
	})

	t.Run("relative target without a workspace root is denied", func(t *testing.T) {
		// Fail closed: without the session workspace root the relative target
		// cannot be anchored, so it cannot be proven to be the resolved plan file.
		decision := writeDecision(t, engine, context.Background(), "docs/plan.md")
		assert.Equal(t, DecisionDeny, decision.Type)
	})

	t.Run("directory allowlist entry covers only its subtree", func(t *testing.T) {
		dirEngine := &Engine{
			Mode:                        ModePlan,
			PlanWriteAllowPaths:         []string{"docs/plan"},
			PlanWriteAllowPathsResolved: []string{filepath.Join(workspace, "docs", "plan")},
		}
		allowed := writeDecision(t, dirEngine, ctx, filepath.Join(workspace, "docs", "plan", "child.md"))
		assert.Equal(t, DecisionAllow, allowed.Type)
		assert.Equal(t, DecisionAllow, writeDecision(t, dirEngine, ctx, "docs/plan/child.md").Type)
		assert.Equal(t, DecisionDeny, writeDecision(t, dirEngine, ctx, filepath.Join(workspace, "docs", "plan-notes.md")).Type)
	})

	t.Run("windows paths compare case-insensitively", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("case-insensitive path comparison is Windows-specific")
		}
		decision := writeDecision(t, engine, ctx, strings.ToUpper(resolvedPlan))
		assert.Equal(t, DecisionAllow, decision.Type)
	})

	t.Run("empty resolved list falls back to the raw relative allowlist", func(t *testing.T) {
		fallback := &Engine{Mode: ModePlan, PlanWriteAllowPaths: []string{"docs/plan.md"}}
		assert.Equal(t, DecisionAllow, writeDecision(t, fallback, context.Background(), "docs/plan.md").Type)
		assert.Equal(t, DecisionDeny, writeDecision(t, fallback, context.Background(), "other/plan.md").Type)
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

	SetPlanWriteAllowPathsResolved(engine, " docs/plan.md ", "docs/plan.md", "")
	assert.Equal(t, []string{filepath.Clean("docs/plan.md")}, engine.PlanWriteAllowPathsResolved)
	SetPlanWriteAllowPathsResolved(engine)
	assert.Nil(t, engine.PlanWriteAllowPathsResolved, "empty input clears the resolved list")
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
