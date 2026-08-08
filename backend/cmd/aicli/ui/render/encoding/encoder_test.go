package encoding

import (
	"fmt"
	"strings"
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
// 两个独立 Item：reasoning 内容绝不覆盖 assistant 块；成功 request finished
// 只关闭 reasoning，assistant 等待后到的权威 final。
func TestEncodeReasoningIndependentOfAssistant(t *testing.T) {
	e := NewEventEncoder()
	if cs := e.Encode(llmStarted()); len(cs.Changes) != 0 {
		t.Fatalf("llm_started changes = %+v, want no visible placeholder", cs.Changes)
	}
	e.Encode(event(runtimechat.EventAssistantReasoning, map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1", "text": "thinking...",
	}))
	cs := e.Encode(assistantDelta("Hello", 1))
	if len(cs.Changes) != 3 || cs.Changes[0].Item.Kind != KindReasoning ||
		cs.Changes[0].Item.Status != StatusCompleted || cs.Changes[1].Op != OpAppend ||
		cs.Changes[1].Item.Kind != KindAssistant || cs.Changes[2].Op != OpUpsert ||
		cs.Changes[2].Item.Kind != KindAssistant {
		t.Fatalf("reasoning -> assistant boundary changes = %+v", cs.Changes)
	}
	m := e.Snapshot()
	if len(m.Items) != 2 {
		t.Fatalf("items = %d, want 2 (reasoning + assistant)", len(m.Items))
	}
	if m.Items[0].Kind != KindReasoning || !strings.Contains(m.Items[0].Head, "thinking...") || !strings.Contains(m.Items[0].Head, " reasoning ") || !strings.Contains(m.Items[0].Head, " end reasoning ") || m.Items[0].Status != StatusCompleted {
		t.Fatalf("items[0] = %+v, want completed reasoning with dividers", m.Items[0])
	}
	if m.Items[1].Kind != KindAssistant || m.Items[1].Head != "Hello" {
		t.Fatalf("items[1] = %+v, want assistant with Hello", m.Items[1])
	}
	if !strings.HasPrefix(m.Items[0].Head, "─") || !strings.Contains(m.Items[0].Head, " reasoning ") {
		t.Fatalf("items[0].Head = %q, want reasoning divider prefix", m.Items[0].Head)
	}
	// request finished 先于 assistant_message：不得提前提交 assistant。
	if cs := e.Encode(llmFinished()); len(cs.Changes) != 0 {
		t.Fatalf("successful llm_finished changes = %+v, want no assistant finalization", cs.Changes)
	}
	m2 := e.Snapshot()
	if m2.Items[0].Status != StatusCompleted {
		t.Fatalf("reasoning status = %s, want completed", m2.Items[0].Status)
	}
	if m2.Items[1].Status != StatusRunning {
		t.Fatalf("assistant status = %s, want running until final", m2.Items[1].Status)
	}
	e.Encode(assistantFinal("Hello"))
	if got := e.Snapshot().Items[1].Status; got != StatusCompleted {
		t.Fatalf("assistant status after final = %s, want completed", got)
	}
}

// The local ReAct loop emits the dotted legacy event name with a nested
// ReasoningBlock. Keep that production shape on the reasoning route: falling
// back to opSystem renders the literal event type and loses the thought body.
func TestEncodeDottedAssistantReasoningUsesNestedTypedPayload(t *testing.T) {
	e := NewEventEncoder()
	first := event("assistant.reasoning", map[string]interface{}{
		"trace_id": "trace-reasoning",
		"reasoning": map[string]interface{}{
			"format":  "stream_delta",
			"summary": "first thought. ",
		},
	})
	second := event("assistant.reasoning", map[string]interface{}{
		"trace_id": "trace-reasoning",
		"reasoning": map[string]interface{}{
			"format":  "stream_delta",
			"summary": "second thought.",
		},
	})
	e.Encode(first)
	e.Encode(second)

	model := e.Snapshot()
	if len(model.Items) != 1 {
		t.Fatalf("items = %d, want one reasoning item: %#v", len(model.Items), model.Items)
	}
	item := model.Items[0]
	if item.Kind != KindReasoning || !strings.HasSuffix(item.Head, "first thought. second thought.") || !strings.Contains(item.Head, " reasoning ") {
		t.Fatalf("reasoning item = %+v", item)
	}
	if item.Status != StatusRunning {
		t.Fatalf("reasoning status = %s, want running", item.Status)
	}

	e.Encode(event(runtimechat.EventSessionEnd, nil))
	if got := e.Snapshot().Items[0].Status; got != StatusCompleted {
		t.Fatalf("reasoning status after session end = %s, want completed", got)
	}
}

