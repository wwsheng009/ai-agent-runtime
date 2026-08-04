package encoding

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// event 构造测试事件（payload 语义与 chat runtime 一致）。
func event(typ string, payload map[string]interface{}) runtimeevents.Event {
	return runtimeevents.Event{Type: typ, Payload: payload}
}

func assistantDelta(text string, seq uint64) runtimeevents.Event {
	return event(runtimechat.EventAssistantDelta, map[string]interface{}{
		"turn_id":   "turn-1",
		"stream_id": "stream-1",
		"delta":     text,
		"sequence":  seq,
	})
}

func llmStarted() runtimeevents.Event {
	return event(runtimechat.EventLLMRequestStarted, map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1",
	})
}

func llmFinished() runtimeevents.Event {
	return event(runtimechat.EventLLMRequestFinished, map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1",
	})
}

func assistantFinal(text string) runtimeevents.Event {
	return event(runtimechat.EventAssistantMessage, map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1",
		"content": text,
	})
}

func toolStarted(callID, name string) runtimeevents.Event {
	return event(runtimechat.EventToolStarted, map[string]interface{}{
		"tool_call_id": callID,
		"tool_name":    name,
	})
}

func toolFinished(callID, output string) runtimeevents.Event {
	return event(runtimechat.EventToolFinished, map[string]interface{}{
		"tool_call_id": callID,
		"output":       output,
	})
}

// itemIDs 提取模型 Item ID 列表。
func itemIDs(m *RenderModel) []string {
	out := make([]string, 0, len(m.Items))
	for _, it := range m.Items {
		out = append(out, it.ID)
	}
	return out
}

// kinds 提取模型 Item Kind 列表。
func kinds(m *RenderModel) []string {
	out := make([]string, 0, len(m.Items))
	for _, it := range m.Items {
		out = append(out, string(it.Kind))
	}
	return out
}

// TestEncodeBasicSequence 验证基础链路：LLM 流 -> assistant 块 -> 工具链路，
// 渲染顺序与事件因果一致，Seq 单调。
func TestEncodeBasicSequence(t *testing.T) {
	e := NewEventEncoder()
	evs := []runtimeevents.Event{
		// system 块占位用 legacy 可见事件：session_start 已静默（零 cell）。
		event(runtimechat.EventSessionCompactCompleted, map[string]interface{}{"message": "session started"}),
		llmStarted(),
		assistantDelta("你", 1),
		assistantDelta("好", 2),
		assistantFinal("你好"),
		llmFinished(),
		toolStarted("call-1", "read_file"),
		toolFinished("call-1", "file content"),
	}
	for _, ev := range evs {
		e.Encode(ev)
	}
	m := e.Snapshot()
	if len(m.Items) != 4 {
		t.Fatalf("items = %d, want 4 (system, assistant, tool_call, tool_output)", len(m.Items))
	}

	// 逐项校验
	if m.Items[0].Kind != KindSystem {
		t.Fatalf("items[0].Kind = %s, want system", m.Items[0].Kind)
	}
	if m.Items[1].Kind != KindAssistant {
		t.Fatalf("items[1].Kind = %s, want assistant", m.Items[1].Kind)
	}
	if m.Items[1].Head != "你好" {
		t.Fatalf("assistant head = %q, want 你好", m.Items[1].Head)
	}
	if m.Items[1].Status != StatusCompleted {
		t.Fatalf("assistant status = %s, want completed", m.Items[1].Status)
	}
	if m.Items[2].Kind != KindToolCall {
		t.Fatalf("items[2].Kind = %s, want tool_call", m.Items[2].Kind)
	}
	if m.Items[3].Kind != KindToolOutput {
		t.Fatalf("items[3].Kind = %s, want tool_output", m.Items[3].Kind)
	}
	if m.Items[3].CauseID != m.Items[2].ID {
		t.Fatalf("tool_output.CauseID = %s, want tool_call.ID %s", m.Items[3].CauseID, m.Items[2].ID)
	}
	// Seq 单调
	for i := 1; i < len(m.Items); i++ {
		if m.Items[i].Seq <= m.Items[i-1].Seq {
			t.Fatalf("Seq 不单调: items[%d].Seq=%d <= items[%d].Seq=%d", i, m.Items[i].Seq, i-1, m.Items[i-1].Seq)
		}
	}
	// ID 唯一
	seen := map[string]bool{}
	for _, it := range m.Items {
		if seen[it.ID] {
			t.Fatalf("ID 重复: %s", it.ID)
		}
		seen[it.ID] = true
	}
	// Tail 指向最后一项
	if m.Tail == nil || m.Tail.ItemID != m.Items[len(m.Items)-1].ID {
		t.Fatalf("Tail = %+v, want ItemID %s", m.Tail, m.Items[len(m.Items)-1].ID)
	}
}

