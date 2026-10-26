package commands

import (
	"strings"
	"time"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// 本文件维护 aicli 上报给网关的会话状态投影（docs/gateway/08-notifications.md §3、
// docs/gateway/02-architecture.md §3.2）。
//
// 语义分工：
//   - 共享核心（gatewayagent.SessionStateStore）拥有 attention/state 词表、
//     turn 生命周期（completed/interrupted/error）与变更检测；
//   - 本文件只做 aicli 本地事件 → 核心调用的映射：把 TUI surface 状态机
//     （chatSurfaceStatus）投影成 canonical state + 单行动态状态文本。
//
// attention 是持久状态（随 SESSION_UPDATE 上报，控制台据此显示徽标），通知是
// 瞬时事件（网关在状态"新出现/变化"时派生 Web Push）——这里只维护状态。
//
// 范围：aicli chat 进程通常只绑定一个会话，但会话可切换，所以按 sessionID 建表
// （表由共享核心持有）。

// gatewaySurfaceStatusFor maps the host's structured activity status to the
// canonical gateway state + one-line status text. An empty state means "no
// activity to report" (idle/notice): callers must leave the lifecycle state
// (completed/interrupted/error/idle) owned by the turn finisher untouched.
func gatewaySurfaceStatusFor(s chatSurfaceStatus, inputMode chatInputMode) (string, string) {
	// §6.12 托管挂起（Parked）不在 isRunning() 词表里（界面上的 "Worked for …"
	// 冻结判据需要它保持"非运行"），但它恰恰是网关必须看到的等待态：
	// docs/gateway/08-notifications.md §3 要求挂起投影为 attention=waiting +
	// 等待子任务文案。因此在这里显式处理，避免挂起被当成"无活动"而丢失投影。
	if s.kind == chatSurfaceStatusParked {
		return wire.SessionStateRunning, gatewayParkedStatusText
	}
	switch normalizeChatInputMode(inputMode) {
	case chatInputModeApproval:
		return wire.SessionStateReview, statusTextWithDetail("Waiting for approval", s.detail)
	case chatInputModeAnswer:
		return wire.SessionStateWaiting, statusTextWithDetail("Waiting for answer", s.detail)
	case chatInputModeSelection:
		return wire.SessionStateWaiting, "Selecting an option"
	case chatInputModeConfirmation:
		return wire.SessionStateWaiting, "Waiting for confirmation"
	case chatInputModeSecret:
		return wire.SessionStateWaiting, "Waiting for credentials"
	case chatInputModePanel:
		return wire.SessionStateWaiting, "Navigating panel"
	}
	if !s.isRunning() {
		return "", ""
	}
	action, _, _ := chatDynamicStatusAction(s, inputMode)
	switch s.kind {
	case chatSurfaceStatusTool, chatSurfaceStatusRetrying, chatSurfaceStatusStopping:
		return wire.SessionStateRunning, action
	case chatSurfaceStatusStreaming:
		return wire.SessionStateAnswering, action
	case chatSurfaceStatusThinking, chatSurfaceStatusPlanning:
		return wire.SessionStateThinking, action
	case chatSurfaceStatusApproval:
		return wire.SessionStateReview, statusTextWithDetail(action, s.detail)
	case chatSurfaceStatusAnswer:
		return wire.SessionStateWaiting, statusTextWithDetail(action, s.detail)
	case chatSurfaceStatusWaiting:
		return wire.SessionStateWaiting, action
	}
	return "", ""
}

// statusTextWithDetail 把待处理条目的一行身份（审批工具名 / 问题正文，调用方
// 已压为单行且有界）附加到粗粒度等待文案后："Waiting for approval: shell_exec"。
// 远程控制台据此在会话卡片上显示待处理的具体内容；空 detail 保持原样
// （非交互路径与旧流程不带明细）。
func statusTextWithDetail(base, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return base
	}
	return base + ": " + detail
}

// projectGatewayPendingDetail 为非交互（Web/远程接管）路径补写网关投影的等待
// 文案：交互路径由 stage 明细自然产出「Waiting for approval: <tool>」，Web 路径
// 没有 stage 投影，这里在同一 store 上直接落同一份文案（调用方随后补推一帧
// SESSION_UPDATE，让 attention 与明细同帧到达）。仅当当前 attention 与期望一致
// 且文案尚未带明细时写入；返回是否实际写入。
func projectGatewayPendingDetail(sessionID, attention, detail string) bool {
	sessionID = strings.TrimSpace(sessionID)
	attention = strings.TrimSpace(attention)
	detail = compactStatusValue(oneLineStatusDetail(detail), chatAgentStageDetailMaxWidth)
	if sessionID == "" || detail == "" {
		return false
	}
	base, state := "", ""
	switch attention {
	case gatewayAttentionApproval:
		base, state = "Waiting for approval", wire.SessionStateReview
	case gatewayAttentionQuestion:
		base, state = "Waiting for answer", wire.SessionStateWaiting
	default:
		return false
	}
	st := gatewaySessionStateFor(sessionID)
	if st.Attention != attention || strings.HasPrefix(strings.TrimSpace(st.StatusText), base+":") {
		return false
	}
	now := time.Now()
	return gatewayAttentionStore.SetSurface(sessionID, state, statusTextWithDetail(base, detail), now, now)
}