func TestEncodeSessionEndFinalizesOpenToolCell(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(toolStarted("call-orphan", "shell"))
	before := e.Snapshot()
	if len(before.Items) != 1 || before.Items[0].Kind != KindToolCall || before.Items[0].Status.Terminal() {
		t.Fatalf("open tool fixture = %+v", before.Items)
	}

	cs := e.Encode(event(runtimechat.EventSessionEnd, map[string]interface{}{"success": true}))
	if len(cs.Changes) != 1 || cs.Changes[0].Op != OpUpsert ||
		cs.Changes[0].Item.ID != before.Items[0].ID || cs.Changes[0].Item.Status != StatusCompleted {
		t.Fatalf("session-end tool finalization = %+v", cs.Changes)
	}
	after := e.Snapshot()
	if len(after.Items) != 1 || after.Items[0].Status != StatusCompleted {
		t.Fatalf("tool remained mutable after session end: %+v", after.Items)
	}
	if duplicate := e.Encode(event(runtimechat.EventSessionEnd, map[string]interface{}{"success": true})); len(duplicate.Changes) != 0 {
		t.Fatalf("repeated session end re-finalized tool: %+v", duplicate.Changes)
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
	if got := e.Snapshot().Items[0].Head; got != "• Running shell\n50% complete" {
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

// TestEncodeToolCallDisplayHeadRestoresLegacyDetails 验证工具调用前/调用后
// 渲染细节恢复（对齐旧 compactToolDisplayTextWithSource /
// compactToolCompletionTitle）：
//   - 调用前：tool cell head 显示命令文本（shell）或参数预览（其他工具），
//     带 [meta]/[mcp] 来源前缀，无细节时退化为工具名；
//   - progress 保留 started 建立的 display，不被 payload tool_name 重置；
//   - 调用后：head 更新为 "• Completed/Failed <display>[ via <backend>]
//     [ in <duration>]"，首行替换标题、保留 progress 细节行。
func TestEncodeToolCallDisplayHeadRestoresLegacyDetails(t *testing.T) {
	cases := []struct {
		name  string
		event runtimeevents.Event
		want  string
	}{
		{
			name:  "shell 命令文本",
			event: event("tool.requested", map[string]interface{}{"tool_call_id": "c1", "tool_name": "shell", "command_text": "echo hello"}),
			want:  "• Running echo hello",
		},
		{
			name:  "shell 命令预览回退",
			event: event("tool.requested", map[string]interface{}{"tool_call_id": "c2", "tool_name": "bash", "arg_preview": "command=ls -la"}),
			want:  "• Running ls -la",
		},
		{
			name:  "非 shell 参数预览",
			event: event("tool.requested", map[string]interface{}{"tool_call_id": "c3", "tool_name": "read_file", "arg_preview": "path=a.go"}),
			want:  "• Running read_file path=a.go",
		},
		{
			name:  "来源前缀",
			event: event("tool.requested", map[string]interface{}{"tool_call_id": "c4", "tool_name": "bash", "command_text": "go test ./...", "tool_source": "meta"}),
			want:  "• Running [meta] go test ./...",
		},
		{
			name:  "无细节回退工具名",
			event: event("tool.requested", map[string]interface{}{"tool_call_id": "c5", "tool_name": "shell"}),
			want:  "• Running shell",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEventEncoder()
			e.Encode(tc.event)
			got := e.Snapshot().Items[0].Head
			if got != tc.want {
				t.Fatalf("started head = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("progress 保留调用前 display", func(t *testing.T) {
		e := NewEventEncoder()
		e.Encode(event("tool.requested", map[string]interface{}{"tool_call_id": "p1", "tool_name": "shell", "command_text": "echo hello"}))
		e.Encode(event("tool.progress", map[string]interface{}{"tool_call_id": "p1", "message": "50% complete"}))
		if got := e.Snapshot().Items[0].Head; got != "• Running echo hello\n50% complete" {
			t.Fatalf("progress head = %q, want running 前缀保留", got)
		}
	})

	t.Run("completed 标题", func(t *testing.T) {
		e := NewEventEncoder()
		e.Encode(event("tool.requested", map[string]interface{}{"tool_call_id": "f1", "tool_name": "shell", "command_text": "echo hello"}))
		e.Encode(event("tool.completed", map[string]interface{}{"tool_call_id": "f1", "logical_tool": "shell", "output": "hello", "execution_backend": "pwsh"}))
		if got := e.Snapshot().Items[0].Head; got != "• Completed echo hello via pwsh" {
			t.Fatalf("completed head = %q", got)
		}
	})

	t.Run("failed 标题", func(t *testing.T) {
		e := NewEventEncoder()
		e.Encode(event("tool.requested", map[string]interface{}{"tool_call_id": "f2", "tool_name": "read_file", "arg_preview": "path=a.go"}))
		e.Encode(event("tool.failed", map[string]interface{}{"tool_call_id": "f2", "logical_tool": "read_file", "error": "not found"}))
		if got := e.Snapshot().Items[0].Head; got != "• Failed read_file path=a.go" {
			t.Fatalf("failed head = %q", got)
		}
	})

	t.Run("duration 后缀", func(t *testing.T) {
		e := NewEventEncoder()
		e.Encode(event("tool.requested", map[string]interface{}{"tool_call_id": "f3", "tool_name": "view"}))
		e.Encode(event("tool.completed", map[string]interface{}{"tool_call_id": "f3", "logical_tool": "view", "duration_ms": uint64(5)}))
		if got := e.Snapshot().Items[0].Head; got != "• Completed view in 5ms" {
			t.Fatalf("duration head = %q", got)
		}
	})

	t.Run("completed 保留 progress 细节行", func(t *testing.T) {
		e := NewEventEncoder()
		e.Encode(event("tool.requested", map[string]interface{}{"tool_call_id": "f4", "tool_name": "shell"}))
		e.Encode(event("tool.progress", map[string]interface{}{"tool_call_id": "f4", "message": "50% complete"}))
		e.Encode(event("tool.completed", map[string]interface{}{"tool_call_id": "f4", "logical_tool": "shell", "output": "ok"}))
		if got := e.Snapshot().Items[0].Head; got != "• Completed shell\n50% complete" {
			t.Fatalf("completed head = %q, want 标题 + progress 行", got)
		}
	})
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
	if len(cs.Changes) != 0 || len(e.Snapshot().Items) != 0 {
		t.Fatalf("llm_started changes = %+v model=%+v, want no visible item", cs.Changes, e.Snapshot().Items)
	}
	cs = e.Encode(assistantDelta("a", 1))
	if len(cs.Changes) != 2 || cs.Changes[0].Op != OpAppend || cs.Changes[0].Revision != 1 ||
		cs.Changes[1].Op != OpUpsert || cs.Changes[1].Revision != 2 {
		t.Fatalf("delta changes = %+v, want append rev=1 then upsert rev=2", cs.Changes)
	}
}

func TestEncodeProductionDottedLifecyclePreservesReasoningBeforeAssistant(t *testing.T) {
	e := NewEventEncoder()
	traceID := "trace-production-order"
	turnID := "turn-production-order"
	streamID := "stream-production-order"
	e.Encode(runtimeevents.Event{Type: "llm.request.started", TraceID: traceID, Payload: map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": 1,
	}})
	e.Encode(runtimeevents.Event{Type: "assistant.reasoning", TraceID: traceID, Payload: map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "step": 1,
		"reasoning": map[string]interface{}{"format": "stream_delta", "summary": "reasoning first"},
	}})
	cs := e.Encode(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, TraceID: traceID, Payload: map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": streamID,
		"step": 1, "sequence": uint64(1), "delta": "assistant second",
	}})
	if len(cs.Changes) != 3 || cs.Changes[0].Item.Kind != KindReasoning ||
		cs.Changes[0].Item.Status != StatusCompleted || cs.Changes[1].Op != OpAppend ||
		cs.Changes[1].Item.Kind != KindAssistant || cs.Changes[2].Op != OpUpsert ||
		cs.Changes[2].Item.Kind != KindAssistant {
		t.Fatalf("first assistant delta changes = %+v", cs.Changes)
	}
	if cs := e.Encode(runtimeevents.Event{Type: "llm.request.finished", TraceID: traceID, Payload: map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": 1, "success": true,
	}}); len(cs.Changes) != 0 {
		t.Fatalf("successful dotted finished changes = %+v, want lifecycle-only update", cs.Changes)
	}
	if cs := e.Encode(runtimeevents.Event{Type: runtimechat.EventAssistantMessage, TraceID: traceID, Payload: map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": streamID,
		"content": "assistant second",
	}}); len(cs.Changes) != 1 || cs.Changes[0].Op != OpUpsert ||
		cs.Changes[0].Item.Kind != KindAssistant || cs.Changes[0].Item.Status != StatusCompleted {
		t.Fatalf("authoritative final changes = %+v", cs.Changes)
	}

	model := e.Snapshot()
	if len(model.Items) != 2 || model.Items[0].Kind != KindReasoning || !strings.Contains(model.Items[0].Head, "reasoning first") ||
		model.Items[1].Kind != KindAssistant || model.Items[1].Head != "assistant second" {
		t.Fatalf("production item order = %+v", model.Items)
	}
	for _, item := range model.Items {
		if item.Head == "llm.request.finished" || item.Head == "assistant.reasoning" {
			t.Fatalf("raw lifecycle label leaked into model: %+v", model.Items)
		}
	}
}

