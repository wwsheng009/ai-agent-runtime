package commands

import (
	"strings"
	"time"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// 本文件是共享核心（internal/gateway/agent.SessionStateStore）在 aicli 侧的
// 单例与别名，不再自建状态表：
//
//   - 状态词表、attention↔state 耦合、turn 生命周期、变更检测全部归核心所有，
//     aicli 与 runtime-server 共用同一套语义（docs/gateway/08-notifications.md §3）；
//   - aicli 只负责把本地事件映射到核心调用（见 chat_gateway_attention.go）。
var gatewayAttentionStore = gatewayagent.NewSessionStateStore()

// Live-output tail throttle (shared cadence + duplicate suppression, see
// internal/gateway/agent.TailThrottle): the console renders at human speed,
// so at most one tail push per second per session.
var gatewayTailThrottle = gatewayagent.NewTailThrottle(time.Second)

// gatewayAttention 取值别名（与 wire.SessionSummary.Attention 对应）。
const (
	gatewayAttentionCompleted = gatewayagent.AttentionCompleted
	gatewayAttentionError     = gatewayagent.AttentionError
	gatewayAttentionApproval  = gatewayagent.AttentionApproval
	gatewayAttentionQuestion  = gatewayagent.AttentionQuestion
	gatewayAttentionWaiting   = gatewayagent.AttentionWaiting
)

// NOTIFY 帧 kind/priority 别名（与 wire 常量同源，避免两处字面量漂移）。
const (
	gatewayNotifyKindError    = wire.NotifyKindError
	gatewayNotifyPriorityHigh = wire.NotifyPriorityHigh
)

// setGatewayAttention records (or clears, when attention == "") the attention
// state for sessionID, reporting whether the value actually changed (callers
// use it to decide whether a SESSION_UPDATE push is needed).
func setGatewayAttention(sessionID, attention string) bool {
	return gatewayAttentionStore.SetAttention(strings.TrimSpace(sessionID), strings.TrimSpace(attention))
}

// markGatewaySubagentFailure projects a failed / timed-out / orphaned child
// batch onto the parent session's attention axis and sends the high-priority
// error notification (docs/gateway/08-notifications.md §3 的失败行)。父会话稍后
// 会被 wake 唤醒处理，但控制台在此之前就应看到 error 徽标；父会话下一轮
// BeginTurn 会清除它。
func markGatewaySubagentFailure(session *ChatSession, sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	setGatewayAttention(sessionID, gatewayAttentionError)
	syncChatGatewaySession(session)
	notifyChatGatewaySession(session, gatewayNotifyKindError, gatewayNotifyPriorityHigh)
}

// gatewaySessionStateFor returns the shared projection for sessionID.
func gatewaySessionStateFor(sessionID string) gatewayagent.SessionState {
	return gatewayAttentionStore.Get(strings.TrimSpace(sessionID))
}
