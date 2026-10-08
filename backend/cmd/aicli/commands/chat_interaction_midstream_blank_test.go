package commands

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// maxBlankRunAboveBottom returns the largest run of consecutive blank rows in
// [1, bottomExclusive). bottomExclusive is typically the first bottom-pane row.
func maxBlankRunAboveBottom(screen *screenVT, bottomExclusive int) (maxRun int, startRow int) {
	return screen.MaxBlankRun(bottomExclusive)
}

// gapBetweenLastScrollbackAndBand measures blank rows between the last
// non-empty row above the band and the first non-empty band row.
func gapBetweenLastScrollbackAndBand(screen *screenVT, bandStart, bandEnd int) int {
	lastScrollback := screen.LastNonBlankRowAbove(bandStart)
	firstBand := 0
	for row := bandStart; row <= bandEnd; row++ {
		if strings.TrimSpace(screen.line(row)) != "" {
			firstBand = row
			break
		}
	}
	if lastScrollback == 0 || firstBand == 0 {
		return -1
	}
	return firstBand - lastScrollback - 1
}

// TestChatInteractionCoordinator_MidStreamActiveBandLeavesNoBlankGap reproduces
// the user-reported failure: during a single long markdown reply (no resize),
// about ActiveBandMaxRows blank rows appear between prior transcript and the
// live band. Prior tests only asserted post-finalize adjacency and "band
// reaches status", missing mid-stream holes above the band.
func TestChatInteractionCoordinator_MidStreamActiveBandLeavesNoBlankGap(t *testing.T) {
	const width = 80
	height := 48 // tall enough for ActiveBandMaxRows == 14
	wantBandBudget := ui.ActiveBandRows(height)
	if wantBandBudget != ui.ActiveBandMaxRows {
		t.Fatalf("precondition: height %d should budget %d band rows, got %d",
			height, ui.ActiveBandMaxRows, wantBandBudget)
	}

	// Build a reply that repeatedly promotes stable blocks into scrollback while
	// retaining only the mutable tail in ActiveBand.
	var reply strings.Builder
	reply.WriteString("# 长回复\n\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&reply, "## 章节 %d\n\n这是第 %d 段正文，用来撑满 ActiveBand。\n\n", i, i)
	}
	reply.WriteString("收尾段落。\n")

	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newTestChatInteractionCoordinator(t, session)
	coord.stableCommitDelay = time.Hour
	t.Cleanup(coord.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)

	// Seed scrollback so a reserve-growth hole is visible against real content.
	coord.SetWriter(os.Stdout)
	surface.ShowPrompt("> ")
	surface.ClearPromptRows(1)
	for i := 1; i <= 20; i++ {
		coord.RenderAsyncLine(fmt.Sprintf("seed-line-%02d prior transcript", i))
	}

	// Stream the long reply in small chunks and inspect AFTER the band has
	// grown, still mid-stream (before finalize).
	content := reply.String()
	// Character-ish chunks to force many band growth/paint steps.
	for len(content) > 0 {
		runes := []rune(content)
		n := 24
		if n > len(runes) {
			n = len(runes)
		}
		chunk := string(runes[:n])
		content = string(runes[n:])
		coord.RenderAssistantDelta(chunk)
	}
	coord.mu.Lock()
	coord.stopActiveStableCommitLocked()
	coord.drainActiveStableCommitLocked(true)
	coord.mu.Unlock()
	coord.waitUIActorIdle()

	// L3-3：band 与已提交 transcript 分属 AppState 与历史窗口（band 的物理
	// 屏面已退役）。逐项断言原语义：band 有界且含最新 mutable tail；旧稳定块
	// 在 scrollback；历史区/接缝没有 band 级空白洞。
	band := s2BandLines(t, coord)
	if len(band) == 0 {
		t.Fatal("expected active band mid-stream")
	}
	if len(band) > wantBandBudget {
		t.Fatalf("active tail exceeded budget %d, got %d lines: %v", wantBandBudget, len(band), band)
	}
	if joined := strings.Join(band, "\n"); !strings.Contains(joined, "收尾段落") {
		t.Fatalf("expected newest mutable tail in ActiveBand, got %v", band)
	}
	if strings.TrimSpace(band[len(band)-1]) == "" {
		t.Fatalf("band tail row must stay painted, got %v", band)
	}

	history := s2TrimLeadingBlanks(s2HistoryRows(surface))
	if n := s2Count(history, "章节 29"); n != 1 {
		t.Fatalf("expected older stable block 章节 29 exactly once in history window, got %d\n%#v", n, history)
	}
	if run := s2MaxBlankRun(s2TrimTrailingBlanks(history)); run >= ui.ActiveBandMinRows {
		t.Fatalf("mid-stream history window has band-sized blank run %d (>= min band %d)\n%#v",
			run, ui.ActiveBandMinRows, history)
	}
	// 历史尾部与 band 之间最多一个分隔空行。
	if gap := s2TrailingBlankCount(history); gap > 1 {
		t.Fatalf("mid-stream blank gap above active band = %d (band rows=%d budget=%d)\n%#v",
			gap, len(band), wantBandBudget, history)
	}

	// Finalize must still keep transcript adjacent to the restored prompt.
	coord.FinalizeAssistantDelta()
	surface.ShowPrompt("> ")
	coord.waitUIActorIdle()
	if got := len(s2BandLines(t, coord)); got != 0 {
		t.Fatalf("finalize should clear active band, still %d lines", got)
	}
	finalRows := s2TrimLeadingBlanks(s2HistoryRows(surface))
	if n := s2Count(finalRows, "收尾段落"); n != 1 {
		t.Fatalf("expected finalized tail 收尾段落 exactly once, got %d\n%#v", n, finalRows)
	}
	if run := s2MaxBlankRun(s2TrimTrailingBlanks(finalRows)); run > 1 {
		t.Fatalf("post-finalize transcript left a %d-row blank hole\n%#v", run, finalRows)
	}
	if gap := s2TrailingBlankCount(finalRows); gap > 2 {
		t.Fatalf("post-finalize gap above prompt = %d\n%#v", gap, finalRows)
	}
}

