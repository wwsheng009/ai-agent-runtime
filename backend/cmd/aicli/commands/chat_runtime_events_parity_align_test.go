package commands

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestTextParityDesyncSelfHealsAcrossToolCell 复现生产错位场景：Scene 侧存在
// legacy 无对应完整块的 cell（tool 链投影，C1 任务 2 完成前的已知覆盖缺口），
// 对照探针不能把游标永久钉在该 cell 上。
//
// 旧实现下首个 mismatch 后 textParityCell 不推进，之后每个完整块都与同一
// tool cell 重复对照 → 每个块追加一次 missed（会话越长噪声越大），而本测试
// 断言：错位只影响其后的第一个块，第二个块在窗口内重对齐后重新 matched。
func TestTextParityDesyncSelfHealsAcrossToolCell(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)

	// 块 1/2：user + assistant，对齐良好（matched）。
	coord.RenderSubmittedUserInput("U1")
	for _, ev := range c0TurnEvents("turn-1", "stream-1", "你好", false) {
		bridge.encodeRenderModelEvent(ev)
	}
	coord.RenderAssistant("你好")

	// Scene 侧多出 tool cell（legacy 无对应完整块提交点），制造错位。
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type: runtimechat.EventToolStarted, SessionID: "session-1", TraceID: "trace-tool",
		Payload: map[string]interface{}{"tool_name": "view", "tool_call_id": "call-1"},
	})
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type: runtimechat.EventToolFinished, SessionID: "session-1", TraceID: "trace-tool",
		Payload: map[string]interface{}{"tool_name": "view", "tool_call_id": "call-1", "duration_ms": uint64(5)},
	})

	// 块 3：错位后的第一个块，与 tool cell 不一致 → mismatch + 重对齐。
	for _, ev := range c0TurnEvents("turn-2", "stream-2", "世界", false) {
		bridge.encodeRenderModelEvent(ev)
	}
	coord.RenderAssistant("世界")

	// 块 4：游标已前进，必须恢复 matched（旧实现下此处仍是 mismatch）。
	for _, ev := range c0TurnEvents("turn-3", "stream-3", "再会", false) {
		bridge.encodeRenderModelEvent(ev)
	}
	coord.RenderAssistant("再会")

	blocks, matched, missed, lastErr := bridge.textParityStats()
	if blocks != 4 || matched != 3 || missed != 1 {
		t.Fatalf("parity: blocks=%d matched=%d missed=%d (want 4/3/1) last=%q\nlegacy out=%q",
			blocks, matched, missed, lastErr, out.String())
	}
	resyncs, skips := bridge.textParityAlignmentStats()
	if resyncs == 0 {
		t.Fatalf("resyncs=%d skips=%d want resync>0（错位应由窗口内重对齐修复）", resyncs, skips)
	}
}

// TestTextParityStuckCellForcesProgress 覆盖容忍度兜底：窗口内找不到一致分组
// （Scene 内容确实缺失）时，同一 cell 连续 mismatch 达到容忍度必须强制跳过一个
// cell，避免对照游标永久停留；跳过分组不得越界。
func TestTextParityStuckCellForcesProgress(t *testing.T) {
	bridge := &chatRuntimeEventBridge{}
	bridge.textParityCell = 3
	bridge.textParityStuckCell = 3

	if bridge.noteTextParityMismatch() {
		t.Fatal("首次 mismatch 必须被容忍（瞬时滞后不应跳过分组）")
	}
	if !bridge.noteTextParityMismatch() {
		t.Fatal("同一 cell 连续 mismatch 达到容忍度后应要求强制前进")
	}
	bridge.skipStuckTextParityCell(10)
	if bridge.textParityCell != 4 || bridge.textParitySkips != 1 {
		t.Fatalf("cell=%d skips=%d want cell=4 skips=1", bridge.textParityCell, bridge.textParitySkips)
	}
	if bridge.textParityStuckCount != 0 {
		t.Fatalf("stuckCount=%d want 0（跳过后重新计数）", bridge.textParityStuckCount)
	}

	// 已到分组末尾：跳过为 no-op。
	bridge.textParityCell = 10
	bridge.skipStuckTextParityCell(10)
	if bridge.textParityCell != 10 || bridge.textParitySkips != 1 {
		t.Fatalf("越界跳过必须 no-op: cell=%d skips=%d", bridge.textParityCell, bridge.textParitySkips)
	}
}

// TestParityCompleteBlockCompareChromeAndGaps 固化对照口径：user/system 样式
// chrome 剥离、reasoning 跳过遗留的前导 gap 行前置、行数/行内容不一致给出可读
// 详情（/debug 审计段定位用）。
func TestParityCompleteBlockCompareChromeAndGaps(t *testing.T) {
	ui.SetTheme(ui.ThemeAuto)

	assistant := sceneBlockGroup{kind: scene.KindAssistant, lines: []string{"hello"}}
	if ok, detail := parityCompleteBlockCompare(assistant, []string{"hello"}, 0); !ok {
		t.Fatalf("相同行必须一致: %s", detail)
	}
	if ok, detail := parityCompleteBlockCompare(assistant, []string{"hello", "extra"}, 0); ok || detail == "" {
		t.Fatalf("行数不一致必须失败并给出详情: ok=%v detail=%q", ok, detail)
	}
	if ok, detail := parityCompleteBlockCompare(assistant, []string{"other"}, 0); ok || !strings.Contains(detail, "legacy=") {
		t.Fatalf("内容不一致详情 = %q (ok=%v)", detail, ok)
	}

	user := sceneBlockGroup{kind: scene.KindUser, lines: []string{"hi"}}
	if ok, detail := parityCompleteBlockCompare(user, []string{"> hi"}, 0); !ok {
		t.Fatalf("user 前缀必须剥离: %s", detail)
	}

	system := sceneBlockGroup{kind: scene.KindSystem, lines: []string{"boom"}}
	iconRow := ui.GetTheme(ui.ThemeAuto).ErrorIcon + "  boom"
	if ok, detail := parityCompleteBlockCompare(system, []string{iconRow}, 0); !ok {
		t.Fatalf("system 错误图标必须剥离: %s", detail)
	}

	// reasoning 跳过后遗留的前导 gap 行按 legacy 口径补在分组行前。
	if ok, detail := parityCompleteBlockCompare(assistant, []string{"", "hello"}, 1); !ok {
		t.Fatalf("前导 gap 行必须前置: %s", detail)
	}
}

// BenchmarkSceneBlockGroupsLargeScene 度量对照探针与 Scene presenter 源的共享
// 分组热路径（每个完整块提交调用一次，属 UI actor 提交路径）。cell kind 索引
// 已从按行线性扫描 snap.Cells（长历史下 O(cells²)）改为一次建表 O(cells)，
// 本基准防止该路径随历史规模平方增长（生产会话曾出现 UI actor 追不上事件
// 发布的症状，见 chat_runtime_events.go 的对账自愈注释）。
func BenchmarkSceneBlockGroupsLargeScene(b *testing.B) {
	b.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)

	for i := 0; i < 300; i++ {
		coord.RenderSubmittedUserInput(fmt.Sprintf("U%d", i))
		coord.RenderAssistant(fmt.Sprintf("A%d", i))
	}
	snap := bridge.sceneSnapshot()
	if snap == nil || len(snap.Cells) < 300 {
		b.Fatalf("snapshot cells = %v, want >= 300", snap)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if groups := sceneBlockGroups(snap); len(groups) == 0 {
			b.Fatal("sceneBlockGroups returned no groups")
		}
	}
}
