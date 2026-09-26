package commands

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

const chatInterruptExitWindow = 2 * time.Second

type chatInterruptExitState struct {
	mu           sync.Mutex
	lastSignalAt time.Time
}

func (s *chatInterruptExitState) handleInterruptSignal(session *ChatSession, shouldExit *atomic.Bool, now time.Time) bool {
	if shouldExit != nil && shouldExit.Load() {
		if session != nil {
			session.InterruptPreservePendingInput()
		}
		return true
	}

	s.mu.Lock()
	recent := !s.lastSignalAt.IsZero() && now.Sub(s.lastSignalAt) <= chatInterruptExitWindow
	s.lastSignalAt = now
	s.mu.Unlock()

	if !chatInterruptSignalBusy(session) {
		if shouldExit != nil {
			shouldExit.Store(true)
		}
		renderChatInterruptExitNotice(session)
		return true
	}

	if recent {
		if shouldExit != nil {
			shouldExit.Store(true)
		}
		if session != nil {
			session.InterruptPreservePendingInput()
		}
		renderChatInterruptExitNotice(session)
		return true
	}

	if session != nil {
		session.InterruptPreservePendingInput()
	}
	renderChatInterruptExitHint(session)
	return false
}

func chatInterruptSignalBusy(session *ChatSession) bool {
	if session == nil || session.Interaction == nil {
		return true
	}
	// While Stopping there is no cancellable work left; treat Ctrl+C as an
	// exit request instead of arming another two-press interrupt window.
	if session.Interaction.AgentStage() == chatAgentStageStopping {
		return false
	}
	return !session.Interaction.IsReady()
}

func renderChatInterruptExitHint(session *ChatSession) {
	renderChatInterruptNotice(session, "已中断 - 再次按 Ctrl+C 可退出")
}

func renderChatInterruptExitNotice(session *ChatSession) {
	renderChatInterruptNotice(session, "正在退出...")
	// 退出前有界推完：瞬时通知不写事件日志，没有后续重放可以补画这一行。
	flushEphemeralDirectInteractiveOutput(session)
}

func renderChatInterruptNotice(session *ChatSession, message string) {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return
	}
	if session.Interaction != nil {
		// 退出/中断提示属于当前这一次运行的画面，不写入会话事件日志：
		// 否则历次运行的"正在退出..."会在下一次 resume 时被重放。
		session.Interaction.RenderEphemeralSupplement(message)
		return
	}
	printDirectInteractiveOutput(session, fmt.Sprintln(ui.NewStatus(ui.StatusInfo, message).Build()))
}
