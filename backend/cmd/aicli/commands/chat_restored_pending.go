package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// 恢复态（上一进程遗留）的 pending 提问/审批在 TUI 的投影与作答路由。
//
// 背景（真实复现，见 plan.md 线 A）：进程在 ask_user_question / 审批等待中被
// 结束，RuntimeState 里的 PendingQuestion / PendingApproval 会跨进程存活
// （internal/chat/actor.go 的 loadState + RecoverStale 对 waiting_input /
// waiting_approval 保留 payload），但提问/审批的 waiter 只存在于内存
// （questionWaiters / approvalWaiters），新进程没有任何东西把它们重新投影到
// 输入面。于是 resume 后界面只有历史、没有卡片；用户随便输入一句会落进
// waitForAICLIActorReady 的 30s 超时（"actor 等待就绪超时"）。
//
// actor 侧跨进程作答的续跑链路已经具备（AnswerQuestion →
// resumePendingToolWithResult → resumePendingBatchAfterCurrentResult；
// ApproveTool → resumeApprovedPendingTool），本文件只负责：
//  1. 启动/切换会话后把 durable state 里的 pending 请求投影成与 live 路径
//     一致的卡片 + 底部 answer prompt（复用 coordinator 的 stage / 输入模式 /
//     priority popup，不新造渲染通道）；
//  2. 在交互主循环里把接下来的输入行路由给 bridge.resolveQuestion /
//     resolveApproval，使被挂起的 turn 能像 live 作答一样复活。
//
// 重放（replaySessionLoadEventLog）必须保持零交互副作用：投影只由本文件的
// 显式入口触发，绝不在事件重放里弹卡片（chat_restored_pending_test.go 锁定）。
//
// 并发约定：本文件的注册状态只在交互主 goroutine（主循环、命令派发、
// composer 回调）读写；helper 里的 cleanup/stage 回调与 live 路径同源。

const (
	// chatRestoredPendingLoadTimeout 是启动期读取 durable 状态的硬上限：
	// 投影是 best-effort，读不到就退化为不投影（绝不把启动卡在会话库上）。
	chatRestoredPendingLoadTimeout = 3 * time.Second
)

type chatRestoredPendingKind string

const (
	chatRestoredPendingQuestion chatRestoredPendingKind = "question"
	chatRestoredPendingApproval chatRestoredPendingKind = "approval"
)

// chatRestoredPendingPrompt 是一次已投影、等待作答的挂起请求。
// 字段含义与 live 路径逐项对应（lines/promptLine 用于作答后的转录回显）。
type chatRestoredPendingPrompt struct {
	kind      chatRestoredPendingKind
	sessionID string

	questionID  string
	requestID   string
	prompt      string
	suggestions []string
	required    bool

	approval   *runtimechat.ApprovalRequest
	reuseScope string

	lines        []string
	promptLine   string
	detailsShown bool

	cleanupBody func()
	restoreFan  func()
	suspension  *chatPendingInputSuspension
}

func currentRestoredPendingPrompt(session *ChatSession) *chatRestoredPendingPrompt {
	if session == nil {
		return nil
	}
	session.restoredPendingMu.Lock()
	defer session.restoredPendingMu.Unlock()
	return session.restoredPending
}

func setRestoredPendingPrompt(session *ChatSession, pending *chatRestoredPendingPrompt) {
	if session == nil {
		return
	}
	session.restoredPendingMu.Lock()
	session.restoredPending = pending
	session.restoredPendingMu.Unlock()
}

// restoredPendingPromptCheckDone 报告本会话 epoch 是否已检查过 durable state。
// 每次会话切换（/new、/resume、/load、启动恢复）都必须重置，否则新会话错过投影。
func restoredPendingPromptCheckDone(session *ChatSession) bool {
	if session == nil {
		return true
	}
	session.restoredPendingMu.Lock()
	defer session.restoredPendingMu.Unlock()
	return session.restoredPendingChecked
}

