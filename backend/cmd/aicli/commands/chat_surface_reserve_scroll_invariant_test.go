package commands

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// screenRowsContaining reports every 1-based screen row that contains marker.
// Bottom-reserve compensation must never duplicate or erase committed rows, so
// the expected result is always exactly one row per marker.
func screenRowsContaining(screen *screenVT, marker string) []int {
	return screen.RowsContaining(marker)
}

// assertCommittedRowsIntact fails when a committed transcript marker was
// overwritten, duplicated or reordered by reserve growth/shrink compensation.
func assertCommittedRowsIntact(t *testing.T, screen *screenVT, markers []string, stage string) int {
	t.Helper()
	last := 0
	for _, marker := range markers {
		rows := screenRowsContaining(screen, marker)
		if len(rows) != 1 {
			t.Fatalf("%s: expected %q exactly once on screen, got rows %v\n%s", stage, marker, rows, screen.dump())
		}
		if rows[0] <= last {
			t.Fatalf("%s: %q landed on row %d, out of order after row %d\n%s", stage, marker, rows[0], last, screen.dump())
		}
		last = rows[0]
	}
	return last
}

func surfaceBandLines(count int) []string {
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		lines = append(lines, fmt.Sprintf("band-%02d", i))
	}
	return lines
}

func writeSurfaceTranscriptRow(t *testing.T, surface *ui.FixedBottomSurface, marker string) {
	t.Helper()
	if _, err, ok := surface.WriteOutput(os.Stdout, marker+" committed transcript row\n"); !ok || err != nil {
		t.Fatalf("WriteOutput(%s): ok=%t err=%v", marker, ok, err)
	}
}

func surfacePromptRow(t *testing.T, screen *screenVT) int {
	t.Helper()
	for row := screen.Height(); row >= 1; row-- {
		if strings.HasPrefix(screen.line(row), ">") {
			return row
		}
	}
	t.Fatalf("prompt row not found\n%s", screen.dump())
	return 0
}

// TestFixedBottomSurface_ActiveBandIsLayoutNeutral replays the real byte stream
// of a streaming turn on a reconstructed terminal instead of asserting the
// compensation sequences themselves.
//
// Two invariants matter and neither is visible at byte level:
//   - committed transcript rows are irreversible: reserve growth may scroll
//     them, never overwrite them (absorbing the trailing blank row used to park
//     content on the row every writer targets, silently eating one line);
//   - an active band that appears and is released again must be layout neutral:
//     the final transcript position must match a turn that never showed a band.
func TestFixedBottomSurface_ActiveBandIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			markers := make([]string, 0, 8)
			for i := 1; i <= 8; i++ {
				markers = append(markers, fmt.Sprintf("L%02d", i))
			}

			baseScreen, baseLast, basePrompt := surfaceLayoutBaseline(t, width, height, markers)

			// Live: the band grows over the trailing blank output row, more rows
			// commit underneath it, then the band is released and the prompt
			// returns.
			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			screen := newScreenVT(width, height)
			screen.feed(captureSurfaceStdout(t, func() {
				surface.ShowPrompt("> ")
				surface.ClearPromptRows(1)
				for _, marker := range markers[:5] {
					writeSurfaceTranscriptRow(t, surface, marker)
				}
				surface.SetActiveBand(surfaceBandLines(6))
				for _, marker := range markers[5:] {
					writeSurfaceTranscriptRow(t, surface, marker)
				}
			}))
			bandLast := assertCommittedRowsIntact(t, screen, markers, "band growth")
			for _, band := range surfaceBandLines(6) {
				if rows := screenRowsContaining(screen, band); len(rows) != 1 {
					t.Fatalf("band row %q should be painted once, got %v\n%s", band, rows, screen.dump())
				}
			}
			// One blank row is expected: the output region bottom row is the
			// position every writer targets, so it stays empty between writes.
			// Anything larger is a reserve-growth hole.
			if bandStart := screenRowsContaining(screen, "band-01"); len(bandStart) == 1 && bandStart[0]-bandLast > 2 {
				t.Fatalf("transcript left a %d-row hole above the active band\n%s", bandStart[0]-bandLast-1, screen.dump())
			}

			screen.feed(captureSurfaceStdout(t, func() {
				surface.ClearActiveBand()
				surface.ShowPrompt("> ")
			}))
			liveLast := assertCommittedRowsIntact(t, screen, markers, "band release")
			livePrompt := surfacePromptRow(t, screen)
			for _, band := range surfaceBandLines(6) {
				if rows := screenRowsContaining(screen, band); len(rows) != 0 {
					t.Fatalf("released band row %q still on screen at %v\n%s", band, rows, screen.dump())
				}
			}
			if livePrompt != basePrompt {
				t.Fatalf("prompt row %d != baseline %d\nlive:\n%s\nbaseline:\n%s",
					livePrompt, basePrompt, screen.dump(), baseScreen.dump())
			}
			if got, want := livePrompt-liveLast, basePrompt-baseLast; got != want {
				t.Fatalf("gap between transcript and prompt = %d rows, baseline %d\nlive:\n%s\nbaseline:\n%s",
					got-1, want-1, screen.dump(), baseScreen.dump())
			}
		})
	}
}