func TestEncodeLateReasoningAfterSuccessfulBoundaryInsertsBeforeMutableAssistant(t *testing.T) {
	e := NewEventEncoder()
	identity := map[string]interface{}{
		"turn_id": "late-turn", "stream_id": "late-stream", "step": 1,
	}
	e.Encode(event("llm.request.started", identity))
	e.Encode(event(runtimechat.EventAssistantDelta, map[string]interface{}{
		"turn_id": "late-turn", "stream_id": "late-stream", "step": 1,
		"sequence": uint64(1), "delta": "assistant final",
	}))
	e.Encode(event("llm.request.finished", map[string]interface{}{
		"turn_id": "late-turn", "stream_id": "late-stream", "step": 1, "success": true,
	}))
	cs := e.Encode(event("assistant.reasoning", map[string]interface{}{
		"turn_id": "late-turn", "stream_id": "late-stream", "step": 1,
		"reasoning": map[string]interface{}{"format": "summary", "summary": "late reasoning"},
	}))
	if len(cs.Changes) != 1 || cs.Changes[0].Op != OpAppend || cs.Changes[0].Item.Kind != KindReasoning {
		t.Fatalf("late reasoning changes = %+v", cs.Changes)
	}
	if cs.Changes[0].BeforeID != "item-1" || cs.Changes[0].AfterID != "" {
		t.Fatalf("late reasoning anchor = before %q after %q, want before item-1", cs.Changes[0].BeforeID, cs.Changes[0].AfterID)
	}
	model := e.Snapshot()
	if len(model.Items) != 2 || model.Items[0].Kind != KindReasoning || model.Items[1].Kind != KindAssistant {
		t.Fatalf("late reasoning model order = %+v", model.Items)
	}
}

