package commands

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// maxBlankRunAboveBottom returns the largest run of consecutive blank rows in
// [1, bottomExclusive). bottomExclusive is typically the first bottom-pane row.
func maxBlankRunAboveBottom(screen *screenVT, bottomExclusive int) (maxRun int, startRow int) {
	run := 0
	runStart := 0
	seenContent := false
	for row := 1; row < bottomExclusive; row++ {
		if strings.TrimSpace(screen.line(row)) == "" {
			if !seenContent {
				continue
			}
			if run == 0 {
				runStart = row
			}
			run++
			if run > maxRun {
				maxRun = run
				startRow = runStart
			}
			continue
		}
		seenContent = true
		run = 0
	}
	return maxRun, startRow
}

// gapBetweenLastScrollbackAndBand measures blank rows between the last
// non-empty row above the band and the first non-empty band row.
func gapBetweenLastScrollbackAndBand(screen *screenVT, bandStart, bandEnd int) int {
	lastScrollback := 0
	for row := bandStart - 1; row >= 1; row-- {
		if strings.TrimSpace(screen.line(row)) != "" {
			lastScrollback = row
			break
		}
	}
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

	// Build a reply that forces the live band to the max row budget.
	var reply strings.Builder
	reply.WriteString("# 长回复\n\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&reply, "## 章节 %d\n\n这是第 %d 段正文，用来撑满 ActiveBand。\n\n", i, i)
	}
	reply.WriteString("收尾段落。\n")

	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newChatInteractionCoordinator(session)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)

	screen := newScreenVT(width, height)

	// Seed scrollback so a reserve-growth hole is visible against real content.
	seed := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		surface.ShowPrompt("> ")
		surface.ClearPromptRows(1)
		for i := 1; i <= 20; i++ {
			coord.RenderAsyncLine(fmt.Sprintf("seed-line-%02d prior transcript", i))
		}
	})
	screen.feed(seed)

	// Stream the long reply in small chunks and inspect AFTER the band has
	// grown, still mid-stream (before finalize).
	streaming := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		content := reply.String()
		// Character-ish chunks to force many band growth/paint steps.
		for len(content) > 0 {
			n := 24
			if n > len(content) {
				n = len(content)
			}
			// avoid splitting multi-byte runes roughly by cutting on bytes then
			// letting the stream normalizer handle it; keep simple rune walk.
			runes := []rune(content)
			if n > len(runes) {
				n = len(runes)
			}
			chunk := string(runes[:n])
			content = string(runes[n:])
			coord.RenderAssistantDelta(chunk)
		}
	})
	screen.feed(streaming)

	band := surface.ActiveBandLines()
	if len(band) == 0 {
		t.Fatalf("expected active band mid-stream, screen:\n%s", screen.dump())
	}
	if len(band) < wantBandBudget {
		t.Fatalf("expected band to reach budget %d mid-stream, got %d lines: %v\nscreen:\n%s",
			wantBandBudget, len(band), band, screen.dump())
	}

	// Band is laid out bottom-up above the status row while prompt is hidden.
	statusRow := height
	bandEnd := statusRow - 1
	bandStart := bandEnd - len(band) + 1
	if bandStart < 1 {
		t.Fatalf("band geometry invalid start=%d end=%d, screen:\n%s", bandStart, bandEnd, screen.dump())
	}

	// Status and bottom band row must stay painted.
	if got := screen.line(statusRow); strings.TrimSpace(got) == "" {
		t.Fatalf("status row blank, screen:\n%s", screen.dump())
	}
	if got := screen.line(bandEnd); strings.TrimSpace(got) == "" {
		t.Fatalf("band tail row %d blank, screen:\n%s", bandEnd, screen.dump())
	}

	gap := gapBetweenLastScrollbackAndBand(screen, bandStart, bandEnd)
	if gap < 0 {
		t.Fatalf("could not locate scrollback/band boundary (start=%d end=%d), screen:\n%s",
			bandStart, bandEnd, screen.dump())
	}
	if gap > 1 {
		t.Fatalf("mid-stream blank gap above active band = %d (band rows=%d budget=%d); screen:\n%s",
			gap, len(band), wantBandBudget, screen.dump())
	}

	// Also forbid a long blank run anywhere above the band (the classic ~14 hole).
	if maxRun, at := maxBlankRunAboveBottom(screen, bandStart); maxRun >= ui.ActiveBandMinRows {
		t.Fatalf("mid-stream blank run of %d rows starting at row %d (>= min band %d); screen:\n%s",
			maxRun, at, ui.ActiveBandMinRows, screen.dump())
	}

	// Finalize must still keep transcript adjacent to the restored prompt.
	final := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		coord.FinalizeAssistantDelta()
		surface.ShowPrompt("> ")
	})
	screen.feed(final)

	promptRow := height - 1
	lastText := 0
	for row := promptRow - 1; row >= 1; row-- {
		if strings.TrimSpace(screen.line(row)) != "" {
			lastText = row
			break
		}
	}
	if lastText == 0 {
		t.Fatalf("expected finalized transcript, screen:\n%s", screen.dump())
	}
	if gap := promptRow - lastText - 1; gap > 1 {
		t.Fatalf("post-finalize gap above prompt = %d, screen:\n%s", gap, screen.dump())
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
	screen := newScreenVT(width, height)

	seed := captureSurfaceStdout(t, func() {
		surface.ShowPrompt("> ")
		surface.ClearPromptRows(1)
		for i := 1; i <= 25; i++ {
			_, err, ok := surface.WriteOutput(os.Stdout, fmt.Sprintf("prior-%02d\n", i))
			if !ok || err != nil {
				t.Fatalf("WriteOutput prior: ok=%v err=%v", ok, err)
			}
		}
	})
	screen.feed(seed)

	lines := make([]string, 0, budget)
	for row := 1; row <= budget; row++ {
		lines = append(lines, fmt.Sprintf("band-row-%02d", row))
		step := captureSurfaceStdout(t, func() {
			if !surface.SetActiveBand(lines) {
				t.Fatal("SetActiveBand failed")
			}
		})
		screen.feed(step)

		bandStart := height - 1 - len(lines) + 1
		gap := gapBetweenLastScrollbackAndBand(screen, bandStart, height-1)
		if gap > 1 {
			t.Fatalf("after growing to %d band rows, gap above band = %d; screen:\n%s",
				len(lines), gap, screen.dump())
		}
		if maxRun, at := maxBlankRunAboveBottom(screen, bandStart); maxRun >= ui.ActiveBandMinRows {
			t.Fatalf("after growing to %d band rows, blank run %d at row %d; screen:\n%s",
				len(lines), maxRun, at, screen.dump())
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
	screen := newScreenVT(width, height)

	seed := captureSurfaceStdout(t, func() {
		surface.ShowPrompt("> ")
		surface.ClearPromptRows(1)
		for i := 1; i <= 20; i++ {
			if _, err, ok := surface.WriteOutput(os.Stdout, fmt.Sprintf("prior-%02d\n", i)); !ok || err != nil {
				t.Fatalf("WriteOutput prior: ok=%v err=%v", ok, err)
			}
		}
	})
	screen.feed(seed)

	bandLines := make([]string, 0, budget)
	for i := 1; i <= budget; i++ {
		bandLines = append(bandLines, fmt.Sprintf("live-band-%02d", i))
	}
	grow := captureSurfaceStdout(t, func() {
		if !surface.SetActiveBand(bandLines) {
			t.Fatal("SetActiveBand failed")
		}
	})
	screen.feed(grow)

	// Step 1: commit final transcript while the band is still reserved.
	commit := captureSurfaceStdout(t, func() {
		if _, err, ok := surface.WriteOutput(os.Stdout, "final-committed-line\n"); !ok || err != nil {
			t.Fatalf("WriteOutput final: ok=%v err=%v", ok, err)
		}
	})
	screen.feed(commit)

	bandStart := height - 1 - budget + 1
	if gap := gapBetweenLastScrollbackAndBand(screen, bandStart, height-1); gap > 1 {
		t.Fatalf("after commit-with-band, gap above band = %d (budget=%d); screen:\n%s",
			gap, budget, screen.dump())
	}
	if maxRun, at := maxBlankRunAboveBottom(screen, bandStart); maxRun >= ui.ActiveBandMinRows {
		t.Fatalf("after commit-with-band, blank run %d at row %d; screen:\n%s", maxRun, at, screen.dump())
	}
	foundFinal := false
	for row := 1; row < bandStart; row++ {
		if strings.Contains(screen.line(row), "final-committed-line") {
			foundFinal = true
			break
		}
	}
	if !foundFinal {
		t.Fatalf("expected final line in scrollback above band, screen:\n%s", screen.dump())
	}

	// Step 2: release the band — freed rows must be reclaimed by scroll-down.
	release := captureSurfaceStdout(t, func() {
		if !surface.ClearActiveBand() {
			t.Fatal("ClearActiveBand failed")
		}
	})
	screen.feed(release)
	if want := fmt.Sprintf("\x1b[%dT", budget); !strings.Contains(release, want) {
		t.Fatalf("ClearActiveBand should reclaim all %d released band rows, output=%q", budget, release)
	}
	if got := len(surface.ActiveBandLines()); got != 0 {
		t.Fatalf("expected band cleared, still %d lines", got)
	}

	statusRow := height
	lastText := 0
	for row := statusRow - 1; row >= 1; row-- {
		if strings.TrimSpace(screen.line(row)) != "" {
			lastText = row
			break
		}
	}
	if lastText == 0 {
		t.Fatalf("expected transcript after band release, screen:\n%s", screen.dump())
	}
	// No prompt yet: content should sit against status with at most one blank.
	if gap := statusRow - lastText - 1; gap > 1 {
		t.Fatalf("after ClearActiveBand, gap above status = %d (budget=%d); screen:\n%s",
			gap, budget, screen.dump())
	}
	if maxRun, at := maxBlankRunAboveBottom(screen, statusRow); maxRun >= ui.ActiveBandMinRows {
		t.Fatalf("after ClearActiveBand, blank run %d at row %d; screen:\n%s", maxRun, at, screen.dump())
	}

	// Step 3: restore prompt — still no band-sized hole above it.
	promptOut := captureSurfaceStdout(t, func() {
		if !surface.ShowPrompt("> ") {
			t.Fatal("ShowPrompt failed")
		}
	})
	screen.feed(promptOut)

	promptRow := height - 1
	if got := screen.line(promptRow); !strings.HasPrefix(strings.TrimSpace(got), ">") {
		t.Fatalf("expected prompt on row %d, got %q, screen:\n%s", promptRow, got, screen.dump())
	}
	lastText = 0
	for row := promptRow - 1; row >= 1; row-- {
		if strings.TrimSpace(screen.line(row)) != "" {
			lastText = row
			break
		}
	}
	if lastText == 0 {
		t.Fatalf("expected transcript above prompt, screen:\n%s", screen.dump())
	}
	if gap := promptRow - lastText - 1; gap > 1 {
		t.Fatalf("after ShowPrompt, gap above prompt = %d (budget=%d); screen:\n%s",
			gap, budget, screen.dump())
	}
	if maxRun, at := maxBlankRunAboveBottom(screen, promptRow); maxRun >= ui.ActiveBandMinRows {
		t.Fatalf("after ShowPrompt, blank run %d at row %d; screen:\n%s", maxRun, at, screen.dump())
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
	coord := newChatInteractionCoordinator(session)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)

	seed := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		surface.ShowPrompt("> ")
		surface.ClearPromptRows(1)
		for i := 1; i <= 12; i++ {
			coord.RenderAsyncLine(fmt.Sprintf("seed-%02d", i))
		}
	})
	screen.feed(seed)

	streaming := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
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
	})
	screen.feed(streaming)
	if got := len(surface.ActiveBandLines()); got < budget {
		t.Fatalf("expected full band before finalize, got %d want >= %d; screen:\n%s",
			got, budget, screen.dump())
	}

	// Finalize alone: commit + ClearActiveBand (no prompt yet).
	finalized := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		coord.FinalizeAssistantDelta()
	})
	screen.feed(finalized)
	if got := len(surface.ActiveBandLines()); got != 0 {
		t.Fatalf("finalize should clear active band, still %d lines", got)
	}
	statusRow := height
	lastText := 0
	for row := statusRow - 1; row >= 1; row-- {
		if strings.TrimSpace(screen.line(row)) != "" {
			lastText = row
			break
		}
	}
	if lastText == 0 {
		t.Fatalf("expected committed transcript after finalize, screen:\n%s", screen.dump())
	}
	if gap := statusRow - lastText - 1; gap > 1 {
		t.Fatalf("after finalize(clear band), gap above status = %d; screen:\n%s", gap, screen.dump())
	}
	if maxRun, at := maxBlankRunAboveBottom(screen, statusRow); maxRun >= ui.ActiveBandMinRows {
		t.Fatalf("after finalize, blank run %d at row %d; screen:\n%s", maxRun, at, screen.dump())
	}

	// A later output is the user-visible failure mode: if release only clears
	// the band without pulling the transcript down, WriteOutput jumps to the new
	// output bottom and leaves the released 14 rows as a hole in the middle.
	continued := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		coord.RenderAsyncLine("post-release-output")
	})
	screen.feed(continued)
	if maxRun, at := maxBlankRunAboveBottom(screen, statusRow); maxRun >= ui.ActiveBandMinRows {
		t.Fatalf("continued output exposed a post-release blank run %d at row %d; screen:\n%s",
			maxRun, at, screen.dump())
	}
	foundContinued := false
	for row := 1; row < statusRow; row++ {
		if strings.Contains(screen.line(row), "post-release-output") {
			foundContinued = true
			break
		}
	}
	if !foundContinued {
		t.Fatalf("expected continued output after band release, screen:\n%s", screen.dump())
	}

	promptOut := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		surface.ShowPrompt("> ")
	})
	screen.feed(promptOut)

	promptRow := height - 1
	lastText = 0
	for row := promptRow - 1; row >= 1; row-- {
		if strings.TrimSpace(screen.line(row)) != "" {
			lastText = row
			break
		}
	}
	if lastText == 0 {
		t.Fatalf("expected transcript above prompt, screen:\n%s", screen.dump())
	}
	if gap := promptRow - lastText - 1; gap > 1 {
		t.Fatalf("after ShowPrompt, gap above prompt = %d; screen:\n%s", gap, screen.dump())
	}
	if maxRun, at := maxBlankRunAboveBottom(screen, promptRow); maxRun >= ui.ActiveBandMinRows {
		t.Fatalf("after ShowPrompt, blank run %d at row %d; screen:\n%s", maxRun, at, screen.dump())
	}
}