// surfaceLayoutBaseline replays the same committed rows on a fresh surface that
// never reserves anything beyond prompt+status. It is the layout oracle: any
// band / popup / settle cycle must leave the transcript and the prompt on the
// same rows as this run.
func surfaceLayoutBaseline(t *testing.T, width, height int, markers []string) (*screenVT, int, int) {
	t.Helper()
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	screen := newScreenVT(width, height)
	screen.feed(captureSurfaceStdout(t, func() {
		surface.ShowPrompt("> ")
		surface.ClearPromptRows(1)
		for _, marker := range markers {
			writeSurfaceTranscriptRow(t, surface, marker)
		}
		surface.ShowPrompt("> ")
	}))
	return screen, assertCommittedRowsIntact(t, screen, markers, "baseline"), surfacePromptRow(t, screen)
}

// assertLayoutMatchesBaseline compares a reserve-cycle run against the oracle:
// same prompt row, same last transcript row, therefore the same gap.
func assertLayoutMatchesBaseline(t *testing.T, stage string, live *screenVT, liveLast, livePrompt int, base *screenVT, baseLast, basePrompt int) {
	t.Helper()
	if livePrompt != basePrompt {
		t.Fatalf("%s: prompt row %d != baseline %d\nlive:\n%s\nbaseline:\n%s",
			stage, livePrompt, basePrompt, live.dump(), base.dump())
	}
	if liveLast != baseLast {
		t.Fatalf("%s: last transcript row %d != baseline %d (gap %d vs %d)\nlive:\n%s\nbaseline:\n%s",
			stage, liveLast, baseLast, livePrompt-liveLast-1, basePrompt-baseLast-1, live.dump(), base.dump())
	}
}

