package commands

import (
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func startChatEscapeInterruptWatcher(session *ChatSession) func() {
	if session == nil || session.NoInteractive || session.KeyHandler == nil || !session.KeyHandler.IsEnabled() {
		return func() {}
	}

	escCh := session.KeyHandler.GetESCChannel()
	drainChatEscapeEvents(escCh)
	session.KeyHandler.Arm()

	done := make(chan struct{})
	stopped := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(stopped)
		for {
			select {
			case <-escCh:
				// A second Esc while Stopping has no new interrupt target.
				// Ignore it instead of opening backtrack or re-rendering.
				if session.IsInterrupted() {
					continue
				}
				session.InterruptPreservePendingInput()
				renderChatEscapeInterruptNotice(session)
			case <-done:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			session.KeyHandler.Disarm()
			close(done)
			<-stopped
			drainChatEscapeEvents(escCh)
		})
	}
}

func drainChatEscapeEvents(ch <-chan bool) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func renderChatEscapeInterruptNotice(session *ChatSession) {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return
	}
	if session.Interaction != nil {
		session.Interaction.RenderLocalSupplement("已中断 - ESC 取消当前操作")
		return
	}
	printDirectInteractiveOutput(session, ui.NewStatus(ui.StatusInfo, "已中断 - ESC 取消当前操作").Build()+"\n")
}
