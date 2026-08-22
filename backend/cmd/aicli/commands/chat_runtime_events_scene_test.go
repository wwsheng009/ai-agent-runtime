package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// testRuntimeSceneEvents 返回一组覆盖 system / assistant 流式 / 工具链的
// 事件序列（与 encoding 包测试同款 payload 语义），用于驱动 bridge 的
// ChangeSet → Scene 消费端。
func testRuntimeSceneEvents() []runtimeevents.Event {
	return []runtimeevents.Event{
		// 用 legacy 可见的 system 事件做 system cell 占位：session_start /
		// compact_skipped 等内部生命周期事件已静默（编码器零 cell），
		// 不再适合作为 Scene system 块样例。
		{Type: runtimechat.EventSessionCompactCompleted, SessionID: "session-1", TraceID: "trace-0",
			Payload: map[string]interface{}{"message": "session compacted"}},
		{Type: runtimechat.EventLLMRequestStarted, SessionID: "session-1", TraceID: "trace-1",
			Payload: map[string]interface{}{"turn_id": "turn-1", "stream_id": "stream-1"}},
		{Type: runtimechat.EventAssistantDelta, SessionID: "session-1", TraceID: "trace-1",
			Payload: map[string]interface{}{"turn_id": "turn-1", "stream_id": "stream-1", "delta": "你", "sequence": uint64(1)}},
		{Type: runtimechat.EventAssistantDelta, SessionID: "session-1", TraceID: "trace-1",
			Payload: map[string]interface{}{"turn_id": "turn-1", "stream_id": "stream-1", "delta": "好", "sequence": uint64(2)}},
		{Type: runtimechat.EventAssistantMessage, SessionID: "session-1", TraceID: "trace-1",
			Payload: map[string]interface{}{"turn_id": "turn-1", "stream_id": "stream-1", "content": "你好"}},
		{Type: runtimechat.EventLLMRequestFinished, SessionID: "session-1", TraceID: "trace-1",
			Payload: map[string]interface{}{"turn_id": "turn-1", "stream_id": "stream-1"}},
		{Type: runtimechat.EventToolStarted, SessionID: "session-1", TraceID: "trace-2",
			Payload: map[string]interface{}{"tool_call_id": "call-1", "tool_name": "read_file"}},
		{Type: runtimechat.EventToolFinished, SessionID: "session-1", TraceID: "trace-2",
			Payload: map[string]interface{}{"tool_call_id": "call-1", "output": "file content"}},
	}
}

// bridgeSceneCells 返回 bridge 当前 Scene 的 cell 副本（测试只读断言）。
func bridgeSceneCells(t *testing.T, b *chatRuntimeEventBridge) []*scene.TranscriptCell {
	t.Helper()
	if b == nil {
		t.Fatal("nil bridge")
	}
	b.sceneMu.RLock()
	defer b.sceneMu.RUnlock()
	if b.renderScene == nil {
		return nil
	}
	return b.renderScene.Cells()
}

// assertBridgeSceneStats 断言 Scene cell 数与失败计数（revision 是事务
// 计数，随事件序列变化，不在此断言具体数值）。
func assertBridgeSceneStats(t *testing.T, b *chatRuntimeEventBridge, wantCells, wantFailures uint64) {
	t.Helper()
	cells, revision, failures, lastErr := b.sceneStats()
	if cells != wantCells {
		t.Fatalf("scene cells=%d want %d", cells, wantCells)
	}
	if failures != wantFailures {
		t.Fatalf("scene failures=%d want %d (lastErr=%q)", failures, wantFailures, lastErr)
	}
	if wantCells > 0 && revision == 0 {
		t.Fatal("scene revision=0 with non-empty cells, want > 0")
	}
	if wantCells == 0 && wantFailures == 0 && revision != 0 {
		t.Fatalf("scene revision=%d want 0 for empty scene", revision)
	}
}

