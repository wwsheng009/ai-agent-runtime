package ui

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// TestFixedBottomSurface_OverOneScreenOutputNeverDuplicatesHistory reproduces
// the real-screen bug: once committed output exceeds one screen, streaming
// appends and a bottom-pane shrink used to scroll already-rendered history
// lines into the terminal a SECOND time, leaving visible duplicates on screen
// (diagnosed as "24..40, 22" — the 22nd line rendered again below the viewport).
//
// The test replays the REAL render byte stream (captured from os.Stdout while
// the surface paints live) into a vt.Screen and asserts every history row
// appears at most once on the visible screen, and that the newest write is
// visible.
func TestFixedBottomSurface_OverOneScreenOutputNeverDuplicatesHistory(t *testing.T) {
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

	// 阶段 1：单次大写入超过一屏（诊断中的 40 行长回复，一次 WriteOutput）。
	write(chunk(0, 40))

	// 阶段 2：流式追加，模拟逐块到达的回复（每轮 3 行，共 7 轮 → line-61）。
	for i := 0; i < 7; i++ {
		write(chunk(40+i*3, 3))
	}

	// 阶段 3：prompt shrink——底部保留区变大、可见输出区变小，commitExcess
	// 会把更多已渲染行再次滚进 scrollback（诊断中重复渲染的触发点）。
	stream.WriteString(captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
	}))

	// 阶段 4：shrink 之后继续追加（诊断中的下一次写入）。
	write("line-61 这是第61行的长回复内容\n")

	// 回放真实渲染字节流到终端屏幕模型。
	screen := vt.NewScreen(80, 24)
	screen.Feed(stream.String())

	// 断言 1：屏幕上每个历史行至多出现一次（核心：无重复渲染）。
	seen := map[string]int{}
	for row := 1; row <= screen.Height(); row++ {
		text := strings.TrimSpace(screen.Line(row))
		if strings.HasPrefix(text, "line-") {
			seen[text]++
		}
	}
	for text, count := range seen {
		if count > 1 {
			t.Fatalf("历史行重复渲染: %q 在屏幕上出现 %d 次\n屏幕:\n%s",
				text, count, dumpScreen(screen))
		}
	}

	// 断言 2：最新写入的行必须可见（末尾追加不能被滚动丢掉）。
	if _, ok := seen["line-61 这是第61行的长回复内容"]; !ok {
		t.Fatalf("最新写入 line-61 未出现在屏幕上:\n%s", dumpScreen(screen))
	}

	// 断言 3：屏幕从上到下出现的 line 行号必须严格递增——重复渲染会令
	// 旧行号重新出现在新行号之后（诊断证据 "24..40, 22" 即 22 出现在 40 之后）。
	last := -1
	for row := 1; row <= screen.Height(); row++ {
		text := strings.TrimSpace(screen.Line(row))
		if !strings.HasPrefix(text, "line-") {
			continue
		}
		var num int
		if _, err := fmt.Sscanf(text, "line-%d", &num); err != nil {
			continue
		}
		if num <= last {
			t.Fatalf("屏幕行序错乱: 第 %d 行出现 line-%d, 前面已有 line-%d (重复/乱序渲染)\n屏幕:\n%s",
				row, num, last, dumpScreen(screen))
		}
		last = num
	}
}

// TestFixedBottomSurface_UnderScreenWriteIsScrollFree locks INV-SCROLL-03: a
// write that fits entirely inside the visible output region must not emit any
// scroll bytes (no DECSTBM region set, no CSI S/T scroll), must not advance the
// handoff frontier, and the replayed screen must equal the authoritative
// history model. This is the L1 byte-stream assertion the e2e TTY layer cannot
// isolate: the interactive stream mixes prompt/status ANSI with output, so the
// zero-scroll invariant is only provable at the ui layer.
func TestFixedBottomSurface_UnderScreenWriteIsScrollFree(t *testing.T) {
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

	// 阶段 1：多次短写入，总量远小于可见区（3 行 < visible）。
	write("hello-01 first line\n")
	write("hello-02 second line\n")
	write("hello-03 third line\n")

	// 断言 1：字节流不含任何滚动序列。
	// DECSTBM: CSI top;bottom r；CSI S (scroll up): CSI [n]S；
	// CSI T (scroll down): CSI [n]T。
	scrollSeq := regexp.MustCompile(`\x1b\[\d+(?:;\d+)*[rST]`)
	if m := scrollSeq.FindString(stream.String()); m != "" {
		t.Fatalf("未超屏写入产生了滚动序列 %q\n字节流:\n%s",
			m, stream.String())
	}

	// 断言 2：frontier 不推进（没有任何行进入 scrollback 账本）。
	if frontier := surface.HistoryHandedOffForTest(); frontier != 0 {
		t.Fatalf("未超屏写入推进了 handoff frontier: %d", frontier)
	}

	// 断言 3：屏幕内容 == 模型（historyWindow 物理展开）。全帧重绘把保留
	// 历史画在可见区底部，具体起始行受 cursor-parking blank 与 bottomRows
	// 影响，因此先定位模型首行在屏幕上的位置，再逐行比对，避免猜偏移。
	screen := vt.NewScreen(80, 24)
	screen.Feed(stream.String())
	history := surface.HistoryWindowForTest()
	physical := surface.expandHistoryLinesLocked(history)
	if len(physical) > visible {
		t.Fatalf("precondition: physical rows %d must fit visible %d", len(physical), visible)
	}
	firstText := strings.TrimRight(cellsToTextForTest(physical[0]), " ")
	start := -1
	for row := 1; row <= screen.Height(); row++ {
		if strings.TrimRight(screen.Line(row), " ") == firstText {
			start = row
			break
		}
	}
	if start < 1 {
		t.Fatalf("模型首行 %q 未出现在屏幕上:\n%s", firstText, dumpScreen(screen))
	}
	for row := 1; row <= len(physical); row++ {
		want := strings.TrimRight(cellsToTextForTest(physical[row-1]), " ")
		got := strings.TrimRight(screen.Line(start+row-1), " ")
		if got != want {
			t.Fatalf("屏幕第 %d 行与模型不符:\n  got  %q\n  want %q\n屏幕:\n%s",
				start+row-1, got, want, dumpScreen(screen))
		}
	}
}

