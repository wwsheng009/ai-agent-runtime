package runtimeapi

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

type apiChildAdmissionRecorder struct{ calls int }

func (r *apiChildAdmissionRecorder) RequestApproval(context.Context, runtimepolicy.ApprovalRequest) (runtimepolicy.ApprovalResponse, error) {
	r.calls++
	return runtimepolicy.ApprovalResponse{Allowed: true}, nil
}

func TestAPIChildConstructionPreservesResolvedPermissionMode(t *testing.T) {
	cases := []struct {
		name     string
		args     toolbroker.SpawnAgentArgs
		wantMode runtimepolicy.Mode
	}{
		{"read_only_without_role", toolbroker.SpawnAgentArgs{ReadOnly: true}, runtimepolicy.ModeBypassPermissions},
		{"explore_broker_without_run_meta", toolbroker.SpawnAgentArgs{AgentType: "explore", ReadOnly: true, PermissionMode: "plan", RequestedRouteCaptured: true}, runtimepolicy.ModeBypassPermissions},
		{"explore_broker_with_run_meta", toolbroker.SpawnAgentArgs{AgentType: "explore", ReadOnly: true, PermissionMode: "bypass_permissions", RequestedRouteCaptured: true}, runtimepolicy.ModeBypassPermissions},
		{"explore_explicit_plan", toolbroker.SpawnAgentArgs{AgentType: "explore", ReadOnly: true, PermissionMode: "plan"}, runtimepolicy.ModePlan},
		{"explore_explicit_dont_ask", toolbroker.SpawnAgentArgs{AgentType: "explore", ReadOnly: true, PermissionMode: "dont_ask"}, runtimepolicy.ModeDontAsk},
		{"plan_role_default", toolbroker.SpawnAgentArgs{AgentType: "plan", PermissionMode: "plan", RequestedRouteCaptured: true}, runtimepolicy.ModePlan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			parent := chat.NewSession("user")
			parent.SetContext(toolbroker.AgentSessionContextPermissionMode, "bypass_permissions")
			child := chat.NewSession("user")
			controller := &sessionAgentController{}
			args, err := controller.resolveSpawnAgentRoute(parent, child.ID, tc.args)
			require.NoError(t, err)
			require.True(t, args.RequestedRouteCaptured)
			wantRequested := tc.args.RequestedPermissionMode
			if !tc.args.RequestedRouteCaptured {
				wantRequested = tc.args.PermissionMode
			}
			require.Equal(t, wantRequested, args.RequestedPermissionMode)
			again, err := controller.resolveSpawnAgentRoute(parent, child.ID, args)
			require.NoError(t, err)
			require.Equal(t, args.PermissionMode, again.PermissionMode)
			require.Equal(t, args.RequestedPermissionMode, again.RequestedPermissionMode)
			child.SetContext(toolbroker.AgentSessionContextAgentType, args.AgentType)
			toolbroker.ApplySpawnAgentRouteContext(child, args)
			store := chat.NewInMemoryStorage()
			require.NoError(t, store.Save(ctx, child))
			restored, err := store.Load(ctx, child.ID)
			require.NoError(t, err)
			sessionCtx := runtimehost.ExtractSessionContext(restored)
			handler := NewHandler(skill.NewRegistry(nil), nil, nil)
			a, _ := handler.buildSessionAgentForContext(child.ID, sessionCtx, nil, nil)
			require.NotNil(t, a)
			engine := a.GetPermissionEngine()
			require.NotNil(t, engine)
			require.Equal(t, tc.wantMode, engine.Mode)
			require.Equal(t, string(tc.wantMode), sessionCtx.PermissionMode)
			if tc.wantMode == runtimepolicy.ModePlan {
				require.NotEmpty(t, engine.PlanWriteAllowPaths)
			}
			policy := a.GetToolExecutionPolicy()
			require.NotNil(t, policy)
			require.Equal(t, args.ReadOnly, policy.ReadOnly)
			if args.ReadOnly {
				for _, name := range []string{"write", "edit", "apply_patch", "download", "job_start"} {
					require.Error(t, policy.AllowTool(name), name)
				}
				shell := skill.ToolInfo{Name: "shell", MCPTrustLevel: "local", ExecutionMode: "local_mcp"}
				require.NoError(t, policy.AllowToolCall(shell, map[string]interface{}{"command": "git status --short"}))
				require.Error(t, policy.AllowToolCall(shell, map[string]interface{}{"command": "echo changed > file.txt"}))
				workspace := t.TempDir()
				approvals := &apiChildAdmissionRecorder{}
				engine.AskHandler = approvals
				engine.ExternalDirTempRoots = []string{filepath.Join(workspace, "not-temp")}
				decision, err := engine.Evaluate(toolctx.WithWorkspaceRoot(ctx, workspace), runtimepolicy.EvalRequest{
					ToolName: "view", Args: map[string]interface{}{"file_path": filepath.Join(t.TempDir(), "A.java")},
				})
				require.NoError(t, err)
				want := runtimepolicy.DecisionAllow
				if tc.wantMode == runtimepolicy.ModeDontAsk {
					want = runtimepolicy.DecisionDeny
				}
				require.Equal(t, want, decision.Type)
				wantAsks := 0
				if tc.wantMode == runtimepolicy.ModePlan {
					wantAsks = 1
				}
				require.Equal(t, wantAsks, approvals.calls)
			}
		})
	}
}