// TestEncodeIdempotent 验证幂等：重复 final 不产生变更（跳过计数），
// 空 delta 不产生变更。
func TestEncodeIdempotent(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(assistantDelta("hi", 1))
	e.Encode(assistantFinal("hi"))
	before := e.Snapshot()

	// 重复 final：内容一致 -> upsert 幂等跳过（但 status 已是终态，无变更）
	cs := e.Encode(assistantFinal("hi"))
	if len(cs.Changes) != 0 {
		t.Fatalf("重复 final 产生 %d 变更, want 0", len(cs.Changes))
	}
	// 重复 llm_finished：终态跳过
	cs = e.Encode(llmFinished())
	if len(cs.Changes) != 0 {
		t.Fatalf("重复 llm_finished 产生 %d 变更, want 0", len(cs.Changes))
	}
	// 空 delta 不追加内容
	cs = e.Encode(assistantDelta("", 2))
	if len(cs.Changes) != 0 {
		t.Fatalf("空 delta 产生 %d 变更, want 0", len(cs.Changes))
	}
	after := e.Snapshot()
	if len(after.Items) != len(before.Items) {
		t.Fatalf("幂等后 items = %d, want %d", len(after.Items), len(before.Items))
	}
	if e.Stats().DuplicateCount == 0 {
		t.Fatalf("DuplicateCount = 0, want > 0")
	}
}

// TestEncodeOutOfOrder 验证乱序有序提交：delta 先于 llm_started 到达时
// 创建空块且后续 llm_started 复用（同一流只有一个块）；同流 sequence
// 乱序被统计，但最终文本按 sequence 有序拼接（对齐旧终端
// orderAssistantDelta 的重排语义，不丢信息、不错序）。
func TestEncodeOutOfOrder(t *testing.T) {
	e := NewEventEncoder()
	// 乱序：先来 delta（无 llm_started 打底）→ 创建空块并缓存乱序段
	e.Encode(assistantDelta("E", 5))
	// 再来 llm_started：复用已创建块（不 append 新块）
	e.Encode(llmStarted())
	m := e.Snapshot()
	if len(m.Items) != 1 {
		t.Fatalf("items = %d, want 1 (single assistant block)", len(m.Items))
	}
	// 补齐 1..4：最终按 sequence 有序拼接为 ABCDE
	e.Encode(assistantDelta("A", 1))
	e.Encode(assistantDelta("B", 2))
	e.Encode(assistantDelta("C", 3))
	e.Encode(assistantDelta("D", 4))
	m2 := e.Snapshot()
	if head := m2.Items[0].Head; head != "ABCDE" {
		t.Fatalf("head = %q, want ABCDE (sequence-ordered)", head)
	}
	// 同流 sequence 乱序：1 -> 3 -> 2 应计 1 次乱序并按序提交为 ABC
	e2 := NewEventEncoder()
	e2.Encode(llmStarted())
	e2.Encode(assistantDelta("A", 1))
	e2.Encode(assistantDelta("C", 3))
	e2.Encode(assistantDelta("B", 2))
	if e2.Stats().OutOfOrderCount != 1 {
		t.Fatalf("OutOfOrderCount = %d, want 1", e2.Stats().OutOfOrderCount)
	}
	m3 := e2.Snapshot()
	if len(m3.Items) != 1 {
		t.Fatalf("items = %d, want 1 (single assistant)", len(m3.Items))
	}
	if head := m3.Items[0].Head; head != "ABC" {
		t.Fatalf("head = %q, want ABC (sequence-ordered)", head)
	}
}