// TestFixedBottomSurface_OverOneScreenWrappedOutputNeverDuplicatesHistory is
// the narrow-terminal variant: every logical line wraps to multiple physical
// rows (width 40, 50-column lines). Same live sequence as the wide test —
// overflow burst, streaming appends, prompt shrink, final append — and the
// same assertion: each history row may appear at most once on screen, in
// strictly increasing order.
func TestFixedBottomSurface_OverOneScreenWrappedOutputNeverDuplicatesHistory(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(40, 24)
	visible := surface.visibleOutputRowsForTest()
	if visible < 10 {
		t.Fatalf("precondition: visible output rows too small: %d", visible)
	}

	line := func(idx int) string {
		return fmt.Sprintf("wrap-%02d-%s\n", idx, strings.Repeat("x", 44))
	}

	var stream strings.Builder
	write := func(text string) {
		stream.WriteString(captureUIStdout(t, func() {
			surface.WriteOutput(io.Discard, text)
		}))
	}

	// 阶段 1：单次大写入超过一屏（25 行 × 2 物理行 = 50 物理行 > 19 可见）。
	var burst strings.Builder
	for i := 0; i < 25; i++ {
		burst.WriteString(line(i))
	}
	write(burst.String())

	// 阶段 2：流式追加 5 轮 × 2 行（wrap-25..wrap-34）。
	for i := 0; i < 5; i++ {
		var chunk strings.Builder
		for j := 0; j < 2; j++ {
			chunk.WriteString(line(25 + i*2 + j))
		}
		write(chunk.String())
	}

	// 阶段 3：prompt shrink（可见区变小，commitExcess 滚出更多已渲染行）。
	stream.WriteString(captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
	}))

	// 阶段 4：shrink 后继续追加。
	write(line(35))

	screen := vt.NewScreen(40, 24)
	screen.Feed(stream.String())

	// 断言：每个 wrap-NN 行至多出现一次，且屏幕从上到下严格递增。
	seen := map[string]int{}
	last := -1
	for row := 1; row <= screen.Height(); row++ {
		text := strings.TrimSpace(screen.Line(row))
		if !strings.HasPrefix(text, "wrap-") {
			continue
		}
		seen[text]++
		if seen[text] > 1 {
			t.Fatalf("wrapped 历史行重复渲染: %q 出现 %d 次\n屏幕:\n%s",
				text, seen[text], dumpScreen(screen))
		}
		var num int
		if _, err := fmt.Sscanf(text, "wrap-%d", &num); err != nil {
			continue
		}
		if num <= last {
			t.Fatalf("屏幕行序错乱: 第 %d 行出现 wrap-%d, 前面已有 wrap-%d (重复/乱序渲染)\n屏幕:\n%s",
				row, num, last, dumpScreen(screen))
		}
		last = num
	}
}

func dumpScreen(screen *vt.Screen) string {
	var b strings.Builder
	for row := 1; row <= screen.Height(); row++ {
		fmt.Fprintf(&b, "%2d|%s\n", row, screen.Line(row))
	}
	return b.String()
}