// TestFixedBottomSurface_MidStreamBandGrowthKeepsScrollbackAdjacent is the
// surface-only variant: grow the band one row at a time up to the max budget
// and assert no hole opens between prior WriteOutput content and the band.
func TestFixedBottomSurface_MidStreamBandGrowthKeepsScrollbackAdjacent(t *testing.T) {
	const width = 80
	height := 48
	budget := ui.ActiveBandRows(height)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	// 该 surface 无 coordinator poster，facade 走同步路径；L3-2 后物理绘制
	// 退役，权威观察面是 ComposedFrameForTest（历史 + band + 保留区）。
	surface.ShowPrompt("> ")
	surface.ClearPromptRows(1)
	for i := 1; i <= 25; i++ {
		if _, err, ok := surface.WriteOutput(io.Discard, fmt.Sprintf("prior-%02d\n", i)); !ok || err != nil {
			t.Fatalf("WriteOutput prior: ok=%v err=%v", ok, err)
		}
	}

	lines := make([]string, 0, budget)
	for row := 1; row <= budget; row++ {
		lines = append(lines, fmt.Sprintf("band-row-%02d", row))
		if !surface.SetActiveBand(lines) {
			t.Fatal("SetActiveBand failed")
		}
		frame := composedFrameLines(surface)
		bandStart := frameRowOf(t, surface, "band-row-01")
		priorRow := frameRowOf(t, surface, "prior-25")
		if gap := bandStart - priorRow - 1; gap > 1 {
			t.Fatalf("after growing to %d band rows, gap above band = %d\n%s",
				len(lines), gap, composedFrameText(surface))
		}
		if run := s2MaxBlankRun(frame[priorRow-1 : bandStart-1]); run >= ui.ActiveBandMinRows {
			t.Fatalf("after growing to %d band rows, blank run %d above band\n%s",
				len(lines), run, composedFrameText(surface))
		}
		for _, bandLine := range lines {
			assertFrameMarkerOnce(t, fmt.Sprintf("band growth %d", len(lines)), surface, bandLine)
		}
	}
}

