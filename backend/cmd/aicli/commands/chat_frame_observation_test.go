package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 本文件是 commands 侧的统一测试观察面 helper。
//
// L3-2 起 FixedBottomSurface 的物理绘制（owned viewport 直写）已退役：
// WriteOutput / SetActiveBand / ShowPopup 等只推进状态，不再发射终端字节，
// 因此「回放 stdout 字节到 vt.Screen 再看行」的旧观察面恒为空屏。新的权威
// 观察面是 ComposedFrameForTest()（历史 + 底部保留区的合成帧），它与生产
// 快照路径同源；布局/历史/打印类断言必须读帧，而不是读字节流。

// composedFrameLines 把合成帧渲染为每个终端行一个字符串。
func composedFrameLines(surface *ui.FixedBottomSurface) []string {
	rows := surface.ComposedFrameForTest()
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		var b strings.Builder
		for _, cell := range row {
			if !cell.Cont {
				b.WriteString(cell.Text)
			}
		}
		lines = append(lines, b.String())
	}
	return lines
}

// composedFrameText 是 composedFrameLines 的整帧文本形式。
func composedFrameText(surface *ui.FixedBottomSurface) string {
	return strings.Join(composedFrameLines(surface), "\n")
}

// frameRowsContaining 返回包含 marker 的 1-based 帧行号。
func frameRowsContaining(lines []string, marker string) []int {
	var rows []int
	for i, line := range lines {
		if strings.Contains(line, marker) {
			rows = append(rows, i+1)
		}
	}
	return rows
}

// frameRowOf 要求 marker 在合成帧里恰好出现一次，并返回其 1-based 行号。
func frameRowOf(t *testing.T, surface *ui.FixedBottomSurface, marker string) int {
	t.Helper()
	lines := composedFrameLines(surface)
	rows := frameRowsContaining(lines, marker)
	if len(rows) != 1 {
		t.Fatalf("expected %q exactly once in composed frame, got rows %v\n%s",
			marker, rows, strings.Join(lines, "\n"))
	}
	return rows[0]
}

// assertFrameMarkerOnce 要求 marker 在合成帧中恰好出现一次。
func assertFrameMarkerOnce(t *testing.T, stage string, surface *ui.FixedBottomSurface, marker string) {
	t.Helper()
	text := composedFrameText(surface)
	if count := strings.Count(text, marker); count != 1 {
		t.Fatalf("%s: composed frame marker %q count=%d want 1:\n%s", stage, marker, count, text)
	}
}

// assertComposedFramesEqual 用整帧文本比较 live 与 baseline：布局中性
// （layout neutral）的最强口径就是两者合成帧逐行一致。
func assertComposedFramesEqual(t *testing.T, stage string, got, want []string) {
	t.Helper()
	gotText, wantText := strings.Join(got, "\n"), strings.Join(want, "\n")
	if gotText != wantText {
		t.Fatalf("%s: composed frame differs from baseline\n--- got ---\n%s\n--- want ---\n%s",
			stage, gotText, wantText)
	}
}

// assertNoFrameBlankRun 要求 firstMarker 与 lastMarker（含）之间的帧行不出现
// 连续两行以上空白——即历史回放区没有多行空洞。
func assertNoFrameBlankRun(t *testing.T, stage string, surface *ui.FixedBottomSurface, firstMarker, lastMarker string) {
	t.Helper()
	lines := composedFrameLines(surface)
	first := frameRowOf(t, surface, firstMarker)
	last := frameRowOf(t, surface, lastMarker)
	if last < first {
		t.Fatalf("%s: last marker %q (row %d) precedes first marker %q (row %d)\n%s",
			stage, lastMarker, last, firstMarker, first, strings.Join(lines, "\n"))
	}
	run := 0
	for row := first; row <= last; row++ {
		if strings.TrimSpace(lines[row-1]) == "" {
			run++
			if run > 1 {
				t.Fatalf("%s: %d consecutive blank frame rows ending at row %d inside replay region\n%s",
					stage, run, row, strings.Join(lines, "\n"))
			}
			continue
		}
		run = 0
	}
}