// TestFixedBottomSurface_WrappedOverflowScreenContentMatchesModel is a
// STRONGER assertion than the row-uniqueness checks: after the same live
// sequence (overflow burst, streaming appends, prompt shrink, final append),
// the visible scroll region of the replayed screen must equal the last
// `visible` physical rows of the authoritative history model. Any leftover
// duplicate/desync shows up as a content mismatch, not just a duplicate row
// number.
func TestFixedBottomSurface_WrappedOverflowScreenContentMatchesModel(t *testing.T) {
	surface := newOwnedTestFixedBottomSurfaceWithSize(40, 24)
	visible := surface.visibleOutputRowsForTest()
	if visible < 10 {
		t.Fatalf("precondition: visible output rows too small: %d", visible)
	}

	line := func(idx int) string {
		return fmt.Sprintf("wrap-%02d-%s\n", idx, strings.Repeat("x", 44))
	}

	var stream strings.Builder
	write := func(text string) {
		stream.WriteString(captureUIStdout(t, func() {
			surface.WriteOutput(io.Discard, text)
		}))
	}

	// 阶段 1：单次大写入超过一屏（25 行 × 2 物理行 = 50 物理行）。
	var burst strings.Builder
	for i := 0; i < 25; i++ {
		burst.WriteString(line(i))
	}
	write(burst.String())

	// 阶段 2：流式追加 5 轮 × 2 行（wrap-25..wrap-34）。
	for i := 0; i < 5; i++ {
		var chunk strings.Builder
		for j := 0; j < 2; j++ {
			chunk.WriteString(line(25 + i*2 + j))
		}
		write(chunk.String())
	}

	// 阶段 3：prompt shrink。
	stream.WriteString(captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
	}))

	// 阶段 4：shrink 后继续追加。
	write(line(35))

	screen := vt.NewScreen(40, 24)
	screen.Feed(stream.String())

	// 权威模型：historyWindow 按当前宽度展开的物理行。可见行数必须取最终
	// 几何——ShowPrompt 改变了 bottomRows（可见区 23 → 20），若用写入前的
	// visible 断言，会与直写滚动区（regionBottom=20）错位 bottomRows 的
	// 差值。模型与滚动区在同一几何下比较才有一致性（INV-SCROLL-02）。
	history := surface.HistoryWindowForTest()
	physical := surface.expandHistoryLinesLocked(history)
	visible = surface.visibleOutputRowsForTest()
	if len(physical) <= visible {
		t.Fatalf("precondition: physical rows %d must exceed visible %d", len(physical), visible)
	}

	// 屏幕滚动区（1..visible）应显示物理行序列的最后 visible 行。
	modelRows := physical[len(physical)-visible:]
	for row := 1; row <= visible; row++ {
		want := strings.TrimRight(cellsToTextForTest(modelRows[row-1]), " ")
		got := strings.TrimRight(screen.Line(row), " ")
		if got != want {
			t.Fatalf("屏幕第 %d 行与模型不符:\n  got  %q\n  want %q\n屏幕:\n%s",
				row, got, want, dumpScreen(screen))
		}
	}
}

// cellsToTextForTest flattens a physical row of cells to plain text.
func cellsToTextForTest(cells []vt.Cell) string {
	var b strings.Builder
	for _, c := range cells {
		b.WriteString(c.Text)
	}
	return b.String()
}

// TestFixedBottomSurface_DirectWriteBandToggleScreenMatchesModel exercises the
// nastiest interleaving in the current architecture: direct-scroll writes and
// full-frame repaints (ActiveBand grow/shrink) alternate on the same terminal.
// The replayed screen must still equal the authoritative model's last `visible`
// physical rows at every stage — no ghost rows, no leftover duplicates from a
// stale front buffer.
func TestFixedBottomSurface_DirectWriteBandToggleScreenMatchesModel(t *testing.T) {
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
	burst := func(base, n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(fmt.Sprintf("line-%02d 第%d行的正文内容\n", base+i, base+i))
		}
		return b.String()
	}

	// 阶段 1：直写超屏（40 行）。
	write(burst(0, 40))
	// 阶段 2：ActiveBand 出现（全帧重绘路径）。
	stream.WriteString(captureUIStdout(t, func() {
		surface.SetActiveBand([]string{"• Running"})
	}))
	// 阶段 3：band 存在时直写追加。
	write(burst(40, 5))
	// 阶段 4：ActiveBand 消失（重绘 + scroll region 回扩）。
	stream.WriteString(captureUIStdout(t, func() {
		surface.ClearActiveBand()
	}))
	// 阶段 5：band 清除后直写追加。
	write(burst(45, 5))

	// 阶段 6：prompt shrink + 最后追加。
	stream.WriteString(captureUIStdout(t, func() {
		surface.ShowPrompt("> ")
	}))
	write("line-50 第50行的正文内容\n")

	screen := vt.NewScreen(80, 24)
	screen.Feed(stream.String())

	// 权威模型对照。
	history := surface.HistoryWindowForTest()
	physical := surface.expandHistoryLinesLocked(history)
	visibleNow := surface.visibleOutputRowsForTest()
	if len(physical) < visibleNow {
		t.Fatalf("precondition: physical rows %d < visible %d", len(physical), visibleNow)
	}
	modelRows := physical[len(physical)-visibleNow:]
	for row := 1; row <= visibleNow; row++ {
		want := strings.TrimRight(cellsToTextForTest(modelRows[row-1]), " ")
		got := strings.TrimRight(screen.Line(row), " ")
		if got != want {
			t.Fatalf("屏幕第 %d 行与模型不符:\n  got  %q\n  want %q\n屏幕:\n%s",
				row, got, want, dumpScreen(screen))
		}
	}
}