func markRestoredPendingPromptChecked(session *ChatSession) {
	if session == nil {
		return
	}
	session.restoredPendingMu.Lock()
	session.restoredPendingChecked = true
	session.restoredPendingMu.Unlock()
}

func resetRestoredPendingPromptCheck(session *ChatSession) {
	if session == nil {
		return
	}
	session.restoredPendingMu.Lock()
	session.restoredPendingChecked = false
	session.restoredPendingMu.Unlock()
}

// clearRestoredPendingPrompt 撤掉投影并释放输入面（幂等）。
// 只在状态真正结束（已作答 / 已由其它入口解决 / 切换会话）时调用。
func clearRestoredPendingPrompt(session *ChatSession) {
	if session == nil {
		return
	}
	session.restoredPendingMu.Lock()
	pending := session.restoredPending
	session.restoredPending = nil
	session.restoredPendingMu.Unlock()
	if pending == nil {
		return
	}
	releaseRestoredPendingSurface(session, pending)
	if session.Interaction != nil {
		session.Interaction.DiscardPrompt()
		session.Interaction.RefreshStatus("")
	}
}

func releaseRestoredPendingSurface(session *ChatSession, pending *chatRestoredPendingPrompt) {
	if pending == nil {
		return
	}
	if pending.cleanupBody != nil {
		pending.cleanupBody()
		pending.cleanupBody = nil
	}
	if pending.restoreFan != nil {
		pending.restoreFan()
		pending.restoreFan = nil
	}
	if pending.suspension != nil {
		pending.suspension.Restore()
		pending.suspension = nil
	}
	if session != nil && session.Interaction != nil {
		session.Interaction.DiscardPrompt()
	}
}

// loadRestoredPendingRuntimeState 读取会话的 durable runtime state。
// 只读、无副作用：不走 LoadRuntimeStateForInspection 的“无租约即收敛”路径，
// 因为挂起态正是我们要保留的语义（收敛会把 waiting_input 误判成陈旧 run）。
func loadRestoredPendingRuntimeState(session *ChatSession) *runtimechat.RuntimeState {
	if session == nil || session.LocalRuntimeHost == nil || session.RuntimeSession == nil {
		return nil
	}
	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		return nil
	}
	// warmup 已经建好 actor 时优先读内存状态（同一权威，且省一次读库）。
	if warmup := currentChatActorWarmup(session, sessionID); warmup != nil {
		select {
		case <-warmup.done:
			if warmup.err == nil && warmup.actor != nil {
				if state := warmup.actor.StateForInspection(); state != nil {
					return state
				}
			}
		default:
		}
	}
	store := session.LocalRuntimeHost.RuntimeStore
	if store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatRestoredPendingLoadTimeout)
	defer cancel()
	state, err := store.LoadState(ctx, sessionID)
	if err != nil {
		return nil
	}
	return state
}

// ensureRestoredPendingInteractivePrompt 是主循环每轮输入前的入口：
// 已登记则重绘卡片（历史重放/切换会话可能清掉过弹层），未登记则尝试投影。
// 返回 true 表示当前会话处于恢复态作答模式。
func ensureRestoredPendingInteractivePrompt(session *ChatSession) bool {
	if pending := currentRestoredPendingPrompt(session); pending != nil {
		repaintRestoredPendingPrompt(session, pending)
		return true
	}
	if restoredPendingPromptCheckDone(session) {
		return false
	}
	return reconcileRestoredInteractivePending(session)
}

// reconcileRestoredInteractivePending 在启动/切换会话后把 durable state 里
// 遗留的 pending 提问/审批投影到输入面。返回是否成功建立起作答态。
func reconcileRestoredInteractivePending(session *ChatSession) bool {
	if session == nil || !shouldRenderInteractiveOutput(session) || session.Interaction == nil {
		return false
	}
	// 已有投影（含用户正在作答）时不得覆盖。
	if currentRestoredPendingPrompt(session) != nil {
		return false
	}
	markRestoredPendingPromptChecked(session)
	state := loadRestoredPendingRuntimeState(session)
	if state == nil {
		return false
	}
	switch {
	case state.PendingQuestion != nil:
		return projectRestoredPendingQuestion(session, state.PendingQuestion)
	case state.PendingApproval != nil:
		return projectRestoredPendingApproval(session, state.PendingApproval)
	default:
		return false
	}
}

