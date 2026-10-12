package commands

import (
	"time"

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
	case chatSurfaceStatusAnswer, chatSurfaceStatusWaiting, chatSurfaceStatusParked:
		return wire.SessionStateWaiting, action
	}
	return "", ""
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