func TestEncodeRequestAliasesKeepReActStepsDistinct(t *testing.T) {
	e := NewEventEncoder()
	for step := 1; step <= 2; step++ {
		streamID := fmt.Sprintf("stream-step-%d", step)
		body := fmt.Sprintf("answer-step-%d", step)
		e.Encode(event("llm.request.started", map[string]interface{}{
			"turn_id": "shared-turn", "stream_id": streamID, "step": step,
		}))
		e.Encode(event(runtimechat.EventAssistantDelta, map[string]interface{}{
			"turn_id": "shared-turn", "stream_id": streamID, "step": step,
			"sequence": uint64(1), "delta": body,
		}))
		e.Encode(event("llm.request.finished", map[string]interface{}{
			"turn_id": "shared-turn", "stream_id": streamID, "step": step, "success": true,
		}))
		e.Encode(event(runtimechat.EventAssistantMessage, map[string]interface{}{
			"turn_id": "shared-turn", "stream_id": streamID, "content": body,
		}))
	}
	model := e.Snapshot()
	if len(model.Items) != 2 || model.Items[0].Head != "answer-step-1" ||
		model.Items[1].Head != "answer-step-2" || model.Items[0].ID == model.Items[1].ID {
		t.Fatalf("multi-step request aliases merged or duplicated cells: %+v", model.Items)
	}
	for _, item := range model.Items {
		if item.Kind != KindAssistant || item.Status != StatusCompleted {
			t.Fatalf("multi-step assistant not committed independently: %+v", item)
		}
	}
}

