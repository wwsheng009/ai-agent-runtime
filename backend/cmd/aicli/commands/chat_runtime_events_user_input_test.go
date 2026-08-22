package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestRenderLayer_TextParity_LiveUserInputBlocks 固化切片 10 的核心等价：
// 用户输入经 coordinator live 提交点注入统一渲染数据面后，旧路径完整块
// 序列与 Scene cell 序列对用户块也一一对应（此前仅事件流覆盖的块成立）。
//
// 会话序列：U1 → turn-1（你好）→ U2 → turn-2（世界），与真实运行时一致
// （用户输入与 turn 事件流交错进入数据面）。探针逐块对照全部 matched
// （4 块 = 2 user + 2 assistant），且 Scene 快照 RenderText 与旧路径输出
// 全量一致（含 gap 空行位置）。
func TestRenderLayer_TextParity_LiveUserInputBlocks(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge

	// 交错注入：用户输入（经 coordinator live 提交点）与 turn 事件流
	// 按真实顺序进入数据面——用户提交 → turn-1 事件 → 用户提交 →
	// turn-2 事件。
	evs := renderParityTwoTurnEvents()
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)
	coord.RenderSubmittedUserInput("U1")
	for _, ev := range evs[:5] {
		bridge.encodeRenderModelEvent(ev) // turn-1：你好
	}
	coord.RenderAssistant("你好")
	// prompt 重绘的 gap 消费（与切片 7 同款：U2 前的空行来自此处）。
	coord.writePromptGapLocked()
	coord.RenderSubmittedUserInput("U2")
	for _, ev := range evs[5:] {
		bridge.encodeRenderModelEvent(ev) // turn-2：世界
	}
	coord.RenderAssistant("世界")

	// 探针：4 个完整块全部 matched。
	blocks, matched, missed, lastErr := bridge.textParityStats()
	if blocks != 4 || matched != 4 || missed != 0 {
		t.Fatalf("parity: blocks=%d matched=%d missed=%d last=%q\nlegacy out=%q",
			blocks, matched, missed, lastErr, out.String())
	}

	// 全量对照：Scene 快照 RenderText == 旧路径输出（含 gap 空行）。
	// 样式归一化与探针一致：user cell 行在旧路径带 "> " 前缀（RenderText
	// 只投影语义内容，样式属于 presenter 层）。
	legacyLines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	snap := bridge.sceneSnapshot()
	want := scene.RenderText(snap.Cells, snap.Revision)
	if len(legacyLines) != len(want) {
		t.Fatalf("lines: legacy=%d (%q) scene=%d (%q)", len(legacyLines), legacyLines, len(want), want)
	}
	layoutRows := scene.LayoutTranscript(snap.Cells, snap.Revision)
	userRow := make(map[int]bool)
	userCell := make(map[scene.CellID]bool)
	for _, c := range snap.Cells {
		if c != nil && c.Kind == scene.KindUser {
			userCell[c.ID] = true
		}
	}
	for _, row := range layoutRows {
		if userCell[row.CellID] {
			userRow[row.Index] = true
		}
	}
	for i := range legacyLines {
		legacy := legacyLines[i]
		if userRow[i] {
			legacy = strings.TrimPrefix(legacy, "> ")
		}
		if legacy != want[i] {
			t.Fatalf("line %d: legacy=%q scene=%q", i, legacyLines[i], want[i])
		}
	}
	// gap 位置逐项一致（LayoutTranscript 规则表 vs 旧路径空行）。
	rows := scene.LayoutTranscript(snap.Cells, snap.Revision)
	var newGaps []int
	for i, row := range rows {
		if row.Gap > 0 {
			newGaps = append(newGaps, i)
		}
	}
	legacyGaps := blankLinePositions(legacyLines)
	if len(legacyGaps) != len(newGaps) {
		t.Fatalf("gap count: legacy=%v layout=%v", legacyGaps, newGaps)
	}
	for i := range legacyGaps {
		if legacyGaps[i] != newGaps[i] {
			t.Fatalf("gap position %d: legacy=%d layout=%d", i, legacyGaps[i], newGaps[i])
		}
	}
}

