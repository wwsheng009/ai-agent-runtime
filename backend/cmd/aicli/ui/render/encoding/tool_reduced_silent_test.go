package encoding_test

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestToolReducedIsSilent 回归：真实 agent 事件序列 tool.requested ->
// tool.completed -> tool.reduced 进入渲染数据面后，tool.reduced 不得产生
// 任何可见 Item（曾因落入 opSystem 而被渲染为字面 "tool.reduced" 的
// system cell）；tool call 与 tool output 仍须正常呈现。
func TestToolReducedIsSilent(t *testing.T) {
	e := encoding.NewEventEncoder()

	events := []runtimeevents.Event{
		{
			Type:      "tool.requested",
			SessionID: "s1",
			ToolName:  "bash",
			Payload: map[string]interface{}{
				"tool_call_id": "call-1",
				"logical_tool": "bash",
				"step":         1,
				"trace_id":     "tr-1",
				"command_text": "echo hello",
			},
		},
		{
			Type:      "tool.completed",
			SessionID: "s1",
			ToolName:  "bash",
			Payload: map[string]interface{}{
				"tool_call_id": "call-1",
				"logical_tool": "bash",
				"step":         1,
				"trace_id":     "tr-1",
				"command_text": "echo hello",
				"summary":      "hello\n(exit 0)",
				"error":        "",
			},
		},
		{
			Type:      "tool.reduced",
			SessionID: "s1",
			ToolName:  "bash",
			Payload: map[string]interface{}{
				"tool_call_id": "call-1",
				"step":         1,
				"trace_id":     "tr-1",
				"error":        "",
				"ok":           true,
				"reducer":      "bash_stdout",
			},
		},
	}

	m, err := e.Replay(events)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	stats := e.Stats()
	if stats.UnknownCount != 0 {
		t.Errorf("UnknownCount = %d, want 0 (tool.reduced 应属已知静默类型)", stats.UnknownCount)
	}
	if stats.EncodeCount != 3 {
		t.Errorf("EncodeCount = %d, want 3", stats.EncodeCount)
	}

	var toolCall, toolOutput int
	for _, it := range m.Items {
		if it.Kind == encoding.KindSystem && strings.Contains(it.Head, "tool.reduced") {
			t.Errorf("BUG: tool.reduced 被渲染为可见 system cell: head=%q", it.Head)
		}
		switch it.Kind {
		case encoding.KindToolCall:
			toolCall++
		case encoding.KindToolOutput:
			toolOutput++
		}
	}
	if toolCall != 1 {
		t.Errorf("KindToolCall items = %d, want 1", toolCall)
	}
	if toolOutput != 1 {
		t.Errorf("KindToolOutput items = %d, want 1", toolOutput)
	}
}
