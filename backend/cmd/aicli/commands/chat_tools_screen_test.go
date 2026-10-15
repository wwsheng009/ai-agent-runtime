package commands

// /tools 屏渲染的单测（§14/§17 收官：TUI 观测面与 web /runtime/tools 同源）。
// 冻结面分支与本地目录分支各一组，渲染断言按账本分组口径。

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRenderToolsScreenBodyGroupsByLedger(t *testing.T) {
	tools := []runtimetypes.ToolDefinition{
		{Name: "shell", Description: "Run a shell command", Metadata: map[string]interface{}{"surface_source": "core"}},
		{Name: "view", Description: "View file content", Metadata: map[string]interface{}{"surface_source": "core"}},
		{Name: "job_start", Description: "Start a background job", Metadata: map[string]interface{}{"surface_source": "orchestration"}},
		{Name: "take_snapshot", Description: "Snapshot page", Metadata: map[string]interface{}{"surface_source": "mcp:chrome-devtools"}},
	}

	body := renderToolsScreenBody(tools)
	require.Contains(t, body, "当前会话工具面：4 个工具")
	require.Contains(t, body, "[core] ×2")
	require.Contains(t, body, "[orchestration] ×1")
	require.Contains(t, body, "[mcp:chrome-devtools] ×1")
	require.Contains(t, body, "• shell — Run a shell command")
	// 组内字母序。
	coreBlock := body[strings.Index(body, "[core]"):strings.Index(body, "[orchestration]")]
	require.Less(t, strings.Index(coreBlock, "shell"), strings.Index(coreBlock, "view"))
}

func TestRenderToolsScreenBodyDefaultsToCoreWithoutLedger(t *testing.T) {
	tools := []runtimetypes.ToolDefinition{{Name: "bare_tool"}}
	body := renderToolsScreenBody(tools)
	require.Contains(t, body, "[core] ×1")
	require.Contains(t, body, "• bare_tool")
}

func TestRenderToolsScreenBodyTruncatesLongDescription(t *testing.T) {
	tools := []runtimetypes.ToolDefinition{
		{Name: "verbose", Description: strings.Repeat("很长的描述", 60)},
	}
	body := renderToolsScreenBody(tools)
	// 96 rune 截断 + … 结尾（180 rune 输入 → 96 rune 输出 + 省略号）。
	require.Contains(t, body, "…")
	require.NotContains(t, body, strings.Repeat("很长的描述", 60))
}

// 冻结面缺失时（刚 /profile use、尚未开始 turn）的兜底口径：基线候选必须按
// session.ToolPolicy 过滤，展示下一 turn 请求面，并列出被策略裁剪的工具名。
func TestBuildToolsScreenBodyFallbackAppliesSessionPolicy(t *testing.T) {
	session := &ChatSession{
		ChatExecutor: newAICLIActorChatExecutor(),
		LocalRuntimeHost: &localChatRuntimeHost{
			ToolSurface: stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
				{Name: "view", Description: "View file content"},
				{Name: "shell", Description: "Run a shell command"},
				{Name: "write", Description: "Write file content"},
			}},
		},
		ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
	}

	body, ok := buildToolsScreenBody(session)
	require.True(t, ok)
	require.Contains(t, body, "当前会话工具面：1 个工具（下一 turn 生效面")
	require.Contains(t, body, "• view — View file content")
	require.NotContains(t, body, "• shell")
	require.NotContains(t, body, "• write")
	require.Contains(t, body, "策略裁剪: 2 个工具不进入下一 turn 请求面: shell, write")
}

// disable_tools 与请求面口径一致：只关执行与 tool_choice，目录保持完整，
// 展示不按策略裁剪（与 agent loop 的 disable_tools 例外同语义）。
func TestBuildToolsScreenBodyFallbackKeepsCatalogWhenDisableTools(t *testing.T) {
	session := &ChatSession{
		ChatExecutor: newAICLIActorChatExecutor(),
		DisableTools: true,
		LocalRuntimeHost: &localChatRuntimeHost{
			ToolSurface: stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
				{Name: "view"},
				{Name: "shell"},
			}},
		},
		ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
	}

	body, ok := buildToolsScreenBody(session)
	require.True(t, ok)
	require.Contains(t, body, "当前会话工具面：2 个工具（与 /api/runtime/tools 同源）")
	require.Contains(t, body, "• shell")
	require.NotContains(t, body, "策略裁剪")
}

// 策略把候选全部收窄掉时也必须可见（0 个工具 + 裁剪明细），不能退回
// "没有可枚举的工具面"。
func TestBuildToolsScreenBodyFallbackReportsEmptyFilteredSurface(t *testing.T) {
	session := &ChatSession{
		ChatExecutor: newAICLIActorChatExecutor(),
		LocalRuntimeHost: &localChatRuntimeHost{
			ToolSurface: stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
				{Name: "shell"},
			}},
		},
		ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
	}

	body, ok := buildToolsScreenBody(session)
	require.True(t, ok)
	require.Contains(t, body, "当前会话工具面：0 个工具")
	require.Contains(t, body, "策略裁剪: 1 个工具不进入下一 turn 请求面: shell")
}

// 无策略时保持原有基线口径（不新增过滤说明），避免对无 profile 会话改变输出。
func TestBuildToolsScreenBodyFallbackWithoutPolicyKeepsBaselineRendering(t *testing.T) {
	session := &ChatSession{
		ChatExecutor: newAICLIActorChatExecutor(),
		LocalRuntimeHost: &localChatRuntimeHost{
			ToolSurface: stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
				{Name: "view"},
				{Name: "shell"},
			}},
		},
	}

	body, ok := buildToolsScreenBody(session)
	require.True(t, ok)
	require.Contains(t, body, "当前会话工具面：2 个工具（与 /api/runtime/tools 同源）")
	require.NotContains(t, body, "策略裁剪")
	require.NotContains(t, body, "下一 turn 生效面")
}

func TestFormatBlockedToolNamesTruncatesOverLimit(t *testing.T) {
	names := make([]string, 0, 18)
	for i := 0; i < 18; i++ {
		names = append(names, fmt.Sprintf("tool_%02d", i))
	}

	text := formatBlockedToolNames(names)
	require.Contains(t, text, "tool_00, tool_01")
	require.Contains(t, text, "…（共 18 个）")
	require.NotContains(t, text, "tool_17")
}