// TestEncodeReasoningIndependentOfAssistant 验证 reasoning 与 assistant 是
// 两个独立 Item：reasoning 内容绝不覆盖 assistant 块，且 LLM 请求结束时
// 两者都进入终态。
func TestEncodeReasoningIndependentOfAssistant(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(event(runtimechat.EventAssistantReasoning, map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1", "text": "thinking...",
	}))
	e.Encode(assistantDelta("Hello", 1))
	m := e.Snapshot()
	if len(m.Items) != 2 {
		t.Fatalf("items = %d, want 2 (reasoning + assistant)", len(m.Items))
	}
	// llm_started 先建 assistant 块（空 Head，渲染层跳过空块），
	// reasoning 随后 append；两者互不覆盖。
	if m.Items[0].Kind != KindAssistant || m.Items[0].Head != "Hello" {
		t.Fatalf("items[0] = %+v, want assistant with Hello", m.Items[0])
	}
	if m.Items[1].Kind != KindReasoning || m.Items[1].Head != "thinking..." {
		t.Fatalf("items[1] = %+v, want reasoning with thinking...", m.Items[1])
	}
	// LLM 请求结束：reasoning 与 assistant 均终态
	e.Encode(llmFinished())
	m2 := e.Snapshot()
	if m2.Items[0].Status != StatusCompleted {
		t.Fatalf("reasoning status = %s, want completed", m2.Items[0].Status)
	}
	if m2.Items[1].Status != StatusCompleted {
		t.Fatalf("assistant status = %s, want completed", m2.Items[1].Status)
	}
}

// TestEncodeTerminalStateFrozen 验证终态保护：assistant final 后到达的
// delta 被丢弃，状态不会被改回 running。
func TestEncodeTerminalStateFrozen(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(assistantDelta("hi", 1))
	e.Encode(assistantFinal("hi"))
	cs := e.Encode(assistantDelta("late", 2))
	if len(cs.Changes) != 0 {
		t.Fatalf("final 后 delta 产生 %d 变更, want 0", len(cs.Changes))
	}
	m := e.Snapshot()
	if len(m.Items) != 1 || m.Items[0].Head != "hi" || m.Items[0].Status != StatusCompleted {
		t.Fatalf("final 后模型被修改: %+v", m.Items)
	}
}

// TestEncodeToolIdempotent 验证工具链路幂等：重复 started 不新建 ToolCall，
// 重复 finished 不重复 append ToolOutput。
func TestEncodeToolIdempotent(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(toolStarted("call-1", "read_file"))
	e.Encode(toolStarted("call-1", "read_file"))
	e.Encode(toolFinished("call-1", "content"))
	e.Encode(toolFinished("call-1", "content"))
	m := e.Snapshot()
	if len(m.Items) != 2 {
		t.Fatalf("items = %d, want 2 (tool_call + tool_output)", len(m.Items))
	}
	if e.Stats().DuplicateCount == 0 {
		t.Fatal("DuplicateCount = 0, want > 0")
	}
	if m.Items[0].Kind != KindToolCall || m.Items[1].Kind != KindToolOutput {
		t.Fatalf("kinds = %s,%s, want tool_call,tool_output", m.Items[0].Kind, m.Items[1].Kind)
	}
	if m.Items[0].Status != StatusCompleted || m.Items[1].Status != StatusCompleted {
		t.Fatalf("statuses = %s,%s, want completed,completed", m.Items[0].Status, m.Items[1].Status)
	}
}

func TestEncodeLegacyToolLifecycleUsesCallIdentity(t *testing.T) {
	e := NewEventEncoder()
	requested := event("tool.requested", map[string]interface{}{
		"tool_call_id": "legacy-call-1",
		"tool_name":    "shell",
	})
	if cs := e.Encode(requested); len(cs.Changes) != 1 || cs.Changes[0].Op != OpAppend {
		t.Fatalf("requested changes = %+v, want one append", cs.Changes)
	}
	model := e.Snapshot()
	if len(model.Items) != 1 || model.Items[0].Kind != KindToolCall || model.Items[0].Status != StatusPending {
		t.Fatalf("requested model = %+v, want one mutable tool call", model.Items)
	}

	progress := event("tool.progress", map[string]interface{}{
		"tool_call_id": "legacy-call-1",
		"message":      "50% complete",
	})
	if cs := e.Encode(progress); len(cs.Changes) != 1 || cs.Changes[0].Op != OpUpsert {
		t.Fatalf("progress changes = %+v, want one upsert", cs.Changes)
	}
	if got := e.Snapshot().Items[0].Head; got != "shell\n50% complete" {
		t.Fatalf("progress head = %q, want source-backed tool summary", got)
	}

	completed := event("tool.completed", map[string]interface{}{
		"tool_call_id":  "legacy-call-1",
		"summary_lines": []interface{}{"ok", "2 files"},
	})
	if cs := e.Encode(completed); len(cs.Changes) != 2 {
		t.Fatalf("completed changes = %+v, want tool finalize + output", cs.Changes)
	}
	model = e.Snapshot()
	if len(model.Items) != 2 || model.Items[0].Status != StatusCompleted || model.Items[1].Kind != KindToolOutput || model.Items[1].CauseID != model.Items[0].ID {
		t.Fatalf("completed model = %+v, want finalized tool chain", model.Items)
	}
}

