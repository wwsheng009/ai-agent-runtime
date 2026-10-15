package commands

// 回归 2026-10-15 真机（session_20261015171200_z7m74wJq，qq-bot1 绑定会话）：
// connector/IM 提交的轮走收养路径（sidecar/IM 直接 SubmitPrompt，无 CLI
// BeginRun/CompleteWaiting）。session_end 关闭收养 run 后，UI 的 agent 阶段与
// 运行时钟必须显式复位——否则 Planning 阶段与 dynamicStatusStarted 残留，状态行
// 变成永不推进的"幽灵 Analyzing"（turn 已 agent.turn.finished，仍显示
// "Analyzing (1m 45s • ctrl+c to stop)"）。

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestChatRuntimeEventBridge_EndAdoptedRunResetsGhostAnalyzing(t *testing.T) {
	session := &ChatSession{Stream: true}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	// 模拟 connector 回合进行中：Planning 阶段 + 运行时钟已起（状态行 Analyzing）。
	coord.SetAgentStage(chatAgentStagePlanning)
	coord.mu.Lock()
	require.False(t, coord.dynamicStatusStarted.IsZero(), "planning stage must arm the running clock")
	require.True(t, coord.surfaceStatus.isRunning(), "planning stage must render as a running surface")
	coord.mu.Unlock()

	bridge := newChatRuntimeEventBridge(session)
	bridge.renderMu.Lock()
	bridge.runStarted = true
	bridge.runActive = true
	bridge.runEpoch = 1
	bridge.adoptedTurnID = "turn-adopted"
	bridge.adoptedRunEpoch = 1
	bridge.activeTurnID = "turn-adopted"
	bridge.renderMu.Unlock()

	// session_end 关闭收养 run：桥内 run 关闭 + UI 状态复位（修复点）。
	bridge.endAdoptedRun(runtimeevents.Event{
		Type:    runtimechat.EventSessionEnd,
		Payload: map[string]interface{}{"turn_id": "turn-adopted"},
	})

	bridge.renderMu.Lock()
	require.False(t, bridge.runActive, "adopted run must close on its session_end")
	require.Empty(t, bridge.adoptedTurnID)
	bridge.renderMu.Unlock()

	coord.mu.Lock()
	defer coord.mu.Unlock()
	require.Equal(t, chatAgentStageIdle, normalizeChatAgentStage(coord.agentStage),
		"stage must drop to idle so no ghost Analyzing remains")
	require.False(t, coord.surfaceStatus.isRunning(), "surface must leave the running state")
	require.True(t, coord.dynamicStatusStarted.IsZero(),
		"the run clock must be cleared; otherwise the status line keeps counting forever")
}