// TestChatRuntimeEventBridge_SceneFollowsModelOrder 验证 P3 消费端核心链路：
// 事件经 Encode → ChangeSet → ChangeSetMapper.Apply 进入 Scene，渲染顺序 =
// 模型数组顺序（Codex Thread 模型），身份 = Item.ID（"item-{n}" → CellID n）。
func TestChatRuntimeEventBridge_SceneFollowsModelOrder(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	for _, ev := range testRuntimeSceneEvents() {
		bridge.encodeRenderModelEvent(ev)
	}
	assertBridgeSceneStats(t, bridge, 3, 0)

	cells := bridgeSceneCells(t, bridge)
	if len(cells) != 3 {
		t.Fatalf("cells=%d want 3", len(cells))
	}
	// system（session compact completed，item-1）
	if cells[0].ID != 1 || cells[0].Kind != scene.KindSystem || cells[0].Source != "session compacted" {
		t.Fatalf("cell[0]=%+v want system cell-1", cells[0])
	}
	if cells[0].Phase != scene.CellCommitted {
		t.Fatalf("cell[0].Phase=%v want committed", cells[0].Phase)
	}
	// assistant 流式合并 + 终态（item-2）
	if cells[1].ID != 2 || cells[1].Kind != scene.KindAssistant || cells[1].Source != "你好" {
		t.Fatalf("cell[1]=%+v want assistant cell-2 %q", cells[1], "你好")
	}
	if cells[1].Phase != scene.CellCommitted || cells[1].ChainKey != "" {
		t.Fatalf("cell[1].Phase=%v ChainKey=%q want committed/top-level", cells[1].Phase, cells[1].ChainKey)
	}
	// tool chain：tool_call 链首 + tool_output 合并（item-3，稠密 cell）
	if cells[2].ID != 3 || cells[2].Kind != scene.KindToolChain || cells[2].ChainKey != "item-3" {
		t.Fatalf("cell[2]=%+v want tool_chain cell-3 chain item-3", cells[2])
	}
	if cells[2].Source != "• Completed read_file\nfile content" {
		t.Fatalf("cell[2].Source=%q want %q", cells[2].Source, "• Completed read_file\nfile content")
	}
	// 顺序 = 模型数组顺序：模型为 [item-1, item-2, item-3, item-4]，Scene
	// 合并 tool_output 进链首后仍保持 [cell-1, cell-2, cell-3]。
	model := bridge.renderModelSnapshot()
	if model == nil || len(model.Items) != 4 {
		t.Fatalf("model items=%d want 4", len(model.Items))
	}
}

// TestChatRuntimeEventBridge_SceneReplayRebuildsScene 验证 P5 与 P3 衔接：
// 事件日志重放后 Scene 从空重建，与实时编码路径的 Scene 逐 cell 等价
// （身份/顺序/内容/revision 一致）。
func TestChatRuntimeEventBridge_SceneReplayRebuildsScene(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")

	bridge1 := newChatRuntimeEventBridge(&ChatSession{})
	bridge1.eventLogPathOverride = logPath
	for _, ev := range testRuntimeSceneEvents() {
		bridge1.encodeRenderModelEvent(ev)
	}
	before := bridge1.sceneSnapshot()
	if before == nil || len(before.Cells) != 3 {
		t.Fatalf("live scene cells=%d want 3", len(before.Cells))
	}

	bridge2 := newChatRuntimeEventBridge(&ChatSession{})
	bridge2.eventLogPathOverride = logPath
	replayed, err := bridge2.replayEventLog()
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed != 8 {
		t.Fatalf("replayed=%d want 8", replayed)
	}
	after := bridge2.sceneSnapshot()
	if after == nil {
		t.Fatal("nil replayed scene")
	}
	if after.Revision != before.Revision {
		t.Fatalf("scene revision=%d want %d", after.Revision, before.Revision)
	}
	if len(after.Cells) != len(before.Cells) {
		t.Fatalf("replayed cells=%d want %d", len(after.Cells), len(before.Cells))
	}
	for i := range before.Cells {
		w, g := before.Cells[i], after.Cells[i]
		if w.ID != g.ID || w.Kind != g.Kind || w.Source != g.Source ||
			w.Revision != g.Revision || w.Phase != g.Phase || w.ChainKey != g.ChainKey {
			t.Fatalf("cell %d mismatch:\n live: %+v\n replay: %+v", i, w, g)
		}
	}
	assertBridgeSceneStats(t, bridge2, 3, 0)
}