// A stable-prefix promotion writes into the old output region before the
// mutable tail contracts. The contraction must reclaim the released rows
// without leaving an extra cursor-park row between scrollback and ActiveBand.
func TestFixedBottomSurface_StableCommitThenBandShrinkKeepsAdjacency(t *testing.T) {
	const width, height = 80, 48
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)

	for i := 1; i <= 20; i++ {
		if _, err, ok := surface.WriteOutput(io.Discard, fmt.Sprintf("prior-%02d\n", i)); !ok || err != nil {
			t.Fatalf("WriteOutput prior: ok=%v err=%v", ok, err)
		}
	}
	if !surface.SetActiveBand([]string{"assistant", "one", "two", "three", "four", "five"}) {
		t.Fatal("SetActiveBand failed")
	}

	if _, err, ok := surface.WriteOutput(io.Discard, "heading\n\nbody\n"); !ok || err != nil {
		t.Fatalf("WriteOutput stable prefix: ok=%v err=%v", ok, err)
	}
	if !surface.SetActiveBand([]string{"assistant", "mutable tail"}) {
		t.Fatal("SetActiveBand shrink failed")
	}

	// 收缩后的合成帧：提交前缀（heading/body）在 band 上方相邻，旧 band 行
	// 不得残留或重复，新 mutable tail 恰好一次。
	bandRow := frameRowOf(t, surface, "assistant")
	bodyRow := frameRowOf(t, surface, "body")
	if gap := bandRow - bodyRow - 1; gap > 1 {
		t.Fatalf("stable commit + band shrink left gap=%d\n%s", gap, composedFrameText(surface))
	}
	assertFrameMarkerOnce(t, "shrink", surface, "mutable tail")
	for _, released := range []string{"one", "two", "three", "four", "five"} {
		if rows := frameRowsContaining(composedFrameLines(surface), released); len(rows) != 0 {
			t.Fatalf("released band row %q still in composed frame at %v\n%s",
				released, rows, composedFrameText(surface))
		}
	}
}

