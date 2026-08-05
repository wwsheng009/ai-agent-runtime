package commands

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// screenVT reconstructs what the fixed bottom surface actually leaves on
// screen. Sequence-level assertions cannot catch row math errors such as a band
// painted above the rows it reserved, so these tests replay the byte stream and
// inspect the resulting rows.
//
// The emulator itself lives in ui/vt so the ui package can assert on real
// screens too; it is display-width aware, tracks SGR per cell and is covered by
// its own tests. This wrapper only keeps the compact lowercase call sites.
type screenVT struct {
	*vt.Screen
}

func newScreenVT(width, height int) *screenVT {
	return &screenVT{Screen: vt.NewScreen(width, height)}
}

func (v *screenVT) feed(stream string) { v.Screen.Feed(stream) }

func (v *screenVT) line(row int) string { return v.Screen.Line(row) }

func (v *screenVT) dump() string { return v.Screen.Dump() }

func captureSurfaceStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	restoreTerminalOutput := ui.SetTerminalOutputForTesting(writer)
	restored := false
	restore := func() {
		if restored {
			return
		}
		restoreTerminalOutput()
		os.Stdout = original
		restored = true
	}
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(reader)
		done <- buf.String()
	}()
	defer restore()
	fn()
	restore()
	_ = writer.Close()
	return <-done
}

// TestChatInteractionCoordinator_StreamLeavesNoBlankRowsAbovePrompt replays a
// full streaming turn (prompt hidden -> active band -> markdown commit -> prompt
// restored) and asserts the reconstructed screen keeps the transcript adjacent
// to the bottom pane. The band used to be painted above the rows it reserved,
// leaving as many blank rows above the status line as the band was tall.
func TestChatInteractionCoordinator_StreamLeavesNoBlankRowsAbovePrompt(t *testing.T) {
	const markdownReply = "# 结论\n\n这是第一段说明文字。\n\n- 第一项\n- 第二项\n- 第三项\n\n```go\nfunc main() {\n\tprintln(\"one\")\n}\n```\n\n收尾说明。\n"

	for _, height := range []int{24, 40} {
		t.Run(fmt.Sprintf("height=%d", height), func(t *testing.T) {
			const width = 80
			session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
			coord := newChatInteractionCoordinator(session)
			coord.stableCommitDelay = time.Hour
			t.Cleanup(coord.Shutdown)
			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)

			screen := newScreenVT(width, height)

			streaming := captureSurfaceStdout(t, func() {
				// SetSurface publishes the initial persistent status frame. Keep
				// it in the same byte stream that is replayed into screen;
				// otherwise the owned viewport front buffer legitimately omits
				// that unchanged row from subsequent minimal diffs.
				coord.SetSurface(surface)
				coord.SetWriter(os.Stdout)
				surface.ShowPrompt("> ")
				coord.waitUIActorIdle()
				// The chat loop clears the prompt when the user submits, so the
				// band renders while no prompt rows are reserved.
				surface.ClearPromptRows(1)
				coord.RenderAsyncLine("[tool] view backend/main.go")
				for _, chunk := range strings.SplitAfter(markdownReply, "\n") {
					if chunk != "" {
						coord.RenderAssistantDelta(chunk)
					}
				}
				// Phase 1：facade action 由 UI actor 异步应用，capture 结束前等
				// actor 排空，保证重放字节流包含完整渲染。
				coord.waitUIActorIdle()
			})
			screen.feed(streaming)

			if got := screen.line(height); strings.TrimSpace(got) == "" {
				t.Fatalf("expected status row %d to stay painted, screen:\n%s", height, screen.dump())
			}
			if got := screen.line(height - 1); strings.TrimSpace(got) == "" {
				t.Fatalf("active band must reach row %d, leaving no blank gap above the status row, screen:\n%s",
					height-1, screen.dump())
			}

			final := captureSurfaceStdout(t, func() {
				coord.SetWriter(os.Stdout)
				coord.FinalizeAssistantDelta()
				surface.ShowPrompt("> ")
				coord.waitUIActorIdle()
			})
			screen.feed(final)

			promptRow := height - 2
			if got := screen.line(promptRow); !strings.HasPrefix(got, ">") {
				t.Fatalf("expected prompt on row %d, got %q, screen:\n%s", promptRow, got, screen.dump())
			}
			if got := screen.line(height); strings.TrimSpace(got) == "" {
				t.Fatalf("expected status row %d to stay painted, screen:\n%s", height, screen.dump())
			}
			lastText := 0
			for row := promptRow - 1; row >= 1; row-- {
				if strings.TrimSpace(screen.line(row)) != "" {
					lastText = row
					break
				}
			}
			if lastText == 0 {
				t.Fatalf("expected committed transcript above the prompt, screen:\n%s", screen.dump())
			}
			if gap := promptRow - lastText - 1; gap > 2 {
				t.Fatalf("expected only the composer top margin and one transcript separator above the prompt, got %d blank rows, screen:\n%s",
					gap, screen.dump())
			}
			if got := screen.line(lastText); !strings.Contains(got, "收尾说明。") {
				t.Fatalf("expected the last committed markdown line above the prompt, got %q, screen:\n%s",
					got, screen.dump())
			}
		})
	}
}

