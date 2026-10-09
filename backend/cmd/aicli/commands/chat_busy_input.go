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

// chatBusyScreenActiveForSession 以 surface 的副屏租约为事实源判断
// 「是否有副屏正持有 stdin」——无需额外会话标志，租约释放即门打开。
func chatBusyScreenActiveForSession(session *ChatSession) bool {
	return chatSurfaceLeased(session)
}

// waitForBusyScreenIdle 是主循环读输入前的跨回合门（P2-4b ④）：
// 副屏租约活跃期间阻塞等待（100ms 轮询），期间给出一次可见提示；
// 会话被中断时立即返回中断错误，让主循环走既有中断分支。
func waitForBusyScreenIdle(session *ChatSession) error {
	if !chatBusyScreenActiveForSession(session) {
		return nil
	}
	notified := false
	for chatBusyScreenActiveForSession(session) {
		if session.IsInterrupted() {
			return errChatInteractivePromptCancelled
		}
		if !notified && session.Interaction != nil {
			session.Interaction.RenderLocalSupplement("[input] 副屏交互进行中，关闭后将恢复输入。")
			notified = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// dispatchBusySlashCommandFromMainLoop 在主循环读到 slash 命令且会话非 Ready
// （典型：托管挂起 "Waiting for subagents"，§6.12）时，把 I/S 档命令接回忙时
// 通道，与 capture 通道（startBusyQueuedInputCapture -> consumeBusyCommand）
// 共用同一策略解析与宿主执行入口（runtimeCommandHost.SubmitBusy）。
//
// 背景（功能隔断点）：忙时 capture 的生命周期绑定 sendMessage 的一次前台 run
// （chat_send.go -> startBusyQueuedInputCapture）；托管挂起期间前台 run 已结束
// （actor 仍 Busy 等待义务），capture 已停止，主循环重新成为读侧。若主循环只按
// 「不是 Ready」整体拒绝，/agents、/todos、/help 等设计上允许忙时打开副屏的
// 命令在挂起期全部不可用——I/S 档路由只存在于 capture 内，跨不过 run 边界。
//
// 返回 true 表示命令已被宿主占有（执行或拒绝并给出提示）；false 表示未占有
// （非 I/S 档、能力/仲裁门降级等），调用方保持原有拒绝提示路径（fail-closed）。
func dispatchBusySlashCommandFromMainLoop(session *ChatSession, text string) bool {
	if session == nil || session.Interaction == nil || !isSlashCommandInput(text) {
		return false
	}
	switch chatInputCommandBusyPolicy(session, text) {
	case chatBusyPolicyImmediate, chatBusyPolicyScreen:
		return runtimeCommandHostFor(session).SubmitBusy(text)
	default:
		return false
	}
}

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
	// 阶段 F P2b：灰度开关打开时由仲裁器独占驱动 Suspend/Resume。
	enforced := chatInputArbitrationEnforced()
	if session.KeyHandler != nil && !enforced {
		session.KeyHandler.Suspend()
	}
	queue.setExternalInputCaptureActive(true)
	// 阶段 F P0：影子仲裁登记（capture 是 busy 期的独占 stdin 持有者）。
	releaseShadowCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer func() {
			queue.setExternalInputCaptureActive(false)
			if session.KeyHandler != nil && !enforced {
				session.KeyHandler.Resume()
			}
			// 单写者模式下这里经 syncChatInputArbitration 完成 Resume。
			releaseShadowCapture()
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
			// A priority prompt (approval/question) whose reader asked for the
			// merged answer row must not use the popup input row: this capture
			// owns stdin, so it paints the answer into the bottom prompt row.
			capture := newChatBusyComposerCapture(session, prompt, priorityPrompt, priorityPrompt && queue.priorityAnswerMergedPrompt())
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
				// Esc cancelled the turn: PreserveDraft first restores the
				// half-typed draft the editor cleared on cancel (D-C / T6a),
				// then releases the painted prompt rows.
				capture.PreserveDraft()
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
			if result.immediate() || result.screen() {
				item := chatQueuedInput{
					Text:       line,
					Source:     chatInputSourceStdin,
					EnqueuedAt: time.Now().UTC(),
				}
				if queue.consumeBusyCommand(item) {
					// 命令已由 busy 通道执行并渲染（P1-5）；不置 queued echo。
					continue
				}
				// 未占有（消费方缺失、执行降级，或 P2-4 副屏未落地）：回退入队，
				// 绝不丢输入（「占有后必达」兜底）。
				queue.requeueFront(item)
				result = chatInputRouteResult{Disposition: chatInputRouteQueued}
			}
			if result.queued() {
				session.setQueuedInputEchoed(true)
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
	session.InputQueue.setCommandPolicyResolver(func(text string) chatBusyCommandPolicy {
		return chatInputCommandBusyPolicy(session, text)
	})
	session.InputQueue.setBusyCommandExecutor(func(item chatQueuedInput) bool {
		return runtimeCommandHostFor(session).SubmitBusy(item.Text)
	})
	session.InputQueue.setRouteFeedback(func(text string, result chatInputRouteResult) {
		renderBusyInputRouteFeedback(session, text, result)
	})
	return session.InputQueue
}