func TestEncodeReActToolBoundaryFinalizesIntermediateAssistant(t *testing.T) {
	e := NewEventEncoder()
	traceID := "react-tool-boundary"
	turnID := "react-turn"
	stream1 := "react-stream-1"
	stream2 := "react-stream-2"

	e.Encode(event("llm.request.started", map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": stream1, "step": 1,
	}))
	e.Encode(event(runtimechat.EventAssistantDelta, map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": stream1,
		"step": 1, "sequence": uint64(1), "delta": "intermediate tool plan",
	}))
	e.Encode(event("llm.request.finished", map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": stream1, "step": 1, "success": true,
	}))

	start := event("tool.requested", map[string]interface{}{
		"trace_id": traceID, "step": 1, "tool_call_id": "react-call-1", "tool_name": "read_file",
	})
	cs := e.Encode(start)
	if len(cs.Changes) != 2 || cs.Changes[0].Op != OpUpsert ||
		cs.Changes[0].Item.Kind != KindAssistant || cs.Changes[0].Item.Status != StatusCompleted ||
		cs.Changes[1].Op != OpAppend || cs.Changes[1].Item.Kind != KindToolCall {
		t.Fatalf("tool boundary changes = %+v, want assistant finalize then tool append", cs.Changes)
	}
	model := e.Snapshot()
	if len(model.Items) != 2 || model.Items[0].Status != StatusCompleted ||
		model.Items[1].Kind != KindToolCall || model.Items[1].Status.Terminal() {
		t.Fatalf("intermediate request remained mutable across tool boundary: %+v", model.Items)
	}

	e.Encode(event("tool.completed", map[string]interface{}{
		"tool_call_id": "react-call-1", "output": "file content",
	}))
	e.Encode(event("llm.request.started", map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": stream2, "step": 2,
	}))
	e.Encode(event(runtimechat.EventAssistantDelta, map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": stream2,
		"step": 2, "sequence": uint64(1), "delta": "final answer",
	}))
	e.Encode(event("llm.request.finished", map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": stream2, "step": 2, "success": true,
	}))
	e.Encode(event(runtimechat.EventAssistantMessage, map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": stream2, "content": "final answer",
	}))

	model = e.Snapshot()
	if len(model.Items) != 4 {
		t.Fatalf("ReAct model items = %d, want assistant + tool call + output + assistant: %+v", len(model.Items), model.Items)
	}
	if model.Items[0].Kind != KindAssistant || model.Items[0].Status != StatusCompleted ||
		model.Items[1].Kind != KindToolCall || model.Items[1].Status != StatusCompleted ||
		model.Items[2].Kind != KindToolOutput || model.Items[2].Status != StatusCompleted ||
		model.Items[3].Kind != KindAssistant || model.Items[3].Status != StatusCompleted ||
		model.Items[3].Head != "final answer" {
		t.Fatalf("ReAct model did not converge after tool boundary: %+v", model.Items)
	}
}

