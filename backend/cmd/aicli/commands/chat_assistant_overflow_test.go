package commands

// 复现验证：LLM 流式 markdown 回复超过一屏时，最终提交的历史尾部必须
// 保留溢出提示行（与 /debug display 命令文档同一契约），避免用户误以为
// 回复被裁剪/覆盖——完整内容实际已滚入终端滚动缓冲区。
//
// 覆盖两条最终收口路径：
//  1. 全量路径（renderFormattedAssistantStreamLocked，流式期间未提交过前缀）
//  2. residual 路径（writeResidualFormattedAssistantStreamLocked，流式期间
//     已通过稳定提交把前缀写入 history，只补尾部）

import (
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

const assistantOverflowHintMarker = "请向上滚动查看"

// newOverflowTestCoord builds a coordinator with a small surface (24-row
// terminal) and a width-locked markdown formatter, mirroring the fixture in
// chat_debug_display_viewport_test.go.
func newOverflowTestCoord(t *testing.T, width, height int) (*chatInteractionCoordinator, *ui.FixedBottomSurface) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
		Surface:      surface,
	}
	formatter := formatter.NewMarkdownFormatter(false)
	formatter.Width = width
	session.Formatter = formatter
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	coord.SetSurface(surface)
	return coord, surface
}

// paintAssistantFinal runs the final assistant paint under the surface stdout
// capture so terminal writes are observable, then returns the captured bytes.
func paintAssistantFinal(t *testing.T, coord *chatInteractionCoordinator, paint func()) string {
	t.Helper()
	return captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		paint()
	})
}

func historyContainsHint(history []string) bool {
	for _, line := range history {
		if strings.Contains(line, assistantOverflowHintMarker) {
			return true
		}
	}
	return false
}

// TestAssistantMarkdownOverflowHintFullPaint: 长 markdown 回复走全量收口
// 路径（流式期间无稳定前缀提交），提示行必须出现在 history 尾部与可见帧。
func TestAssistantMarkdownOverflowHintFullPaint(t *testing.T) {
	coord, surface := newOverflowTestCoord(t, 80, 24)

	// 30 段 × 每段 4 行 ≈ 120+ 渲染行，远超 24 行终端的可见输出区。
	var body strings.Builder
	for i := 0; i < 30; i++ {
		body.WriteString("## 小节 ")
		body.WriteString(strings.Repeat(string(rune('A'+i%26)), 8))
		body.WriteString("\n\n")
		body.WriteString("这是第 ")
		body.WriteString(strings.Repeat("段", 2))
		body.WriteString(" 的正文内容，包含 **加粗** 与 `inline` 代码。\n")
	}
	markdownText := body.String()

	paintAssistantFinal(t, coord, func() {
		coord.mu.Lock()
		coord.streamMode = assistantStreamModeMarkdown
		coord.renderFormattedAssistantStreamLocked(markdownText)
		coord.mu.Unlock()
	})

	history := surface.HistoryWindowForTest()
	frame := commandResultFrameText(surface)
	t.Logf("historyWindow=%d lines handedOff=%d", len(history), surface.HistoryHandedOffForTest())
	t.Logf("history tail 4=%q", history[maxInt(0, len(history)-4):])
	t.Logf("frame tail:\n%s", frame)

	if !historyContainsHint(history) {
		t.Errorf("history missing overflow hint; tail=%q", history[maxInt(0, len(history)-6):])
	}
	if !strings.Contains(frame, assistantOverflowHintMarker) {
		t.Errorf("visible frame missing hint %q", assistantOverflowHintMarker)
	}
	// 提示行位于 history 尾部（可见区内），而不是被移交。
	if tail := history[len(history)-1]; !strings.Contains(tail, assistantOverflowHintMarker) {
		t.Errorf("hint must be the last history row, got %q", tail)
	}
}

// TestAssistantMarkdownOverflowHintResidual: 流式期间前缀已稳定提交
// （streamRendered=true + streamRenderedPrefixLen>0），最终只补尾部；
// 提示行必须仍出现在尾部，且不重复。
func TestAssistantMarkdownOverflowHintResidual(t *testing.T) {
	coord, surface := newOverflowTestCoord(t, 80, 24)

	var body strings.Builder
	for i := 0; i < 30; i++ {
		body.WriteString("## 小节 ")
		body.WriteString(strings.Repeat(string(rune('A'+i%26)), 8))
		body.WriteString("\n\n")
		body.WriteString("这是第 ")
		body.WriteString(strings.Repeat("段", 2))
		body.WriteString(" 的正文内容。\n")
	}
	markdownText := body.String()

	// 模拟流式中间：前 60% 已通过稳定提交写入 history。
	prefixLen := len(markdownText) * 60 / 100
	paintAssistantFinal(t, coord, func() {
		coord.mu.Lock()
		defer coord.mu.Unlock()
		coord.streamMode = assistantStreamModeMarkdown
		coord.streamRendered = true
		coord.streamRenderedPrefixLen = prefixLen
		coord.streamBuffer.Reset()
		coord.streamBuffer.WriteString(markdownText)
		coord.writeResidualFormattedAssistantStreamLocked(markdownText)
	})

	history := surface.HistoryWindowForTest()
	frame := commandResultFrameText(surface)
	t.Logf("historyWindow=%d lines handedOff=%d", len(history), surface.HistoryHandedOffForTest())
	t.Logf("history tail 4=%q", history[maxInt(0, len(history)-4):])

	if !historyContainsHint(history) {
		t.Errorf("history missing overflow hint; tail=%q", history[maxInt(0, len(history)-6):])
	}
	if !strings.Contains(frame, assistantOverflowHintMarker) {
		t.Errorf("visible frame missing hint %q", assistantOverflowHintMarker)
	}
	if tail := history[len(history)-1]; !strings.Contains(tail, assistantOverflowHintMarker) {
		t.Errorf("hint must be the last history row, got %q", tail)
	}
}

// TestAssistantShortReplyNoHint: 短回复完整落在可见区内，不追加提示。
func TestAssistantShortReplyNoHint(t *testing.T) {
	coord, surface := newOverflowTestCoord(t, 80, 24)

	shortText := "## 小结\n\n这是不超过一屏的简短回复。\n"

	paintAssistantFinal(t, coord, func() {
		coord.mu.Lock()
		coord.streamMode = assistantStreamModeMarkdown
		coord.renderFormattedAssistantStreamLocked(shortText)
		coord.mu.Unlock()
	})

	history := surface.HistoryWindowForTest()
	if historyContainsHint(history) {
		t.Errorf("short reply must not get an overflow hint; history=%q", history)
	}
}
