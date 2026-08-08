package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// renderParityTwoTurnEvents 返回两个 assistant 完整 turn 的真实事件流
// （与切片 8 文本 parity 测试同一会话序列）：Scene 快照 RenderText 为
// ["你好" "" "世界"]（2 cell / 1 gap）。
func renderParityTwoTurnEvents() []runtimeevents.Event {
	return []runtimeevents.Event{
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
}

// TestRenderLayer_TextParity_RealWritePathBlocks 固化切片 9 的运行时双跑
// 文本对照：bridge 处理真实事件流后，coordinator 经真实完整块提交点
// （writeRowsLocked，构造器自动接线）把每个块的实际行序列交给探针，与
// Scene 快照 RenderText 逐行对照全部 matched。这证明对照探针在真实写入
// 路径上工作（不只是切片 7/8 的测试内手工驱动），且 /debug 审计段展示
// 统计。
func TestRenderLayer_TextParity_RealWritePathBlocks(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	for _, ev := range renderParityTwoTurnEvents() {
		bridge.encodeRenderModelEvent(ev)
	}

	coord := newChatInteractionCoordinator(session) // 构造器自动接线探针
	var out bytes.Buffer
	coord.SetWriter(&out)
	coord.RenderAssistant("你好")
	coord.RenderAssistant("世界")

	blocks, matched, missed, lastErr := bridge.textParityStats()
	if blocks != 2 || matched != 2 || missed != 0 {
		t.Fatalf("parity: blocks=%d matched=%d missed=%d last=%q\nlegacy out=%q",
			blocks, matched, missed, lastErr, out.String())
	}

	// /debug 审计段展示统计（切换前人工排查入口）。
	doc := buildChatDebugDisplayDocument(session)
	plain := renderDocPlainText(doc)
	for _, marker := range []string{
		"Unified Render Scene:",
		"Text Parity Blocks: 2",
		"Text Parity Matched: 2",
		"Text Parity Missed: 0",
	} {
		if !strings.Contains(plain, marker) {
			t.Fatalf("debug display 缺少锚点 %q\n---\n%s", marker, plain)
		}
	}
}

// TestRenderLayer_TextParity_DetectsDivergence 证明探针真的在对照（不是
// 空转）：Scene 只有 1 个 cell（1 turn 事件），旧路径却提交了 2 个完整块
// ——第 2 块行序列越界，被记为 missed 且 Last Error 给出块号详情。
func TestRenderLayer_TextParity_DetectsDivergence(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	// 只编码 turn-1（Scene 1 cell "你好"）。
	for _, ev := range renderParityTwoTurnEvents()[:5] {
		bridge.encodeRenderModelEvent(ev)
	}

	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)
	coord.RenderAssistant("你好")
	coord.RenderAssistant("世界") // Scene 没有对应内容 → overflow mismatch

	blocks, matched, missed, lastErr := bridge.textParityStats()
	if blocks != 2 || matched != 1 || missed != 1 {
		t.Fatalf("parity: blocks=%d matched=%d missed=%d last=%q",
			blocks, matched, missed, lastErr)
	}
	if !strings.Contains(lastErr, "block 2") || !strings.Contains(lastErr, "overflow") {
		t.Fatalf("last error 应指向第 2 块越界，got %q", lastErr)
	}
}

// TestRenderLayer_TextParity_NilProbeSafe 固化探针的旁路语义：无 bridge
// （探针 nil）时旧路径完整块写入行为完全不变，不 panic、不产生额外输出。
func TestRenderLayer_TextParity_NilProbeSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{} // 无 RuntimeEventBridge
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)
	coord.RenderAssistant("你好")
	coord.RenderAssistant("世界")
	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := []string{"• 你好", "", "• 世界"}
	if len(got) != len(want) {
		t.Fatalf("lines=%d want %d got=%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d=%q want %q", i, got[i], want[i])
		}
	}
}
