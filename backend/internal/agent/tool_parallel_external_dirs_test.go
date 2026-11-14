package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestParallelToolBatchPlanRequiresExternalDirectoryAdmission(t *testing.T) {
	for _, mode := range []runtimepolicy.Mode{runtimepolicy.ModePlan, runtimepolicy.ModeDefault, runtimepolicy.ModeBypassPermissions, runtimepolicy.ModeDontAsk} {
		t.Run(string(mode), func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
			apiAgent := NewAgentWithLLM(&Config{Name: "test"}, &parallelSchedulerMCPManager{maxParallelCalls: 2}, nil)
			engine := NewPermissionEngine()
			engine.Mode = mode
			runtimepolicy.EnsurePlanWriteAllowPaths(engine)
			engine.ExternalDirTempRoots = []string{filepath.Join(workspace, "no-temp-exemption")}
			// Planning must never silently admit a directory or invoke a host
			// mutation callback, even in bypass mode.
			engine.ApproveExternalDir = func([]string) { t.Fatal("planning admitted a directory") }
			apiAgent.SetPermissionEngine(engine)
			loop := NewReActLoop(apiAgent, nil, &LoopReActConfig{
				EnableToolCalls: true, EnableParallelTools: true, MaxParallelToolCalls: 2,
			})
			calls := []types.ToolCall{
				{ID: "1", Name: "read_a", Args: map[string]interface{}{"path": filepath.Join(outside, "a.txt")}},
				{ID: "2", Name: "read_b", Args: map[string]interface{}{"path": filepath.Join(outside, "b.txt")}},
			}
			require.Nil(t, loop.buildParallelToolBatchPlan(ctx, calls, nil))
			engine.ExternalAllowedRootsSource = func() []string { return []string{outside} }
			require.NotNil(t, loop.buildParallelToolBatchPlan(ctx, calls, nil), "admitted reads remain parallel")
			staleCtx := toolctx.WithAllowedRoots(ctx, []string{outside})
			engine.ExternalAllowedRoots = []string{outside}
			engine.ExternalAllowedRootsSource = func() []string { return nil }
			require.Nil(t, loop.buildParallelToolBatchPlan(staleCtx, calls, nil), "revoked reads must not bypass admission through old snapshots")
			engine.ExternalAllowedRoots = nil
			engine.ExternalAllowedRootsSource = nil
			calls[0].Args["path"] = filepath.Join(workspace, "a.txt")
			calls[1].Args["path"] = filepath.Join(workspace, "b.txt")
			require.NotNil(t, loop.buildParallelToolBatchPlan(ctx, calls, nil), "workspace reads remain parallel")
		})
	}
}

func TestParallelToolBatchPlanChecksSymlinkRealPath(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(workspace, "java")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(outside, "A.java"), []byte("class A {}"), 0600))
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	apiAgent := NewAgentWithLLM(&Config{Name: "test"}, &parallelSchedulerMCPManager{maxParallelCalls: 2}, nil)
	engine := NewPermissionEngine()
	engine.ExternalDirTempRoots = []string{filepath.Join(workspace, "no-temp-exemption")}
	apiAgent.SetPermissionEngine(engine)
	loop := NewReActLoop(apiAgent, nil, &LoopReActConfig{
		EnableToolCalls: true, EnableParallelTools: true, MaxParallelToolCalls: 2,
	})
	calls := []types.ToolCall{
		{ID: "1", Name: "read_a", Args: map[string]interface{}{"path": filepath.Join(link, "A.java")}},
		{ID: "2", Name: "read_b", Args: map[string]interface{}{"path": filepath.Join(workspace, "README.md")}},
	}
	require.Nil(t, loop.buildParallelToolBatchPlan(ctx, calls, nil))
}
