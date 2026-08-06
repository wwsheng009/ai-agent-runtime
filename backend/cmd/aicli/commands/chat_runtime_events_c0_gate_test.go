package commands

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// c0TurnEvents 返回一个 assistant turn 的固定事件序列（流式 delta 事件 +
// assistant 消息，消息可选携带 reasoning 元数据）：[started, delta,
// delta, message, finished]。与 renderParityTwoTurnEvents 同构，但 turn
// 之间不互相耦合，C0 门禁可自由组合。
func c0TurnEvents(turnID, streamID string, content string, withReasoning bool) []runtimeevents.Event {
	messagePayload := map[string]interface{}{
		"turn_id": turnID, "stream_id": streamID, "content": content,
	}
	if withReasoning {
		messagePayload["reasoning"] = map[string]interface{}{
			"provider": "nvidia", "format": "openai_compatible",
			"summary": "先思考", "visibility": "summary", "streamable": true,
		}
	}
	return []runtimeevents.Event{
		{Type: runtimechat.EventLLMRequestStarted, SessionID: "session-1", TraceID: traceIDOf(turnID),
			Payload: map[string]interface{}{"turn_id": turnID, "stream_id": streamID}},
		{Type: runtimechat.EventAssistantDelta, SessionID: "session-1", TraceID: traceIDOf(turnID),
			Payload: map[string]interface{}{"turn_id": turnID, "stream_id": streamID, "delta": firstRune(content), "sequence": uint64(1)}},
		{Type: runtimechat.EventAssistantDelta, SessionID: "session-1", TraceID: traceIDOf(turnID),
			Payload: map[string]interface{}{"turn_id": turnID, "stream_id": streamID, "delta": restRunes(content), "sequence": uint64(2)}},
		{Type: runtimechat.EventAssistantMessage, SessionID: "session-1", TraceID: traceIDOf(turnID),
			Payload: messagePayload},
		{Type: runtimechat.EventLLMRequestFinished, SessionID: "session-1", TraceID: traceIDOf(turnID),
			Payload: map[string]interface{}{"turn_id": turnID, "stream_id": streamID}},
	}
}

func traceIDOf(turnID string) string { return "trace-" + turnID }

func firstRune(s string) string {
	if s == "" {
		return ""
	}
	return string([]rune(s)[:1])
}

func restRunes(s string) string {
	r := []rune(s)
	if len(r) <= 1 {
		return ""
	}
	return string(r[1:])
}

// TestRenderLayer_C0_ParityGate_FullSessionKinds 是 C0 双跑门禁化的锚点测试
// （`aicli-tui-scene-presenter-convergence-design.md` §3 C0 任务 1）：
// 固定会话序列覆盖 user/assistant/system/error/reasoning + 流式 + gap
// 全部阶段，跑完后 textParityMatched == textParityBlocks &&
// textParityMissed == 0 —— 双跑偏差在提交时失败，而不是上线后观察。
//
// 完整块（探针逐一对照 Scene 投影）共 6 个：
//
//	U1 → turn-1 assistant（含 reasoning 元数据）→ command → error(system)
//	→ prompt gap + U2 → turn-2 assistant
//
// tool 阶段不在本序列中交错：tool 事件的 Scene 侧投影（KindToolChain cell）
// 与旧路径完整块序列的对齐属 C1 任务 2（"补齐所有 finalized cell 类型的
// Scene 投影覆盖（tool chain…）"）；当前旧路径 tool 可见渲染走 ActiveBand
// 流式带，无完整块提交点，交错注入会使探针逐 cell 对照错位。C0 对 tool
// 的覆盖见 TestRenderLayer_C0_ToolStageDoesNotSkewParity（事件注入 + 无
// 探针偏差）。C1 完成后在此序列追加 tool 阶段并保持全绿。
func TestRenderLayer_C0_ParityGate_FullSessionKinds(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)

	// 阶段 1：user 完整块（live 提交点注入 KindUser）。
	coord.RenderSubmittedUserInput("U1")
	// 阶段 2：turn-1（流式 delta + reasoning 元数据）→ assistant 完整块。
	for _, ev := range c0TurnEvents("turn-1", "stream-1", "你好", true) {
		bridge.encodeRenderModelEvent(ev)
	}
	coord.RenderAssistant("你好")
	// 阶段 3：command 完整块（live 点注入 KindCommand 终态块）。
	if ok := coord.RenderCommandDocument(renderParityCommandDoc()); !ok {
		t.Fatal("RenderCommandDocument returned false")
	}
	// 阶段 4：error 完整块（live 点注入 KindSystem 终态块）。
	coord.RenderError(errors.New("boom"))
	// 阶段 5：prompt gap + user 完整块。
	coord.writePromptGapLocked()
	coord.RenderSubmittedUserInput("U2")
	// 阶段 6：turn-2 → assistant 完整块。
	for _, ev := range c0TurnEvents("turn-2", "stream-2", "世界", false) {
		bridge.encodeRenderModelEvent(ev)
	}
	coord.RenderAssistant("世界")

	// C0 出口断言：parity 门禁全绿（6 完整块 = 2 user + 2 assistant + command + system）。
	blocks, matched, missed, lastErr := bridge.textParityStats()
	if blocks != 6 || matched != 6 || missed != 0 {
		t.Fatalf("parity: blocks=%d matched=%d missed=%d last=%q\nlegacy out=%q",
			blocks, matched, missed, lastErr, out.String())
	}

	// Scene cell 类型序列：user, assistant, command, system, user, assistant。
	snap := bridge.sceneSnapshot()
	var kinds []string
	for _, c := range snap.Cells {
		if c != nil {
			kinds = append(kinds, c.Kind.String())
		}
	}
	wantKinds := []string{"user", "assistant", "command", "system", "user", "assistant"}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("cell kinds = %v, want %v", kinds, wantKinds)
	}
	for i := range wantKinds {
		if kinds[i] != wantKinds[i] {
			t.Fatalf("cell %d kind = %q, want %q (all=%v)", i, kinds[i], wantKinds[i], kinds)
		}
	}

	// 全量对照：Scene 快照 RenderText == 旧路径输出（样式归一化与探针一致：
	// user 行剥 "> "，error 行剥 ErrorIcon 前缀）。
	legacyLines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := scene.RenderText(snap.Cells, snap.Revision)
	if len(legacyLines) != len(want) {
		t.Fatalf("lines: legacy=%d (%q) scene=%d (%q)", len(legacyLines), legacyLines, len(want), want)
	}
	layoutRows := scene.LayoutTranscript(snap.Cells, snap.Revision)
	userRow := make(map[int]bool)
	for _, row := range layoutRows {
		if row.CellID != 0 {
			for _, c := range snap.Cells {
				if c != nil && c.ID == row.CellID && c.Kind == scene.KindUser {
					userRow[row.Index] = true
				}
			}
		}
	}
	for i := range legacyLines {
		legacy := legacyLines[i]
		if userRow[i] {
			legacy = strings.TrimPrefix(legacy, "> ")
		}
		legacy = strings.TrimPrefix(legacy, ui.GetTheme(ui.ThemeAuto).ErrorIcon+"  ")
		if legacy != want[i] {
			t.Fatalf("line %d: legacy=%q scene=%q", i, legacyLines[i], want[i])
		}
	}

	// gap 位置逐项一致（LayoutTranscript gap 归属 vs 旧路径空行）。
	legacyGaps := blankLinePositions(legacyLines)
	var newGaps []int
	for i, row := range layoutRows {
		if row.Gap > 0 {
			newGaps = append(newGaps, i)
		}
	}
	if len(legacyGaps) != len(newGaps) {
		t.Fatalf("gap count: legacy=%v layout=%v", legacyGaps, newGaps)
	}
	for i := range legacyGaps {
		if legacyGaps[i] != newGaps[i] {
			t.Fatalf("gap position %d: legacy=%d layout=%d", i, legacyGaps[i], newGaps[i])
		}
	}
}

