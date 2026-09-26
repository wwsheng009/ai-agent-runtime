package commands

import (
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// publishLocalTurnResumed 把 supervision 的 host-neutral 播报翻成 CLI 宿主总线上的
// §6.8 turn.resumed 事件（审计缺口 G3）。
//
// 交付契约由 internal/events/contract.go 决定（A+D：落盘 + 回合末尾巴帧）：A 通道
// 由宿主既有的会话事件库桥（chat_actor_host.go 的 EventBus 订阅）完成，尾巴帧由
// 回合末机制消费。best-effort——总线为空或没有订阅者都不影响 wake 投递结果。
func (h *localChatRuntimeHost) publishLocalTurnResumed(announcement supervision.ResumeAnnouncement) {
	if h == nil || h.EventBus == nil {
		return
	}
	h.EventBus.Publish(runtimeevents.Event{
		Type:      runtimeevents.EventTurnResumed,
		SessionID: strings.TrimSpace(announcement.ParentSessionID),
		Payload:   announcement.EventPayload(),
	})
}
