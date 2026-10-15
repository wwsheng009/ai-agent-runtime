package connectors

// 契约（aicli 入站流式增量，2026-10-15）：assistant 增量 → 回调——
// 两种事件名、按会话过滤、载荷候选键、空/非字符串跳过、退订与 nil 防御。

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestSubscribeAssistantDeltasForwarding(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(16)
	var got []string
	unsubscribe := SubscribeAssistantDeltas(bus, "sess-1", func(text string) {
		got = append(got, text)
	})

	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: "sess-1", Payload: map[string]interface{}{"delta": "你"}})
	bus.Publish(runtimeevents.Event{Type: "assistant.delta", SessionID: "sess-1", Payload: map[string]interface{}{"text": "好"}})
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: "sess-1", Payload: map[string]interface{}{"content": "世界"}})
	// 其它会话不串台；空文本/非字符串值跳过。
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: "sess-2", Payload: map[string]interface{}{"delta": "别处"}})
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: "sess-1", Payload: map[string]interface{}{"delta": ""}})
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: "sess-1", Payload: map[string]interface{}{"delta": 42}})

	if len(got) != 3 || got[0] != "你" || got[1] != "好" || got[2] != "世界" {
		t.Fatalf("forwarded deltas = %v", got)
	}

	unsubscribe()
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: "sess-1", Payload: map[string]interface{}{"delta": "停"}})
	if len(got) != 3 {
		t.Fatalf("unsubscribe must stop forwarding, got %v", got)
	}
}

func TestSubscribeAssistantDeltasDefensive(t *testing.T) {
	// nil 总线 / nil 回调 / 空会话：no-op（不 panic、可安全退订、不投递）。
	var nilBus *runtimeevents.Bus
	SubscribeAssistantDeltas(nilBus, "sess-1", func(string) {})()

	bus := runtimeevents.NewBusWithRetention(8)
	SubscribeAssistantDeltas(bus, "sess-1", nil)()

	called := 0
	unsubscribe := SubscribeAssistantDeltas(bus, "  ", func(string) { called++ })
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: "", Payload: map[string]interface{}{"delta": "x"}})
	unsubscribe()
	if called != 0 {
		t.Fatalf("empty session must not subscribe, got %d calls", called)
	}
}
