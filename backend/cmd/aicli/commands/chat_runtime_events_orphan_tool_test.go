package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestChatRuntimeEventBridge_BeginRunClosesOrphanedToolRunningCell：上一轮
// 取消/中断后残留的工具单元格（终态事件丢失或迟到的 tool.started）会以
// "• Running ..." 永久驻留 transcript 并钉住 ActiveBand（线上
// session_20260918172548_iHgz994o 在取消后残留 "• Running grep" 数分钟）。
// 下一次 BeginRun 是"上一轮所有权已结束"的确定性边界，必须在挂载新一轮
// 之前把遗留工具单元格收敛为 canceled。
func TestChatRuntimeEventBridge_BeginRunClosesOrphanedToolRunningCell(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "orphan-tool-run"},
	}
	interaction := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(interaction.Shutdown)
	interaction.SetWriter(&bytes.Buffer{})
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	interaction.SetSurface(surface)
	session.Interaction = interaction

	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "orphan-tool-run",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "orphan-tool-run",
		TraceID:   "trace-orphan",
		ToolName:  "shell",
		Payload: map[string]interface{}{
			"turn_id":      "turn-1",
			"tool_call_id": "call-orphan",
			"command_text": "rg --files",
			"tool_source":  "meta",
		},
	})
	interaction.waitUIActorIdle()
	if band := strings.Join(surface.ActiveBandLines(), "\n"); !strings.Contains(band, "• Running [meta] rg --files") {
		t.Fatalf("precondition: ActiveBand running row missing, got %q", band)
	}

	// 上一轮没有 tool.completed/failed 终态事件；新一轮开始必须收敛遗留项。
	bridge.BeginRun()
	interaction.waitUIActorIdle()

	model := bridge.renderEncoder.Snapshot()
	found := false
	for _, it := range model.Items {
		if it.Kind != encoding.KindToolCall {
			continue
		}
		found = true
		if it.Status != encoding.StatusCanceled {
			t.Fatalf("tool cell status = %s, want canceled (head=%q)", it.Status, it.Head)
		}
		if !strings.Contains(it.Head, "• Canceled [meta] rg --files") {
			t.Fatalf("tool cell head = %q, want canceled head", it.Head)
		}
	}
	if !found {
		t.Fatalf("no tool cell in render model after sweep: %#v", model.Items)
	}
	if band := strings.Join(surface.ActiveBandLines(), "\n"); strings.Contains(band, "• Running ") {
		t.Fatalf("ActiveBand still shows a running row after BeginRun sweep: %q", band)
	}
}
