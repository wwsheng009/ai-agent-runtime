package commands

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// screenVT is a minimal VT100 subset (scroll region, cursor addressing, erase
// line, index/reverse index) used to reconstruct what the fixed bottom surface
// actually leaves on screen. Sequence-level assertions cannot catch row math
// errors such as a band painted above the rows it reserved, so these tests
// replay the byte stream and inspect the resulting rows.
type screenVT struct {
	width, height int
	rows          [][]rune
	row, col      int
	top, bottom   int
	savedRow      int
	savedCol      int
	hasSaved      bool
}

func newScreenVT(width, height int) *screenVT {
	v := &screenVT{width: width, height: height, row: 1, col: 1, top: 1, bottom: height}
	v.rows = make([][]rune, height)
	for i := range v.rows {
		v.rows[i] = blankScreenRow(width)
	}
	return v
}

func blankScreenRow(width int) []rune {
	row := make([]rune, width)
	for i := range row {
		row[i] = ' '
	}
	return row
}

func (v *screenVT) index() {
	if v.row == v.bottom {
		copy(v.rows[v.top-1:v.bottom-1], v.rows[v.top:v.bottom])
		v.rows[v.bottom-1] = blankScreenRow(v.width)
		return
	}
	if v.row < v.height {
		v.row++
	}
}

func (v *screenVT) reverseIndex() {
	if v.row == v.top {
		for i := v.bottom - 1; i > v.top-1; i-- {
			v.rows[i] = v.rows[i-1]
		}
		v.rows[v.top-1] = blankScreenRow(v.width)
		return
	}
	if v.row > 1 {
		v.row--
	}
}

func (v *screenVT) scrollDown(rows int) {
	if rows < 1 {
		rows = 1
	}
	regionRows := v.bottom - v.top + 1
	if rows > regionRows {
		rows = regionRows
	}
	for row := v.bottom - 1; row >= v.top-1+rows; row-- {
		v.rows[row] = v.rows[row-rows]
	}
	for row := v.top - 1; row < v.top-1+rows; row++ {
		v.rows[row] = blankScreenRow(v.width)
	}
}

func (v *screenVT) put(r rune) {
	if v.col > v.width {
		v.col = 1
		v.index()
	}
	v.rows[v.row-1][v.col-1] = r
	v.col++
}

func (v *screenVT) feed(s string) {
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\r':
			v.col = 1
		case '\n':
			v.index()
			v.col = 1
		case 0x1b:
			i += v.escape(runes[i+1:])
		default:
			v.put(runes[i])
		}
	}
}

// escape consumes one escape sequence and reports how many runes it used,
// excluding the leading ESC.
func (v *screenVT) escape(rest []rune) int {
	if len(rest) == 0 {
		return 0
	}
	switch rest[0] {
	case 'M':
		v.reverseIndex()
		return 1
	case 'D':
		v.index()
		return 1
	case '7':
		v.savedRow, v.savedCol, v.hasSaved = v.row, v.col, true
		return 1
	case '8':
		if v.hasSaved {
			v.row, v.col = v.savedRow, v.savedCol
		}
		return 1
	case ']':
		for i := 1; i < len(rest); i++ {
			if rest[i] == 0x07 {
				return i + 1
			}
			if rest[i] == 0x1b && i+1 < len(rest) && rest[i+1] == '\\' {
				return i + 2
			}
		}
		return len(rest)
	case '[':
		j := 1
		for j < len(rest) && (rest[j] == '?' || rest[j] == ';' || (rest[j] >= '0' && rest[j] <= '9')) {
			j++
		}
		if j >= len(rest) {
			return len(rest)
		}
		v.csi(string(rest[1:j]), rest[j])
		return j + 1
	}
	return 1
}

func (v *screenVT) csi(params string, final rune) {
	fields := []int{}
	for _, part := range strings.Split(strings.TrimPrefix(params, "?"), ";") {
		if part == "" {
			fields = append(fields, 0)
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			n = 0
		}
		fields = append(fields, n)
	}
	arg := func(i, def int) int {
		if i < len(fields) && fields[i] > 0 {
			return fields[i]
		}
		return def
	}
	clamp := func(row int) int {
		if row < 1 {
			return 1
		}
		if row > v.height {
			return v.height
		}
		return row
	}
	switch final {
	case 'r':
		v.top, v.bottom = clamp(arg(0, 1)), clamp(arg(1, v.height))
	case 'H', 'f':
		v.row, v.col = clamp(arg(0, 1)), arg(1, 1)
	case 'A':
		v.row = clamp(v.row - arg(0, 1))
	case 'B':
		v.row = clamp(v.row + arg(0, 1))
	case 'C':
		v.col += arg(0, 1)
	case 'D':
		if v.col = v.col - arg(0, 1); v.col < 1 {
			v.col = 1
		}
	case 'T':
		v.scrollDown(arg(0, 1))
	case 'K':
		switch arg(0, 0) {
		case 1:
			for i := 0; i < v.col && i < v.width; i++ {
				v.rows[v.row-1][i] = ' '
			}
		case 2:
			v.rows[v.row-1] = blankScreenRow(v.width)
		default:
			for i := v.col - 1; i < v.width; i++ {
				v.rows[v.row-1][i] = ' '
			}
		}
	case 'J':
		if arg(0, 0) == 2 {
			for i := range v.rows {
				v.rows[i] = blankScreenRow(v.width)
			}
			break
		}
		for i := v.col - 1; i < v.width; i++ {
			v.rows[v.row-1][i] = ' '
		}
		for i := v.row; i < v.height; i++ {
			v.rows[i] = blankScreenRow(v.width)
		}
	case 's':
		v.savedRow, v.savedCol, v.hasSaved = v.row, v.col, true
	case 'u':
		if v.hasSaved {
			v.row, v.col = v.savedRow, v.savedCol
		}
	}
}

// line returns the 1-based screen row with trailing padding removed.
func (v *screenVT) line(row int) string {
	if row < 1 || row > v.height {
		return ""
	}
	return strings.TrimRight(string(v.rows[row-1]), " ")
}

func (v *screenVT) dump() string {
	var b strings.Builder
	for i := 1; i <= v.height; i++ {
		fmt.Fprintf(&b, "%02d|%s\n", i, v.line(i))
	}
	return b.String()
}

func captureSurfaceStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(reader)
		done <- buf.String()
	}()
	defer func() {
		os.Stdout = original
	}()
	fn()
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
			coord.SetSurface(surface)

			screen := newScreenVT(width, height)

			streaming := captureSurfaceStdout(t, func() {
				coord.SetWriter(os.Stdout)
				surface.ShowPrompt("> ")
				// The chat loop clears the prompt when the user submits, so the
				// band renders while no prompt rows are reserved.
				surface.ClearPromptRows(1)
				coord.RenderAsyncLine("[tool] view backend/main.go")
				for _, chunk := range strings.SplitAfter(markdownReply, "\n") {
					if chunk != "" {
						coord.RenderAssistantDelta(chunk)
					}
				}
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
