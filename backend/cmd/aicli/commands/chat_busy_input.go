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
	queue.setExternalInputCaptureActive(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer func() {
			queue.setExternalInputCaptureActive(false)
			close(done)
		}()
		for ctx.Err() == nil {
			prompt, priorityPrompt, revision := queue.capturePrompt(formatSessionUserPrompt(session))
			readCtx, cancelRead := context.WithCancel(ctx)
			promptChanged := make(chan struct{}, 1)
			if changes := queue.priorityCaptureChanges(); changes != nil {
				go func(expected uint64) {
					for {
						select {
						case <-changes:
							if queue.priorityCaptureRevision() != expected {
								select {
								case promptChanged <- struct{}{}:
								default:
								}
								cancelRead()
								return
							}
						case <-readCtx.Done():
							return
						}
					}
				}(revision)
			}
			cancelled := false
			line, err := session.InputBox.ReadTransientPromptWithHooksContext(readCtx, prompt, ui.LineEditorHooks{
				OnChange: func(snapshot ui.LineEditorSnapshot) {
					if !priorityPrompt && session.Interaction != nil {
						session.Interaction.SetPromptInputSnapshot(snapshot)
					}
				},
				OnBeforeTerminalWrite: func(snapshot ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot) string {
					if !priorityPrompt && session.Interaction != nil {
						return session.Interaction.PromptCursorPrefix(render.LastCursorRow, render.LastCursorCol)
					}
					return ""
				},
				OnCancel: func(ui.LineEditorSnapshot) bool {
					cancelled = true
					return true
				},
			})
			cancelRead()
			select {
			case <-promptChanged:
				if session.Interaction != nil && !priorityPrompt {
					session.Interaction.RenderPromptInputSnapshot(ui.LineEditorSnapshot{})
				}
				continue
			default:
			}
			if cancelled {
				if queue.isPriorityMode() {
					queue.signalReadError(errChatInteractivePromptCancelled)
					return
				}
				if session.Interaction != nil {
					session.Interaction.RenderPromptInputSnapshot(ui.LineEditorSnapshot{})
				}
				continue
			}
			if err != nil {
				if errors.Is(err, context.Canceled) && ctx.Err() == nil && queue.priorityCaptureRevision() != revision {
					continue
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				if errors.Is(err, ui.ErrInteractiveInputExitRequested) || errors.Is(err, ui.ErrInteractiveInputInterrupted) || errors.Is(err, io.EOF) {
					if queue.isPriorityMode() {
						queue.signalReadError(err)
					}
					return
				}
				if queue.isPriorityMode() {
					queue.signalReadError(err)
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