func projectRestoredPendingQuestion(session *ChatSession, question *runtimechat.UserQuestionRequest) bool {
	if session == nil || question == nil {
		return false
	}
	snapshot := *question
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(snapshot.SessionID), currentRuntimeSessionID(session))
	if strings.TrimSpace(snapshot.ID) == "" || sessionID == "" {
		return false
	}
	suggestions := normalizedQuestionSuggestions(snapshot.Suggestions)
	promptLine := questionAnswerPrompt(snapshot.Required, len(suggestions) > 0)
	lines, suspension := beginRestoredPendingPromptLines(session, "问题提示",
		questionPriorityPromptLines(snapshot.Prompt, suggestions))

	pending := &chatRestoredPendingPrompt{
		kind:        chatRestoredPendingQuestion,
		sessionID:   sessionID,
		questionID:  strings.TrimSpace(snapshot.ID),
		prompt:      strings.TrimSpace(snapshot.Prompt),
		suggestions: suggestions,
		required:    snapshot.Required,
		lines:       lines,
		promptLine:  promptLine,
		suspension:  suspension,
		restoreFan:  pushChatComposerAgentStage(session, chatAgentStageAwaitingAnswer),
	}
	pending.cleanupBody = showRestoredPendingPromptBody(session, lines, promptLine)
	setRestoredPendingPrompt(session, pending)
	if session.Interaction != nil {
		session.Interaction.ShowAnswerPrompt()
		session.Interaction.RefreshStatus("")
	}
	writeSessionDebugInfo(session,
		"[restored-pending] projected pending question question_id="+pending.questionID+
			" required="+boolText(pending.required), false)
	return true
}

func projectRestoredPendingApproval(session *ChatSession, approval *runtimechat.ApprovalRequest) bool {
	if session == nil || approval == nil {
		return false
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(approval.SessionID), currentRuntimeSessionID(session))
	if strings.TrimSpace(approval.ID) == "" || sessionID == "" {
		return false
	}
	// 与 ACP 恢复审批同口径（agent_stdio_approval_recovery.go）：
	// yolo 下审批本不该出现 → 直接允许；窗口已过 → 直接拒绝；否则重新投影让用户决定。
	switch decideACPRestoredApproval(chatSessionPermissionMode(session), approval, time.Now()) {
	case acpRestoredApprovalAllow:
		resolveRestoredPendingApprovalWithoutPrompt(session, sessionID, approval, true,
			"已按当前权限模式自动允许（无需审批）")
		return false
	case acpRestoredApprovalDeny:
		resolveRestoredPendingApprovalWithoutPrompt(session, sessionID, approval, false,
			"审批窗口已过，已自动拒绝")
		return false
	}

	snapshot := *approval
	snapshot.ArgsJSON = append([]byte(nil), approval.ArgsJSON...)
	reuseScope := approvalReusePromptScope(session, &snapshot)
	approvalLines := approvalPriorityPromptLines(&snapshot, nil)
	if reuseScope != "" && len(approvalLines) > 0 {
		approvalLines[len(approvalLines)-1] = fmt.Sprintf(
			"[审批] 操作：[1] 仅本次允许  [2] 拒绝  [3] 查看完整参数  [4] 允许并在%s复用同类只读审批 10 分钟",
			reuseScope,
		)
	}
	promptLine := approvalDecisionPromptWithReuse(reuseScope)
	lines, suspension := beginRestoredPendingPromptLines(session, "审批提示", approvalLines)

	pending := &chatRestoredPendingPrompt{
		kind:       chatRestoredPendingApproval,
		sessionID:  sessionID,
		requestID:  strings.TrimSpace(snapshot.ID),
		approval:   &snapshot,
		reuseScope: reuseScope,
		lines:      lines,
		promptLine: promptLine,
		suspension: suspension,
		restoreFan: pushChatComposerAgentStage(session, chatAgentStageAwaitingApproval),
	}
	pending.cleanupBody = showRestoredPendingPromptBody(session, lines, promptLine)
	setRestoredPendingPrompt(session, pending)
	if session.Interaction != nil {
		session.Interaction.ShowAnswerPrompt()
		session.Interaction.RefreshStatus("")
	}
	writeSessionDebugInfo(session,
		"[restored-pending] projected pending approval request_id="+pending.requestID+
			" tool="+strings.TrimSpace(snapshot.ToolName), false)
	return true
}