func TestEncodeLegacyToolStartWithoutIdentityFallsBackToSystem(t *testing.T) {
	for name, payload := range map[string]map[string]interface{}{
		"missing call id":   {"tool_name": "shell"},
		"missing tool name": {"tool_call_id": "orphan-call"},
	} {
		t.Run(name, func(t *testing.T) {
			e := NewEventEncoder()
			cs := e.Encode(event("tool.requested", payload))
			if cs == nil || len(cs.Changes) != 1 || cs.Changes[0].Item.Kind != KindSystem {
				t.Fatalf("changes = %+v, want one system fallback", cs)
			}
			model := e.Snapshot()
			if len(model.Items) != 1 || model.Items[0].Kind != KindSystem {
				t.Fatalf("model = %+v, want one system item", model.Items)
			}
		})
	}
}

// TestEncodeReplay 验证重放：Reset 后按相同事件序列重放，模型等价。
func TestEncodeReplay(t *testing.T) {
	evs := []runtimeevents.Event{
		// system 块占位用 legacy 可见事件：session_start 已静默（零 cell）。
		event(runtimechat.EventSessionCompactCompleted, map[string]interface{}{"message": "start"}),
		llmStarted(),
		assistantDelta("re", 1),
		assistantDelta("play", 2),
		assistantFinal("replay"),
		llmFinished(),
		toolStarted("call-9", "bash"),
		toolFinished("call-9", "out"),
	}
	live := NewEventEncoder()
	for _, ev := range evs {
		live.Encode(ev)
	}
	replayed := NewEventEncoder()
	if _, err := replayed.Replay(evs); err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	lv, rv := live.Snapshot(), replayed.Snapshot()
	if len(lv.Items) != len(rv.Items) {
		t.Fatalf("重放后 items 数量不一致: %d vs %d", len(lv.Items), len(rv.Items))
	}
	for i := range lv.Items {
		a, b := lv.Items[i], rv.Items[i]
		if a.ID != b.ID || a.Seq != b.Seq || a.Head != b.Head || a.Kind != b.Kind || a.Status != b.Status || a.CauseID != b.CauseID {
			t.Fatalf("item[%d] 不一致: live=%+v replay=%+v", i, a, b)
		}
	}
	// Reset 后重放等价于全新编码器
	e3 := NewEventEncoder()
	for _, ev := range evs {
		e3.Encode(ev)
	}
	live.Reset()
	if _, err := live.Replay(evs); err != nil {
		t.Fatalf("Reset+Replay error: %v", err)
	}
	lv2 := live.Snapshot()
	if len(lv2.Items) != len(lv.Items) {
		t.Fatalf("Reset+Replay items = %d, want %d", len(lv2.Items), len(lv.Items))
	}
}

// TestEncodeUnknownEvent 验证未知事件兜底：append system 块，不丢信息。
func TestEncodeUnknownEvent(t *testing.T) {
	e := NewEventEncoder()
	cs := e.Encode(event("future_event_type", map[string]interface{}{"message": "unknown"}))
	if len(cs.Changes) != 1 || cs.Changes[0].Op != OpAppend {
		t.Fatalf("unknown event changes = %+v, want 1 append", cs.Changes)
	}
	if e.Stats().UnknownCount != 1 {
		t.Fatalf("UnknownCount = %d, want 1", e.Stats().UnknownCount)
	}
	m := e.Snapshot()
	if len(m.Items) != 1 || m.Items[0].Kind != KindSystem {
		t.Fatalf("unknown event item = %+v, want system item", m.Items)
	}
	if m.Items[0].Head != "unknown" {
		t.Fatalf("head = %q, want unknown", m.Items[0].Head)
	}
}