// TestFixedBottomSurface_EOSFusionLeavesNoBlankGap pins the stream-end
// ActiveBand fusion window:
//
//	prior WriteOutput → full band → commit final while band still up
//	→ ClearActiveBand → ShowPrompt
//
// Large holes here are reserve shrink failures (freed band rows left blank),
// not markdown spacing. Intermediate commit-with-band must also keep the final
// transcript adjacent to the live band.
func TestFixedBottomSurface_EOSFusionLeavesNoBlankGap(t *testing.T) {
	const width = 80
	height := 48
	budget := ui.ActiveBandRows(height)
	if budget != ui.ActiveBandMaxRows {
		t.Fatalf("precondition: height %d should budget %d, got %d", height, ui.ActiveBandMaxRows, budget)
	}

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	// 无 coordinator poster：facade 走同步路径；物理绘制退役后由合成帧观察布局。
	surface.ShowPrompt("> ")
	surface.ClearPromptRows(1)
	for i := 1; i <= 20; i++ {
		if _, err, ok := surface.WriteOutput(io.Discard, fmt.Sprintf("prior-%02d\n", i)); !ok || err != nil {
			t.Fatalf("WriteOutput prior: ok=%v err=%v", ok, err)
		}
	}

	bandLines := make([]string, 0, budget)
	for i := 1; i <= budget; i++ {
		bandLines = append(bandLines, fmt.Sprintf("live-band-%02d", i))
	}
	if !surface.SetActiveBand(bandLines) {
		t.Fatal("SetActiveBand failed")
	}

	// Step 1: commit final transcript while the band is still reserved.
	if _, err, ok := surface.WriteOutput(io.Discard, "final-committed-line\n"); !ok || err != nil {
		t.Fatalf("WriteOutput final: ok=%v err=%v", ok, err)
	}

	frame := composedFrameLines(surface)
	bandStart := frameRowOf(t, surface, "live-band-01")
	// 最后一次提交（final-committed-line）是 band 上方最后一行 transcript。
	lastScrollback := 0
	for row := bandStart - 1; row >= 1; row-- {
		if strings.TrimSpace(frame[row-1]) != "" {
			lastScrollback = row
			break
		}
	}
	if lastScrollback == 0 {
		t.Fatalf("expected scrollback above band\n%s", composedFrameText(surface))
	}
	if gap := bandStart - lastScrollback - 1; gap > 1 {
		t.Fatalf("after commit-with-band, gap above band = %d (budget=%d)\n%s",
			gap, budget, composedFrameText(surface))
	}
	if run := s2MaxBlankRun(frame[lastScrollback-1 : bandStart-1]); run >= ui.ActiveBandMinRows {
		t.Fatalf("after commit-with-band, blank run %d above band\n%s", run, composedFrameText(surface))
	}
	if finalRow := frameRowOf(t, surface, "final-committed-line"); finalRow >= bandStart {
		t.Fatalf("expected final line in scrollback above band (row %d, band start %d)\n%s",
			finalRow, bandStart, composedFrameText(surface))
	}

	// Step 2: release the band — freed rows must be reclaimed by scroll-down.
	if !surface.ClearActiveBand() {
		t.Fatal("ClearActiveBand failed")
	}
	// Owned path recomposes the full frame; assert the final frame content instead of CSI T.
	frame = composedFrameLines(surface)
	for _, bandLine := range bandLines {
		if rows := frameRowsContaining(frame, bandLine); len(rows) != 0 {
			t.Fatalf("released band row %q still in composed frame at %v\n%s",
				bandLine, rows, composedFrameText(surface))
		}
	}
	if got := len(surface.ActiveBandLines()); got != 0 {
		t.Fatalf("expected band cleared, still %d lines", got)
	}
	// 只检查第一行内容与 status 之间的空洞；transcript 短于可见区时屏幕
	// 顶部必然留白，那不是 band 收缩失败。
	contentStart := 0
	for i, line := range frame[:len(frame)-1] {
		if strings.TrimSpace(line) != "" {
			contentStart = i
			break
		}
	}
	if run := s2MaxBlankRun(frame[contentStart : len(frame)-1]); run > 1 {
		t.Fatalf("after ClearActiveBand, blank run %d above status\n%s", run, composedFrameText(surface))
	}
	trimmed := s2TrimTrailingBlanks(s2TrimLeadingBlanks(frame))
	if len(trimmed) == 0 {
		t.Fatalf("expected transcript after band release\n%s", composedFrameText(surface))
	}
	// No prompt yet: content should sit against status with at most one blank.
	if gap := s2TrailingBlankCount(frame); gap > 1 {
		t.Fatalf("after ClearActiveBand, gap above status = %d (budget=%d)\n%s",
			gap, budget, composedFrameText(surface))
	}

	// Step 3: restore prompt — still no band-sized hole above it.
	if !surface.ShowPrompt("> ") {
		t.Fatal("ShowPrompt failed")
	}
	frame = composedFrameLines(surface)
	promptRow := frameRowOf(t, surface, ">")
	if promptRow != height-2 {
		t.Fatalf("expected prompt on frame row %d, got row %d\n%s",
			height-2, promptRow, composedFrameText(surface))
	}
	// 与上一步同口径：只检查内容区内的空洞，顶部自然留白不计。
	contentStart = 0
	for i, line := range frame[:promptRow-1] {
		if strings.TrimSpace(line) != "" {
			contentStart = i
			break
		}
	}
	if run := s2MaxBlankRun(frame[contentStart : promptRow-1]); run >= ui.ActiveBandMinRows {
		t.Fatalf("after ShowPrompt, blank run %d above prompt\n%s", run, composedFrameText(surface))
	}
	if gap := s2TrailingBlankCount(frame[:promptRow-1]); gap > 2 {
		t.Fatalf("after ShowPrompt, gap above prompt = %d (budget=%d)\n%s",
			gap, budget, composedFrameText(surface))
	}
}

