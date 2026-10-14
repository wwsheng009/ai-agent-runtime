package commands

// /tools 屏渲染的单测（§14/§17 收官：TUI 观测面与 web /runtime/tools 同源）。
// 冻结面分支与本地目录分支各一组，渲染断言按账本分组口径。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