// TestChangeSetOrder 验证变更集语义：append 的 Revision 为 1，
// upsert 单调递增。
func TestChangeSetOrder(t *testing.T) {
	e := NewEventEncoder()
	cs := e.Encode(llmStarted())
	if len(cs.Changes) != 1 || cs.Changes[0].Op != OpAppend || cs.Changes[0].Revision != 1 {
		t.Fatalf("llm_started changes = %+v, want 1 append rev=1", cs.Changes)
	}
	cs = e.Encode(assistantDelta("a", 1))
	if len(cs.Changes) != 1 || cs.Changes[0].Op != OpUpsert || cs.Changes[0].Revision != 2 {
		t.Fatalf("delta changes = %+v, want 1 upsert rev=2", cs.Changes)
	}
}

// knownEventTypes 穷举 internal/chat 的全部事件类型常量（events.go 31 个），
// 与 classify 的显式映射清单一一对应。新增事件类型时必须同时补本表与
// classify 映射，否则 TestEncodeExhaustiveKnownEventTypes 失败。
func knownEventTypes() []string {
	return []string{
		runtimechat.EventSessionStart,
		runtimechat.EventSessionEnd,
		runtimechat.EventSessionInterrupted,
		runtimechat.EventAssistantDelta,
		runtimechat.EventAssistantReasoning,
		runtimechat.EventAssistantMessage,
		runtimechat.EventLLMRequestStarted,
		runtimechat.EventLLMRequestFinished,
		runtimechat.EventToolStarted,
		runtimechat.EventToolFinished,
		runtimechat.EventToolReceiptRecorded,
		runtimechat.EventToolReceiptReplayed,
		runtimechat.EventApprovalRequested,
		runtimechat.EventApprovalResolved,
		runtimechat.EventQuestionAsked,
		runtimechat.EventQuestionAnswered,
		runtimechat.EventCheckpointCreated,
		runtimechat.EventSessionCompactStarted,
		runtimechat.EventSessionCompactCompleted,
		runtimechat.EventSessionCompactSkipped,
		runtimechat.EventSessionCompactFailed,
		runtimechat.EventContextReconciled,
		runtimechat.EventRewindStarted,
		runtimechat.EventRewindFinished,
		runtimechat.EventBacktrackStarted,
		runtimechat.EventBacktrackFinished,
		runtimechat.EventJobStarted,
		runtimechat.EventJobOutput,
		runtimechat.EventJobCancelled,
		runtimechat.EventJobFinished,
		runtimechat.EventMailboxReceived,
	}
}

// TestEncodeExhaustiveKnownEventTypes 穷举断言（P2 验证）：
// 每个已知事件类型单独编码——不 panic、Encode 确实执行（EncodeCount == 1）、
// 且在 classify 中显式映射（UnknownCount == 0）。未知类型会走 default
// 兜底，见 TestEncodeUnknownTypeFallsBackToSystem。
//
// 注意：无主终态事件（孤儿 llm_request_finished / tool_finished——没有
// 对应 started 事件）被编码器静默忽略，允许变更集为空；有变更时则必须
// 至少产生一个 Item（信息不丢）。
func TestEncodeExhaustiveKnownEventTypes(t *testing.T) {
	for _, typ := range knownEventTypes() {
		t.Run(typ, func(t *testing.T) {
			e := NewEventEncoder()
			ev := event(typ, map[string]interface{}{"message": "probe"})
			cs := e.Encode(ev)
			st := e.Stats()
			if st.EncodeCount != 1 {
				t.Fatalf("%s: EncodeCount = %d, want 1", typ, st.EncodeCount)
			}
			if st.UnknownCount != 0 {
				t.Fatalf("%s: UnknownCount = %d, want 0（classify 未显式映射该类型）", typ, st.UnknownCount)
			}
			if len(cs.Changes) > 0 {
				m := e.Snapshot()
				if len(m.Items) == 0 {
					t.Fatalf("%s: 产生变更但 items = 0，事件信息被丢弃", typ)
				}
			}
		})
	}
}

