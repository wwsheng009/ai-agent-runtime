package commands

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func startBusyQueuedInputCapture(session *ChatSession) func() {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return func() {}
	}
	if session.InputBox == nil || session.Interaction == nil || !shouldUseInteractiveLineEditor(session) {
		return func() {}
	}
	queue := ensureChatBufferedInputQueue(session)
	if queue == nil {
		return func() {}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			prompt := formatSessionUserPrompt(session)
			line, err := session.InputBox.ReadTransientPromptWithHooksContext(ctx, prompt, ui.LineEditorHooks{
				OnChange: func(snapshot ui.LineEditorSnapshot) {
					if session.Interaction != nil {
						session.Interaction.SetPromptInputSnapshot(snapshot)
					}
				},
				OnBeforeTerminalWrite: func(snapshot ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot) string {
					if session.Interaction != nil {
						return session.Interaction.PromptCursorPrefix(render.LastCursorRow, render.LastCursorCol)
					}
					return ""
				},
			})
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				if errors.Is(err, ui.ErrInteractiveInputExitRequested) || errors.Is(err, ui.ErrInteractiveInputInterrupted) || errors.Is(err, io.EOF) {
					return
				}
				return
			}
			line = strings.TrimSpace(normalizeQueuedInputLine(line))
			if line == "" {
				if session.Interaction != nil {
					session.Interaction.RenderPromptInputSnapshot(ui.LineEditorSnapshot{})
				}
				continue
			}
			queue.routeInputText(line)
			session.queuedInputEchoed = true
			if session.Interaction != nil {
				session.Interaction.RefreshStatus("")
			}
			if !isSlashCommandInput(line) && session.InputBox != nil {
				session.InputBox.AddToHistory(line)
			}
			if session.Interaction != nil {
				session.Interaction.RenderPromptInputSnapshot(ui.LineEditorSnapshot{})
			}
		}
	}()

	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func ensureChatBufferedInputQueue(session *ChatSession) *chatInputQueue {
	if session == nil {
		return nil
	}
	if session.InputQueue == nil {
		session.InputQueue = newChatInputQueue(chatSessionInputReader(session))
	}
	session.InputQueue.setDraftNotifier(func(active bool, lines int, text string) {
		notifyChatInputDraftState(session, active, lines, text)
	})
	session.InputQueue.setCommandGate(func(text string) bool {
		return chatInputCommandAllowed(session, text)
	})
	return session.InputQueue
}
