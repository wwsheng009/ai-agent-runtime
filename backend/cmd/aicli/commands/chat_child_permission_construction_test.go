package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

type localChildAdmissionRecorder struct{ calls int }

func (r *localChildAdmissionRecorder) RequestApproval(context.Context, runtimepolicy.ApprovalRequest) (runtimepolicy.ApprovalResponse, error) {
	r.calls++
	return runtimepolicy.ApprovalResponse{Allowed: true}, nil
}

// Cover durable parent resolution -> child metadata -> the real CLI agent
// constructor. A broker-only test misses the later agentdef plan overwrite.
func TestLocalChildConstructionPreservesResolvedPermissionMode(t *testing.T) {
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
			workspace := t.TempDir()
			store := runtimechat.NewInMemoryStorage()
			parent := runtimechat.NewSession("user")
			parent.SetContext(toolbroker.AgentSessionContextPermissionMode, "bypass_permissions")
			parentRoots := []string{t.TempDir()}
			parent.SetContext(runtimechat.AllowedRootsContextKey, parentRoots)
			require.NoError(t, store.Save(ctx, parent))
			child := runtimechat.NewSession("user")
			args, err := (&localActorRegistry{}).resolveSpawnAgentRoute(parent, child.ID, tc.args)
			require.NoError(t, err)
			require.True(t, args.RequestedRouteCaptured)
			wantRequested := tc.args.RequestedPermissionMode
			if !tc.args.RequestedRouteCaptured {
				wantRequested = tc.args.PermissionMode
			}
			require.Equal(t, wantRequested, args.RequestedPermissionMode)
			again, err := (&localActorRegistry{}).resolveSpawnAgentRoute(parent, child.ID, args)
			require.NoError(t, err)
			require.Equal(t, args.PermissionMode, again.PermissionMode)
			require.Equal(t, args.RequestedPermissionMode, again.RequestedPermissionMode)
			child.SetContext(toolbroker.AgentSessionContextAgentType, args.AgentType)
			toolbroker.ApplySpawnAgentRouteContext(child, args)
			require.NoError(t, store.Save(ctx, child))
			base := &ChatSession{RuntimeSession: parent}
			bootstrap, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{Config: runtimecfg.DefaultRuntimeConfig()})
			require.NoError(t, err)
			t.Cleanup(func() { _ = bootstrap.Stop() })
			host := &localChatRuntimeHost{SessionStore: store, Bootstrap: bootstrap}
			deps := host.aicliRuntimeDeps(base, store, nil, workspace, child.ID)
			a, _, _ := deps.PrepareAgent(ctx, child.ID, deps.SessionContext)
			require.NotNil(t, a)
			for _, key := range []string{"allowed_roots", "additional_directories", "additionalDirectories"} {
				require.NotContains(t, a.GetConfig().Options, key, "child construction must not import the base session's grants")
			}
			require.Equal(t, parentRoots, chatAllowedRoots(base), "child construction must not clear parent grants")
			engine := a.GetPermissionEngine()
			require.NotNil(t, engine)
			require.Equal(t, tc.wantMode, engine.Mode)
			require.Equal(t, string(tc.wantMode), deps.SessionContext.PermissionMode)
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
			}
			// Without RunMeta the restored engine must still make the right
			// external-directory decision (not silently fall back to plan).
			if args.ReadOnly {
				approvals := &localChildAdmissionRecorder{}
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