// beginRestoredPendingPromptLines 在挂起常规输入的前提下组装卡片正文
// （含“已挂起 N 条待处理输入”的提示行），与 live 路径同序。
func beginRestoredPendingPromptLines(session *ChatSession, promptKind string, lines []string) ([]string, *chatPendingInputSuspension) {
	suspension, notice := suspendPendingInteractiveInputForPriorityPrompt(session, promptKind)
	if notice == "" {
		return lines, suspension
	}
	merged := make([]string, 0, len(lines)+1)
	merged = append(merged, notice)
	merged = append(merged, lines...)
	return merged, suspension
}

// showRestoredPendingPromptBody 渲染卡片；固定底部 surface 不可用时退化为
// 一行可见提示（绝不静默）。
func showRestoredPendingPromptBody(session *ChatSession, lines []string, promptLine string) func() {
	body := append(append([]string(nil), lines...), answerPromptBodyLines(promptLine)...)
	if cleanup, ok := showChatRuntimePriorityPromptBody(session, body); ok {
		return cleanup
	}
	printDirectInteractiveOutput(session, strings.Join(body, "\n")+"\n")
	return func() {}
}

// repaintRestoredPendingPrompt 重绘已登记的卡片（历史重放会重置底部面板）。
// prompt 行与输入模式保持不变，避免打断用户正在输入的答案。
func repaintRestoredPendingPrompt(session *ChatSession, pending *chatRestoredPendingPrompt) {
	if session == nil || pending == nil || session.Interaction == nil {
		return
	}
	if pending.cleanupBody != nil {
		pending.cleanupBody()
	}
	pending.cleanupBody = showRestoredPendingPromptBody(session, pending.lines, pending.promptLine)
	if session.Interaction.ShowAnswerPrompt() {
		return
	}
	session.Interaction.PrintPrompt()
}

