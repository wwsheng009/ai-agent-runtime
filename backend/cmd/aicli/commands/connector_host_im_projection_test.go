package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// TestProjectIMInputToDisplayedSession 钉住 2026-10-15 的 IM 可见性契约
// （用户报告：aicli TUI 中看不到 IM 的输入信息）：
//   - 绑定会话恰为本进程当前展示的 TUI 会话时，IM 文本必须以 KindUser 块进入
//     transcript（与 TUI 直输/Web 注入同款渲染）；
//   - 目标不是展示会话时不得写入（不串台到用户眼前的其他会话）。
//
// 投影是纯显示：回合提交仍由 localConnectorBackend.Handle 唯一负责。
func TestProjectIMInputToDisplayedSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const target = "conn-qq-bot1-user-UNIT"
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: target}}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge

	previous := chatDebugDisplaySessionProvider
	chatDebugDisplaySessionProvider = func() *ChatSession { return session }
	t.Cleanup(func() { chatDebugDisplaySessionProvider = previous })

	rendered := func() string {
		snap := bridge.sceneSnapshot()
		if snap == nil {
			return ""
		}
		return strings.Join(scene.RenderText(snap.Cells, snap.Revision), "\n")
	}

	projectIMInputToDisplayedSession(target, "IM-HELLO-UNIT")
	require.Contains(t, rendered(), "IM-HELLO-UNIT",
		"展示会话的 IM 输入应投影进 transcript（KindUser 块）")

	before := rendered()
	projectIMInputToDisplayedSession("conn-qq-bot1-user-OTHER", "IM-MISROUTED-UNIT")
	require.NotContains(t, rendered(), "IM-MISROUTED-UNIT",
		"非展示会话的 IM 输入不得串台投影")
	require.Equal(t, before, rendered(), "错台调用应完全不改动渲染面")
}