// TestEncodeUnknownTypeFallsBackToSystem 验证兜底路径：未映射类型
// append 为 system 块（信息不丢）且计入 UnknownCount。
func TestEncodeUnknownTypeFallsBackToSystem(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(event("future.event.type", map[string]interface{}{"message": "x"}))
	m := e.Snapshot()
	if len(m.Items) != 1 || m.Items[0].Kind != KindSystem || m.Items[0].Head != "x" {
		t.Fatalf("未知事件兜底失败: items=%+v", m.Items)
	}
	if u := e.Stats().UnknownCount; u != 1 {
		t.Fatalf("UnknownCount = %d, want 1", u)
	}
	if n := e.Stats().EncodeCount; n != 1 {
		t.Fatalf("EncodeCount = %d, want 1", n)
	}
}

// knownLegacyEventTypes 是 agent/skills 层直接 emit、经事件总线全量进入
// 编码器的 legacy 事件类型（未走 chatcore 类型转换）。它们属已知呈现事件
// （system 块），不应计入 UnknownCount；新增 emit 类型时必须补本表，
// 否则 TestEncodeExhaustiveKnownLegacyEventTypes 失败。仅内部生命周期/
// 遥测类型（llm.request.started、planning.started、subagent.started 等）
// 不在此表：它们走 isSilentSystemEventType 静默分类（零可见输出，见
// TestEncodeSilentSystemEventTypes），新增此类事件应补静默表而非本表。
func knownLegacyEventTypes() []string {
	return []string{
		"assistant.reasoning",
		"context.preflight.started",
		"context.preflight.compacted",
		"context.preflight.failed",
		"llm.request.finished",
		"llm.retry",
		"patch.decision",
		"patch.applied",
		"response.created",
		"response.completed",
		"subagent.completed",
		"subagent.denied",
		"subagent.batch.completed",
		"tool.requested",
		"tool.progress",
		"tool.completed",
		"tool.denied",
		"tool.reduced",
		"planning.completed",
		"team.completed",
		"team.interrupted",
		"team.summary",
		"team.task.completed",
		"team.plan.replanned",
	}
}

// silentSystemEventTypes 是静默分类的完整事件集（与
// isSilentSystemEventType 保持同构；测试穷举防止漏改）。新增静默类型时
// 必须同时补 encoder.go 的 isSilentSystemEventType 与本表。
func silentSystemEventTypes() []string {
	return []string{
		runtimechat.EventSessionStart,
		runtimechat.EventSessionEnd,
		runtimechat.EventSessionInterrupted,
		runtimechat.EventSessionCompactSkipped,
		runtimechat.EventContextReconciled,
		"llm.request.started",
		"planning.started",
		"subagent.batch.started",
		"subagent.started",
		"task.started",
		"team.task.started",
		"context.tool_schema.frozen",
	}
}

// TestEncodeSilentSystemEventTypes 验证静默分类：内部生命周期/遥测事件
// 不产生任何变更与 Item。旧终端 timeline 渲染器对它们零可见输出（或
// DebugOnly 默认隐藏）；若编码器为它们创建 system cell，Scene presenter
// 会把 "❌ <type>" 泄漏到可见 transcript（真实会话已复现）。事件仍被
// 消费（EncodeCount=1），且不计 Unknown。
func TestEncodeSilentSystemEventTypes(t *testing.T) {
	for _, typ := range silentSystemEventTypes() {
		t.Run(typ, func(t *testing.T) {
			e := NewEventEncoder()
			cs := e.Encode(event(typ, map[string]interface{}{"message": "probe"}))
			if cs == nil || len(cs.Changes) != 0 {
				t.Fatalf("%s: 变更集 = %+v, want 空", typ, cs)
			}
			if m := e.Snapshot(); len(m.Items) != 0 {
				t.Fatalf("%s: items = %+v, want 0", typ, m.Items)
			}
			if u := e.Stats().UnknownCount; u != 0 {
				t.Fatalf("%s: UnknownCount = %d, want 0（已知静默类型不应计未知）", typ, u)
			}
			if n := e.Stats().EncodeCount; n != 1 {
				t.Fatalf("%s: EncodeCount = %d, want 1", typ, n)
			}
		})
	}
}

