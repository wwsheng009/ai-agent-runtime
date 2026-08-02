package ui

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// TestFixedBottomSurface_BandShrinkRestoreNeverReplaysHandedOffHistory
// reproduces the second class of real-screen duplication: rows that already
// entered native scrollback must never be painted into the visible region
// again by a later full-frame repaint.
//
// Root cause: composedPlanLocked builds the frame from the ENTIRE retained
// historyWindow, and historyWindow dual-retains rows that have already been
// handed off to scrollback (headroom for shrink restore). When the ActiveBand
// disappears (scroll region grows back), repaintActiveBandLocked runs a full
// renderOwnedViewportLocked; the diffing Flush compares the new frame against
// the stale front buffer (which still holds the pre-scroll rows) and repaints
// the handed-off rows into the visible region a SECOND time.
//
// A vt.Screen cannot see this: scrolled-out rows leave the simulated screen,
// so a restore repaint looks like a fresh appearance. Only byte-stream
// analysis (each history row text written to the terminal at most once)
// catches the duplication — the same row exists once in native scrollback and
// once on screen.
func TestFixedBottomSurface_BandShrinkRestoreNeverReplaysHandedOffHistory(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(80, 24)
	visible := surface.visibleOutputRowsForTest()
	if visible < 10 {
		t.Fatalf("precondition: visible output rows too small: %d", visible)
	}

	var stream strings.Builder
	write := func(text string) {
		stream.WriteString(captureUIStdout(t, func() {
			surface.WriteOutput(io.Discard, text)
		}))
	}
	chunk := func(base, n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			idx := base + i
			b.WriteString(fmt.Sprintf("line-%02d 这是第%02d行的长回复内容\n", idx, idx))
		}
		return b.String()
	}

	// 阶段 1：单次大写入超过一屏 → 行 1..N 滚入 native scrollback。
	write(chunk(0, 40))
	handedOff := surface.HistoryHandedOffForTest()
	t.Logf("阶段1后: frontier=%d window=%d visible=%d", handedOff, len(surface.HistoryWindowForTest()), surface.visibleOutputRowsForTest())
	if handedOff < 5 {
		t.Fatalf("precondition: expected history handed off to scrollback, got %d", handedOff)
	}

	// 阶段 2：ActiveBand 出现（工具调用工作状态流）→ 滚动区变小。
	stream.WriteString(captureUIStdout(t, func() {
		surface.SetActiveBand([]string{"band-run-1", "band-run-2", "band-run-3", "band-run-4", "band-run-5", "band-run-6"})
	}))
	t.Logf("阶段2后: frontier=%d window=%d visible=%d", surface.HistoryHandedOffForTest(), len(surface.HistoryWindowForTest()), surface.visibleOutputRowsForTest())

	// 阶段 3：band 存在期间继续输出 → commitExcess 把更多行滚入 scrollback。
	write(chunk(40, 12))
	t.Logf("阶段3后: frontier=%d window=%d visible=%d", surface.HistoryHandedOffForTest(), len(surface.HistoryWindowForTest()), surface.visibleOutputRowsForTest())

	// 阶段 4：band 消失（工作状态流结束）→ 滚动区变大 → 全帧重绘恢复。
	stream.WriteString(captureUIStdout(t, func() {
		surface.ClearActiveBand()
	}))
	t.Logf("阶段4后: frontier=%d window=%d visible=%d", surface.HistoryHandedOffForTest(), len(surface.HistoryWindowForTest()), surface.visibleOutputRowsForTest())

	// 阶段 5：恢复后追加输出（下一轮流式写入）。
	write(chunk(52, 3))

	// scrollback 感知断言：每个历史行文本作为完整行内容被写入终端的
	// 次数至多一次。>1 意味着该行已滚入 scrollback 后又以"行内容"形式
	// 被重画进可见区 —— 用户看到同一行在 scrollback 与屏幕各一份。
	duplicates := []string{}
	raw := stream.String()
	for i := 0; i <= 55; i++ {
		lineText := fmt.Sprintf("line-%02d 这是第%02d行的长回复内容", i, i)
		if n := strings.Count(raw, lineText); n > 1 {
			duplicates = append(duplicates, fmt.Sprintf("%s ×%d", lineText, n))
		}
	}
	if len(duplicates) > 0 {
		// 诊断：打印第一个重复行的每次出现上下文（定位写入路径）。
		var diag strings.Builder
		for i := 0; i <= 55; i++ {
			lineText := fmt.Sprintf("line-%02d 这是第%02d行的长回复内容", i, i)
			if strings.Count(raw, lineText) <= 1 {
				continue
			}
			diag.WriteString(fmt.Sprintf("== %s (%d 次) ==\n", lineText, strings.Count(raw, lineText)))
			idx := 0
			for k := 0; k < strings.Count(raw, lineText); k++ {
				pos := strings.Index(raw[idx:], lineText) + idx
				start := pos - 60
				if start < 0 {
					start = 0
				}
				end := pos + len(lineText) + 40
				if end > len(raw) {
					end = len(raw)
				}
				diag.WriteString(fmt.Sprintf("  #%d @%d: %q\n", k+1, pos, strings.ReplaceAll(raw[start:end], "\x1b", "<ESC>")))
				idx = pos + len(lineText)
			}
			break
		}
		t.Fatalf("已滚入 scrollback 的历史行被再次渲染上屏（scrollback+屏幕重复）:\n%s\n%s", strings.Join(duplicates, "\n"), diag.String())
	}

	// 可见区完整性：最新写入必须可见（恢复过程不能弄丢内容）。
	screen := vt.NewScreen(80, 24)
	screen.Feed(stream.String())
	latest := "line-54 这是第54行的长回复内容"
	found := false
	for row := 1; row <= screen.Height(); row++ {
		if strings.TrimSpace(screen.Line(row)) == latest {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("最新写入 line-54 未出现在屏幕上:\n%s", dumpScreen(screen))
	}
}
