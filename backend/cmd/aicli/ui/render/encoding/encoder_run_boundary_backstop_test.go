package encoding

import (
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func reasoningDeltaEvent(text string, seq uint64) runtimeevents.Event {
	return event("assistant.reasoning", map[string]interface{}{
		"turn_id":   "turn-1",
		"stream_id": "stream-1",
		"sequence":  seq,
		"mode":      "append",
		"reasoning": map[string]interface{}{
			"format":  "stream_delta",
			"summary": text,
		},
	})
}

// TestReasoningSnapshotRecoversDroppedDeltas：reasoning 是桥接层允许按
// coalesce 预算丢弃的流（assistant 文本不允许丢弃），seq 空洞会让增量拼装
// 永久缺失中间内容。llm.request.finished 携带的 reasoning_snapshot 必须在
// 请求边界整段收敛（对应线上 session_20260918172548_iHgz994o：长思考期间
// 4073 条 reasoning delta 被丢弃，界面没有可见过程内容）。
func TestReasoningSnapshotRecoversDroppedDeltas(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(reasoningDeltaEvent("第一段推理。", 1))
	// seq 2 在投递链路上被丢弃；seq 3 只能进入乱序缓冲，无法拼出完整文本。
	e.Encode(reasoningDeltaEvent("第三段推理。", 3))

	full := "第一段推理。第二段推理。第三段推理。"
	e.Encode(event(runtimechat.EventLLMRequestFinished, map[string]interface{}{
		"turn_id":            "turn-1",
		"stream_id":          "stream-1",
		"reasoning_snapshot": full,
	}))

	m := e.Snapshot()
	if len(m.Items) != 1 {
		t.Fatalf("items = %d, want 1 reasoning item: %#v", len(m.Items), m.Items)
	}
	it := m.Items[0]
	if it.Kind != KindReasoning {
		t.Fatalf("kind = %s, want reasoning", it.Kind)
	}
	if it.Head != full {
		t.Fatalf("reasoning head = %q, want authoritative snapshot %q", it.Head, full)
	}
	if it.Status != StatusCompleted {
		t.Fatalf("reasoning status = %s, want completed", it.Status)
	}
}

// TestFinalizeOpenToolCellsClosesOrphanRunningCell：上一轮取消/中断后没有
// 终态事件的工具单元格必须能被 run 边界按 canceled 收敛（"• Running ..."
// 首行改写为 "• Canceled ..."），而不是永久驻留并钉住 ActiveBand。
func TestFinalizeOpenToolCellsClosesOrphanRunningCell(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(toolStarted("call-orphan", "grep"))

	cs := e.FinalizeOpenToolCells(StatusCanceled)
	if cs == nil || len(cs.Changes) == 0 {
		t.Fatalf("orphan sweep produced no changes")
	}
	m := e.Snapshot()
	if len(m.Items) != 1 || m.Items[0].Kind != KindToolCall {
		t.Fatalf("items = %#v, want one tool cell", m.Items)
	}
	it := m.Items[0]
	if it.Status != StatusCanceled {
		t.Fatalf("tool status = %s, want canceled", it.Status)
	}
	if !strings.Contains(it.Head, "• Canceled grep") {
		t.Fatalf("tool head = %q, want canceled head", it.Head)
	}

	// 幂等：终态单元格不会被再次改写，二轮 sweep 必须是无操作。
	again := e.FinalizeOpenToolCells(StatusCanceled)
	if again != nil && len(again.Changes) != 0 {
		t.Fatalf("second sweep changed %d item(s), want idempotent no-op", len(again.Changes))
	}
}
