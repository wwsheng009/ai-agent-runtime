package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// 派发面回归（2026-09-30 真机 12 子会话多数失败的同一类原因）：
// 1) 子任务只给 task_type（schema 已把 role 标为 deprecated routing alias）
//    时，角色家族/能力地板必须按 task_type 归一，而不是落进 unknown-role：
//    unknown-role + nil 白名单曾让子代理继承到 shell 工具面却被能力门禁拒绝
//    （policy:capability not allowed by execution policy: exec_shell）。
// 2) 显式白名单漏掉 shell、但 goal 明确要跑命令时，spawn 结果必须带
//    route_warning，父模型在决策点补 shell 白名单，而不是让子代理整轮猜工具名
//    （tool not found: commands / shell_commands）。

func newShellSurfaceTestParent(scope []runtimepolicy.Capability) *Agent {
	parent := NewAgent(&Config{Name: "parent"}, nil)
	policy := runtimepolicy.NewToolExecutionPolicy(nil, false)
	if scope != nil {
		policy.SetCapabilityScope(scope)
	}
	parent.SetToolExecutionPolicy(policy)
	return parent
}

func TestResolveChildToolSurfaceFallsBackToTaskTypeRoleDefaults(t *testing.T) {
	parent := newShellSurfaceTestParent(nil)

	resolved, child, err := resolveChildToolSurface(parent, SubagentTask{
		ID:       "task-type-only",
		TaskType: "implement",
		Goal:     "implement the fix",
	})
	require.NoError(t, err)
	require.Contains(t, resolved.ToolsWhitelist, "shell",
		"task_type implement must resolve to the write-family defaults")
	require.NoError(t, child.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapExecShell}))
}

func TestResolveChildToolSurfaceInheritedShellSurvivesUnknownRole(t *testing.T) {
	parent := newShellSurfaceTestParent(nil)

	resolved, child, err := resolveChildToolSurface(parent, SubagentTask{
		ID:   "unknown-role",
		Role: "analyst",
		Goal: "run go test ./... and report failures",
	})
	require.NoError(t, err)
	require.NoError(t, child.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapExecShell}),
		"an inherited shell tool must not be denied by a scope the parent never had")
	require.False(t, routeWarningsContain(resolved.RouteWarnings, "goal_appears_to_require_commands"),
		"inherited shell is usable; no command-surface warning")
}

func TestResolveChildToolSurfaceWarnsWhenCommandGoalWhitelistOmitsShell(t *testing.T) {
	parent := newShellSurfaceTestParent(nil)

	resolved, _, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "no-shell-whitelist",
		Role:           "researcher",
		Goal:           "run go test ./... and report failures",
		ToolsWhitelist: []string{"view", "grep"},
	})
	require.NoError(t, err)
	require.True(t, routeWarningsContain(resolved.RouteWarnings, "goal_appears_to_require_commands"),
		"a command goal with a shell-less whitelist must warn at spawn time")
}

func TestResolveChildToolSurfaceWarnsOnReadOnlyMutatingShellGoal(t *testing.T) {
	parent := newShellSurfaceTestParent(nil)

	resolved, _, err := resolveChildToolSurface(parent, SubagentTask{
		ID:       "read-only-tests",
		Role:     "verifier",
		Goal:     "run go test ./... and report failures",
		ReadOnly: true,
	})
	require.NoError(t, err)
	require.True(t, routeWarningsContain(resolved.RouteWarnings, "goal_appears_to_require_mutating_shell"),
		"read_only only allows classified read-only commands; build/test goals must warn")
}

func TestResolveChildToolSurfaceWarnsWhenParentScopeLacksShell(t *testing.T) {
	parent := newShellSurfaceTestParent([]runtimepolicy.Capability{runtimepolicy.CapReadOnly})

	resolved, _, err := resolveChildToolSurface(parent, SubagentTask{
		ID:   "parent-scope-narrow",
		Role: "analyst",
		Goal: "run git status and report the diff",
	})
	require.NoError(t, err)
	require.True(t, routeWarningsContain(resolved.RouteWarnings, "goal_appears_to_require_commands"),
		"a parent policy without exec_shell is a true limitation and must surface")
}

func TestResolveChildToolSurfaceNoCommandWarningForReadOnlyInspection(t *testing.T) {
	parent := newShellSurfaceTestParent(nil)

	resolved, _, err := resolveChildToolSurface(parent, SubagentTask{
		ID:             "inspection",
		Role:           "researcher",
		Goal:           "inspect the config file and summarize findings",
		ToolsWhitelist: []string{"view", "grep"},
	})
	require.NoError(t, err)
	require.False(t, routeWarningsContain(resolved.RouteWarnings, "goal_appears_to_require_commands"))
	require.False(t, routeWarningsContain(resolved.RouteWarnings, "goal_appears_to_require_mutating_shell"))
}

// The scheduler and the child factory both resolve the surface; the warning must
// not accumulate duplicates when the same task is resolved twice.
func TestResolveChildToolSurfaceShellWarningIsNotDuplicated(t *testing.T) {
	parent := newShellSurfaceTestParent(nil)
	task := SubagentTask{
		ID:             "dedupe",
		Role:           "researcher",
		Goal:           "run go test ./... and report failures",
		ToolsWhitelist: []string{"view", "grep"},
	}

	first, _, err := resolveChildToolSurface(parent, task)
	require.NoError(t, err)
	second, _, err := resolveChildToolSurface(parent, first)
	require.NoError(t, err)

	count := 0
	for _, warning := range second.RouteWarnings {
		if strings.Contains(warning, "goal_appears_to_require_commands") {
			count++
		}
	}
	require.Equal(t, 1, count)
}