func TestEncodeFailedDottedRequestPreservesPartialAndReadableError(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(event("llm.request.started", map[string]interface{}{
		"turn_id": "failed-turn", "stream_id": "failed-stream", "step": 1,
	}))
	e.Encode(event(runtimechat.EventAssistantDelta, map[string]interface{}{
		"turn_id": "failed-turn", "stream_id": "failed-stream", "step": 1,
		"sequence": uint64(1), "delta": "partial answer",
	}))
	finished := event("llm.request.finished", map[string]interface{}{
		"turn_id": "failed-turn", "stream_id": "failed-stream", "step": 1,
		"success": false, "error": "provider unavailable", "error_code": "503", "retryable": true,
	})
	cs := e.Encode(finished)
	if len(cs.Changes) != 2 || cs.Changes[0].Op != OpUpsert ||
		cs.Changes[0].Item.Kind != KindAssistant || cs.Changes[0].Item.Status != StatusFailed ||
		cs.Changes[1].Op != OpAppend || cs.Changes[1].Item.Kind != KindSystem {
		t.Fatalf("failed request changes = %+v", cs.Changes)
	}
	model := e.Snapshot()
	if len(model.Items) != 2 || model.Items[0].Head != "partial answer" ||
		model.Items[0].Status != StatusFailed || !strings.Contains(model.Items[1].Head, "provider unavailable") ||
		strings.Contains(model.Items[1].Head, "llm.request.finished") {
		t.Fatalf("failed request model = %+v", model.Items)
	}
	if duplicate := e.Encode(finished); len(duplicate.Changes) != 0 {
		t.Fatalf("duplicate failed lifecycle appended output: %+v", duplicate.Changes)
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
// 遥测类型（planning.started、subagent.started、tool.reduced 等）不在此
// 表：它们走 isSilentSystemEventType 静默分类（零可见输出，见
// TestEncodeSilentSystemEventTypes）。LLM request dotted lifecycle 与 typed
// lifecycle 共用流状态操作，由生产顺序测试单独覆盖。
func knownLegacyEventTypes() []string {
	return []string{
		"context.preflight.started",
		"context.preflight.compacted",
		"context.preflight.failed",
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
		"planning.started",
		"subagent.batch.started",
		"subagent.started",
		"task.started",
		"team.task.started",
		"context.tool_schema.frozen",
		"tool.reduced",
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
			if typ == "llm.retry" {
				// 重试是过程状态：重试信息由 bridge 渲染在动态数据状态区域，
				// encoder 静默（不产生 transcript/system cell）。
				if len(cs.Changes) != 0 {
					t.Fatalf("%s: 重试应静默，got %d changes", typ, len(cs.Changes))
				}
				if u := e.Stats().UnknownCount; u != 0 {
					t.Fatalf("%s: UnknownCount = %d, want 0（已知 legacy 类型不应计未知）", typ, u)
				}
				return
			}
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

func TestSubmitAssistant(t *testing.T) {
	e := NewEventEncoder()
	cs := e.SubmitAssistant("legacy final response")
	if cs == nil || len(cs.Changes) != 1 {
		t.Fatalf("changes = %+v, want 1 append", cs)
	}
	it := e.Snapshot().Items[0]
	if it.Kind != KindAssistant || it.Status != StatusCompleted || it.Head != "legacy final response" {
		t.Fatalf("item = %+v, want completed assistant", it)
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

// TestSubmitSupplement 固化无 runtime 事件的本地补充契约：它必须保留
// supplement 语义，不能借用 error/system API，并且作为一次性终态推进 Tail。
func TestSubmitSupplement(t *testing.T) {
	e := NewEventEncoder()
	e.SubmitUserInput("U1")
	cs := e.SubmitSupplement("[retry] retrying request")
	if cs == nil || len(cs.Changes) != 1 {
		t.Fatalf("changes = %+v, want 1 append", cs)
	}
	it := e.Snapshot().Items[len(e.Snapshot().Items)-1]
	if it.Kind != KindSupplement || it.Status != StatusCompleted {
		t.Fatalf("item = %+v, want KindSupplement completed", it)
	}
	if it.Head != "[retry] retrying request" {
		t.Fatalf("head = %q", it.Head)
	}
	if tl := e.Tail(); tl == nil || tl.ItemID != it.ID {
		t.Fatalf("tail = %+v, want ItemID %q", tl, it.ID)
	}
	if st := e.Stats(); st.EncodeCount != 2 {
		t.Fatalf("stats = %+v, want EncodeCount=2", st)
	}
}

func TestPriorityPromptTranscriptLifecycleOrdering(t *testing.T) {
	requested := event(runtimechat.EventApprovalRequested, map[string]interface{}{
		"request_id": "approval-1",
		"tool_name":  "execute_shell_command",
	})
	resolved := event(runtimechat.EventApprovalResolved, map[string]interface{}{
		"request_id": "approval-1",
		"allowed":    true,
	})
	transcript := "[审批] 工具：execute_shell_command\n[审批] 请选择： 1"

	tests := []struct {
		name string
		run  func(*EventEncoder)
	}{
		{
			name: "request resolved transcript",
			run: func(e *EventEncoder) {
				e.Encode(requested)
				e.Encode(resolved)
				mustPriorityTranscriptChange(t, e.SubmitPriorityPromptTranscript(
					runtimechat.EventApprovalRequested, "approval-1", transcript), transcript)
			},
		},
		{
			name: "resolved request transcript",
			run: func(e *EventEncoder) {
				if cs := e.Encode(resolved); len(cs.Changes) != 0 {
					t.Fatalf("early resolved changes = %+v, want none", cs.Changes)
				}
				e.Encode(requested)
				mustPriorityTranscriptChange(t, e.SubmitPriorityPromptTranscript(
					runtimechat.EventApprovalRequested, "approval-1", transcript), transcript)
			},
		},
		{
			name: "request transcript resolved",
			run: func(e *EventEncoder) {
				e.Encode(requested)
				mustPriorityTranscriptChange(t, e.SubmitPriorityPromptTranscript(
					runtimechat.EventApprovalRequested, "approval-1", transcript), transcript)
				if cs := e.Encode(resolved); len(cs.Changes) != 0 {
					t.Fatalf("resolved changes = %+v, want none", cs.Changes)
				}
			},
		},
		{
			name: "duplicate request before transcript",
			run: func(e *EventEncoder) {
				e.Encode(requested)
				if cs := e.Encode(requested); len(cs.Changes) != 0 {
					t.Fatalf("duplicate request changes = %+v, want none", cs.Changes)
				}
				mustPriorityTranscriptChange(t, e.SubmitPriorityPromptTranscript(
					runtimechat.EventApprovalRequested, "approval-1", transcript), transcript)
			},
		},
		{
			name: "duplicate resolved after transcript",
			run: func(e *EventEncoder) {
				e.Encode(requested)
				mustPriorityTranscriptChange(t, e.SubmitPriorityPromptTranscript(
					runtimechat.EventApprovalRequested, "approval-1", transcript), transcript)
				e.Encode(resolved)
				if cs := e.Encode(resolved); len(cs.Changes) != 0 {
					t.Fatalf("duplicate resolved changes = %+v, want none", cs.Changes)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := NewEventEncoder()
			test.run(e)
			model := e.Snapshot()
			if len(model.Items) != 1 || model.Items[0].Kind != KindPriorityPrompt ||
				model.Items[0].Status != StatusCompleted || model.Items[0].Head != transcript {
				t.Fatalf("model = %+v, want one completed priority transcript", model.Items)
			}
			if duplicate := e.SubmitPriorityPromptTranscript(
				runtimechat.EventApprovalRequested, "approval-1", transcript); duplicate != nil {
				t.Fatalf("duplicate transcript = %+v, want nil", duplicate)
			}
		})
	}
}

func mustPriorityTranscriptChange(t *testing.T, cs *ChangeSet, transcript string) {
	t.Helper()
	if cs == nil || len(cs.Changes) != 1 || cs.Changes[0].Op != OpAppend ||
		cs.Changes[0].Item.Kind != KindPriorityPrompt ||
		cs.Changes[0].Item.Status != StatusCompleted || cs.Changes[0].Item.Head != transcript {
		t.Fatalf("transcript changes = %+v, want one completed append", cs)
	}
}

func TestPriorityRequestAndResolutionDoNotCreatePlaceholderItem(t *testing.T) {
	e := NewEventEncoder()
	for _, ev := range []runtimeevents.Event{
		event(runtimechat.EventQuestionAsked, map[string]interface{}{
			"question_id": "question-1", "prompt": "choose a model",
		}),
		event(runtimechat.EventQuestionAnswered, map[string]interface{}{
			"question_id": "question-1", "answer": "gpt-test",
		}),
	} {
		if cs := e.Encode(ev); len(cs.Changes) != 0 {
			t.Fatalf("priority lifecycle event created visible changes: %+v", cs.Changes)
		}
	}
	if got := e.Snapshot(); len(got.Items) != 0 {
		t.Fatalf("priority lifecycle created placeholder items: %+v", got.Items)
	}
}

func TestSubmitToolLifecycleUsesOneMutableChain(t *testing.T) {
	e := NewEventEncoder()
	if e.SubmitToolCall("call-direct", "read_file", map[string]interface{}{"path": "a.go"}) == nil {
		t.Fatal("SubmitToolCall returned nil")
	}
	if e.SubmitToolProgress("call-direct", "read_file", "reading") == nil {
		t.Fatal("SubmitToolProgress returned nil")
	}
	if e.SubmitToolResult("call-direct", "read_file", "file content", "", true) == nil {
		t.Fatal("SubmitToolResult returned nil")
	}
	// A duplicate final is idempotent and must not append a second chain/output.
	e.SubmitToolResult("call-direct", "read_file", "file content", "", true)
	m := e.Snapshot()
	if len(m.Items) != 2 {
		t.Fatalf("items=%d want one call plus one output, model=%+v", len(m.Items), m.Items)
	}
	if m.Items[0].Kind != KindToolCall || m.Items[0].Status != StatusCompleted {
		t.Fatalf("call item=%+v want completed tool_call", m.Items[0])
	}
	if m.Items[1].Kind != KindToolOutput || m.Items[1].CauseID != m.Items[0].ID || m.Items[1].Head != "file content" {
		t.Fatalf("output item=%+v want output caused by call", m.Items[1])
	}

	direct := NewEventEncoder()
	direct.SubmitToolCall("call-display", "read_file", map[string]interface{}{"path": "a.go"})
	direct.SubmitToolResultDisplay("call-display", "• Completed read_file path=a.go")
	directModel := direct.Snapshot()
	if len(directModel.Items) != 1 {
		t.Fatalf("display result items=%d want one finalized chain", len(directModel.Items))
	}
	if item := directModel.Items[0]; item.Kind != KindToolCall || item.Status != StatusCompleted || item.Head != "• Completed read_file path=a.go" {
		t.Fatalf("display result item=%+v want completed display-head chain", item)
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
