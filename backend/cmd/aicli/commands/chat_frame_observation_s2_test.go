package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	uirender "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// 本文件是 L3-3 S2（interaction/streaming 族）迁移的观察面 helper。
//
// L3-3 后 SetSurface 安装 facade poster：band/prompt/status 等 facade action
// 不再同步 mutate surface 状态，而是投递给 UI actor（reducer 总会消费，
// 因此 AppState 是权威状态）；旧观察面 captureSurfaceStdout + newScreenVT
// 恒为空屏。按断言语义选择新观察面：
//   - band 内容/样式：coord.uiActor.AppState().Bottom.ActiveBandLines /
//     ActiveBandStyled；
//   - 已提交 transcript（scrollback）：surface.HistoryWindowForTest()（与
//     ComposedFrameForTest 同源的历史窗口）；
//   - 交付到 presenter 的可见屏幕布局由 unified 路径测试覆盖；legacy
//     coordinator 流在本套件中不再有物理绘制面。

// s2BandLines 读取 UI actor AppState 里的权威 band 内容（reducer 对
// SetActiveBandAction 的投影；非 unified 会话下为原始行文本）。
func s2BandLines(t *testing.T, coord *chatInteractionCoordinator) []string {
	t.Helper()
	actor := coord.currentUIActor()
	if actor == nil {
		t.Fatal("ui actor not started")
	}
	return append([]string(nil), actor.AppState().Bottom.ActiveBandLines...)
}

// s2BandStyled 读取 AppState 里的结构化 band 行（保留 dim/role 等样式）。
func s2BandStyled(t *testing.T, coord *chatInteractionCoordinator) []uirender.Line {
	t.Helper()
	actor := coord.currentUIActor()
	if actor == nil {
		t.Fatal("ui actor not started")
	}
	return append([]uirender.Line(nil), actor.AppState().Bottom.ActiveBandStyled...)
}

// s2BandText 是 band 内容的整段文本。
func s2BandText(t *testing.T, coord *chatInteractionCoordinator) string {
	t.Helper()
	return strings.Join(s2BandLines(t, coord), "\n")
}

// s2SettleBand 复刻 settleBandFrame：停止稳定提交定时器、排空队列并强制
// 落一帧，然后等 UI actor 排空（facade action 异步应用）。
func s2SettleBand(coord *chatInteractionCoordinator) {
	coord.mu.Lock()
	coord.stopActiveStableCommitLocked()
	coord.drainActiveStableCommitLocked(true)
	_ = coord.publishActiveStreamFrameLocked(true)
	coord.mu.Unlock()
	coord.waitUIActorIdle()
}

// s2WaitForBandText 轮询 AppState band，直到包含 expected。
func s2WaitForBandText(t *testing.T, coord *chatInteractionCoordinator, expected string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		joined := s2BandText(t, coord)
		if strings.Contains(joined, expected) {
			return joined
		}
		if time.Now().After(deadline) {
			t.Fatalf("active band did not contain %q before timeout; got %q", expected, joined)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// s2HistoryRows 返回 surface 历史窗口的可见行（已提交 transcript）。
// 末尾可能含保留区的分隔空行，调用方按需裁剪。
func s2HistoryRows(surface *ui.FixedBottomSurface) []string {
	return surface.HistoryWindowForTest()
}

// s2TrimLeadingBlanks 去掉历史窗口首部已滚出的空白行。
func s2TrimLeadingBlanks(rows []string) []string {
	start := 0
	for start < len(rows) && strings.TrimSpace(rows[start]) == "" {
		start++
	}
	return rows[start:]
}

// s2TrimTrailingBlanks 去掉历史窗口尾部的空白行。
func s2TrimTrailingBlanks(rows []string) []string {
	end := len(rows)
	for end > 0 && strings.TrimSpace(rows[end-1]) == "" {
		end--
	}
	return rows[:end]
}

// s2BlankRowsBefore 返回 lines 中第一个包含 anchor 的行上方连续空白行数，
// anchor 不存在时返回 -1。
func s2BlankRowsBefore(lines []string, anchor string) int {
	anchorRow := -1
	for i, line := range lines {
		if strings.Contains(line, anchor) {
			anchorRow = i
			break
		}
	}
	if anchorRow < 0 {
		return -1
	}
	gap := 0
	for i := anchorRow - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			break
		}
		gap++
	}
	return gap
}