// clearGatewayAttentionIfSet 清掉指定会话的挂起 attention（仅当仍等于期望值），
// 并在实际变化时补推一帧 SESSION_UPDATE。用于决议事件（approval_resolved /
// question_answered）：交互路径自己会清（幂等），Web/远程决议路径此前无人清理，
// 徽标会一直挂到回合结束（docs/gateway/08-notifications.md §3 要求决议即清）。
func clearGatewayAttentionIfSet(session *ChatSession, sessionID, attention string) {
	sessionID = strings.TrimSpace(sessionID)
	attention = strings.TrimSpace(attention)
	if sessionID == "" || attention == "" {
		return
	}
	if gatewaySessionStateFor(sessionID).Attention != attention {
		return
	}
	if setGatewayAttention(sessionID, "") {
		syncChatGatewaySession(session)
	}
}

// gatewayParkedStatusText is the one-line activity mirror for a parked managed
// turn; it is the shared constant so aicli and runtime-server cannot drift
// (docs/gateway/08-notifications.md §3), and it matches the TUI dynamic status
// line and the wire comment for SessionStateWaiting.
const gatewayParkedStatusText = gatewayagent.StatusTextWaitingSubagents

// markGatewayParkedTurn projects a parked turn onto the gateway axes per
// docs/gateway/08-notifications.md §3: attention=waiting (persistent badge /
// inbox) with the "Waiting for subagents" activity line. It is the run-end
// finisher's parked branch as well as the live surface projection's edge.
func markGatewayParkedTurn(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	now := time.Now()
	setGatewayAttention(sessionID, gatewayAttentionWaiting)
	gatewayAttentionStore.SetSurface(sessionID, wire.SessionStateRunning, gatewayParkedStatusText, now, now)
}

// clearGatewayWaitingAttention clears the parked attention only when it is the
// one currently set: approval/question badges own the axis and must survive an
// unrelated parked→running transition. It reports whether the value changed.
func clearGatewayWaitingAttention(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	if gatewaySessionStateFor(sessionID).Attention == gatewayAttentionWaiting {
		return setGatewayAttention(sessionID, "")
	}
	return false
}

// beginChatGatewayTurn marks the session busy and clears attention at turn start
// (the session is running again), returning a finisher that records the
// terminal state: completed, interrupted (user cancel), or error. The activity
// phases in between (thinking/answering/running/waiting/review) are projected
// separately from the host's surface status (chat_interaction.go).
func beginChatGatewayTurn(session *ChatSession) func(err error) {
	sessionID := chatSessionID(session)
	if sessionID == "" {
		return func(error) {}
	}
	gatewayAttentionStore.BeginTurn(sessionID, time.Now())
	syncChatGatewaySession(session)
	return func(err error) {
		// §6.12 托管挂起：run 结束 ≠ 托管 turn 结束。仍有 durable 挂起记录时不能
		// 投影成 completed——否则控制台显示"已完成/待处理"并推送"回合完成"，与
		// 仍在等待子代理的事实冲突（docs/gateway/08-notifications.md §3 要求
		// attention=waiting + 等待子任务）。恢复由下一轮 BeginTurn 复位。
		if err == nil && chatSessionHasParkedTurnRecord(session) {
			markGatewayParkedTurn(sessionID)
			syncChatGatewaySession(session)
			notifyChatGatewaySession(session, wire.NotifyKindWaiting, wire.NotifyPriorityNormal)
			return
		}
		outcome := wire.SessionStateCompleted
		kind := wire.NotifyKindTurnCompleted
		priority := wire.NotifyPriorityNormal
		switch {
		case session != nil && session.IsInterrupted():
			// User cancel is not a failure and needs no attention badge or
			// push notification; the state alone tells the story.
			outcome = wire.SessionStateInterrupted
			kind = ""
			priority = ""
		case err != nil:
			outcome = wire.SessionStateError
			kind = wire.NotifyKindError
			priority = wire.NotifyPriorityHigh
		}
		gatewayAttentionStore.EndTurn(sessionID, outcome)
		syncChatGatewaySession(session)
		if kind != "" {
			notifyChatGatewaySession(session, kind, priority)
		}
	}
}
