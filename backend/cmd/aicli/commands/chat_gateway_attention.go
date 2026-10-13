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
		return wire.SessionStateReview, "Waiting for approval"
	case chatInputModeAnswer:
		return wire.SessionStateWaiting, "Waiting for answer"
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
		return wire.SessionStateReview, action
	case chatSurfaceStatusAnswer, chatSurfaceStatusWaiting:
		return wire.SessionStateWaiting, action
	}
	return "", ""
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
