package encoding

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestToolReceiptReplayNoDuplicateToolRows 用真实会话的 runtime-events.jsonl
// （环境变量 AICLI_E2E_RUNTIME_EVENTS_JSONL，未设置时跳过）做端到端回放，
// 锁定历史缺陷：回合末批量落账的 tool_receipt_recorded 不得为同一 call 追加
// 第二个工具行。
//
// 不变式：模型里 KindToolCall 的条目数 == 事件流中出现过的不同工具调用 ID 数。
// 缺陷版本下每个回执都会在已终态的调用上再 append 一行裸
// "• Completed <tool>"，该计数会翻倍（示例会话：86 → 172 行）。
func TestToolReceiptReplayNoDuplicateToolRows(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("AICLI_E2E_RUNTIME_EVENTS_JSONL"))
	if path == "" {
		t.Skip("AICLI_E2E_RUNTIME_EVENTS_JSONL 未设置，跳过真实事件日志回放")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	e := NewEventEncoder()
	callIDs := map[string]struct{}{}
	receipts := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev runtimeevents.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		switch ev.Type {
		case runtimechat.EventToolReceiptRecorded:
			receipts++
		case runtimechat.EventToolStarted, "tool.requested",
			runtimechat.EventToolFinished, "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled":
			if id := toolCallID(ev); id != "" {
				callIDs[id] = struct{}{}
			}
		}
		e.Encode(ev)
	}
	m := e.Snapshot()
	toolRows := 0
	for _, it := range m.Items {
		if it.Kind == KindToolCall {
			toolRows++
		}
	}
	if receipts == 0 || len(callIDs) == 0 {
		t.Fatalf("事件日志缺少工具事件（receipts=%d distinct_calls=%d），回放无意义", receipts, len(callIDs))
	}
	if toolRows != len(callIDs) {
		t.Fatalf("tool_call rows = %d, want %d（每个 call 只允许一行；回执重复投影会翻倍）", toolRows, len(callIDs))
	}
	t.Logf("replay ok: items=%d tool_call_rows=%d distinct_calls=%d receipts=%d",
		len(m.Items), toolRows, len(callIDs), receipts)
}
