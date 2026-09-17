package commands

import (
	"context"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestLocalHostPersistsToolLifecycleEvents 锁定方案 §0.2 / G2 的回归：
// aicli 本地 host 必须有 A 通道桥，让 agent loop 发布的
// tool.requested / tool.completed 落进 session_events（落盘别名
// tool_started / tool_finished，前端 trajectory 回放依赖该别名）。
func TestLocalHostPersistsToolLifecycleEvents(t *testing.T) {
	store := runtimechat.NewInMemoryRuntimeStore(64)
	bus := runtimeevents.NewBus()
	host := &localChatRuntimeHost{EventBus: bus, EventStore: store}
	host.bindRuntimeEventPersistence()

	// 生产者的真实形态：agent loop 通过 Agent.emitRuntimeEvent 发布，
	// 载荷不含 seq（未落库）。
	bus.Publish(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "session-tools",
		TraceID:   "turn-1",
		ToolName:  "shell",
		Payload:   map[string]interface{}{"tool_call_id": "call-1"},
		Timestamp: time.Now().UTC(),
	})
	// tool.completed 由 loop 的 emitRuntimeEvent 发布并带处置元数据。
	bus.Publish(runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "session-tools",
		TraceID:   "turn-1",
		ToolName:  "shell",
		Payload: map[string]interface{}{
			"tool_call_id": "call-1",
			"ok":           false,
			"error_code":   "exit_code",
		},
		Timestamp: time.Now().UTC(),
	})

	events, err := store.ListEvents(context.Background(), "session-tools", 0, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("期望 2 条落盘事件，实际 %d: %+v", len(events), events)
	}
	if events[0].Type != runtimechat.EventToolStarted {
		t.Fatalf("tool.requested 应落盘为 %s，实际 %s", runtimechat.EventToolStarted, events[0].Type)
	}
	if events[1].Type != runtimechat.EventToolFinished {
		t.Fatalf("tool.completed 应落盘为 %s，实际 %s", runtimechat.EventToolFinished, events[1].Type)
	}
	if got := events[1].Payload["error_code"]; got != "exit_code" {
		t.Fatalf("tool.completed 载荷应完整保留，error_code=%v", got)
	}
}

// TestLocalHostEventPersistenceSkipsAlreadyPersistedAndLiveOnly 锁定桥的两个
// 边界：生产者已直接落库的事件（携带 seq）不得重复 append；live-only 事件
// 不得落盘（注册表语义）。
func TestLocalHostEventPersistenceSkipsAlreadyPersistedAndLiveOnly(t *testing.T) {
	store := runtimechat.NewInMemoryRuntimeStore(64)
	bus := runtimeevents.NewBus()
	host := &localChatRuntimeHost{EventBus: bus, EventStore: store}
	host.bindRuntimeEventPersistence()

	bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "session-skip",
		Payload:   map[string]interface{}{"seq": int64(1), "content": "already persisted"},
	})
	bus.Publish(runtimeevents.Event{
		Type:      "tool.progress",
		SessionID: "session-skip",
		Payload:   map[string]interface{}{"tool_call_id": "call-1"},
	})
	bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "session-skip",
		Payload:   map[string]interface{}{"provider": "openai"},
	})

	events, err := store.ListEvents(context.Background(), "session-skip", 0, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("仅 session_start 应被桥接落盘，实际 %d 条: %+v", len(events), events)
	}
	if events[0].Type != runtimechat.EventSessionStart {
		t.Fatalf("落盘事件类型应为 session_start，实际 %s", events[0].Type)
	}
}