// TestChatInteractionCoordinator_SubmittedUserInputDoesNotOverwriteHistory
// pins the submit-path blank-absorption bug: after a completed history line
// ends with LF, ShowPrompt absorbs that blank into the bottom reserve. If the
// user echo is written while the prompt is still reserved, WriteOutput lands
// on the last history row and overwrites it (no newline / visual overlap).
func TestChatInteractionCoordinator_SubmittedUserInputDoesNotOverwriteHistory(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 80, 24
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	t.Cleanup(coord.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)

	screen := newScreenVT(width, height)

	// Establish layout, write a completed history line (trailing LF), then show the
	// ready prompt so it absorbs that blank into the bottom reserve — the state
	// the submit path used to overwrite.
	seed := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		if !surface.ShowPrompt(ui.UserPromptText(0)) {
			t.Fatal("expected initial ShowPrompt")
		}
		if !surface.ClearPromptRows(1) {
			t.Fatal("expected ClearPromptRows")
		}
		coord.RenderAssistant("上一轮助手回复内容")
		if !surface.ShowPrompt(ui.UserPromptText(0)) {
			t.Fatal("expected ready ShowPrompt")
		}
		coord.mu.Lock()
		coord.promptVisible = true
		coord.promptRenderedOnSurface = true
		coord.waitingActive = true
		coord.mu.Unlock()
	})
	screen.feed(seed)

	if !strings.Contains(screen.dump(), "上一轮助手回复内容") {
		t.Fatalf("precondition: history must be on screen, got:\n%s", screen.dump())
	}

	// Bug path: submit echo without an external ClearPromptRows. The coordinator
	// itself must free the composer before writing the user block.
	echo := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		coord.RenderSubmittedUserInput("用户新问题")
	})
	screen.feed(echo)

	dump := screen.dump()
	if !strings.Contains(dump, "上一轮助手回复内容") {
		t.Fatalf("user echo must not overwrite prior history, screen:\n%s", dump)
	}
	// FormatUserMessage may include icon chrome; match on the user text itself.
	if !strings.Contains(dump, "用户新问题") {
		t.Fatalf("expected submitted user text on its own row, screen:\n%s", dump)
	}
	// History and user echo must not share a single reconstructed row.
	for row := 1; row <= height; row++ {
		line := screen.line(row)
		if strings.Contains(line, "上一轮助手回复内容") && strings.Contains(line, "用户新问题") {
			t.Fatalf("history and user echo overlapped on row %d: %q\nscreen:\n%s", row, line, dump)
		}
	}
}
