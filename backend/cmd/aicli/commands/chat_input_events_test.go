package commands

import (
	"testing"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestIsChatInputQueueDiagnosticEvent(t *testing.T) {
	for _, typ := range []string{chatEventInputQueueDetected, chatEventInputQueueDiscarded, chatEventInputQueueDrained} {
		if !isChatInputQueueDiagnosticEvent(typ) {
			t.Fatalf("expected %q to be an input queue diagnostic event", typ)
		}
	}
	for _, typ := range []string{"input.queue.unknown", "assistant.message", "tool.requested", "session.start", ""} {
		if isChatInputQueueDiagnosticEvent(typ) {
			t.Fatalf("unexpectedly treated %q as an input queue diagnostic event", typ)
		}
	}
}

func TestIsChatRenderDataPlaneSuppressedEvent(t *testing.T) {
	for _, typ := range []string{chatEventInputQueueDetected, chatEventInputQueueDiscarded, chatEventInputQueueDrained, chatWebDynamicStatusBusEvent} {
		if !isChatRenderDataPlaneSuppressedEvent(typ) {
			t.Fatalf("expected %q to be suppressed from the render data plane", typ)
		}
	}
	for _, typ := range []string{"assistant.message", "session.start", "tool.requested", ""} {
		if isChatRenderDataPlaneSuppressedEvent(typ) {
			t.Fatalf("unexpectedly suppressed %q from the render data plane", typ)
		}
	}
}

// TestEncodeRenderModelEventSkipsInputQueueDiagnosticsFromScene 验证
// 本地诊断/镜像事件（input.queue.*、aicli.chat.dynamic_status）只进事件
// 日志、不进入 Scene（渲染数据面），因此不会出现在 web 消息信息流；
// 而其他未映射事件仍按 KindSystem 兜底进入（不丢信息的既有行为不受影响）。
func TestEncodeRenderModelEventSkipsDiagnosticsFromScene(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})

	countCells := func() int {
		snap := bridge.sceneSnapshot()
		if snap == nil {
			return 0
		}
		return len(snap.Cells)
	}

	before := countCells()

	// 1) input.queue.drained：不得进入渲染数据面（Scene 不新增 cell）。
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:      chatEventInputQueueDrained,
		SessionID: "session-1",
		Payload:   map[string]interface{}{"queued_input_count": 1},
	})
	if got := countCells(); got != before {
		t.Fatalf("input.queue.drained leaked into Scene: cells before=%d after=%d", before, got)
	}

	// 2) 其他未映射事件仍按原有 KindSystem 兜底 append（不丢信息行为保持）。
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:      "some.unmapped.diagnostic",
		SessionID: "session-1",
		Payload:   map[string]interface{}{"note": "x"},
	})
	if got := countCells(); got != before+1 {
		t.Fatalf("unmapped non-input-queue event should still be appended as fallback system cell: before=%d got=%d", before, got)
	}

	// 3) input.queue.detected / discarded 同样被挡在渲染数据面之外。
	bridge.encodeRenderModelEvent(runtimeevents.Event{Type: chatEventInputQueueDetected, SessionID: "session-1", Payload: map[string]interface{}{"queued_input_count": 2}})
	bridge.encodeRenderModelEvent(runtimeevents.Event{Type: chatEventInputQueueDiscarded, SessionID: "session-1", Payload: map[string]interface{}{"discarded_count": 1}})
	if got := countCells(); got != before+1 {
		t.Fatalf("input.queue.detected/discarded leaked into Scene: want %d got %d", before+1, got)
	}

	// 4) aicli.chat.dynamic_status 镜像事件同样不进渲染数据面。
	bridge.encodeRenderModelEvent(runtimeevents.Event{Type: chatWebDynamicStatusBusEvent, SessionID: "session-1", Payload: map[string]interface{}{"text": "◦ Analyzing 2m 20s", "running": true}})
	if got := countCells(); got != before+1 {
		t.Fatalf("aicli.chat.dynamic_status leaked into Scene: want %d got %d", before+1, got)
	}
}