// TestRenderLayer_Scene_ReplayRestoresUserInput 固化切片 10 的日志闭环：
// 用户输入与事件交错落同一事件日志，新 bridge 重放后 Scene 与实时路径
// 等价（cell 顺序/kinds/RenderText 一致）。
func TestRenderLayer_Scene_ReplayRestoresUserInput(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")

	bridge1 := newChatRuntimeEventBridge(&ChatSession{})
	bridge1.eventLogPathOverride = logPath
	evs := renderParityTwoTurnEvents()
	bridge1.submitUserInput("U1")
	for _, ev := range evs[:5] {
		bridge1.encodeRenderModelEvent(ev)
	}
	bridge1.submitUserInput("U2")
	for _, ev := range evs[5:] {
		bridge1.encodeRenderModelEvent(ev)
	}

	path, count, replayed, failures := bridge1.eventLogStats()
	if path != logPath || count != 11 || replayed != 0 || failures != 0 {
		t.Fatalf("stats after write: path=%q count=%d replayed=%d failures=%d", path, count, replayed, failures)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if lines := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); lines != 11 {
		t.Fatalf("event log lines=%d want 11", lines)
	}

	snap1 := bridge1.sceneSnapshot()
	want := scene.RenderText(snap1.Cells, snap1.Revision)

	// 新 bridge（模拟会话重启）重放日志：user 输入记录同样恢复。
	bridge2 := newChatRuntimeEventBridge(&ChatSession{})
	bridge2.eventLogPathOverride = logPath
	n, err := bridge2.replayEventLog()
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n != 11 {
		t.Fatalf("replayed=%d want 11", n)
	}
	snap2 := bridge2.sceneSnapshot()
	if len(snap2.Cells) != len(snap1.Cells) {
		t.Fatalf("cells after replay=%d want %d", len(snap2.Cells), len(snap1.Cells))
	}
	for i := range snap1.Cells {
		if snap1.Cells[i].Kind != snap2.Cells[i].Kind {
			t.Fatalf("cell %d kind: live=%v replay=%v", i, snap1.Cells[i].Kind, snap2.Cells[i].Kind)
		}
	}
	got := scene.RenderText(snap2.Cells, snap2.Revision)
	if len(got) != len(want) {
		t.Fatalf("render rows after replay=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("render row %d: live=%q replay=%q", i, want[i], got[i])
		}
	}
}

// TestRenderLayer_UserInput_ReplayPathDoesNotInject 固化切片 10 的边界：
// 历史回放路径（RenderReplayedUserInput）不注入数据面——回放由
// replayEventLog 从事件日志恢复，重复注入会产生重复 user cell。live
// 提交（RenderSubmittedUserInput）才注入。两者输出行为均不变。
func TestRenderLayer_UserInput_ReplayPathDoesNotInject(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// 本测试断言旧路径（非 Scene presenter）的跨块空行输出；外部环境若
	// 设置了 AICLI_SCENE_PRESENTER=1，coordinator 会切换到 Scene 投影并
	// 吞掉 replay 路径的空行，因此显式固定该开关（t.Setenv 进程级，顺序
	// 测试间自动恢复）。
	t.Setenv("AICLI_SCENE_PRESENTER", "")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	for _, ev := range renderParityTwoTurnEvents() {
		bridge.encodeRenderModelEvent(ev)
	}
	before := len(bridge.sceneSnapshot().Cells)

	// 回放路径：不注入（Scene cell 数不变）。
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)
	coord.RenderReplayedUserInput("旧消息")
	coord.RenderAssistant("你好")
	after := len(bridge.sceneSnapshot().Cells)
	if after != before {
		t.Fatalf("replay path injected: cells before=%d after=%d", before, after)
	}
	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := []string{"> 旧消息", "", "你好"}
	if len(got) != len(want) {
		t.Fatalf("lines=%d want %d got=%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d=%q want %q", i, got[i], want[i])
		}
	}

	// live 提交：注入（Scene cell 数 +1，RenderText 含用户文本）。
	coord2 := newChatInteractionCoordinator(session)
	var out2 bytes.Buffer
	coord2.SetWriter(&out2)
	coord2.RenderSubmittedUserInput("新消息")
	afterLive := len(bridge.sceneSnapshot().Cells)
	if afterLive != before+1 {
		t.Fatalf("live path not injected: cells before=%d after=%d", before, afterLive)
	}
	snap := bridge.sceneSnapshot()
	rows := scene.RenderText(snap.Cells, snap.Revision)
	found := false
	for _, r := range rows {
		if r == "新消息" {
			found = true
		}
	}
	if !found {
		t.Fatalf("live path: RenderText 缺少用户文本，rows=%q", rows)
	}
}
