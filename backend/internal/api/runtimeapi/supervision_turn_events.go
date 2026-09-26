package runtimeapi

import (
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// publishTurnResumed 把 supervision 的 host-neutral 播报翻成 API 宿主总线上的
// §6.8 turn.resumed 事件（审计缺口 G3）。
//
// 交付契约由 internal/events/contract.go 决定（A+D：落盘 + 回合末尾巴帧），此处
// 只负责发布：落盘由 A 通道桥完成，尾巴帧由宿主既有机制消费。best-effort——总线
// 上的订阅者失败不影响 wake 投递结果（投递语义已由 WakeConsumer 决定），因此这
// 里不返回错误，只做空值保护。
func (h *Handler) publishTurnResumed(announcement supervision.ResumeAnnouncement) {
	if h == nil {
		return
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return
	}
	bus.Publish(runtimeevents.Event{
		Type:      runtimeevents.EventTurnResumed,
		SessionID: strings.TrimSpace(announcement.ParentSessionID),
		Payload:   announcement.EventPayload(),
	})
}
