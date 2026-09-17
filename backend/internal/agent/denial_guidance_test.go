package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRenderDenialGuidanceReadOnlyTool(t *testing.T) {
	reason := "read-only policy blocks write-like tool: write"
	text := renderDenialGuidance("write", reason)

	require.Contains(t, text, "[TOOL_DENIED:ERR_READONLY_TOOL] write")
	require.Contains(t, text, "boundary: read_only")
	require.Contains(t, text, "rule: "+reason)
	require.Contains(t, text, "fix:")
	// 出路文案与子代理 prompt 横幅、父代理 spawn 报告同源。
	require.Contains(t, text, "read_only=false")
	// 原始 reason 文本保留在模型可见文本中，下游 classifyDeniedPolicy /
	// enrichDeniedToolMetadata 的字符串分类仍然命中。
	require.Contains(t, text, reason)
}

func TestRenderDenialGuidancePassthroughForNonReadOnly(t *testing.T) {
	reason := "permission_engine denied: no override configured"
	require.Equal(t, reason, renderDenialGuidance("write", reason))
}

func TestDenialCodeForReason(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{"write-like tool", "read-only policy blocks write-like tool: write", DenialCodeReadOnlyTool},
		{"background command", "read-only policy blocks background command execution: background_task", DenialCodeReadOnlyTool},
		{"compound shell", "read-only policy blocks compound shell command: git add -A && git commit", DenialCodeReadOnlyShellCompound},
		{"redirection", "read-only policy blocks shell redirection or dynamic command syntax: echo x > f", DenialCodeReadOnlyShellDynamic},
		{"explicit command required", "read-only policy requires an explicit read-only shell command for ls -la /tmp; use shell.commands instead", DenialCodeReadOnlyShell},
		{"non-readonly shell", "read-only policy blocks non-readonly shell command: git add .", DenialCodeReadOnlyShell},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, denialCodeForReason(tc.reason))
		})
	}
}

func TestReadOnlyDenyEscalationAdvisory(t *testing.T) {
	text := readOnlyDenyEscalationAdvisory(3)
	require.Contains(t, text, "3rd consecutive read-only denial")
	require.Contains(t, text, "read_only=false")
}

func TestReadOnlyDenialLimitReachedMessage(t *testing.T) {
	text := readOnlyDenialLimitReachedMessage(5, ReadOnlyDenyHardStopThreshold)
	require.Contains(t, text, "5 consecutive read-only denials")
	require.Contains(t, text, "read_only=false")
}

func TestGoalHasWriteIntent(t *testing.T) {
	cases := []struct {
		goal string
		want bool
	}{
		{"Inspect the workspace and report", false},
		{"review logs and summarize findings", false},
		{"Verify the implementation.", false},
		{"Write a summary of findings", true},
		{"apply the patch from the diff", true},
		{"Modify config.yaml to enable tracing", true},
		{"implement the new endpoint", true},
		{"delete the stale file", true},
		{"修改配置并保存", true},
		{"创建新的 agent 定义文件", true},
		{"分析只读目录并汇报", false},
	}
	for _, tc := range cases {
		t.Run(tc.goal, func(t *testing.T) {
			require.Equal(t, tc.want, goalHasWriteIntent(tc.goal))
		})
	}
}

func TestWriteIntentRouteWarning(t *testing.T) {
	text := writeIntentRouteWarning()
	require.Contains(t, text, "goal_appears_to_require_writes")
	require.Contains(t, text, "read_only=false")
}

func TestEnrichReadOnlyToolDescriptions(t *testing.T) {
	shellDef := types.ToolDefinition{Name: "shell", Description: "Run shell commands"}
	viewDef := types.ToolDefinition{Name: "view", Description: "View files"}

	assertPassthrough := func(t *testing.T, got []types.ToolDefinition) {
		t.Helper()
		require.Len(t, got, 2)
		require.Equal(t, "Run shell commands", got[0].Description)
		require.Equal(t, "View files", got[1].Description)
	}

	t.Run("nil policy passthrough", func(t *testing.T) {
		assertPassthrough(t, enrichReadOnlyToolDescriptions([]types.ToolDefinition{shellDef, viewDef}, nil))
	})

	t.Run("non read-only passthrough", func(t *testing.T) {
		policy := runtimepolicy.NewToolExecutionPolicy(nil, false)
		assertPassthrough(t, enrichReadOnlyToolDescriptions([]types.ToolDefinition{shellDef, viewDef}, policy))
	})

	t.Run("read-only injects READ-ONLY MODE on shell/bash", func(t *testing.T) {
		tools := []types.ToolDefinition{
			{Name: "shell", Description: "Run shell commands"},
			{Name: "bash", Description: "Alias of shell"},
			viewDef,
		}
		policy := runtimepolicy.NewToolExecutionPolicy(nil, true)
		got := enrichReadOnlyToolDescriptions(tools, policy)
		require.Len(t, got, 3)
		require.Contains(t, got[0].Description, "READ-ONLY MODE")
		require.Contains(t, got[1].Description, "READ-ONLY MODE")
		require.Equal(t, viewDef.Description, got[2].Description)
		require.Contains(t, got[0].Description, "return the change to the parent")
		// 幂等：二次注入不叠加。
		again := enrichReadOnlyToolDescriptions(got, policy)
		require.Equal(t, got[0].Description, again[0].Description)
	})
}

func TestRenderSubagentResultsReadOnlySource(t *testing.T) {
	text := renderSubagentResults([]SubagentResult{{
		ID:                    "r1",
		Success:               true,
		Summary:               "done",
		ReadOnly:              true,
		ReadOnlySource:        "parent_tool_execution_policy",
		ReadOnlyFilteredTools: []string{"write", "apply_patch"},
	}})
	// 既有行保持不变（向后兼容），来源行新增（M1）。
	require.Contains(t, text, "read-only: the child was denied these requested write-like tools: write, apply_patch")
	require.Contains(t, text, "read-only boundary source: parent_tool_execution_policy")
	require.Contains(t, text, "read_only=false")
}

// routeWarningsContain 检查 route_warnings 列表里是否存在包含 fragment 的元素
// （testify 的 Contains 对 []string 是精确元素相等，不适合子串断言）。
func routeWarningsContain(warnings []string, fragment string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, fragment) {
			return true
		}
	}
	return false
}

func TestDecodeSubagentTasksWriteIntentWarning(t *testing.T) {
	tasks, err := decodeSubagentTasks(map[string]interface{}{
		"agents": []interface{}{
			map[string]interface{}{
				"id":        "w1",
				"goal":      "Modify the config file",
				"read_only": true,
			},
			map[string]interface{}{
				"id":        "r2",
				"goal":      "Inspect the workspace",
				"read_only": true,
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	require.True(t, routeWarningsContain(tasks[0].RouteWarnings, "goal_appears_to_require_writes"))
	require.Equal(t, "spawn_subagents.read_only", tasks[0].ReadOnlySource)
	require.False(t, routeWarningsContain(tasks[1].RouteWarnings, "goal_appears_to_require_writes"))
}
