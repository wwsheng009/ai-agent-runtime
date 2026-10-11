package commands

import "strings"

// 本文件维护 aicli 上报给网关的"注意力"状态（docs/gateway/08-notifications.md §3）。
//
// 语义：attention 是**持久状态**（随 SESSION_UPDATE 上报，控制台据此显示徽标），
// 而通知是**瞬时事件**（由网关在状态发生"新出现/变化"时派生 Web Push）。因此
// 这里只负责维护状态，不直接发通知——网关侧 pushNotifier 会做去重与投递。
//
// 范围：aicli chat 进程通常只绑定一个会话，但会话可切换，所以按 sessionID 建表。

// gatewayAttention 取值（与 wire.SessionSummary.Attention 对应）。
const (
	gatewayAttentionCompleted = "completed"
	gatewayAttentionError     = "error"
	gatewayAttentionApproval  = "approval"
	gatewayAttentionQuestion  = "question"
	gatewayAttentionWaiting   = "waiting"
)

var gatewayAttentionStore = newGatewayAttentionStore()

// setGatewayAttention records (or clears, when attention == "") the attention
// state for sessionID.
func setGatewayAttention(sessionID, attention string) {
	gatewayAttentionStore.setAttention(strings.TrimSpace(sessionID), strings.TrimSpace(attention))
}

// setGatewayBusy records whether a turn is currently in flight.
func setGatewayBusy(sessionID string, busy bool) {
	gatewayAttentionStore.setBusy(strings.TrimSpace(sessionID), busy)
}

// gatewaySessionStateFor returns the current attention/busy for sessionID.
func gatewaySessionStateFor(sessionID string) gatewaySessionState {
	return gatewayAttentionStore.get(strings.TrimSpace(sessionID))
}

// beginChatGatewayTurn marks the session busy and clears attention at turn start
// (the session is running again), returning a finisher that records the
// terminal attention state (completed, or error when err != nil).
func beginChatGatewayTurn(session *ChatSession) func(err error) {
	sessionID := chatSessionID(session)
	if sessionID == "" {
		return func(error) {}
	}
	setGatewayAttention(sessionID, "")
	setGatewayBusy(sessionID, true)
	syncChatGatewaySession(session)
	return func(err error) {
		attention := gatewayAttentionCompleted
		if err != nil {
			attention = gatewayAttentionError
		}
		setGatewayAttention(sessionID, attention)
		setGatewayBusy(sessionID, false)
		syncChatGatewaySession(session)
	}
}
