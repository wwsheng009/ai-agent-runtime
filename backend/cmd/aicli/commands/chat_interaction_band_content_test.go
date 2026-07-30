package commands

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// Mid-stream assertions used to be structural only ("no blank hole above the
// band", "band contains the newest tail"). That cannot see what the band prints:
// a dropped block separator, a duplicated row or a reordered row inside the band
// all passed. These tests compare the band content itself.

// parityRow normalizes a screen row for live-vs-replay comparison: ANSI is
// dropped and the assistant indent is removed, because scrollback rows are
// indented while ActiveBand rows are painted flush left.
func parityRow(row string) string {
	return strings.TrimRight(strings.TrimLeft(stripTerminalDecorations(row), " \t"), " \t")
}

func parityRows(rows []string) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, parityRow(row))
	}
	return out
}

// streamRuneChunks feeds source through the coordinator in small chunks so the
// stable-commit path runs many times, like a real provider stream.
func streamRuneChunks(coord *chatInteractionCoordinator, source string, size int) {
	runes := []rune(source)
	if size < 1 {
		size = 1
	}
	for start := 0; start < len(runes); start += size {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		coord.RenderAssistantDelta(string(runes[start:end]))
	}
}

// bandRegion reports the 1-based screen rows the active band occupies. The band
// is laid out bottom-up above the notice / dynamic status / status rows, and how
// many of those exist depends on run state, so the region is located by matching
// the painted band rows from the bottom instead of assuming an offset.
func bandRegion(t *testing.T, screen *screenVT, band []string) (start, end int) {
	t.Helper()
	if len(band) == 0 {
		t.Fatalf("no band rows to locate\n%s", screen.dump())
	}
	for candidate := screen.Height() - len(band) + 1; candidate >= 1; candidate-- {
		matched := true
		for i, want := range band {
			if screen.line(candidate+i) != strings.TrimRight(want, " \t") {
				matched = false
				break
			}
		}
		if matched {
			return candidate, candidate + len(band) - 1
		}
	}
	t.Fatalf("painted band %#v not found on screen\n%s", band, screen.dump())
	return 0, 0
}

// settleBandFrame stops the stable-commit timer, flushes the queue and lands the
// pending coalesced frame. Production does the last step from the 30 FPS
// animation ticker; a synchronous test would otherwise assert a stale band,
// because FrameScheduler suppresses every paint that arrives inside one frame
// window and only a commit-driven repaint forces it out.
func settleBandFrame(coord *chatInteractionCoordinator) {
	coord.mu.Lock()
	coord.stopActiveStableCommitLocked()
	coord.drainActiveStableCommitLocked(true)
	_ = coord.publishActiveStreamFrameLocked(true)
	coord.mu.Unlock()
}

// TestChatInteractionCoordinator_MidStreamScreenRowsMatchReplayRows is the
// mid-stream oracle: committed scrollback rows plus the band body must be
// row-identical to a one-shot replay of everything received so far. The band
// header is chrome and the only row excluded.
func TestChatInteractionCoordinator_MidStreamScreenRowsMatchReplayRows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 80, 40
	var reply strings.Builder
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&reply, "## 章节 %d\n\n这是第 %d 段正文。\n\n", i, i)
	}
	// A source ending on a blank line leaves no holdback, so the band shows only
	// rendered Markdown and the comparison can be exact.
	src := reply.String()

	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	t.Cleanup(coord.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)

	screen.feed(captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		surface.ShowPrompt("> ")
		surface.ClearPromptRows(1)
	}))
	screen.feed(captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		streamRuneChunks(coord, src, 12)
		settleBandFrame(coord)
	}))

	band := surface.ActiveBandLines()
	if len(band) < 2 {
		t.Fatalf("expected a mid-stream band with a header and body, got %v\n%s", band, screen.dump())
	}
	bandStart, bandEnd := bandRegion(t, screen, band)
	if rows := screen.OverflowRows(); len(rows) != 0 {
		t.Fatalf("rows %v exceeded the terminal width\n%s", rows, screen.dump())
	}

	got := make([]string, 0, height)
	for row := 1; row < bandStart; row++ {
		got = append(got, parityRow(screen.line(row)))
	}
	for len(got) > 0 && got[0] == "" {
		got = got[1:]
	}
	for row := bandStart + 1; row <= bandEnd; row++ {
		got = append(got, parityRow(screen.line(row)))
	}
	for len(got) > 0 && got[len(got)-1] == "" {
		got = got[:len(got)-1]
	}

	want := parityRows(normalizeWriteLines(ui.FormatAssistantRendered(
		strings.TrimRight(session.Formatter.Format(src), "\r\n"))))

	if len(got) != len(want) {
		t.Fatalf("mid-stream rows=%d replay rows=%d\ngot=%#v\nwant=%#v\nscreen:\n%s",
			len(got), len(want), got, want, screen.dump())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mid-stream row[%d]=%q replay=%q\ngot=%#v\nwant=%#v\nscreen:\n%s",
				i, got[i], want[i], got, want, screen.dump())
		}
	}
}

