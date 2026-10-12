package commands

import (
	"strings"
	"time"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
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

// setGatewayAttention records (or clears, when attention == "") the attention
// state for sessionID.
func setGatewayAttention(sessionID, attention string) {
	gatewayAttentionStore.SetAttention(strings.TrimSpace(sessionID), strings.TrimSpace(attention))
}

// gatewaySessionStateFor returns the shared projection for sessionID.
func gatewaySessionStateFor(sessionID string) gatewayagent.SessionState {
	return gatewayAttentionStore.Get(strings.TrimSpace(sessionID))
}