// TestChatInteractionCoordinator_PendingStableQueueKeepsBandFilled covers the
// production failure mode where CommitStablePrefix ran at enqueue time while the
// animated stable-commit queue still held the lines. With stableCommitManual the
// queue stays pending; the live band must keep showing that content (no hole)
// until an explicit drain writes scrollback and then shrinks the band.
//
// Plain text is used so a few overflow lines enqueue without tripping catch-up
// depth (which would auto-drain and hide the pending-queue window).
func TestChatInteractionCoordinator_PendingStableQueueKeepsBandFilled(t *testing.T) {
	const width = 40
	height := 24
	budget := ui.ActiveBandRows(height)

	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)
	coord.stableCommitDelay = time.Hour
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)

	coord.SetWriter(os.Stdout)
	surface.ShowPrompt("> ")
	surface.ClearPromptRows(1)
	for i := 1; i <= 10; i++ {
		coord.RenderAsyncLine(fmt.Sprintf("seed-prior-%02d", i))
	}

	coord.RenderAssistantDelta("one\ntwo\nthree\nfour\nfive\nsix\nseven\n")
	coord.RenderAssistantDelta("eight\n")
	coord.waitUIActorIdle()

	coord.mu.Lock()
	queued := len(coord.stableCommitQueue)
	enqueued := coord.streamEnqueuedPrefixLen
	emitted := coord.streamRenderedPrefixLen
	coord.mu.Unlock()
	if queued == 0 || enqueued <= emitted {
		t.Fatalf("precondition: expected pending stable queue, queued=%d enqueued=%d emitted=%d",
			queued, enqueued, emitted)
	}

	band := s2BandLines(t, coord)
	if len(band) == 0 {
		t.Fatal("pending queue must keep ActiveBand filled")
	}
	joined := strings.Join(band, "\n")
	if !strings.Contains(joined, "one") || !strings.Contains(joined, "eight") {
		t.Fatalf("queued stable rows must remain in ActiveBand until drain, got %q", joined)
	}
	history := s2TrimLeadingBlanks(s2HistoryRows(surface))
	if run := s2MaxBlankRun(s2TrimTrailingBlanks(history)); run >= ui.ActiveBandMinRows {
		t.Fatalf("pending queue left blank run %d in history window (budget=%d)\n%#v",
			run, budget, history)
	}
	if gap := s2TrailingBlankCount(history); gap > 1 {
		t.Fatalf("pending queue left gap=%d above band (budget=%d)\n%#v", gap, budget, history)
	}

	// Drain must move queued lines to scrollback and shrink the band without a hole.
	coord.mu.Lock()
	coord.stopActiveStableCommitLocked()
	coord.drainActiveStableCommitLocked(true)
	coord.mu.Unlock()
	coord.waitUIActorIdle()

	band = s2BandLines(t, coord)
	joined = strings.Join(band, "\n")
	if strings.Contains(joined, "one") {
		t.Fatalf("emitted stable prefix should leave ActiveBand after drain, got %q", joined)
	}
	// "eight" may still be mutable tail or last retained rows depending on cut.
	// Require a non-empty band so the viewport did not collapse to a hole.
	if len(band) == 0 {
		t.Fatal("expected mutable tail remaining in band after drain")
	}
	history = s2TrimLeadingBlanks(s2HistoryRows(surface))
	if n := s2Count(history, "one"); n != 1 {
		t.Fatalf("drained stable row one should land in scrollback exactly once, got %d\n%#v", n, history)
	}
	if gap := s2TrailingBlankCount(history); gap > 1 {
		t.Fatalf("after drain, gap above band = %d\n%#v", gap, history)
	}
}

