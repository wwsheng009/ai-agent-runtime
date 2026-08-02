package commands

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// renderParityCommandDoc 构造一个命令结果文档（两个段落块，模拟
// renderChatCommandResult 合并后的原子命令输出）。
func renderParityCommandDoc() render.Document {
	return render.Document{
		Blocks: []render.Block{
			{Kind: render.BlockParagraph, Lines: []render.Line{
				{Spans: []render.Span{{Text: "first-block"}}},
			}},
			{Kind: render.BlockParagraph, Lines: []render.Line{
				{Spans: []render.Span{{Text: "second-block"}}},
			}},
		},
	}
}

// TestRenderLayer_TextParity_LiveCommandAndErrorBlocks 固化切片 11 的核心
// 等价：命令结果与操作错误经 coordinator live 提交点注入统一渲染数据面后，
// 旧路径完整块序列与 Scene cell 序列对命令/错误块也一一对应（此前仅事件流
// + 用户输入覆盖的块成立）。
//
// 会话序列：命令块 → turn-1 → 错误块 → turn-2，与真实运行时一致（命令/
// 错误与 turn 事件流交错进入数据面）。探针逐块对照全部 matched，且 Scene
// 快照 RenderText 与旧路径输出全量一致。
func TestRenderLayer_TextParity_LiveCommandAndErrorBlocks(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge

	evs := renderParityTwoTurnEvents()
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)

	// 命令结果提交（live 点注入 Scene：KindCommand 终态块）。
	if ok := coord.RenderCommandDocument(renderParityCommandDoc()); !ok {
		t.Fatal("RenderCommandDocument returned false")
	}
	for _, ev := range evs[:5] {
		bridge.encodeRenderModelEvent(ev) // turn-1：你好
	}
	coord.RenderAssistant("你好")
	// 操作错误提交（live 点注入 Scene：KindSystem 终态块）。
	coord.RenderError(errors.New("boom"))
	for _, ev := range evs[5:] {
		bridge.encodeRenderModelEvent(ev) // turn-2：世界
	}
	coord.RenderAssistant("世界")

	// 探针：6 个完整块全部 matched（command + assistant + system + assistant）。
	blocks, matched, missed, lastErr := bridge.textParityStats()
	if blocks != 4 || matched != 4 || missed != 0 {
		t.Fatalf("parity: blocks=%d matched=%d missed=%d last=%q\nlegacy out=%q",
			blocks, matched, missed, lastErr, out.String())
	}

	// Scene cell 类型序列：command, assistant, system, assistant。
	snap := bridge.sceneSnapshot()
	var kinds []string
	for _, c := range snap.Cells {
		if c != nil {
			kinds = append(kinds, c.Kind.String())
		}
	}
	if len(kinds) != 4 || kinds[0] != "command" || kinds[2] != "system" {
		t.Fatalf("cell kinds = %v, want [command assistant system assistant]", kinds)
	}

	// 全量对照：Scene 快照 RenderText == 旧路径输出。错误块 legacy 行带
	// 样式前缀 "❌  "（theme.ErrorIcon + 空格，presenter 层），剥离后对照
	// 语义内容（与探针同一归一化）。
	legacyLines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := scene.RenderText(snap.Cells, snap.Revision)
	if len(legacyLines) != len(want) {
		t.Fatalf("lines: legacy=%d (%q) scene=%d (%q)", len(legacyLines), legacyLines, len(want), want)
	}
	for i := range legacyLines {
		legacyLines[i] = strings.TrimPrefix(legacyLines[i], ui.GetTheme(ui.ThemeAuto).ErrorIcon+"  ")
		if legacyLines[i] != want[i] {
			t.Fatalf("line %d: legacy=%q scene=%q", i, legacyLines[i], want[i])
		}
	}
}

// TestRenderLayer_Scene_ReplayRestoresCommandAndError 固化切片 11 的日志
// 闭环：命令/错误与事件、用户输入交错落同一事件日志，新 bridge 重放后
// Scene 与实时路径等价（cell 顺序/kinds/RenderText 一致）；旧格式
// user input 记录行（{"user_input": ...}）与新记录共存时仍可解析。
func TestRenderLayer_Scene_ReplayRestoresCommandAndError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")

	bridge1 := newChatRuntimeEventBridge(&ChatSession{})
	bridge1.eventLogPathOverride = logPath
	evs := renderParityTwoTurnEvents()
	bridge1.submitUserInput("U1")
	bridge1.submitCommand("cmd output")
	bridge1.submitError("操作错误: boom")
	for _, ev := range evs {
		bridge1.encodeRenderModelEvent(ev)
	}
	bridge1.submitCommand("cmd output 2")

	path, count, replayed, failures := bridge1.eventLogStats()
	if path != logPath || count != 13 || replayed != 0 || failures != 0 {
		t.Fatalf("stats after write: path=%q count=%d replayed=%d failures=%d", path, count, replayed, failures)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if lines := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); lines != 13 {
		t.Fatalf("event log lines=%d want 13", lines)
	}
	// 旧格式 user input 行保持原样（向后兼容）。
	if !strings.Contains(string(raw), `"user_input":"U1"`) {
		t.Fatalf("log 缺少旧格式 user input 行: %q", string(raw))
	}

	snap1 := bridge1.sceneSnapshot()
	want := scene.RenderText(snap1.Cells, snap1.Revision)

	// 新 bridge（模拟会话重启）重放日志：命令/错误注入记录同样恢复。
	bridge2 := newChatRuntimeEventBridge(&ChatSession{})
	bridge2.eventLogPathOverride = logPath
	n, err := bridge2.replayEventLog()
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n != 13 {
		t.Fatalf("replayed=%d want 13", n)
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

// TestRenderLayer_CommandError_EmptyInputsAreNoOps 固化切片 11 的边界：
// 空命令/错误文本不注入（bridge 层零行为），空命令文档与 nil 错误不产生
// cell（coordinator 层零行为）。Scene cell 数保持不变。
func TestRenderLayer_CommandError_EmptyInputsAreNoOps(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)

	// bridge 层：空文本不注入。
	bridge.submitCommand("   ")
	bridge.submitError("")
	if n := len(bridge.sceneSnapshot().Cells); n != 0 {
		t.Fatalf("empty bridge injection produced %d cells, want 0", n)
	}

	// coordinator 层：空命令文档不提交不注入。
	if ok := coord.RenderCommandDocument(render.Document{}); ok {
		t.Fatal("empty command doc unexpectedly accepted")
	}
	// nil 错误不提交不注入。
	coord.RenderError(nil)
	if n := len(bridge.sceneSnapshot().Cells); n != 0 {
		t.Fatalf("empty coordinator blocks produced %d cells, want 0", n)
	}
	if out.Len() != 0 {
		t.Fatalf("empty blocks wrote output: %q", out.String())
	}

	// 有效命令/错误仍然注入（对照基线）。
	if ok := coord.RenderCommandDocument(renderParityCommandDoc()); !ok {
		t.Fatal("valid command doc unexpectedly rejected")
	}
	coord.RenderError(errors.New("boom"))
	if n := len(bridge.sceneSnapshot().Cells); n != 2 {
		t.Fatalf("valid blocks produced %d cells, want 2", n)
	}
}
