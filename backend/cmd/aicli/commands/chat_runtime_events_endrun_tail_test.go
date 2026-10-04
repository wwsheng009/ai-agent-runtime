package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// 2026-10-04 现场（session_20260930210352_V5o7MDYL）：EndRun 在 18:57:03.459
// drain 结算完成（enqueued == processed），运行时的收尾批次（最终
// assistant_message + session_end）在 18:57:05-06 才入桥，全部被
// "event turn does not match active run" 丢弃——模型产出完整回答，页面却看不到
// 终稿。EndRun 关闭 run 之后仍须接受本轮自身的收尾尾巴。

func endRunTailTestBridge(t *testing.T) (*chatRuntimeEventBridge, *[]string) {
	t.Helper()
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session-tail"}}
	bridge := newChatRuntimeEventBridge(session)
	bridge.setPrimarySessionID("session-tail")
	rendered := &[]string{}
	bridge.renderResponse = func(text string) { *rendered = append(*rendered, text) }
	bridge.BeginRun()
	bridge.renderMu.Lock()
	bridge.activeTurnID = "turn-closed"
	bridge.executorTurnID = "turn-closed"
	bridge.renderMu.Unlock()
	bridge.EndRun()
	return bridge, rendered
}

func endRunTailEvent(eventType, turnID string, payload map[string]interface{}) runtimeevents.Event {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	payload["turn_id"] = turnID
	return runtimeevents.Event{Type: eventType, SessionID: "session-tail", Payload: payload}
}

func TestChatRuntimeEventBridge_EndRunClosingTailRendersLateFinal(t *testing.T) {
	bridge, rendered := endRunTailTestBridge(t)

	require.False(t, bridge.runActive, "EndRun 后 run 必须已关闭")
	require.Equal(t, "turn-closed", bridge.lastClosedTurnID,
		"EndRun 必须保留本轮身份作为收尾窗口锚点")
	_, retired := bridge.retiredTurnIDs["turn-closed"]
	require.True(t, retired, "EndRun 必须退役本轮")

	final := endRunTailEvent(runtimechat.EventAssistantMessage, "turn-closed",
		map[string]interface{}{"content": "最终回答正文"})
	end := endRunTailEvent(runtimechat.EventSessionEnd, "turn-closed",
		map[string]interface{}{"success": true})
	foreign := endRunTailEvent(runtimechat.EventAssistantMessage, "turn-other",
		map[string]interface{}{"content": "别的轮"})
	tool := endRunTailEvent(runtimechat.EventToolFinished, "turn-closed", nil)

	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(final),
		"关闭 run 的本轮终稿必须放行")
	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(end),
		"关闭 run 的本轮 session_end 必须放行")
	require.False(t, bridge.shouldSuppressLatePrimaryRunEvent(final),
		"收尾终稿不得被 late 守卫吞掉（现场丢的正是这条）")
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(foreign),
		"其它轮的终稿仍须拦截")
	require.True(t, bridge.shouldSuppressLatePrimaryRunEvent(foreign),
		"其它轮的迟到终稿仍须拦截")
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(tool),
		"关闭 run 的中途工具事件仍须拦截")

	bridge.handleEvent(final)
	require.Equal(t, []string{"最终回答正文"}, *rendered,
		"收尾终稿必须真正进入渲染路径")
	require.True(t, bridge.HasRenderedAssistantFinal(), "终稿必须标记已渲染")
	require.True(t, bridge.HasCommittedExecutorTurnFinal(),
		"终稿必须提交 exactly-once ownership")

	// session_end 被消费后收尾窗口关闭，同轮事件重新回到"丢弃"语义。
	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(end))
	bridge.handleEvent(end)
	require.Empty(t, bridge.lastClosedTurnID, "收尾边消费后窗口必须关闭")
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(end),
		"窗口关闭后重复 session_end 必须被挡")
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(final),
		"窗口关闭后同轮事件重新被挡")
}

func TestChatRuntimeEventBridge_EndRunClosingTailClearedByNextRun(t *testing.T) {
	bridge, _ := endRunTailTestBridge(t)
	final := endRunTailEvent(runtimechat.EventAssistantMessage, "turn-closed",
		map[string]interface{}{"content": "正文"})

	// 下一个 run（新用户轮 / 收养轮）开始时，上一个 run 的收尾窗口必须失效。
	bridge.BeginRun()
	require.Empty(t, bridge.lastClosedTurnID, "新 run 必须清空收尾窗口")
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(final),
		"新 run 活跃后旧轮终稿必须被 ownership 守卫拦截")
	require.False(t, bridge.shouldSuppressLatePrimaryRunEvent(final),
		"新 run 活跃时 late 守卫不参与（拦截由 ownership 守卫完成）")
}

func TestChatRuntimeEventBridge_EndRunClosingTailNeedsTurnIdentity(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session-tail"}}
	bridge := newChatRuntimeEventBridge(session)
	bridge.setPrimarySessionID("session-tail")
	bridge.BeginRun()
	bridge.EndRun()
	require.Empty(t, bridge.lastClosedTurnID,
		"没有识别出轮身份的 run 不建立收尾窗口")
}
