package encoding

import (
	"fmt"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func llmFinishedWithSnapshot(text string) runtimeevents.Event {
	return event(runtimechat.EventLLMRequestFinished, map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1",
		"assistant_snapshot": text,
	})
}

// TestAssistantDeltaGapRecoveredByRequestSnapshot 复现线上截断缺陷：桥接层丢失
// 一个 delta 后，sequence 出现空洞，后续 delta 进入乱序缓冲。请求边界的权威
// 快照必须把消息恢复成完整文本（对应会话 session_20260918150908_xs14LXgM）。
func TestAssistantDeltaGapRecoveredByRequestSnapshot(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(assistantDelta("好消息：Docker Hub 镜像源速度很快。GHCR 直连太慢，改为**", 1))
	// seq 2 丢失；seq 3 / 4 只能进入乱序缓冲。
	e.Encode(assistantDelta("从源码构建镜像**（国内源加速）。", 3))
	e.Encode(assistantDelta("先停掉 GHCR 拉取，拉取构建所需基础镜像。", 4))

	full := "好消息：Docker Hub 镜像源速度很快。GHCR 直连太慢，改为**从源码构建镜像**（国内源加速）。先停掉 GHCR 拉取，拉取构建所需基础镜像。"
	e.Encode(llmFinishedWithSnapshot(full))

	m := e.Snapshot()
	if len(m.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(m.Items))
	}
	if m.Items[0].Head != full {
		t.Fatalf("assistant head = %q, want authoritative snapshot", m.Items[0].Head)
	}
	if m.Items[0].Status != StatusCompleted {
		t.Fatalf("assistant status = %s, want completed", m.Items[0].Status)
	}
	if e.Stats().OutOfOrderCount == 0 {
		t.Fatalf("expected OutOfOrderCount > 0 for the gapped stream")
	}
}

// TestAssistantSnapshotMaterializesWhenAllDeltasLost：即使所有增量都丢失，
// 权威快照仍必须落出完整 assistant item，且位置先于其后的工具链路。
func TestAssistantSnapshotMaterializesWhenAllDeltasLost(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(llmFinishedWithSnapshot("完整回答"))
	e.Encode(toolStarted("call-1", "write"))

	m := e.Snapshot()
	if len(m.Items) != 2 {
		t.Fatalf("items = %d, want 2 (assistant + tool)", len(m.Items))
	}
	if m.Items[0].Kind != KindAssistant || m.Items[0].Head != "完整回答" {
		t.Fatalf("items[0] = %+v, want complete assistant snapshot", m.Items[0])
	}
	if m.Items[1].Kind != KindToolCall {
		t.Fatalf("items[1].Kind = %s, want tool_call", m.Items[1].Kind)
	}
}

// TestAssistantOutOfOrderOverflowDoesNotTaintStream 锁定修复 2：乱序缓冲超限只
// 淘汰最旧项，绝不 taint 后丢弃整条流（旧行为会让后续 delta 永久丢失）。
func TestAssistantOutOfOrderOverflowDoesNotTaintStream(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	for i := 0; i < assistantStreamPendingLimit+8; i++ {
		e.Encode(assistantDelta(fmt.Sprintf("x%d", i), uint64(1000+i)))
	}
	// 期望的下一序增量：旧实现已被 taint，这里必须正常提交。
	e.Encode(assistantDelta("起点", 1))

	m := e.Snapshot()
	if len(m.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(m.Items))
	}
	if head := m.Items[0].Head; head != "起点" {
		t.Fatalf("head = %q, want 起点 (stream must not be tainted)", head)
	}
}

// TestAssistantFinalCanCorrectSnapshotText：run 级终稿对同一响应仍可做一次
// 文本校正（快照只保证完整性，不冻结后续修正）。
func TestAssistantFinalCanCorrectSnapshotText(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(llmFinishedWithSnapshot("v1"))
	e.Encode(assistantFinal("v2"))

	if head := e.Snapshot().Items[0].Head; head != "v2" {
		t.Fatalf("head = %q, want corrected v2", head)
	}
}
