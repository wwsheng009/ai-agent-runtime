package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// 2026-09-30 现场回归（session_20260929175325_cxHK8mPD）：宿主重启 / auto-continuation
// 之后，陈旧 run 上下文霸占 activeTurnID，新轮的 session_start、assistant_message、
// session_end 全被 ownership 守卫丢弃（debug.log 满屏 render suppressed
// reason="event turn does not match active run"），而新轮的 session_end 同样被丢 →
// 陈旧上下文永不闭合，模型照常产出回答但页面从此全程无反应。
// 修复规则：session_start 是 actor 侧权威的「新一轮开始」信号，所有权必须可移交。

func ownershipTestSessionStart(turnID string) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "session-ownership",
		Payload:   map[string]interface{}{"turn_id": turnID},
	}
}

func ownershipTestPrimaryEvent(eventType, turnID string) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      eventType,
		SessionID: "session-ownership",
		Payload:   map[string]interface{}{"turn_id": turnID},
	}
}

// TestChatRuntimeEventBridge_StaleActiveTurnYieldsToNewSessionStart pins the
// stale-activeTurnID deadlock: the new turn's session_start must take ownership
// away from a run context that never closed, so the rest of that turn renders.
func TestChatRuntimeEventBridge_StaleActiveTurnYieldsToNewSessionStart(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.setPrimarySessionID("session-ownership")
	bridge.renderMu.Lock()
	bridge.runStarted = true
	bridge.runActive = true
	bridge.runEpoch = 3
	bridge.activeTurnID = "turn-stale"
	bridge.renderMu.Unlock()

	// 基线：陈旧上下文下新轮事件被丢弃（这正是现场日志里的每一条）。
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("assistant_message", "turn-new")))

	bridge.observePrimaryRunTurn(ownershipTestSessionStart("turn-new"))

	require.True(t, bridge.isRunActive(), "移交后 run 必须仍然活着")
	bridge.renderMu.Lock()
	require.Equal(t, "turn-new", bridge.activeTurnID)
	_, staleRetired := bridge.retiredTurnIDs["turn-stale"]
	bridge.renderMu.Unlock()
	require.True(t, staleRetired, "被顶替的陈旧轮必须退役")

	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("assistant_message", "turn-new")),
		"新轮的 assistant_message 必须进入渲染管线")
	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("session_end", "turn-new")),
		"新轮的 session_end 必须进入渲染管线（否则陈旧上下文永不闭合）")
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("assistant_message", "turn-stale")),
		"被顶替轮残余事件仍必须被挡下")

	// 幂等：同一轮的 session_start 再次到达不得再退役当前轮。
	bridge.observePrimaryRunTurn(ownershipTestSessionStart("turn-new"))
	bridge.renderMu.Lock()
	require.Equal(t, "turn-new", bridge.activeTurnID)
	bridge.renderMu.Unlock()
	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("assistant_message", "turn-new")))
}

// TestChatRuntimeEventBridge_StaleAdoptedTurnYieldsToNewSessionStart pins the
// second stale shape: an adopted run was closed but its adoptedTurnID stayed
// behind, so maybeAdoptPrimaryRunTurn refused to open a run for any new turn
// (runActive=false + adoptedTurnID!="") and every event was suppressed.
func TestChatRuntimeEventBridge_StaleAdoptedTurnYieldsToNewSessionStart(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.setPrimarySessionID("session-ownership")
	bridge.renderMu.Lock()
	bridge.runStarted = true
	bridge.runActive = false
	bridge.runEpoch = 2
	bridge.adoptedTurnID = "turn-stale"
	bridge.adoptedRunEpoch = 2
	bridge.renderMu.Unlock()

	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("assistant_message", "turn-new")),
		"基线：run 不活跃时主事件必须被挡下")

	bridge.maybeAdoptPrimaryRunTurn(ownershipTestSessionStart("turn-new"))

	require.True(t, bridge.isRunActive(), "新轮的 session_start 必须重新打开 run")
	bridge.renderMu.Lock()
	require.Equal(t, "turn-new", bridge.activeTurnID)
	require.Equal(t, "turn-new", bridge.adoptedTurnID)
	bridge.renderMu.Unlock()
	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("assistant_message", "turn-new")))
}

// TestChatRuntimeEventBridge_ResumedEpisodeRevivesRetiredTurn pins the exact
// 2026-09-30 16:33 live shape: the settled wake resumed the SAME turn id that
// the previous episode had already retired (EndRun retires the turn but leaves
// adoptedTurnID/activeTurnID behind, and the settled turn.resumed edge carries
// no turn_id, so the revival flag never armed). Every event of the resumed
// episode — including its own session_end — was dropped, so the page showed
// nothing while the model produced the final report.
func TestChatRuntimeEventBridge_ResumedEpisodeRevivesRetiredTurn(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.setPrimarySessionID("session-ownership")
	bridge.renderMu.Lock()
	bridge.runStarted = true
	bridge.runActive = false // EndRun 之后
	bridge.runEpoch = 4
	bridge.activeTurnID = "turn-episode" // EndRun 退役后遗留
	bridge.retiredTurnIDs = map[string]struct{}{"turn-episode": {}}
	bridge.retiredTurnOrder = []string{"turn-episode"}
	bridge.adoptedTurnID = "turn-episode" // 上一 episode 的收养记录（EndRun 不清）
	bridge.adoptedRunEpoch = 4
	bridge.renderMu.Unlock()

	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("assistant_message", "turn-episode")),
		"基线：同 id 的恢复 episode 全被丢弃")

	bridge.maybeAdoptPrimaryRunTurn(ownershipTestSessionStart("turn-episode"))

	require.True(t, bridge.isRunActive(), "恢复 episode 必须重开 run")
	bridge.renderMu.Lock()
	require.Equal(t, "turn-episode", bridge.activeTurnID)
	require.Equal(t, "turn-episode", bridge.adoptedTurnID)
	_, retired := bridge.retiredTurnIDs["turn-episode"]
	bridge.renderMu.Unlock()
	require.False(t, retired, "恢复 episode 必须撤销退役标记")
	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("assistant_message", "turn-episode")),
		"恢复 episode 的回答必须进入渲染管线")
	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(ownershipTestPrimaryEvent("session_end", "turn-episode")),
		"恢复 episode 的 session_end 必须进入渲染管线（否则 run 永不闭合）")
}
