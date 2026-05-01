package commands

import (
	"fmt"
	"os"
)

func beginDirectInteractiveOutput(session *ChatSession) {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return
	}
	if session.Surface != nil {
		session.Surface.BeginOutput()
	}
}

func writeChatLogBufferedMarker(session *ChatSession) {
	if shouldRenderInteractiveOutput(session) && session.Surface != nil && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
		return
	}
	fmt.Fprint(os.Stderr, "💾")
}

func writeChatLogSaveError(session *ChatSession, err error) {
	if err == nil {
		return
	}
	message := fmt.Sprintf("[日志保存失败] %v", err)
	if shouldRenderInteractiveOutput(session) && session.Interaction != nil {
		session.Interaction.RenderAsyncLine(message)
		return
	}
	fmt.Fprint(os.Stderr, message)
}
