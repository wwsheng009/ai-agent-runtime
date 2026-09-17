package encoding

import (
	"strings"
	"testing"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestSystemHeadSurfacesCheckpointPersistDiagnostics 锁定修复目标：
// session.checkpoint_persist_error 这类只带结构化载荷的系统事件，
// 在终端与历史文本里必须带上 stage 与错误原因，而不是退化成一行裸事件名。
func TestSystemHeadSurfacesCheckpointPersistDiagnostics(t *testing.T) {
	ev := runtimeevents.Event{
		Type: "session.checkpoint_persist_error",
		Payload: map[string]interface{}{
			"stage": "mid_turn_checkpoint",
			"error": "disk full",
		},
	}
	head := systemHead(ev)
	if head == ev.Type {
		t.Fatalf("systemHead 退化成裸事件名，丢失全部诊断信息: %q", head)
	}
	if !strings.Contains(head, "mid_turn_checkpoint") {
		t.Fatalf("systemHead 丢失 stage 诊断信息: %q", head)
	}
	if !strings.Contains(head, "disk full") {
		t.Fatalf("systemHead 丢失错误原因: %q", head)
	}
}

// TestSystemHeadPrefersMessageThenSummary 保证既有行为不回归：
// message / summary 存在时优先，仅在两者都缺失时才拼装结构化诊断。
func TestSystemHeadPrefersMessageThenSummary(t *testing.T) {
	msg := runtimeevents.Event{Type: "session.note", Payload: map[string]interface{}{"message": "hello"}}
	if got := systemHead(msg); got != "hello" {
		t.Fatalf("message 应优先: got %q", got)
	}
	sum := runtimeevents.Event{Type: "session.note", Payload: map[string]interface{}{"summary": "brief"}}
	if got := systemHead(sum); got != "brief" {
		t.Fatalf("summary 应兜底: got %q", got)
	}
	bare := runtimeevents.Event{Type: "session.note"}
	if got := systemHead(bare); got != "session.note" {
		t.Fatalf("无载荷应退回事件名: got %q", got)
	}
}