// TestChatRuntimeEventBridge_SceneApplyFailureCounted 验证失败不阻塞事件循环：
// 非法 ChangeSet（committed cell 的 remove，映射器显式报错）只计数并记录
// 最后错误，Scene 整体回滚不变（INV-FRAME-01）。
func TestChatRuntimeEventBridge_SceneApplyFailureCounted(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bad := &encoding.ChangeSet{Changes: []encoding.ItemChange{{
		Op:   encoding.OpRemove,
		Item: &encoding.Item{ID: "item-1", Kind: encoding.KindUser, Status: encoding.StatusCompleted, Head: "x"},
	}}}
	bridge.applyChangeSet(bad)
	assertBridgeSceneStats(t, bridge, 0, 1)
	if cells := bridgeSceneCells(t, bridge); len(cells) != 0 {
		t.Fatalf("cells=%d want 0 (atomic rollback)", len(cells))
	}
	_, _, _, lastErr := bridge.sceneStats()
	if lastErr == "" {
		t.Fatal("sceneLastError empty, want recorded error")
	}

	// 后续合法 ChangeSet 仍可正常应用（失败不毒化 mapper）。
	ok := &encoding.ChangeSet{Changes: []encoding.ItemChange{{
		Op:   encoding.OpAppend,
		Item: &encoding.Item{ID: "item-1", Kind: encoding.KindUser, Status: encoding.StatusCompleted, Head: "hi"},
	}}}
	bridge.applyChangeSet(ok)
	assertBridgeSceneStats(t, bridge, 1, 1)
}

// TestChatRuntimeEventBridge_SceneConsistentUnderOutOfOrderDeltas 验证乱序
// 注入下 Scene 与模型一致：编码器按 sequence 有序提交（乱序段缓存、连续
// 补拼，对齐旧终端 orderAssistantDelta 重排语义），Scene 消费端不额外推断
// 顺序——UI 顺序由模型数组位置唯一保证。
func TestChatRuntimeEventBridge_SceneConsistentUnderOutOfOrderDeltas(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	evs := []runtimeevents.Event{
		{Type: runtimechat.EventSessionCompactCompleted, Payload: map[string]interface{}{"message": "start"}},
		// 同流打底：llm_started 建立 assistant 流，后续 delta 走 upsert。
		{Type: runtimechat.EventLLMRequestStarted, Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1"}},
		// 同流 sequence 乱序：3 -> 1 -> 2，编码器缓存乱序段并有序提交。
		{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "delta": "C", "sequence": uint64(3)}},
		{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "delta": "A", "sequence": uint64(1)}},
		{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "delta": "B", "sequence": uint64(2)}},
		// 缺失起点（无 llm_started）的 delta：退化为独立 assistant 块。
		{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
			"turn_id": "turn-2", "stream_id": "stream-2", "delta": "early", "sequence": uint64(1)}},
	}
	for _, ev := range evs {
		bridge.encodeRenderModelEvent(ev)
	}
	assertBridgeSceneStats(t, bridge, 3, 0)

	model := bridge.renderModelSnapshot()
	if model == nil || len(model.Items) != 3 {
		t.Fatalf("model items=%d want 3", len(model.Items))
	}
	cells := bridgeSceneCells(t, bridge)
	if len(cells) != 3 {
		t.Fatalf("cells=%d want 3", len(cells))
	}
	// 每个 cell 的 Source 与对应模型 Item.Head 一致；顺序 = 模型数组顺序。
	for i := range cells {
		want := model.Items[i].Head
		if cells[i].Source != want {
			t.Fatalf("cell[%d].Source=%q want model Head %q", i, cells[i].Source, model.Items[i].Head)
		}
	}
	// 同流乱序按 sequence 有序提交：C(3) 缓存，A(1)+B(2) 提交后补 C -> ABC。
	if cells[1].Source != "ABC" {
		t.Fatalf("assistant Source=%q want %q (sequence order)", cells[1].Source, "ABC")
	}
	// 编码器统计到乱序（诊断面），Scene 面无失败。
	if stats := bridge.renderEncoderStats(); stats.OutOfOrderCount == 0 {
		t.Fatal("OutOfOrderCount=0, want > 0")
	}
	if got := strings.ToLower(cells[1].Kind.String()); got != "assistant" {
		t.Fatalf("cell[1] kind=%q want assistant", got)
	}
}

