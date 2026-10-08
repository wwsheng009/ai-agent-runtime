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
			coord := newTestChatInteractionCoordinator(t, session)
			coord.stableCommitDelay = time.Hour
			t.Cleanup(coord.Shutdown)
			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(width, height)

			// L3-3：band/prompt/status 由 AppState 承载，已提交 transcript 由
			// surface 历史窗口承载。band 直接渲染在保留区上方，不存在“band
			// 画在保留行之上留下空洞”的物理路径；仍钉住 band 有界有尾内容、
			// 历史无 band 级空洞、finalize 后 prompt/status 就绪。
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
			coord.waitUIActorIdle()

			band := s2BandLines(t, coord)
			if len(band) == 0 {
				t.Fatal("expected active band mid-stream")
			}
			if strings.TrimSpace(band[len(band)-1]) == "" {
				t.Fatalf("active band tail row must stay painted, got %v", band)
			}
			if state := coord.uiActor.AppState(); strings.TrimSpace(statusModelPlainText(state.Bottom.StatusModel, width)) == "" {
				t.Fatalf("expected a non-empty status model mid-stream, got %+v", state.Bottom)
			}
			midHistory := s2TrimLeadingBlanks(s2HistoryRows(surface))
			if run := s2MaxBlankRun(s2TrimTrailingBlanks(midHistory)); run >= ui.ActiveBandMinRows {
				t.Fatalf("mid-stream history window left a %d-row blank hole\n%#v", run, midHistory)
			}

			coord.FinalizeAssistantDelta()
			surface.ShowPrompt("> ")
			coord.waitUIActorIdle()

			state := coord.uiActor.AppState()
			if !state.Bottom.PromptVisible || !strings.HasPrefix(state.Bottom.PromptLine, ">") {
				t.Fatalf("expected ready composer prompt after finalize, got %+v", state.Bottom)
			}
			if strings.TrimSpace(statusModelPlainText(state.Bottom.StatusModel, width)) == "" {
				t.Fatalf("expected a non-empty status model after finalize, got %+v", state.Bottom)
			}
			if got := len(s2BandLines(t, coord)); got != 0 {
				t.Fatalf("finalize should clear active band, still %d lines", got)
			}

			history := s2TrimLeadingBlanks(s2HistoryRows(surface))
			trimmed := s2TrimTrailingBlanks(history)
			if len(trimmed) == 0 {
				t.Fatalf("expected committed transcript above the prompt\n%#v", history)
			}
			if n := s2Count(trimmed, "收尾说明。"); n != 1 {
				t.Fatalf("expected the last committed markdown line above the prompt exactly once, got %d\n%#v",
					n, history)
			}
			if gap := s2TrailingBlankCount(history); gap > 2 {
				t.Fatalf("expected only the composer top margin and one transcript separator above the prompt, got %d blank rows\n%#v",
					gap, history)
			}
			if run := s2MaxBlankRun(trimmed); run > 1 {
				t.Fatalf("expected no multi-row transcript hole above the prompt, got run %d\n%#v", run, history)
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
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	t.Cleanup(coord.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	coord.SetSurface(surface)

	// Establish layout, write a completed history line (trailing LF), then show the
	// ready prompt so it absorbs that blank into the bottom reserve — the state
	// the submit path used to overwrite.
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
	coord.waitUIActorIdle()

	// 合成帧是历史 + 底部保留区的权威投影（该 surface 的历史写入不经 poster）。
	frame := composedFrameLines(surface)
	if dump := strings.Join(frame, "\n"); !strings.Contains(dump, "上一轮助手回复内容") {
		t.Fatalf("precondition: history must be in composed frame, got:\n%s", dump)
	}

	// Bug path: submit echo without an external ClearPromptRows. The coordinator
	// itself must free the composer before writing the user block.
	coord.RenderSubmittedUserInput("用户新问题")
	coord.waitUIActorIdle()

	frame = composedFrameLines(surface)
	dump := strings.Join(frame, "\n")
	if !strings.Contains(dump, "上一轮助手回复内容") {
		t.Fatalf("user echo must not overwrite prior history, frame:\n%s", dump)
	}
	// FormatUserMessage may include icon chrome; match on the user text itself.
	if !strings.Contains(dump, "用户新问题") {
		t.Fatalf("expected submitted user text on its own row, frame:\n%s", dump)
	}
	// History and user echo must not share a single reconstructed row.
	for i, line := range frame {
		if strings.Contains(line, "上一轮助手回复内容") && strings.Contains(line, "用户新问题") {
			t.Fatalf("history and user echo overlapped on frame row %d: %q\n%s", i+1, line, dump)
		}
	}
}
