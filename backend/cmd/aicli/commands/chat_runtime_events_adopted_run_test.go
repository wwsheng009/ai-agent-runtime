package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestChatRuntimeEventBridge_AdoptedRunToolLoggingMustNotPanicOrLeakLogMu
// 回归 2026-10-15 真机故障（session_20261015162537_MMZm63Qi，qq-bot1 绑定会话）：
//
// sidecar/IM 提交的轮没有 CLI BeginRun，runActive 由 adoptPrimaryRunTurnLocked
// 打开（chat_runtime_events.go），而 requestLogState / loggedToolCalls /
// loggedToolResults 等日志状态只在 BeginRun 里创建。于是首次 tool.requested /
// tool.completed 事件落到 logToolRequested / logToolCompleted 时对 nil map 写入，
// 触发 panic "assignment to entry in nil map"；panic 被 UIController.apply 的
// recover 吞掉后，原先手写的 b.logMu.Unlock() 被跳过，bridge 日志锁永久泄漏为
// 已锁定 —— 随后 UI actor 在 ensureRequestLogState 的 logMu.Lock 上永久阻塞
// （goroutine 74 现场：actor 卡死 23 分钟、mailbox 积满 256、渲染投影与回合结算
// 全部停摆，用户侧表现为「IM 投递稳定出错、TUI 不显示输入/输出」）。
//
// 本测试钉住两点修复：惰性建 map（与 ensureRequestLogState 同样的守卫）+
// defer 解锁（任何 panic 都不得让 logMu 泄漏）。
func TestChatRuntimeEventBridge_AdoptedRunToolLoggingMustNotPanicOrLeakLogMu(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", false, "")
	require.NoError(t, logger.SetLogDir(t.TempDir()))

	bridge := newChatRuntimeEventBridge(&ChatSession{Stream: true, Logger: logger})
	bridge.setPrimarySessionID("session-adopted")

	// 收养路径的 run 上下文：runActive=true，但从未 BeginRun（日志 map 为 nil）。
	bridge.renderMu.Lock()
	bridge.runStarted = true
	bridge.runActive = true
	bridge.runEpoch = 1
	bridge.activeTurnID = "turn-adopted"
	bridge.renderMu.Unlock()

	// llm.request.started 走 ensureRequestLogState 的受守卫创建路径，先建立
	// 请求日志作用域（否则后续 tool 事件会被 scopeForEvent 提前挡下）。
	started := runtimeevents.Event{
		Type:      "llm.request.started",
		SessionID: "session-adopted",
		TraceID:   "trace-adopted",
		Payload:   map[string]interface{}{"turn_id": "turn-adopted"},
	}
	require.NotPanics(t, func() { bridge.handleStructuredLogEvent(started) })

	requested := runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "session-adopted",
		ToolName:  "shell",
		TraceID:   "trace-adopted",
		Payload: map[string]interface{}{
			"turn_id":      "turn-adopted",
			"tool_call_id": "call-adopted",
		},
	}
	require.NotPanics(t, func() { bridge.handleStructuredLogEvent(requested) },
		"nil loggedToolCalls write must not panic: the recovered panic skips the manual logMu.Unlock and leaks the lock")

	require.True(t, bridge.logMu.TryLock(),
		"logMu must not stay locked after tool.requested (leaked lock wedges the UI actor forever)")
	bridge.logMu.Unlock()

	completed := runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "session-adopted",
		ToolName:  "shell",
		TraceID:   "trace-adopted",
		Payload: map[string]interface{}{
			"turn_id":      "turn-adopted",
			"tool_call_id": "call-adopted",
			"success":      true,
		},
	}
	require.NotPanics(t, func() { bridge.handleStructuredLogEvent(completed) },
		"nil loggedToolResults write must not panic either")

	require.True(t, bridge.logMu.TryLock(),
		"logMu must not stay locked after tool.completed")
	bridge.logMu.Unlock()

	bridge.logMu.Lock()
	_, callLogged := bridge.loggedToolCalls["call-adopted"]
	_, resultLogged := bridge.loggedToolResults["call-adopted"]
	bridge.logMu.Unlock()
	require.True(t, callLogged, "the tool call must be registered for dedupe")
	require.True(t, resultLogged, "the tool result must be registered for dedupe")
}