// TestChatInteractionCoordinator_MidStreamBandKeepsBlockSeparatorBeforeHoldback
// pins the stable/holdback seam on a real screen: the collector cuts the stable
// prefix on a blank line, and Markdown rendering consumes that blank, so the
// mutable tail has to bring the block separator back. A soft line break inside
// one block must stay tight. Structural "no blank hole" assertions cannot see
// either case.
func TestChatInteractionCoordinator_MidStreamBandKeepsBlockSeparatorBeforeHoldback(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	cases := []struct {
		name      string
		source    string
		holdback  string
		wantBlank int
	}{
		{
			name:      "block boundary keeps one blank",
			source:    "## 章节 1\n\n这是第 1 段正文。\n\n下一段正在流",
			holdback:  "下一段正在流",
			wantBlank: 1,
		},
		{
			name:      "soft break stays tight",
			source:    "## 章节 1\n\n这是第 1 段正文。\n同一段的续行",
			holdback:  "同一段的续行",
			wantBlank: 0,
		},
		{
			name:      "open fence keeps one blank",
			source:    "## 章节 1\n\n说明文字。\n\n```go\nfunc pending() {\n",
			holdback:  "```go",
			wantBlank: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const width, height = 80, 40
			session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
			coord := newChatInteractionCoordinator(session)
			coord.stableCommitDelay = time.Hour
			t.Cleanup(coord.Shutdown)
			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)
			coord.SetSurface(surface)
			screen := newScreenVT(width, height)

			screen.feed(captureSurfaceStdout(t, func() {
				coord.SetWriter(os.Stdout)
				surface.ShowPrompt("> ")
				surface.ClearPromptRows(1)
			}))
			screen.feed(captureSurfaceStdout(t, func() {
				coord.SetWriter(os.Stdout)
				streamRuneChunks(coord, tc.source, 8)
				settleBandFrame(coord)
			}))

			band := surface.ActiveBandLines()
			if len(band) == 0 {
				t.Fatalf("expected a mid-stream band\n%s", screen.dump())
			}
			if joined := strings.Join(band, "\n"); !strings.Contains(joined, tc.holdback) {
				t.Fatalf("mutable tail %q missing from band %#v\n%s", tc.holdback, band, screen.dump())
			}
			if rows := screen.OverflowRows(); len(rows) != 0 {
				t.Fatalf("rows %v exceeded the terminal width\n%s", rows, screen.dump())
			}
			if got := blankRowsBeforeAnchor(screen, screen.Height(), tc.holdback); got != tc.wantBlank {
				t.Fatalf("blank rows before the mutable tail %q = %d, want %d\n%s",
					tc.holdback, got, tc.wantBlank, screen.dump())
			}
		})
	}
}

// TestChatInteractionCoordinator_MidStreamBandHoldbackStaysDim uses the per-cell
// SGR tracking of the screen emulator: the mutable tail must remain visually
// distinct from committed content, which sequence-level tests cannot verify.
func TestChatInteractionCoordinator_MidStreamBandHoldbackStaysDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("AICLI_COLOR_DEPTH", "truecolor")
	ui.SetTheme(ui.ThemeDark)
	t.Cleanup(func() { ui.SetTheme(ui.ThemeAuto) })

	const width, height = 80, 40
	const source = "## 章节 1\n\n这是第 1 段正文。\n\n下一段正在流"
	const holdback = "下一段正在流"

	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(true)}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	t.Cleanup(coord.Shutdown)
	// EnableForTest pins geometry. Color/SGR still depend on host driver caps and
	// FORCE_COLOR / AICLI_COLOR_DEPTH env set above.
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)

	screen.feed(captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		surface.ShowPrompt("> ")
		surface.ClearPromptRows(1)
	}))
	screen.feed(captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		streamRuneChunks(coord, source, 8)
		settleBandFrame(coord)
	}))

	rows := screen.RowsContaining(holdback)
	if len(rows) != 1 {
		t.Fatalf("mutable tail should be painted once, got rows %v\n%s", rows, screen.dump())
	}
	codes := screen.RowSGRCodes(rows[0])
	if len(codes) == 0 {
		t.Fatalf("expected styled holdback SGR on row %d, got none\n%s", rows[0], screen.dump())
	}
	if !codes["2"] {
		t.Fatalf("mutable tail row %d lost its dim attribute, codes=%v\n%s", rows[0], codes, screen.dump())
	}
}