// TestEncodeExhaustiveKnownLegacyEventTypes 穷举 agent/skills 层 legacy 事件
// 类型：全部识别为已知事件（UnknownCount == 0）且不 panic。该 fixture
// 刻意不提供 tool_call_id/tool_name，因此 tool lifecycle 项按 incomplete
// payload 的 system fallback 断言；带完整身份的 mutable tool-cell 路径由
// TestEncodeLegacyToolLifecycleUsesCallIdentity 覆盖。这样 /debug 的
// "Unknown Types:" 统计只反映真正未知的类型。
func TestEncodeExhaustiveKnownLegacyEventTypes(t *testing.T) {
	for _, typ := range knownLegacyEventTypes() {
		t.Run(typ, func(t *testing.T) {
			e := NewEventEncoder()
			cs := e.Encode(event(typ, map[string]interface{}{"message": "probe"}))
			if len(cs.Changes) == 0 {
				t.Fatalf("%s: 变更集为空", typ)
			}
			m := e.Snapshot()
			if len(m.Items) != 1 || m.Items[0].Kind != KindSystem {
				t.Fatalf("%s: items = %+v, want 1 个 system 块", typ, m.Items)
			}
			if u := e.Stats().UnknownCount; u != 0 {
				t.Fatalf("%s: UnknownCount = %d, want 0（已知 legacy 类型不应计未知）", typ, u)
			}
		})
	}
}

// TestSubmitCommand 固化 SubmitCommand 契约：命令结果作为一次性终态
// command 块（KindCommand + StatusCompleted）append，时钟/统计/Tail 与
// Encode 对齐；与用户输入交错时保持全序。
func TestSubmitCommand(t *testing.T) {
	e := NewEventEncoder()
	e.SubmitUserInput("U1")
	cs := e.SubmitCommand("cmd output")
	if cs == nil || len(cs.Changes) != 1 {
		t.Fatalf("changes = %+v, want 1 个 append", cs)
	}
	it := e.Snapshot().Items[len(e.Snapshot().Items)-1]
	if it.Kind != KindCommand || it.Status != StatusCompleted {
		t.Fatalf("item = %+v, want KindCommand + completed", it)
	}
	if it.Head != "cmd output" {
		t.Fatalf("head = %q, want cmd output", it.Head)
	}
	// 全序：user 块在前，command 块在后。
	if got := kinds(e.Snapshot()); len(got) != 2 || got[0] != "user" || got[1] != "command" {
		t.Fatalf("kinds = %v, want [user command]", got)
	}
	// 时钟与统计计入。
	if e.Stats().EncodeCount != 2 {
		t.Fatalf("EncodeCount = %d, want 2", e.Stats().EncodeCount)
	}
	if tl := e.Tail(); tl == nil || tl.ItemID != it.ID {
		t.Fatalf("tail = %+v, want ItemID %q", tl, it.ID)
	}
}

// TestSubmitError 固化 SubmitError 契约：操作错误作为一次性终态 system 块
// （KindSystem + StatusCompleted）append（会话/诊断语义），不与 assistant
// 流状态机交互；错误文本原样保留。
func TestSubmitError(t *testing.T) {
	e := NewEventEncoder()
	cs := e.SubmitError("操作错误: boom")
	if cs == nil || len(cs.Changes) != 1 {
		t.Fatalf("changes = %+v, want 1 个 append", cs)
	}
	m := e.Snapshot()
	if len(m.Items) != 1 || m.Items[0].Kind != KindSystem || m.Items[0].Status != StatusCompleted {
		t.Fatalf("items = %+v, want 1 个 KindSystem completed", m.Items)
	}
	if m.Items[0].Head != "操作错误: boom" {
		t.Fatalf("head = %q, want 操作错误: boom", m.Items[0].Head)
	}
	// 终态 append 后不允许再 upsert：ChangeSet 不含对它的后续变更。
	if st := e.Stats(); st.EncodeCount != 1 || st.DuplicateCount != 0 {
		t.Fatalf("stats = %+v, want EncodeCount=1 DuplicateCount=0", st)
	}
}

