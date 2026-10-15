// delta_stream.go 是「assistant 打字机增量 → 连接器 ReplySink」的宿主共享实现
// （aicli 入站回合的 P1 遗留，2026-10-15 接入；runtime-server 走 runtimeapi 内联版，
// 口径一致：按会话过滤 + 两种事件名 + 载荷候选键）。终态与占位消息 Final 仍由
// Service 收尾（service.processMessage）。
package connectors

import (
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// deltaStreamEventTypes 覆盖总线上两种等价写法（点分隔别名见
// internal/runtimeobserve/known_types.go）。
var deltaStreamEventTypes = []string{runtimechat.EventAssistantDelta, "assistant.delta"}

// deltaStreamPayloadKeys 是增量文本在事件载荷中的候选键（与 runtimeapi 同口径）。
var deltaStreamPayloadKeys = []string{"delta", "text", "content"}

// SubscribeAssistantDeltas 订阅目标会话的 assistant 增量并把文本回调给调用方
// （宿主转给 connectors.ReplySink.Delta；不支持流式的通道由 sink 自行 no-op）。
// 返回退订函数，调用方在回合结束时退订（defer）。bus/回调为 nil 或会话为空时
// 返回 no-op。事件按 SessionID 过滤：会话内回合由 actor 串行化，订阅窗口
// （提交前订阅 → 回合结束退订）即本回合。
func SubscribeAssistantDeltas(bus *runtimeevents.Bus, sessionID string, onDelta func(text string)) func() {
	if bus == nil || onDelta == nil {
		return func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return func() {}
	}
	handler := func(event runtimeevents.Event) {
		if strings.TrimSpace(event.SessionID) != sessionID {
			return
		}
		if text := deltaStreamPayloadText(event.Payload); text != "" {
			onDelta(text)
		}
	}
	unsubscribes := make([]func(), 0, len(deltaStreamEventTypes))
	for _, eventType := range deltaStreamEventTypes {
		if unsub := bus.SubscribeCancelable(eventType, handler); unsub != nil {
			unsubscribes = append(unsubscribes, unsub)
		}
	}
	if len(unsubscribes) == 0 {
		return func() {}
	}
	return func() {
		for _, unsub := range unsubscribes {
			unsub()
		}
	}
}

// deltaStreamPayloadText 读取载荷中的第一个非空字符串候选键（非字符串忽略）。
func deltaStreamPayloadText(payload map[string]interface{}) string {
	for _, key := range deltaStreamPayloadKeys {
		if text, ok := payload[key].(string); ok && text != "" {
			return text
		}
	}
	return ""
}
