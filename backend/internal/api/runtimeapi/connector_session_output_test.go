package runtimeapi

// S1b（docs/bots/13 §8）：会话终态正文订阅面——两种事件名（含点分隔别名）
// 都转发给回调；非正文事件不转发；退订后停止；无总线/nil 回调安全返回 nil。

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestSubscribeConnectorSessionOutput(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(16)
	handler := &Handler{runtimeEventBus: bus}

	var received []string
	unsubscribe := handler.SubscribeConnectorSessionOutput(func(event runtimeevents.Event) {
		received = append(received, event.Type+":"+event.SessionID)
	})
	if unsubscribe == nil {
		t.Fatal("subscribe must return an unsubscribe function when bus is available")
	}

	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantMessage, SessionID: "sess-1", Payload: map[string]interface{}{"content": "hi"}})
	bus.Publish(runtimeevents.Event{Type: "assistant.message", SessionID: "sess-2", Payload: map[string]interface{}{"content": "ho"}})
	// 非正文事件不转发（订阅面只服务内容镜像）。
	bus.Publish(runtimeevents.Event{Type: "agent.turn.finished", SessionID: "sess-3"})

	if len(received) != 2 || received[0] != "assistant_message:sess-1" || received[1] != "assistant.message:sess-2" {
		t.Fatalf("received = %v", received)
	}

	unsubscribe()
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantMessage, SessionID: "sess-4"})
	if len(received) != 2 {
		t.Fatalf("unsubscribe must stop delivery, got %v", received)
	}

	// nil 接收者 / nil 回调：返回 nil（不订阅；总线为懒建语义，零值 Handler 会自建）。
	var nilHandler *Handler
	if nilHandler.SubscribeConnectorSessionOutput(func(runtimeevents.Event) {}) != nil {
		t.Fatal("nil handler must return nil")
	}
	if handler.SubscribeConnectorSessionOutput(nil) != nil {
		t.Fatal("nil callback must return nil")
	}
}