// TestRenderLayer_C0_ToolStageDoesNotSkewParity 固化 C0 阶段的 tool 覆盖
// （C1 任务 2 完成前的现状锚点）：
//
//  1. tool 事件经 bridge 注入统一渲染数据面后，Scene 出现 KindToolChain
//     cell（tool_name 文本）——tool 阶段在 Scene 侧已有投影；
//  2. tool 阶段**不产生完整块提交点**（旧路径 tool 可见渲染走 ActiveBand
//     流式带），因此探针 blocks/matched/missed 统计完全不受影响——tool
//     阶段不会引入伪偏差；
//  3. 完整块序列照常全部 matched（本测试在全部完整块之后注入 tool 事件，
//     避免交错错位——交错对齐是 C1 任务 2 的工作项）。
func TestRenderLayer_C0_ToolStageDoesNotSkewParity(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)

	coord.RenderSubmittedUserInput("U1")
	for _, ev := range c0TurnEvents("turn-1", "stream-1", "你好", false) {
		bridge.encodeRenderModelEvent(ev)
	}
	coord.RenderAssistant("你好")

	// tool 阶段（事件注入：started/finished）。
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type: runtimechat.EventToolStarted, SessionID: "session-1", TraceID: "trace-tool",
		Payload: map[string]interface{}{"tool_name": "view", "tool_call_id": "call-1"},
	})
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type: runtimechat.EventToolFinished, SessionID: "session-1", TraceID: "trace-tool",
		Payload: map[string]interface{}{"tool_name": "view", "tool_call_id": "call-1", "duration_ms": uint64(5)},
	})

	// 1) Scene 侧：KindToolChain cell 已投影（tool_name 文本）。
	snap := bridge.sceneSnapshot()
	var toolCells []string
	for _, c := range snap.Cells {
		if c != nil && c.Kind == scene.KindToolChain {
			toolCells = append(toolCells, c.Source)
		}
	}
	if len(toolCells) != 1 || toolCells[0] != "• Completed view in 5ms" {
		t.Fatalf("tool cells = %v, want [• Completed view in 5ms]", toolCells)
	}

	// 2)+3) 探针统计：完整块 2 个全部 matched；tool 阶段零偏差。
	blocks, matched, missed, lastErr := bridge.textParityStats()
	if blocks != 2 || matched != 2 || missed != 0 {
		t.Fatalf("parity: blocks=%d matched=%d missed=%d last=%q\nlegacy out=%q",
			blocks, matched, missed, lastErr, out.String())
	}
}