func TestChatRuntimeEventBridge_ReActToolBoundaryDoesNotLeaveOldMutableAssistant(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	traceID, turnID := "trace-react-boundary", "turn-react-boundary"
	post := func(eventType string, payload map[string]interface{}) {
		t.Helper()
		bridge.encodeRenderModelEvent(runtimeevents.Event{Type: eventType, Payload: payload})
	}

	post("llm.request.started", map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": "stream-1", "step": 1,
	})
	post(runtimechat.EventAssistantDelta, map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": "stream-1",
		"step": 1, "sequence": uint64(1), "delta": "intermediate plan",
	})
	post("llm.request.finished", map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": "stream-1", "step": 1, "success": true,
	})
	post("tool.requested", map[string]interface{}{
		"trace_id": traceID, "step": 1, "tool_call_id": "call-1", "tool_name": "read_file",
	})

	cells := bridgeSceneCells(t, bridge)
	if len(cells) != 2 || cells[0].Kind != scene.KindAssistant || cells[0].Phase != scene.CellCommitted ||
		cells[1].Kind != scene.KindToolChain || cells[1].Phase != scene.CellMutable {
		t.Fatalf("tool boundary scene = %+v, want committed assistant followed by mutable tool", cells)
	}

	post("tool.completed", map[string]interface{}{
		"trace_id": traceID, "step": 1, "tool_call_id": "call-1", "output": "file content",
	})
	post("llm.request.started", map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": "stream-2", "step": 2,
	})
	post(runtimechat.EventAssistantDelta, map[string]interface{}{
		"trace_id": traceID, "turn_id": turnID, "stream_id": "stream-2",
		"step": 2, "sequence": uint64(1), "delta": "next round",
	})

	cells = bridgeSceneCells(t, bridge)
	if len(cells) != 3 || cells[0].Phase != scene.CellCommitted ||
		cells[1].Phase != scene.CellCommitted || cells[2].Kind != scene.KindAssistant ||
		cells[2].Phase != scene.CellMutable || cells[2].Source != "next round" {
		t.Fatalf("next ReAct round scene = %+v, want only newest assistant mutable", cells)
	}
}

func TestChatRuntimeEventBridge_ReActToolBoundaryFinalizesReasoningOnlyStep(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	traceID := "trace-reasoning-tool-boundary"
	for _, event := range []runtimeevents.Event{
		{Type: "llm.request.started", Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": "turn-reasoning", "stream_id": "stream-reasoning", "step": 1,
		}},
		{Type: "llm.request.finished", Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": "turn-reasoning", "stream_id": "stream-reasoning", "step": 1, "success": true,
		}},
		{Type: "assistant.reasoning", Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": "turn-reasoning", "step": 1,
			"reasoning": map[string]interface{}{"format": "summary", "summary": "inspect before tool"},
		}},
		{Type: "tool.requested", Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": "turn-reasoning", "step": 1,
			"tool_call_id": "reasoning-call-1", "tool_name": "read_file",
		}},
	} {
		bridge.encodeRenderModelEvent(event)
	}

	cells := bridgeSceneCells(t, bridge)
	if len(cells) != 2 || cells[0].Kind != scene.KindSupplement ||
		cells[0].Phase != scene.CellCommitted || !strings.Contains(cells[0].Source, "inspect before tool") ||
		cells[1].Kind != scene.KindToolChain || cells[1].Phase != scene.CellMutable {
		t.Fatalf("reasoning-only tool boundary scene = %+v, want committed reasoning followed by mutable tool", cells)
	}
}