// A command popup reserves rows the same way an active band does, but closing it
// deliberately defers the shrink compensation: the slash-completion popup is
// rebuilt on every keystroke, and flushing on close would bounce the whole
// transcript up and down while the user types. The contract is therefore
// "repaid at the next output write" — and after that write the layout must be
// indistinguishable from a run that never opened a popup.
func TestFixedBottomSurface_PopupIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			markers := make([]string, 0, 6)
			for i := 1; i <= 6; i++ {
				markers = append(markers, fmt.Sprintf("L%02d", i))
			}
			popup := []string{"popup-01", "popup-02", "popup-03", "popup-04"}

			// Baseline: the same turn boundary sequence, no popup.
			baseSurface := ui.NewFixedBottomSurface(ui.NewTerminal())
			baseSurface.EnableForTest(width, height)
			baseScreen := newScreenVT(width, height)
			baseScreen.feed(captureSurfaceStdout(t, func() {
				baseSurface.ShowPrompt("> ")
				baseSurface.ClearPromptRows(1)
				for _, marker := range markers[:5] {
					writeSurfaceTranscriptRow(t, baseSurface, marker)
				}
				baseSurface.ShowPrompt("> ")
				baseSurface.ClearPromptRows(1)
				writeSurfaceTranscriptRow(t, baseSurface, markers[5])
				baseSurface.ShowPrompt("> ")
			}))
			baseLast := assertCommittedRowsIntact(t, baseScreen, markers, "baseline")
			basePrompt := surfacePromptRow(t, baseScreen)

			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			screen := newScreenVT(width, height)
			screen.feed(captureSurfaceStdout(t, func() {
				surface.ShowPrompt("> ")
				surface.ClearPromptRows(1)
				for _, marker := range markers[:5] {
					writeSurfaceTranscriptRow(t, surface, marker)
				}
				surface.ShowPrompt("> ")
				surface.ShowPopup(popup)
			}))
			assertCommittedRowsIntact(t, screen, markers[:5], "popup open")
			for _, line := range popup {
				if rows := screenRowsContaining(screen, line); len(rows) != 1 {
					t.Fatalf("popup row %q should be painted once, got %v\n%s", line, rows, screen.dump())
				}
			}

			screen.feed(captureSurfaceStdout(t, func() {
				surface.ClearPopup()
			}))
			assertCommittedRowsIntact(t, screen, markers[:5], "popup close")
			for _, line := range popup {
				if rows := screenRowsContaining(screen, line); len(rows) != 0 {
					t.Fatalf("closed popup row %q still on screen at %v\n%s", line, rows, screen.dump())
				}
			}

			// Next turn: submitting input clears the prompt and the reply write
			// pays the deferred popup compensation.
			screen.feed(captureSurfaceStdout(t, func() {
				surface.ClearPromptRows(1)
				writeSurfaceTranscriptRow(t, surface, markers[5])
				surface.ShowPrompt("> ")
			}))
			liveLast := assertCommittedRowsIntact(t, screen, markers, "popup debt repaid")
			assertLayoutMatchesBaseline(t, "popup cycle", screen, liveLast, surfacePromptRow(t, screen),
				baseScreen, baseLast, basePrompt)
		})
	}
}

// History / resume replay runs after the prompt rows were cleared, which leaves
// deferred shrink compensation. SettleOutputDebt has to absorb that debt before
// the first already-final message is written, so the replayed transcript ends up
// exactly where a plain run would put it.
func TestFixedBottomSurface_HistorySettleIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			markers := make([]string, 0, 6)
			for i := 1; i <= 6; i++ {
				markers = append(markers, fmt.Sprintf("L%02d", i))
			}
			baseScreen, baseLast, basePrompt := surfaceLayoutBaseline(t, width, height, markers)

			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			screen := newScreenVT(width, height)
			screen.feed(captureSurfaceStdout(t, func() {
				surface.ShowPrompt("> ")
				for _, marker := range markers[:2] {
					writeSurfaceTranscriptRow(t, surface, marker)
				}
				// /resume: prompt rows are cleared, then already-final history is
				// replayed after settling layout debt.
				surface.ClearPromptRows(1)
				surface.SettleOutputDebt()
				for _, marker := range markers[2:] {
					writeSurfaceTranscriptRow(t, surface, marker)
				}
				surface.ShowPrompt("> ")
			}))
			liveLast := assertCommittedRowsIntact(t, screen, markers, "history settle")
			assertLayoutMatchesBaseline(t, "history settle", screen, liveLast, surfacePromptRow(t, screen),
				baseScreen, baseLast, basePrompt)
		})
	}
}

