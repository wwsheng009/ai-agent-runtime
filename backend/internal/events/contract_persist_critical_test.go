package events_test

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P1.5/D3：关键持久化类型清单（approval / session 终态 / 工具完成 / checkpoint）。
func TestPersistCriticalRegistry(t *testing.T) {
	critical := []string{
		"approval_requested",
		"approval_resolved",
		"session_start",
		"session_end",
		"session_interrupted",
		"tool.completed",
		"checkpoint_created",
	}
	for _, eventType := range critical {
		if !events.IsPersistCriticalEventType(eventType) {
			t.Fatalf("%q 必须是 persist_critical（D3：关键事件绕过批量等待立即落盘）", eventType)
		}
		if !events.IsPersistedEventType(eventType) {
			t.Fatalf("%q 声明了 persist_critical 但不在 A 通道（session_store）", eventType)
		}
	}

	for _, eventType := range []string{
		"assistant_delta",
		"tool.requested",
		"tool.progress",
		"context.profile.injected",
		"unknown.event.type",
	} {
		if events.IsPersistCriticalEventType(eventType) {
			t.Fatalf("%q 不应是 persist_critical（高频/非终态/未登记）", eventType)
		}
	}
}
