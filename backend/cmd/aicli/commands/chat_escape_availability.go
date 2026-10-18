package commands

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"

// chatEscapeInterruptAvailable reports whether the current session really has a
// live ESC consumer, so the status line only advertises "esc to interrupt" when
// the keypress can be consumed.
//
// 阶段 F P1（§12.3）：优先读统一仲裁器快照；当会话尚未登记任何消费者时回退到
// KeyHandler/capture 位。两条来源做 OR，保证只读替换不会窄化承诺——仲裁器与
// 位路径在 modal（priority prompt）期会分叉（仲裁器按目标语义为 false，位路径
// 为 true），DebugMode 的 `[input-arbitration]` 日志记录该分叉，作为 P2 单一
// 写者灰度的依据（§12.5）。
//
// Non-interactive/PTY hosts fail both checks, which is exactly the case where
// the old kind-driven "esc to interrupt" promise was misleading (plan doc
// docs/plan/esc-interrupt-priority-and-loop-robustness-plan-20260918.md P1-2).
func chatEscapeInterruptAvailable(session *ChatSession) bool {
	if session == nil {
		return false
	}
	if snapshot, ok := chatInputArbitrationSnapshotOf(session); ok && snapshot.EscAvailable {
		return true
	}
	return chatInputEscBitsAvailable(session)
}

// chatInputEscBitsAvailable is the pre-P1 answer derived from the KeyHandler
// bits and the busy-capture flag.
//
// Consumers (see startChatEscapeInterruptWatcher / startBusyQueuedInputCapture):
//   - the turn-scoped KeyHandler consumer: armed && !suspended;
//   - the busy queued-input capture: it owns console stdin while a turn runs.
//
// 只认控制台 capture（consoleInputCaptureActive）：Web/远程接管的粘滞标记
// （webCaptureActive）既不消费 Esc，也不代表有在途回合，不能作为承诺依据
// （2026-10-18 真机：Web 注入过一行后，空闲状态也会被误判为可中断）。
func chatInputEscBitsAvailable(session *ChatSession) bool {
	if session == nil {
		return false
	}
	if chatKeyHandlerConsumesEscape(session.KeyHandler) {
		return true
	}
	return session.InputQueue != nil && session.InputQueue.consoleInputCaptureActive()
}

func chatKeyHandlerConsumesEscape(kh *ui.KeyHandler) bool {
	return kh != nil && kh.IsEnabled() && kh.Armed() && !kh.Suspended()
}