// The composite path users actually hit: /resume replays history, then the next
// turn streams a reply behind an active band. Both paths carry their own layout
// debt (settle before replay, absorb + deferred shrink during the turn), so the
// interesting failure mode is the interaction — a debt from the replay being
// paid inside the streaming turn, or vice versa.
func TestFixedBottomSurface_HistoryReplayThenStreamingTurnIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			history := []string{"H01", "H02", "H03"}
			reply := []string{"R01", "R02", "R03"}
			all := append(append([]string{}, history...), reply...)
			baseScreen, baseLast, basePrompt := surfaceLayoutBaseline(t, width, height, all)

			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			screen := newScreenVT(width, height)

			// /resume: clear the prompt, settle layout debt, replay final history.
			screen.feed(captureSurfaceStdout(t, func() {
				surface.ShowPrompt("> ")
				surface.ClearPromptRows(1)
				surface.SettleOutputDebt()
				for _, marker := range history {
					writeSurfaceTranscriptRow(t, surface, marker)
				}
				surface.ShowPrompt("> ")
			}))
			assertCommittedRowsIntact(t, screen, history, "history replay")

			// Next turn: submit, stream behind an active band, release, restore.
			screen.feed(captureSurfaceStdout(t, func() {
				surface.ClearPromptRows(1)
				surface.SetActiveBand(surfaceBandLines(4))
				for _, marker := range reply {
					writeSurfaceTranscriptRow(t, surface, marker)
				}
				surface.ClearActiveBand()
				surface.ShowPrompt("> ")
			}))
			liveLast := assertCommittedRowsIntact(t, screen, all, "streaming turn")
			for _, band := range surfaceBandLines(4) {
				if rows := screenRowsContaining(screen, band); len(rows) != 0 {
					t.Fatalf("released band row %q still on screen at %v\n%s", band, rows, screen.dump())
				}
			}
			assertLayoutMatchesBaseline(t, "history replay + streaming turn", screen, liveLast,
				surfacePromptRow(t, screen), baseScreen, baseLast, basePrompt)
		})
	}
}

// Progressive markdown streaming does not append line by line: it writes a soft
// tail and rewrites it in place on every stable-commit cut. That rewrite has to
// locate the rows the tail currently occupies, which depends on whether the
// output cursor is parked on a blank row — exactly the state band growth mutates
// when it absorbs that blank. Screen level is the only place where a one-row
// error there is visible.
func TestFixedBottomSurface_SoftTailRewriteUnderActiveBandIsLayoutNeutral(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80
	row := func(marker string) string { return marker + " committed transcript row" }

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			markers := []string{"C01", "S01", "S02"}
			baseScreen, baseLast, basePrompt := surfaceLayoutBaseline(t, width, height, markers)

			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			screen := newScreenVT(width, height)
			screen.feed(captureSurfaceStdout(t, func() {
				surface.ShowPrompt("> ")
				surface.ClearPromptRows(1)
				writeSurfaceTranscriptRow(t, surface, "C01")
				// Band growth absorbs the trailing blank row here.
				surface.SetActiveBand(surfaceBandLines(4))
				if _, err, ok := surface.WriteSoftTrackedOutput(os.Stdout, row("D01-draft")+"\n"); !ok || err != nil {
					t.Fatalf("WriteSoftTrackedOutput: ok=%t err=%v", ok, err)
				}
			}))
			assertCommittedRowsIntact(t, screen, []string{"C01", "D01-draft"}, "soft draft")

			// Stable commit: the draft tail is replaced by two final rows.
			screen.feed(captureSurfaceStdout(t, func() {
				if !surface.RewriteSoftOutputTail(os.Stdout, []string{row("S01"), row("S02")}) {
					t.Fatal("expected soft tail rewrite to be accepted")
				}
			}))
			if rows := screenRowsContaining(screen, "D01-draft"); len(rows) != 0 {
				t.Fatalf("stale draft row still on screen at %v\n%s", rows, screen.dump())
			}
			assertCommittedRowsIntact(t, screen, markers, "soft rewrite")

			screen.feed(captureSurfaceStdout(t, func() {
				surface.ClearActiveBand()
				surface.ShowPrompt("> ")
			}))
			liveLast := assertCommittedRowsIntact(t, screen, markers, "band release")
			assertLayoutMatchesBaseline(t, "soft tail rewrite under band", screen, liveLast,
				surfacePromptRow(t, screen), baseScreen, baseLast, basePrompt)
		})
	}
}