// TestChatInteractionCoordinator_EOSFusionAfterFullBand drives the real
// coordinator path: stream until the live band is full, finalize (commit+clear),
// restore prompt, and require no band-sized hole above the prompt.
func TestChatInteractionCoordinator_EOSFusionAfterFullBand(t *testing.T) {
	const width = 80
	height := 48
	budget := ui.ActiveBandRows(height)
	if budget != ui.ActiveBandMaxRows {
		t.Fatalf("precondition: height %d should budget %d, got %d", height, ui.ActiveBandMaxRows, budget)
	}

	var reply strings.Builder
	reply.WriteString("# 结束融合\n\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&reply, "## 段 %d\n\n正文 %d 用于撑满 ActiveBand。\n\n", i, i)
	}
	reply.WriteString("融合收尾。\n")

	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newTestChatInteractionCoordinator(t, session)
	coord.stableCommitDelay = time.Hour
	t.Cleanup(coord.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)

	coord.SetWriter(os.Stdout)
	surface.ShowPrompt("> ")
	surface.ClearPromptRows(1)
	for i := 1; i <= 12; i++ {
		coord.RenderAsyncLine(fmt.Sprintf("seed-%02d", i))
	}

	content := reply.String()
	for len(content) > 0 {
		runes := []rune(content)
		n := 32
		if n > len(runes) {
			n = len(runes)
		}
		coord.RenderAssistantDelta(string(runes[:n]))
		content = string(runes[n:])
	}
	coord.waitUIActorIdle()
	band := s2BandLines(t, coord)
	if got := len(band); got == 0 || got > budget {
		t.Fatalf("expected bounded mutable tail before finalize, got %d want 1..%d", got, budget)
	}
	if !strings.Contains(strings.Join(band, "\n"), "融合收尾") {
		t.Fatalf("expected final mutable paragraph in ActiveBand, got %v", band)
	}

	// Finalize alone: commit + ClearActiveBand (no prompt yet).
	coord.FinalizeAssistantDelta()
	coord.waitUIActorIdle()
	if got := len(s2BandLines(t, coord)); got != 0 {
		t.Fatalf("finalize should clear active band, still %d lines", got)
	}
	history := s2TrimLeadingBlanks(s2HistoryRows(surface))
	if n := s2Count(history, "融合收尾"); n != 1 {
		t.Fatalf("expected committed 融合收尾 exactly once after finalize, got %d\n%#v", n, history)
	}
	if run := s2MaxBlankRun(s2TrimTrailingBlanks(history)); run >= ui.ActiveBandMinRows {
		t.Fatalf("after finalize, blank run %d in history window\n%#v", run, history)
	}
	if gap := s2TrailingBlankCount(history); gap > 1 {
		t.Fatalf("after finalize(clear band), gap above status = %d\n%#v", gap, history)
	}

	// A later output is the user-visible failure mode: if release only clears
	// the band without pulling the transcript down, WriteOutput jumps to the new
	// output bottom and leaves the released 14 rows as a hole in the middle.
	coord.RenderAsyncLine("post-release-output")
	history = s2TrimLeadingBlanks(s2HistoryRows(surface))
	if n := s2Count(history, "post-release-output"); n != 1 {
		t.Fatalf("expected continued output after band release exactly once, got %d\n%#v", n, history)
	}
	if run := s2MaxBlankRun(s2TrimTrailingBlanks(history)); run >= ui.ActiveBandMinRows {
		t.Fatalf("continued output exposed a post-release blank run %d\n%#v", run, history)
	}

	if !surface.ShowPrompt("> ") {
		t.Fatal("ShowPrompt failed")
	}
	coord.waitUIActorIdle()
	st := coord.uiActor.AppState()
	if !st.Bottom.PromptVisible || !strings.HasPrefix(st.Bottom.PromptLine, ">") {
		t.Fatalf("expected prompt visible in AppState after ShowPrompt, got %+v", st.Bottom)
	}
	history = s2TrimLeadingBlanks(s2HistoryRows(surface))
	if n := s2Count(history, "融合收尾"); n != 1 {
		t.Fatalf("expected transcript above prompt exactly once, got %d\n%#v", n, history)
	}
	if gap := s2TrailingBlankCount(history); gap > 2 {
		t.Fatalf("after ShowPrompt, gap above prompt = %d\n%#v", gap, history)
	}
	if run := s2MaxBlankRun(s2TrimTrailingBlanks(history)); run >= ui.ActiveBandMinRows {
		t.Fatalf("after ShowPrompt, blank run %d\n%#v", run, history)
	}
}
