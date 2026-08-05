package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

var supportsCancelableInteractiveInputRead = ui.SupportsCancelableInteractiveInputRead

func startBusyQueuedInputCapture(session *ChatSession) func() {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return func() {}
	}
	if session.InputBox == nil || session.Interaction == nil || !shouldUseInteractiveLineEditor(session) {
		return func() {}
	}
	if !supportsCancelableInteractiveInputRead() {
		return func() {}
	}
	queue := ensureChatBufferedInputQueue(session)
	if queue == nil {
		return func() {}
	}
	if session.KeyHandler != nil {
		session.KeyHandler.Suspend()
	}
	queue.setExternalInputCaptureActive(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer func() {
			queue.setExternalInputCaptureActive(false)
			if session.KeyHandler != nil {
				session.KeyHandler.Resume()
			}
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
			capture := newChatBusyComposerCapture(session, prompt, priorityPrompt)
			line, err := capture.ReadLine(readCtx)
			cancelRead()
			select {
			case <-promptChanged:
				// Prompt switches only re-own the prompt rows; the next capture
				// re-seeds the same draft.
				capture.PreserveDraft()
				continue
			default:
			}
			if capture.Cancelled() {
				interruptChatTurnFromBusyInputCancel(session)
				if queue.isPriorityMode() {
					queue.signalReadError(errChatInteractivePromptCancelled)
					return
				}
				capture.ClearPrompt()
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
				capture.ClearPrompt()
				continue
			}
			result := queue.routeInputText(line)
			capture.ClearPrompt()
			if result.rejected() {
				continue
			}
			if result.queued() {
				session.queuedInputEchoed = true
				if session.Interaction != nil {
					session.Interaction.RefreshStatus("")
				}
			}
			if result.queued() && !isSlashCommandInput(line) && session.InputBox != nil {
				session.InputBox.AddToHistory(line)
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

func renderBusyInputRouteFeedback(session *ChatSession, input string, result chatInputRouteResult) {
	if session == nil || session.Interaction == nil || !result.rejected() {
		return
	}
	command := strings.TrimSpace(normalizeQueuedInputLine(input))
	fields := strings.Fields(command)
	if len(fields) == 2 && strings.EqualFold(fields[0], "/queue") && strings.EqualFold(fields[1], "clear") {
		session.Interaction.RenderLocalSupplement("[input] Agent 正在运行，无法执行 /queue clear；现有队列保持不变。请等待状态回到 Ready 后再次执行。")
		return
	}
	session.Interaction.RenderLocalSupplement(fmt.Sprintf(
		"[input] Agent 正在运行，slash 命令 %q 未执行，也未加入消息队列；请等待状态回到 Ready 后重试。",
		command,
	))
}

func interruptChatTurnFromBusyInputCancel(session *ChatSession) {
	if session == nil {
		return
	}
	wasInterrupted := session.IsInterrupted()
	session.InterruptPreservePendingInput()
	if !wasInterrupted {
		renderChatEscapeInterruptNotice(session)
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
	session.InputQueue.setRouteFeedback(func(text string, result chatInputRouteResult) {
		renderBusyInputRouteFeedback(session, text, result)
	})
	return session.InputQueue
}
