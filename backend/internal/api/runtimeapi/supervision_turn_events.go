package runtimeapi

import (
	"context"
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// publishTurnResumed 把 supervision 的 host-neutral 播报翻成 API 宿主总线上的
// §6.8 turn.resumed 事件（审计缺口 G3）。
//
// 交付契约由 internal/events/contract.go 决定（A+D：落盘 + 回合末尾巴帧）。
// 这里显式写入 session event store，避免只依赖 A 通道桥的订阅时序；写库失败时
// 仍发布实时事件。best-effort——总线上的订阅者失败不影响 wake 投递结果。
func (h *Handler) publishTurnResumed(announcement supervision.ResumeAnnouncement) {
	if h == nil {
		return
	}
	event := runtimeevents.Event{
		Type:      runtimeevents.EventTurnResumed,
		SessionID: strings.TrimSpace(announcement.ParentSessionID),
		Payload:   announcement.EventPayload(),
	}
	if store := h.getSessionEventStore(); store != nil {
		if seq, err := store.AppendEvent(context.Background(), event); err == nil {
			event.Payload["seq"] = seq
		}
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return
	}
	bus.Publish(event)
}