// TestSubmitUserInteraction 固化 SubmitUserInteraction 契约（P4 Tail 锚定
// 插入，设计文档 §1.3 行 12）：/debug、/model 交互输出以触发时刻模型尾部
// 锚点为界插入渲染序列（锚点 Item 之后），KindUserInteraction 终态块；
// 不推进 Tail（交互输出不参与因果链）；锚点缺失/为空退化为 append。
func TestSubmitUserInteraction(t *testing.T) {
	e := NewEventEncoder()
	e.SubmitUserInput("U1")
	anchor := e.Tail() // 触发时刻锚点：指向 U1
	if anchor == nil || anchor.ItemID != "item-1" {
		t.Fatalf("anchor = %+v, want item-1", anchor)
	}
	// 模型在触发时刻后继续增长（命令/流式输出），锚点仍指向 U1。
	e.SubmitCommand("C1")
	if tl := e.Tail(); tl == nil || tl.ItemID != "item-2" {
		t.Fatalf("tail after growth = %+v, want item-2", tl)
	}

	cs := e.SubmitUserInteraction("/debug 输出", anchor)
	if cs == nil || len(cs.Changes) != 1 {
		t.Fatalf("changes = %+v, want 1 个变更", cs)
	}
	ch := cs.Changes[0]
	if ch.AfterID != "item-1" {
		t.Fatalf("AfterID = %q, want item-1（锚定插入）", ch.AfterID)
	}
	m := e.Snapshot()
	if len(m.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(m.Items))
	}
	// 渲染顺序：U1, /debug 输出, C1（锚定在 U1 之后，而非模型末尾）。
	if m.Items[0].ID != "item-1" || m.Items[1].Kind != KindUserInteraction ||
		m.Items[1].Head != "/debug 输出" || m.Items[2].ID != "item-2" {
		t.Fatalf("order = [%s %s %s], want [item-1 interaction item-2]",
			m.Items[0].ID, m.Items[1].ID, m.Items[2].ID)
	}
	if m.Items[1].Status != StatusCompleted {
		t.Fatalf("interaction status = %v, want completed", m.Items[1].Status)
	}
	// 交互输出不推进 Tail：Tail 仍指向 C1（正常块尾部）。
	if tl := e.Tail(); tl == nil || tl.ItemID != "item-2" {
		t.Fatalf("tail after interaction = %+v, want item-2（交互不推进 Tail）", tl)
	}
}

// TestSubmitUserInteractionAnchorMiss 固化锚点缺失/为空的退化语义（§4.1
// 幂等哲学：目标缺失退化为 append 到模型末尾，不丢信息）。
func TestSubmitUserInteractionAnchorMiss(t *testing.T) {
	e := NewEventEncoder()
	e.SubmitUserInput("U1")

	// 锚点指向不存在的 Item：退化 append。
	cs := e.SubmitUserInteraction("x", &Tail{ItemID: "item-999", Seq: 42})
	if cs == nil || len(cs.Changes) != 1 || cs.Changes[0].AfterID != "" {
		t.Fatalf("miss-anchor changes = %+v, want 1 个退化 append（AfterID 空）", cs)
	}
	// 锚点为空：退化 append。
	cs2 := e.SubmitUserInteraction("y", nil)
	if cs2 == nil || len(cs2.Changes) != 1 || cs2.Changes[0].AfterID != "" {
		t.Fatalf("nil-anchor changes = %+v, want 1 个退化 append", cs2)
	}
	m := e.Snapshot()
	if len(m.Items) != 3 || m.Items[1].Head != "x" || m.Items[2].Head != "y" {
		t.Fatalf("items = %+v, want [U1 x y]", m.Items)
	}
}

// TestSubmitCommandErrorNilReceiver 固化 nil 安全：nil 编码器上调用三个
// Submit API 返回 nil，不 panic（与 Encode 一致）。
func TestSubmitCommandErrorNilReceiver(t *testing.T) {
	var e *EventEncoder
	if cs := e.SubmitUserInput("u"); cs != nil {
		t.Fatalf("nil SubmitUserInput: cs=%+v", cs)
	}
	if cs := e.SubmitCommand("c"); cs != nil {
		t.Fatalf("nil SubmitCommand: cs=%+v", cs)
	}
	if cs := e.SubmitError("e"); cs != nil {
		t.Fatalf("nil SubmitError: cs=%+v", cs)
	}
	if cs := e.SubmitUserInteraction("i", &Tail{ItemID: "x"}); cs != nil {
		t.Fatalf("nil SubmitUserInteraction: cs=%+v", cs)
	}
}
