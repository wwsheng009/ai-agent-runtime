package commands

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

var ansiSeqRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]|\x1b\][^\x07]*\x07|\x1b\\`)

func stripTerminalDecorations(s string) string {
	return ansiSeqRe.ReplaceAllString(s, "")
}

func maxBlankLineRun(s string) int {
	plain := stripTerminalDecorations(s)
	maxRun, run := 0, 0
	for _, line := range strings.Split(plain, "\n") {
		if strings.TrimSpace(line) == "" {
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

func sampleMultiBlockMarkdown() string {
	var b strings.Builder
	b.WriteString("# 长回复\n\n")
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&b, "## 章节 %d\n\n这是第 %d 段正文，包含一点说明。\n\n- 项目 A\n- 项目 B\n\n", i, i)
	}
	b.WriteString("```go\nfunc Hello() {}\n```\n\n收尾段落。\n")
	return b.String()
}

// TestChatInteractionCoordinator_LiveStreamBlankParityWithReplay compares the
// transcript text produced by progressive markdown streaming (surface on,
// stable commits) against one-shot RenderAssistant / history replay. Extra
// blank runs OR missing inter-block blanks on the live path are regressions.
func TestChatInteractionCoordinator_LiveStreamBlankParityWithReplay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)
	src := sampleMultiBlockMarkdown()

	// History / one-shot complete block.
	histSession := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	histCoord := newChatInteractionCoordinator(histSession)
	t.Cleanup(histCoord.Shutdown)
	var histOut bytes.Buffer
	histCoord.SetWriter(&histOut)
	histCoord.RenderAssistant(src)

	// Live progressive stream with surface + deferred stable commits.
	liveSession := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	liveCoord := newChatInteractionCoordinator(liveSession)
	liveCoord.stableCommitDelay = time.Hour
	t.Cleanup(liveCoord.Shutdown)
	var liveOut bytes.Buffer
	liveCoord.SetWriter(&liveOut)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 48)
	liveCoord.SetSurface(surface)

	content := src
	for len(content) > 0 {
		n := 19
		if n > len(content) {
			n = len(content)
		}
		// Avoid splitting UTF-8 continuation bytes.
		for n < len(content) && content[n]&0xc0 == 0x80 {
			n++
		}
		liveCoord.RenderAssistantDelta(content[:n])
		content = content[n:]
	}
	liveCoord.mu.Lock()
	liveCoord.stopActiveStableCommitLocked()
	liveCoord.drainActiveStableCommitLocked(true)
	liveCoord.mu.Unlock()
	liveCoord.FinalizeAssistantDelta()

	histPlain := stripTerminalDecorations(histOut.String())
	livePlain := stripTerminalDecorations(liveOut.String())
	histMax := maxBlankLineRun(histPlain)
	liveMax := maxBlankLineRun(livePlain)

	t.Logf("history maxBlank=%d lines=%d len=%d", histMax, strings.Count(histPlain, "\n")+1, len(histPlain))
	t.Logf("live    maxBlank=%d lines=%d len=%d", liveMax, strings.Count(livePlain, "\n")+1, len(livePlain))

	if liveMax > histMax {
		// Dump a compact line-diff for diagnosis.
		hLines := strings.Split(histPlain, "\n")
		lLines := strings.Split(livePlain, "\n")
		var diff strings.Builder
		max := len(hLines)
		if len(lLines) > max {
			max = len(lLines)
		}
		shown := 0
		for i := 0; i < max && shown < 40; i++ {
			var h, l string
			if i < len(hLines) {
				h = hLines[i]
			} else {
				h = "<missing>"
			}
			if i < len(lLines) {
				l = lLines[i]
			} else {
				l = "<missing>"
			}
			if h != l {
				fmt.Fprintf(&diff, "%03d H:%q\n    L:%q\n", i, h, l)
				shown++
			}
		}
		t.Fatalf("live stream has larger blank run (%d > history %d)\nhistory:\n%s\nlive:\n%s\ndiff:\n%s",
			liveMax, histMax, histPlain, livePlain, diff.String())
	}
	if liveMax < histMax {
		t.Fatalf("live stream lost blank runs (%d < history %d)\nhistory:\n%s\nlive:\n%s",
			liveMax, histMax, histPlain, livePlain)
	}
	if blankLineCount(livePlain) != blankLineCount(histPlain) {
		t.Fatalf("live/history blank line counts differ: live=%d history=%d\nhistory:\n%s\nlive:\n%s",
			blankLineCount(livePlain), blankLineCount(histPlain), histPlain, livePlain)
	}

	// Content should still contain the same key fragments exactly once.
	for _, frag := range []string{"长回复", "章节 1", "章节 6", "func Hello() {}", "收尾段落。"} {
		if strings.Count(livePlain, frag) != 1 {
			t.Fatalf("live transcript should contain %q exactly once, got %q", frag, livePlain)
		}
		if strings.Count(histPlain, frag) != 1 {
			t.Fatalf("history transcript should contain %q exactly once, got %q", frag, histPlain)
		}
	}
}

func blankLineCount(s string) int {
	plain := stripTerminalDecorations(s)
	count := 0
	for _, line := range strings.Split(plain, "\n") {
		if strings.TrimSpace(line) == "" {
			count++
		}
	}
	return count
}

// TestChatInteractionCoordinator_ResidualTailKeepsInterBlockBlank forces the
// production path where stable commits emit a prefix and finalize paints only
// the held-in-band tail via residual. That residual must keep the same
// inter-block blank that one-shot RenderAssistant produces — the old
// Format(suffix)+Trim path dropped it.
func TestChatInteractionCoordinator_ResidualTailKeepsInterBlockBlank(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	src := "# Title\n\n## Section\n\nbody paragraph.\n\n```go\nfunc Hello() {}\n```\n\n收尾段落。\n"

	histSession := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	histCoord := newChatInteractionCoordinator(histSession)
	t.Cleanup(histCoord.Shutdown)
	var histOut bytes.Buffer
	histCoord.SetWriter(&histOut)
	histCoord.RenderAssistant(src)
	histPlain := stripTerminalDecorations(histOut.String())

	liveSession := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	liveCoord := newChatInteractionCoordinator(liveSession)
	liveCoord.stableCommitDelay = time.Hour
	liveCoord.stableCommitManual = true
	t.Cleanup(liveCoord.Shutdown)
	var liveOut bytes.Buffer
	liveCoord.SetWriter(&liveOut)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 40)
	liveCoord.SetSurface(surface)

	liveCoord.RenderAssistantDelta(src)
	liveCoord.mu.Lock()
	liveCoord.stopActiveStableCommitLocked()
	// Drain whatever stable prefix was queued; the newest block stays residual.
	liveCoord.drainActiveStableCommitLocked(true)
	emitted := liveCoord.streamRenderedPrefixLen
	liveCoord.mu.Unlock()
	if emitted <= 0 {
		t.Fatal("precondition: expected some stable prefix to be emitted before residual")
	}
	if emitted >= len(src) {
		t.Fatalf("precondition: expected a residual tail, emitted=%d len=%d", emitted, len(src))
	}

	liveCoord.FinalizeAssistantDelta()
	livePlain := stripTerminalDecorations(liveOut.String())

	if maxBlankLineRun(livePlain) != maxBlankLineRun(histPlain) {
		t.Fatalf("residual path blank run mismatch: live=%d history=%d\nhistory:\n%s\nlive:\n%s",
			maxBlankLineRun(livePlain), maxBlankLineRun(histPlain), histPlain, livePlain)
	}
	if blankLineCount(livePlain) != blankLineCount(histPlain) {
		t.Fatalf("residual path blank count mismatch: live=%d history=%d\nhistory:\n%s\nlive:\n%s",
			blankLineCount(livePlain), blankLineCount(histPlain), histPlain, livePlain)
	}
	// The gap before the closing paragraph is the classic residual regression.
	idxLive := strings.Index(livePlain, "收尾段落。")
	idxHist := strings.Index(histPlain, "收尾段落。")
	if idxLive < 0 || idxHist < 0 {
		t.Fatalf("missing closing paragraph\nhistory:\n%s\nlive:\n%s", histPlain, livePlain)
	}
	liveBefore := livePlain[:idxLive]
	histBefore := histPlain[:idxHist]
	liveGap := trailingBlankLines(liveBefore)
	histGap := trailingBlankLines(histBefore)
	if liveGap != histGap {
		t.Fatalf("blank lines before 收尾段落: live=%d history=%d\nhistory:\n%q\nlive:\n%q",
			liveGap, histGap, histBefore, liveBefore)
	}
}

func trailingBlankLines(s string) int {
	s = strings.TrimRight(s, "\r")
	n := 0
	for strings.HasSuffix(s, "\n") {
		n++
		s = strings.TrimSuffix(s, "\n")
		s = strings.TrimSuffix(s, "\r")
	}
	// Count only pure blank rows after the last non-empty content line.
	// trailing \n from writeLine is the line terminator of the previous row,
	// so n==1 means "ended on content line"; n>=2 means (n-1) visual blanks.
	if n <= 1 {
		return 0
	}
	return n - 1
}

// TestChatInteractionCoordinator_PerChunkFormatVsFullFormat exposes pure
// formatter spacing drift between per-stable-chunk Format and full-document Format.
func TestChatInteractionCoordinator_PerChunkFormatVsFullFormat(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	fmtter := formatter.NewMarkdownFormatter(false)
	src := sampleMultiBlockMarkdown()

	full := strings.TrimRight(fmtter.Format(src), "\r\n")

	// Simulate progressive stable cuts on blank-line boundaries (same as
	// markdownStableScrollbackCut repeatedly advancing).
	var chunks []string
	rest := src
	for {
		idx := strings.Index(rest, "\n\n")
		if idx < 0 {
			if strings.TrimSpace(rest) != "" {
				chunks = append(chunks, rest)
			}
			break
		}
		// Keep the blank boundary with the committed chunk, matching cut = index+2.
		chunk := rest[:idx+2]
		chunks = append(chunks, chunk)
		rest = rest[idx+2:]
	}

	var liveParts []string
	for _, chunk := range chunks {
		rendered := strings.TrimRight(fmtter.Format(chunk), "\r\n")
		if rendered == "" {
			continue
		}
		liveParts = append(liveParts, rendered)
	}
	// join like writeLineLocked per line of each chunk: each chunk's lines
	// become sequential lines with single \n between them; between chunks there
	// is just the last line's writeLine \n — no extra inter-chunk spacer from
	// the join itself beyond what Format put in each chunk.
	var liveBuilder strings.Builder
	for i, part := range liveParts {
		if i > 0 {
			// drain writes each line with trailing \n; next chunk starts on next line.
			// No extra blank is inserted between chunks by the coordinator.
		}
		liveBuilder.WriteString(part)
		if i < len(liveParts)-1 {
			liveBuilder.WriteByte('\n')
		}
	}
	live := liveBuilder.String()

	fullMax := maxBlankLineRun(full)
	liveMax := maxBlankLineRun(live)
	t.Logf("full Format maxBlank=%d\n%s", fullMax, full)
	t.Logf("chunk Format maxBlank=%d\n%s", liveMax, live)

	if liveMax > fullMax {
		t.Fatalf("per-chunk Format introduces larger blank run (%d > full %d)\nfull:\n%s\nchunked:\n%s",
			liveMax, fullMax, full, live)
	}
	if strings.Count(live, "\n\n\n") > strings.Count(full, "\n\n\n") {
		t.Fatalf("per-chunk Format introduces triple newlines not present in full Format\nfull:\n%q\nchunked:\n%q", full, live)
	}
}

// TestChatInteractionCoordinator_MarkdownStableCommitSuffixMatchesFullFormat
// verifies the live-path differential (full-prefix Format, emit suffix) rebuilds
// the same rendered transcript as one-shot history Format.
func TestChatInteractionCoordinator_MarkdownStableCommitSuffixMatchesFullFormat(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	src := sampleMultiBlockMarkdown()
	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	coord.streamBuffer.WriteString(src)

	// Walk the same blank-boundary cuts as markdownStableScrollbackCut.
	var parts []string
	start := 0
	for start < len(src) {
		cut := markdownStableScrollbackCut(src, start, len(src))
		if cut <= start {
			// Final incomplete block: format remaining via absolute prefix.
			cut = len(src)
		}
		chunk := src[start:cut]
		part := coord.markdownStableCommitSuffixLocked(start, cut, chunk)
		if part != "" {
			parts = append(parts, part)
		}
		if cut >= len(src) {
			break
		}
		start = cut
	}

	var live strings.Builder
	for i, part := range parts {
		if i > 0 {
			// writeLineLocked already terminated the previous chunk's last line.
			live.WriteByte('\n')
		}
		live.WriteString(strings.TrimRight(part, "\r\n"))
	}
	full := strings.TrimRight(session.Formatter.Format(src), "\r\n")
	got := live.String()
	if got != full {
		t.Fatalf("stable-commit suffix rebuild diverged from full Format\nfull:\n%q\ngot:\n%q", full, got)
	}
	if maxBlankLineRun(got) > maxBlankLineRun(full) {
		t.Fatalf("suffix rebuild introduced extra blank runs")
	}
}

// TestChatInteractionCoordinator_LiveStreamScreenLayoutParityWithReplay is the
// end-to-end TTY assertion: progressive live streaming on a real fixed-bottom
// surface must not leave mid-stream or post-finalize blank holes larger than a
// one-shot history RenderAssistant on the same surface geometry.
func TestChatInteractionCoordinator_LiveStreamScreenLayoutParityWithReplay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width = 80
	height := 48
	budget := ui.ActiveBandRows(height)
	if budget != ui.ActiveBandMaxRows {
		t.Fatalf("precondition: height %d should budget %d band rows, got %d",
			height, ui.ActiveBandMaxRows, budget)
	}
	src := sampleMultiBlockMarkdown()

	// ---- Live progressive stream with real surface + VT capture ----
	liveSession := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	liveCoord := newChatInteractionCoordinator(liveSession)
	liveCoord.stableCommitDelay = time.Hour
	t.Cleanup(liveCoord.Shutdown)
	liveSurface := ui.NewFixedBottomSurface(ui.NewTerminal())
	liveSurface.EnableForTest(width, height)
	liveCoord.SetSurface(liveSurface)
	liveScreen := newScreenVT(width, height)

	seed := captureSurfaceStdout(t, func() {
		liveCoord.SetWriter(os.Stdout)
		liveSurface.ShowPrompt("> ")
		liveSurface.ClearPromptRows(1)
		for i := 1; i <= 8; i++ {
			liveCoord.RenderAsyncLine(fmt.Sprintf("seed-prior-%02d", i))
		}
	})
	liveScreen.feed(seed)

	// Stream in small rune chunks so stable cuts and band paints interleave.
	streaming := captureSurfaceStdout(t, func() {
		liveCoord.SetWriter(os.Stdout)
		content := src
		for len(content) > 0 {
			runes := []rune(content)
			n := 17
			if n > len(runes) {
				n = len(runes)
			}
			liveCoord.RenderAssistantDelta(string(runes[:n]))
			content = string(runes[n:])
		}
		// Drain the animated queue so mid-stream layout is fully settled before
		// the outer feed samples band adjacency.
		liveCoord.mu.Lock()
		liveCoord.stopActiveStableCommitLocked()
		liveCoord.drainActiveStableCommitLocked(true)
		liveCoord.mu.Unlock()
	})
	liveScreen.feed(streaming)

	band := liveSurface.ActiveBandLines()
	if len(band) == 0 {
		t.Fatalf("expected mutable tail still in ActiveBand mid/end stream; screen:\n%s", liveScreen.dump())
	}
	statusRow := height
	bandEnd := statusRow - 1
	bandStart := bandEnd - len(band) + 1
	midGap := gapBetweenLastScrollbackAndBand(liveScreen, bandStart, bandEnd)
	midRun, _ := maxBlankRunAboveBottom(liveScreen, bandStart)
	if midGap > 1 {
		t.Fatalf("live mid-stream gap above band = %d (budget=%d); screen:\n%s",
			midGap, budget, liveScreen.dump())
	}
	if midRun >= ui.ActiveBandMinRows {
		t.Fatalf("live mid-stream blank run %d (>= min band %d); screen:\n%s",
			midRun, ui.ActiveBandMinRows, liveScreen.dump())
	}

	finalized := captureSurfaceStdout(t, func() {
		liveCoord.SetWriter(os.Stdout)
		liveCoord.FinalizeAssistantDelta()
		liveSurface.ShowPrompt("> ")
	})
	liveScreen.feed(finalized)

	promptRow := height - 2
	lastText := 0
	for row := promptRow - 1; row >= 1; row-- {
		if strings.TrimSpace(liveScreen.line(row)) != "" {
			lastText = row
			break
		}
	}
	if lastText == 0 {
		t.Fatalf("live finalize left no transcript above prompt; screen:\n%s", liveScreen.dump())
	}
	livePostGap := promptRow - lastText - 1
	livePostRun, livePostAt := maxBlankRunAboveBottom(liveScreen, promptRow)
	if livePostGap > 2 {
		t.Fatalf("live post-finalize gap above prompt = %d; screen:\n%s", livePostGap, liveScreen.dump())
	}
	if livePostRun >= ui.ActiveBandMinRows {
		t.Fatalf("live post-finalize blank run %d at row %d; screen:\n%s", livePostRun, livePostAt, liveScreen.dump())
	}

	// No fragment may be painted twice (a double-painted stable chunk). Head
	// fragments are not required to stay visible: a reserved active band scrolls
	// rows into scrollback while it is up, and those rows are irrecoverable, so
	// a transcript taller than the output region legitimately loses its first
	// rows from the screen. Requiring them back on screen only passed while the
	// absorb path was overwriting committed rows instead of scrolling them.
	liveDump := liveScreen.dump()
	for _, frag := range []string{"长回复", "章节 1", "章节 6", "收尾段落。"} {
		if strings.Count(liveDump, frag) > 1 {
			t.Fatalf("live screen painted %q %d times, got screen:\n%s",
				frag, strings.Count(liveDump, frag), liveDump)
		}
	}
	for _, frag := range []string{"章节 6", "收尾段落。"} {
		if strings.Count(liveDump, frag) != 1 {
			t.Fatalf("live screen should keep tail fragment %q exactly once, got screen:\n%s", frag, liveDump)
		}
	}

	// ---- History one-shot RenderAssistant on identical surface geometry ----
	histSession := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	histCoord := newChatInteractionCoordinator(histSession)
	t.Cleanup(histCoord.Shutdown)
	histSurface := ui.NewFixedBottomSurface(ui.NewTerminal())
	histSurface.EnableForTest(width, height)
	histCoord.SetSurface(histSurface)
	histScreen := newScreenVT(width, height)

	histOut := captureSurfaceStdout(t, func() {
		histCoord.SetWriter(os.Stdout)
		histSurface.ShowPrompt("> ")
		histSurface.ClearPromptRows(1)
		for i := 1; i <= 8; i++ {
			histCoord.RenderAsyncLine(fmt.Sprintf("seed-prior-%02d", i))
		}
		histCoord.RenderAssistant(src)
		histSurface.ShowPrompt("> ")
	})
	histScreen.feed(histOut)

	histLastText := 0
	for row := promptRow - 1; row >= 1; row-- {
		if strings.TrimSpace(histScreen.line(row)) != "" {
			histLastText = row
			break
		}
	}
	if histLastText == 0 {
		t.Fatalf("history render left no transcript; screen:\n%s", histScreen.dump())
	}
	histPostGap := promptRow - histLastText - 1
	histPostRun, _ := maxBlankRunAboveBottom(histScreen, promptRow)
	// Strongest available oracle: from the bottom anchor upwards, live must be
	// row-identical to one-shot replay for as long as both still hold the rows
	// on screen. This catches extra/missing blank rows and shifted content
	// without depending on how many rows the band cycle scrolled away.
	assertScreenTailParity(t, liveScreen, histScreen, promptRow)

	// Live must not invent larger layout holes than one-shot history replay.
	if livePostGap > histPostGap+1 {
		t.Fatalf("live post-finalize gap %d exceeds history gap %d+1\nlive:\n%s\nhistory:\n%s",
			livePostGap, histPostGap, liveScreen.dump(), histScreen.dump())
	}
	if livePostRun > histPostRun && livePostRun >= ui.ActiveBandMinRows {
		t.Fatalf("live post-finalize blank run %d exceeds history %d\nlive:\n%s\nhistory:\n%s",
			livePostRun, histPostRun, liveScreen.dump(), histScreen.dump())
	}

	// Compare blank structure at stable tail anchors, not only hole size. Full
	// visible-row histograms are too brittle once content scrolls off the top.

	for _, anchor := range []string{"func Hello() {}", "收尾段落。"} {
		liveGap := blankRowsBeforeAnchor(liveScreen, promptRow, anchor)
		histGap := blankRowsBeforeAnchor(histScreen, promptRow, anchor)
		if liveGap < 0 || histGap < 0 {
			t.Fatalf("anchor %q missing on screen (live=%d hist=%d)\nlive:\n%s\nhistory:\n%s",
				anchor, liveGap, histGap, liveScreen.dump(), histScreen.dump())
		}
		if liveGap != histGap {
			t.Fatalf("blank rows before %q: live=%d history=%d\nlive:\n%s\nhistory:\n%s",
				anchor, liveGap, histGap, liveScreen.dump(), histScreen.dump())
		}
	}

	t.Logf("layout parity: midGap=%d midRun=%d livePostGap=%d livePostRun=%d histPostGap=%d histPostRun=%d",
		midGap, midRun, livePostGap, livePostRun, histPostGap, histPostRun)
}

// assertScreenTailParity compares two reconstructed screens row by row from
// bottomExclusive-1 upwards. Rows a terminal already scrolled away cannot be
// recovered, so the comparison stops as soon as either screen runs out of
// transcript; the overlap must still be long enough to be meaningful.
func assertScreenTailParity(t *testing.T, live, hist *screenVT, bottomExclusive int) {
	t.Helper()
	liveRows := trimmedTranscriptRows(live, bottomExclusive)
	histRows := trimmedTranscriptRows(hist, bottomExclusive)
	overlap := len(liveRows)
	if len(histRows) < overlap {
		overlap = len(histRows)
	}
	if overlap < 20 {
		t.Fatalf("transcript overlap too small to compare (live=%d hist=%d rows)\nlive:\n%s\nhistory:\n%s",
			len(liveRows), len(histRows), live.dump(), hist.dump())
	}
	for i := 1; i <= overlap; i++ {
		liveLine := liveRows[len(liveRows)-i]
		histLine := histRows[len(histRows)-i]
		if liveLine != histLine {
			t.Fatalf("live/history diverge %d rows above the bottom anchor: live=%q history=%q\nlive:\n%s\nhistory:\n%s",
				i, liveLine, histLine, live.dump(), hist.dump())
		}
	}
}

// trimmedTranscriptRows returns transcript rows above bottomExclusive with
// leading blank rows (already scrolled-away area) removed.
func trimmedTranscriptRows(screen *screenVT, bottomExclusive int) []string {
	if screen == nil || bottomExclusive <= 1 {
		return nil
	}
	rows := make([]string, 0, bottomExclusive-1)
	for row := 1; row < bottomExclusive; row++ {
		rows = append(rows, screen.line(row))
	}
	start := 0
	for start < len(rows) && strings.TrimSpace(rows[start]) == "" {
		start++
	}
	return rows[start:]
}

// blankRowsBeforeAnchor counts contiguous blank rows immediately above the
// first screen row containing anchor, scanning only above bottomExclusive.
func blankRowsBeforeAnchor(screen *screenVT, bottomExclusive int, anchor string) int {
	if screen == nil || bottomExclusive <= 1 {
		return -1
	}
	anchorRow := -1
	for row := 1; row < bottomExclusive; row++ {
		if strings.Contains(screen.line(row), anchor) {
			anchorRow = row
			break
		}
	}
	if anchorRow < 0 {
		return -1
	}
	gap := 0
	for row := anchorRow - 1; row >= 1; row-- {
		if strings.TrimSpace(screen.line(row)) != "" {
			break
		}
		gap++
	}
	return gap
}

// TestChatInteractionCoordinator_MarkdownRowsDeltaContract is the unit table
// for the shared live/history spacing rule in row space:
// rows(Format(fullPrefix))[len(rows(Format(prevPrefix))):] — no leading-\\n
// strip. Inter-block blanks appear as a leading "" row in the delta.
func TestChatInteractionCoordinator_MarkdownRowsDeltaContract(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	src := "# Title\n\n## Section\n\nbody paragraph.\n\n```go\nfunc Hello() {}\n```\n\n收尾段落。\n"
	fmtr := formatter.NewMarkdownFormatter(false)
	fmtr.Width = 80

	// Cuts sit on blank-line boundaries the same way markdownStableScrollbackCut
	// advances, so each case is a real stable-commit / residual boundary.
	type cutCase struct {
		name                string
		start               int
		end                 int
		wantLeadingBlankRow bool
		wantContains        string
	}
	cases := []cutCase{
		{
			name:                "first_prefix_no_leading_blank",
			start:               0,
			end:                 strings.Index(src, "## Section"),
			wantLeadingBlankRow: false,
			wantContains:        "Title",
		},
		{
			name:                "section_after_title_keeps_inter_block_blank",
			start:               strings.Index(src, "## Section"),
			end:                 strings.Index(src, "```go"),
			wantLeadingBlankRow: true,
			wantContains:        "Section",
		},
		{
			name:                "code_block_after_body_keeps_blank",
			start:               strings.Index(src, "```go"),
			end:                 strings.Index(src, "收尾段落"),
			wantLeadingBlankRow: true,
			wantContains:        "func Hello()",
		},
		{
			name:                "residual_closing_keeps_blank",
			start:               strings.Index(src, "收尾段落"),
			end:                 len(src),
			wantLeadingBlankRow: true,
			wantContains:        "收尾段落",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.start < 0 || tc.end <= tc.start || tc.end > len(src) {
				t.Fatalf("invalid cut start=%d end=%d len=%d", tc.start, tc.end, len(src))
			}
			session := &ChatSession{Formatter: fmtr}
			coord := newChatInteractionCoordinator(session)
			t.Cleanup(coord.Shutdown)
			coord.streamBuffer.WriteString(src)

			chunk := src[tc.start:tc.end]
			rows := coord.markdownRowsDeltaLocked(tc.start, tc.end, chunk)
			if len(rows) == 0 {
				t.Fatal("expected non-empty row delta")
			}
			joined := strings.Join(rows, "\n")
			if !strings.Contains(stripTerminalDecorations(joined), tc.wantContains) {
				t.Fatalf("delta missing %q: %#v", tc.wantContains, rows)
			}

			hasLeadingBlank := rows[0] == ""
			if hasLeadingBlank != tc.wantLeadingBlankRow {
				t.Fatalf("leading blank row = %v, want %v\nrows=%#v",
					hasLeadingBlank, tc.wantLeadingBlankRow, rows)
			}

			// Never invent double leading blank rows at the chunk join.
			if len(rows) >= 2 && rows[0] == "" && rows[1] == "" {
				t.Fatalf("delta has double leading blank rows: %#v", rows)
			}

			// priorWriteEndedWithLF no longer changes the delta — strip is gone.
			legacyTrue := coord.markdownFullPrefixSuffixLocked(tc.start, tc.end, chunk, true)
			legacyFalse := coord.markdownFullPrefixSuffixLocked(tc.start, tc.end, chunk, false)
			if legacyTrue != legacyFalse {
				t.Fatalf("legacy wrapper must ignore priorWriteEndedWithLF:\ntrue=%q\nfalse=%q", legacyTrue, legacyFalse)
			}

			// Rebuilding prev rows + delta rows must match full one-shot rows.
			if tc.start > 0 {
				prevRows := coord.markdownRowsDeltaLocked(0, tc.start, src[:tc.start])
				fullRows := coord.markdownRowsDeltaLocked(0, tc.end, src[:tc.end])
				rebuilt := append(append([]string{}, prevRows...), rows...)
				if len(rebuilt) != len(fullRows) {
					t.Fatalf("rebuilt row count %d != full %d\nfull=%#v\nrebuilt=%#v\ndelta=%#v",
						len(rebuilt), len(fullRows), fullRows, rebuilt, rows)
				}
				for i := range fullRows {
					if rebuilt[i] != fullRows[i] {
						t.Fatalf("rebuilt row[%d]=%q != full %q", i, rebuilt[i], fullRows[i])
					}
				}
			}
		})
	}
}