// s2Count 返回 lines 中包含 marker 的行数。
func s2Count(lines []string, marker string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, marker) {
			n++
		}
	}
	return n
}

// s2AssertNoBlankRun 要求 firstMarker/lastMarker 各出现一次，且两者之间
// （含边界行的行区间）不出现连续两行以上空白——即没有多行空洞。
func s2AssertNoBlankRun(t *testing.T, stage string, lines []string, firstMarker, lastMarker string) {
	t.Helper()
	first := s2RowOfOnce(t, stage, lines, firstMarker)
	last := s2RowOfOnce(t, stage, lines, lastMarker)
	if last < first {
		t.Fatalf("%s: last marker %q (row %d) precedes first marker %q (row %d)\n%s",
			stage, lastMarker, last, firstMarker, first, strings.Join(lines, "\n"))
	}
	run := 0
	for row := first; row <= last; row++ {
		if strings.TrimSpace(lines[row-1]) == "" {
			run++
			if run > 1 {
				t.Fatalf("%s: %d consecutive blank rows ending at row %d inside transcript region\n%s",
					stage, run, row, strings.Join(lines, "\n"))
			}
			continue
		}
		run = 0
	}
}

// s2RowOfOnce 要求 marker 恰好出现一次，返回 1-based 行号。
func s2RowOfOnce(t *testing.T, stage string, lines []string, marker string) int {
	t.Helper()
	rows := make([]int, 0, 1)
	for i, line := range lines {
		if strings.Contains(line, marker) {
			rows = append(rows, i+1)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("%s: expected %q exactly once, got rows %v\n%s",
			stage, marker, rows, strings.Join(lines, "\n"))
	}
	return rows[0]
}

// s2LineIsDim 报告结构化行是否带 dim 属性（holdback/静默文本的语义样式）。
func s2LineIsDim(line uirender.Line) bool {
	if line.Style.Dim {
		return true
	}
	for _, span := range line.Spans {
		if span.Style.Dim {
			return true
		}
	}
	return false
}

// s2RowsWithinWidth 断言每行显示宽度不超过 width（替代旧 VT 屏 OverflowRows）。
func s2RowsWithinWidth(t *testing.T, stage string, rows []string, width int) {
	t.Helper()
	for _, row := range rows {
		w := 0
		for _, r := range row {
			w += uirender.RuneWidth(r)
		}
		if w > width {
			t.Fatalf("%s: row width %d exceeds terminal width %d: %q", stage, w, width, row)
		}
	}
}

// s2MaxBlankRun 返回最长连续空白行长度。
func s2MaxBlankRun(rows []string) int {
	maxRun, run := 0, 0
	for _, row := range rows {
		if strings.TrimSpace(row) == "" {
			run++
			if run > maxRun {
				maxRun = run
			}
			continue
		}
		run = 0
	}
	return maxRun
}

// s2TrailingBlankCount 返回末尾连续空白行数。
func s2TrailingBlankCount(rows []string) int {
	n := 0
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.TrimSpace(rows[i]) != "" {
			break
		}
		n++
	}
	return n
}

// statusModelPlainText 把 AppState 的状态行模型渲染为纯文本（状态行非空断言）。
func statusModelPlainText(model *style.StatusLineModel, width int) string {
	if model == nil {
		return ""
	}
	return style.StatusLineDocument(*model, width).PlainText()
}

// s2RowsEqual 逐行比较。
func s2RowsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// s2EqualAfterDroppingOneBlank 允许序列中恰有一个空行被提交接缝消费
// （流式 stable 提交与一次性 replay 之间唯一允许的单行差），其余必须逐行相同。
func s2EqualAfterDroppingOneBlank(a, b []string) bool {
	if len(a) == len(b) {
		return false
	}
	longer, shorter := a, b
	if len(b) > len(a) {
		longer, shorter = b, a
	}
	if len(longer) != len(shorter)+1 {
		return false
	}
	for i := range longer {
		if longer[i] != "" {
			continue
		}
		cand := append(append([]string(nil), longer[:i]...), longer[i+1:]...)
		if s2RowsEqual(cand, shorter) {
			return true
		}
	}
	return false
}
