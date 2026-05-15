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

	done := make(chan struct{})
	stopped := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(stopped)
		select {
		case <-escCh:
			session.Interrupt()
			renderChatEscapeInterruptNotice(session)
		case <-done:
		}
	}()

	return func() {
		stopOnce.Do(func() {
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
		session.Interaction.RenderAsyncLine("已中断 - ESC 取消当前操作")
		return
	}
	beginDirectInteractiveOutput(session)
	ui.PrintInfo("已中断 - ESC 取消当前操作")
}
