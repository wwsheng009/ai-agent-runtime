package commands

import (
	"context"
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// publishLocalTurnResumed 把 supervision 的 host-neutral 播报翻成 CLI 宿主总线上的
// §6.8 turn.resumed 事件（审计缺口 G3）。
//
// 交付契约由 internal/events/contract.go 决定（A+D：落盘 + 回合末尾巴帧）：
// 这里显式写入 EventStore 并带上 seq，避免依赖总线桥的订阅时序；尾巴帧由回合末
// 机制消费。best-effort——写库失败仍发布实时事件，不影响 wake 投递结果。
func (h *localChatRuntimeHost) publishLocalTurnResumed(announcement supervision.ResumeAnnouncement) {
	if h == nil {
		return
	}
	event := runtimeevents.Event{
		Type:      runtimeevents.EventTurnResumed,
		SessionID: strings.TrimSpace(announcement.ParentSessionID),
		Payload:   announcement.EventPayload(),
	}
	// The wake-start edge is audit-critical: persist it before publishing so a
	// later replay does not only see the settled closure emitted by the loop.
	if h.EventStore != nil {
		if seq, err := h.EventStore.AppendEvent(context.Background(), event); err == nil {
			event.Payload["seq"] = seq
		}
	}
	if h.EventBus != nil {
		h.EventBus.Publish(event)
	}
}
