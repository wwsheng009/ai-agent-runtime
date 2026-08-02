package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestRenderLayer_TextParity_EventStreamVsLegacyCoordinator 固化渲染层
// 切换的最终文本等价：真实事件流 → EventEncoder → ChangeSet → Scene →
// RenderText 的完整新链路，其输出文本行与旧路径 coordinator 渲染输出
// 逐行一致（含内容行文本与 gap 空行位置，不只是 gap 计数）。
//
// 会话序列：两个 assistant 完整 turn（流式 delta 合并 + 终态 message）。
// 旧路径：RenderAssistant 一次成型（切片 7 已验证流式/一次成型共用
// writeRowsLocked 完整块语义）；新路径：bridge 真实事件接入点。
func TestRenderLayer_TextParity_EventStreamVsLegacyCoordinator(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	// 事件流：turn-1 delta "你"+"好" → message "你好"；turn-2 同理 "世界"。
	evs := []runtimeevents.Event{
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
		{Type: runtimechat.EventLLMRequestStarted, SessionID: "session-1", TraceID: "trace-2",
			Payload: map[string]interface{}{"turn_id": "turn-2", "stream_id": "stream-2"}},
		{Type: runtimechat.EventAssistantDelta, SessionID: "session-1", TraceID: "trace-2",
			Payload: map[string]interface{}{"turn_id": "turn-2", "stream_id": "stream-2", "delta": "世界", "sequence": uint64(1)}},
		{Type: runtimechat.EventAssistantMessage, SessionID: "session-1", TraceID: "trace-2",
			Payload: map[string]interface{}{"turn_id": "turn-2", "stream_id": "stream-2", "content": "世界"}},
		{Type: runtimechat.EventLLMRequestFinished, SessionID: "session-1", TraceID: "trace-2",
			Payload: map[string]interface{}{"turn_id": "turn-2", "stream_id": "stream-2"}},
	}

	// 新路径：真实 bridge 事件接入点（同步驱动，不启动事件循环）。
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	for _, ev := range evs {
		bridge.encodeRenderModelEvent(ev)
	}
	snap := bridge.sceneSnapshot()
	if snap == nil || len(snap.Cells) != 2 {
		t.Fatalf("scene cells=%d want 2", len(snap.Cells))
	}
	newLines := scene.RenderText(snap.Cells, snap.Revision)

	// 旧路径：真实 coordinator，同一会话两个 assistant 块。
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)
	coord.RenderAssistant("你好")
	coord.RenderAssistant("世界")
	legacyLines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")

	// 逐行相等：内容文本与 gap 空行位置都必须一致（渲染层切换的最终
	// 文本等价，比切片 7 的 gap 位置等价更强）。
	if len(legacyLines) != len(newLines) {
		t.Fatalf("line count: legacy=%d new=%d\nlegacy=%q\nnew=%q", len(legacyLines), len(newLines), legacyLines, newLines)
	}
	for i := range legacyLines {
		if legacyLines[i] != newLines[i] {
			t.Fatalf("line %d: legacy=%q new=%q\nlegacy=%q\nnew=%q", i, legacyLines[i], newLines[i], legacyLines, newLines)
		}
	}
	t.Logf("text parity ok: %d lines %q", len(newLines), newLines)
}

// TestRenderLayer_TextParity_ToolChainRenderText 覆盖 tool 链的文本投影：
// 真实事件流（system + assistant 流式 + tool 链）→ Scene → RenderText，
// 断言 gap 结构符合 §7.3 规则表（每个独立 top-level 之间 1 gap；tool
// 链内稠密）且内容行与 cell source 逐行一致。
func TestRenderLayer_TextParity_ToolChainRenderText(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	for _, ev := range testRuntimeSceneEvents() {
		bridge.encodeRenderModelEvent(ev)
	}
	snap := bridge.sceneSnapshot()
	if snap == nil || len(snap.Cells) != 3 {
		t.Fatalf("scene cells=%d want 3", len(snap.Cells))
	}
	lines := scene.RenderText(snap.Cells, snap.Revision)
	want := []string{"session compacted", "", "你好", "", "read_file", "file content"}
	if len(lines) != len(want) {
		t.Fatalf("lines=%d want %d\n got=%q", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d=%q want %q\n got=%q", i, lines[i], want[i], lines)
		}
	}
	// gap 位置由 LayoutTranscript 的 Boundary 行唯一决定（INV-GAP-03），
	// 文本投影只是把 gap 行变成空行。
	rows := scene.LayoutTranscript(snap.Cells, snap.Revision)
	gapRows := 0
	for _, r := range rows {
		if r.Boundary != nil {
			gapRows++
		}
	}
	if gapRows != 2 {
		t.Fatalf("boundary rows=%d want 2", gapRows)
	}
}
