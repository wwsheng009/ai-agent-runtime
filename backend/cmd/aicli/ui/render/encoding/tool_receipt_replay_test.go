package encoding

import (
	"encoding/json"
	"os"
	"sort"
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
	receiptIDs := map[string]struct{}{}
	receipts := 0
	// seenTurnFinished 之后新建的 tool cell 就是「工具行渲染在正文/回合结束之后」
	// 的直接证据（回合末 durable 回执只能复用既有 cell，不允许重建）。
	seenTurnFinished := false
	var toolCellsAfterTurnFinished []string
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
			if id := toolCallID(ev); id != "" {
				receiptIDs[id] = struct{}{}
			}
		case runtimechat.EventToolStarted, "tool.requested",
			runtimechat.EventToolFinished, "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled":
			if id := toolCallID(ev); id != "" {
				callIDs[id] = struct{}{}
			}
		}
		before := len(e.Snapshot().Items)
		e.Encode(ev)
		if ev.Type == "agent.turn.started" || ev.Type == "session_start" {
			// 新回合开始：后续工具行属于这个回合，不再算「回合结束后补行」。
			seenTurnFinished = false
			continue
		}
		if ev.Type == "agent.turn.finished" {
			seenTurnFinished = true
			continue
		}
		if !seenTurnFinished {
			continue
		}
		for _, it := range e.Snapshot().Items[before:] {
			if it != nil && it.Kind == KindToolCall {
				toolCellsAfterTurnFinished = append(toolCellsAfterTurnFinished, it.Head)
			}
		}
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
	// 回执只允许复用既有 cell：没有 live 工具事件的回执会被当成「崩溃恢复」在
	// transcript 尾部重建一行（实证会话 session_20260925221754_dyRAoaEO：参数非法
	// 的合成调用只写了历史、没发 tool.requested/completed，回合末回执在正文与
	// agent.turn.finished 之后补出一行 "• Completed/Failed <tool>"）。
	orphans := make([]string, 0)
	for id := range receiptIDs {
		if _, ok := callIDs[id]; !ok {
			orphans = append(orphans, id)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Fatalf("孤立回执（无 live 工具事件）=%v：回执不得在回合末新建工具行", orphans)
	}
	if len(toolCellsAfterTurnFinished) > 0 {
		t.Fatalf("回合结束后新建了 %d 个工具行：%v（工具行必须就地内联，不得补在正文之后）",
			len(toolCellsAfterTurnFinished), toolCellsAfterTurnFinished)
	}
	t.Logf("replay ok: items=%d tool_call_rows=%d distinct_calls=%d receipts=%d",
		len(m.Items), toolRows, len(callIDs), receipts)
}