// handleRestoredPendingAnswerLine 把一行输入当作恢复态提问/审批的作答。
// handled=true 表示该行已被消费，主循环必须 continue（不得再作为普通 prompt 提交）。
// handled=false 表示当前没有恢复态作答（或请求已被其它入口解决），调用方按常规输入处理。
func handleRestoredPendingAnswerLine(session *ChatSession, input string) bool {
	pending := currentRestoredPendingPrompt(session)
	if pending == nil || session == nil {
		return false
	}
	bridge := session.RuntimeEventBridge
	if bridge == nil {
		clearRestoredPendingPrompt(session)
		return false
	}
	text := strings.TrimSpace(normalizeQueuedInputLine(input))
	switch pending.kind {
	case chatRestoredPendingQuestion:
		if !bridge.questionStillPending(pending.sessionID, pending.questionID) {
			// 已被其它入口（web client / 其它进程）回答：撤掉投影，这一行回归普通输入。
			clearRestoredPendingPrompt(session)
			return false
		}
		answer := mapQuestionSuggestionAnswer(text, pending.suggestions)
		if pending.required && answer == "" {
			rerenderRestoredPendingPrompt(session, pending,
				"[提问] 此问题为必答项", "[提问] 此问题为必答项，请输入回答。")
			return true
		}
		renderChatRuntimePriorityPromptTranscript(session, pending.lines, pending.promptLine, answer)
		if err := bridge.resolveQuestion(context.Background(), pending.sessionID, pending.questionID, answer); err != nil {
			renderRestoredPendingError(session, err)
		}
		// 与 live 作答同源：回答提交后该 turn 会继续执行，bridge 需要预期它的复活。
		bridge.expectResumedTurnAfterAnswer(pending.questionID)
		clearRestoredPendingPrompt(session)
		return true
	case chatRestoredPendingApproval:
		if !bridge.approvalStillPending(pending.sessionID, pending.requestID) {
			clearRestoredPendingPrompt(session)
			return false
		}
		decision := parseApprovalPromptDecisionWithReuse(text, pending.reuseScope != "")
		switch decision {
		case approvalPromptShowDetails:
			if !pending.detailsShown {
				pending.detailsShown = true
				pending.lines = append(pending.lines, approvalFullParameterLines(pending.approval)...)
			}
			rerenderRestoredPendingPrompt(session, pending, "", "")
			return true
		case approvalPromptInvalid:
			validOptions := "1、2、3，或 y/n"
			if pending.reuseScope != "" {
				validOptions = "1、2、3、4，或 y/n"
			}
			rerenderRestoredPendingPrompt(session, pending,
				"[审批] 无效选项", "[审批] 无效选项，请输入 "+validOptions+"。")
			return true
		}
		allowed := decision == approvalPromptAllowOnce || decision == approvalPromptAllowReuse
		reuse := decision == approvalPromptAllowReuse
		renderChatRuntimePriorityPromptTranscript(session, pending.lines, pending.promptLine, text)
		if err := bridge.resolveApproval(context.Background(), pending.sessionID, pending.requestID, allowed); err != nil {
			renderRestoredPendingError(session, err)
		} else {
			bridge.renderApprovalDecision(pending.approval, allowed)
		}
		if allowed && reuse {
			bridge.rememberApprovalGrant(bridge.autoApprovalGrantKey(pending.sessionID, pending.approval))
		}
		clearRestoredPendingPrompt(session)
		return true
	}
	return false
}

// rerenderRestoredPendingPrompt 在卡片正文上追加一行校验/详情并重绘。
// prefix/line 均为空时表示只重绘（用于“查看完整参数”）。
func rerenderRestoredPendingPrompt(session *ChatSession, pending *chatRestoredPendingPrompt, prefix, line string) {
	if session == nil || pending == nil {
		return
	}
	if strings.TrimSpace(prefix) != "" || strings.TrimSpace(line) != "" {
		pending.lines = upsertPriorityPromptValidationLine(pending.lines, prefix, line)
	}
	repaintRestoredPendingPrompt(session, pending)
}

// resolveRestoredPendingApprovalWithoutPrompt 按策略直接给出审批结论
// （yolo / 过期），并保留一行可见的本地后果说明。
func resolveRestoredPendingApprovalWithoutPrompt(
	session *ChatSession, sessionID string, approval *runtimechat.ApprovalRequest, allow bool, reason string,
) {
	if session == nil || approval == nil {
		return
	}
	bridge := session.RuntimeEventBridge
	if bridge == nil {
		return
	}
	if err := bridge.resolveApproval(context.Background(), sessionID, strings.TrimSpace(approval.ID), allow); err != nil {
		renderRestoredPendingError(session, err)
		return
	}
	bridge.renderApprovalDecision(approval, allow)
	if session.Interaction != nil && strings.TrimSpace(reason) != "" {
		session.Interaction.RenderLocalSupplement("[approval] " + strings.TrimSpace(reason))
	}
	writeSessionDebugInfo(session,
		"[restored-pending] resolved pending approval without prompt request_id="+
			strings.TrimSpace(approval.ID)+" allow="+boolText(allow), false)
}

func renderRestoredPendingError(session *ChatSession, err error) {
	if session == nil || err == nil {
		return
	}
	if session.Interaction != nil {
		session.Interaction.RenderError(err)
		return
	}
	if !unifiedInteractiveOutputMustFailClosed(session) {
		ui.PrintError("恢复会话的提问/审批失败: %v", err)
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
