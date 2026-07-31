package commands

import (
	"strings"
	"testing"
)

func TestNormalizeWriteLines_StripsBlockTerminatorKeepsInternalBlanks(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "only terminators", in: "\n\n", want: nil},
		{name: "single line", in: "hello", want: []string{"hello"}},
		{name: "trailing LF", in: "hello\n", want: []string{"hello"}},
		{name: "trailing CRLF", in: "hello\r\n", want: []string{"hello"}},
		{name: "multi line", in: "a\nb\n", want: []string{"a", "b"}},
		{name: "internal blank", in: "a\n\nb\n", want: []string{"a", "", "b"}},
		{name: "cr only parts", in: "a\r\nb\r\n", want: []string{"a", "b"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeWriteLines(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d; got %#v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("line[%d]=%q want %q (full %#v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestBuildRenderedAssistantChunk_EmptyAndNoTrailingLFOnRows(t *testing.T) {
	t.Parallel()

	if !buildRenderedAssistantChunk("").empty() {
		t.Fatal("empty input should be empty chunk")
	}
	if !buildRenderedAssistantChunk("\n\n").empty() {
		t.Fatal("terminator-only input should be empty chunk")
	}

	chunk := buildRenderedAssistantChunk("alpha\nbeta\n")
	if chunk.empty() {
		t.Fatal("expected non-empty chunk")
	}
	for i, line := range chunk.lines {
		if strings.HasSuffix(line, "\n") || strings.HasSuffix(line, "\r") {
			t.Fatalf("line[%d] still carries terminator: %q", i, line)
		}
	}
	if len(chunk.lines) < 2 {
		t.Fatalf("expected at least 2 rows, got %#v", chunk.lines)
	}
}

func TestEnsureStreamTerminatedLocked_Idempotent(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	c := &chatInteractionCoordinator{
		writer:           &buf,
		streamRendered:   true,
		streamTrailingLF: false,
	}
	c.ensureStreamTerminatedLocked()
	if !c.streamTrailingLF {
		t.Fatal("expected streamTrailingLF after terminate")
	}
	first := buf.String()
	c.ensureStreamTerminatedLocked()
	if buf.String() != first {
		t.Fatalf("second terminate must be no-op; got %q then %q", first, buf.String())
	}
	if first != "\n" {
		t.Fatalf("expected single blank terminator, got %q", first)
	}
}

// Phase C: row-boundary cursor must not invent phantom blanks into history.
func TestCloseOpenRowLocked_NoOpWhenAlreadyAtRowBoundary(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	c := &chatInteractionCoordinator{
		writer:           &buf,
		streamTrailingLF: true, // start/reset contract
	}
	c.closeOpenRowLocked()
	if buf.Len() != 0 {
		t.Fatalf("closeOpenRow at row boundary must write nothing, got %q", buf.String())
	}
	if !c.streamTrailingLF {
		t.Fatal("streamTrailingLF must stay true")
	}
}

// Phase C: only a real mid-line open row is terminated.
func TestCloseOpenRowLocked_ClosesMidLineOnce(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	c := &chatInteractionCoordinator{
		writer:           &buf,
		streamTrailingLF: false,
		streamRendered:   true,
	}
	c.closeOpenRowLocked()
	if got := buf.String(); got != "\n" {
		t.Fatalf("mid-line close should write one row terminator, got %q", got)
	}
	if !c.streamTrailingLF {
		t.Fatal("expected streamTrailingLF after close")
	}
	c.closeOpenRowLocked()
	if got := buf.String(); got != "\n" {
		t.Fatalf("second close must be no-op, got %q", got)
	}
}

// Phase D: soft row rebuild ignores completeBlockOutput / gap flags.
func TestRenderSoftEmittedLinesLocked_IgnoresCompleteBlockPollution(t *testing.T) {
	t.Parallel()

	c := &chatInteractionCoordinator{
		streamMode:          assistantStreamModeText,
		completeBlockOutput: true,
	}
	c.streamBuffer.WriteString("alpha\nbeta\n")
	// Pollution flags must not invent a leading blank row in soft rebuild.
	got := c.renderSoftEmittedLinesLocked(0, c.streamBuffer.Len(), 80)
	if len(got) == 0 {
		t.Fatal("expected soft rows")
	}
	if got[0] == "" {
		t.Fatalf("soft rebuild must not invent a leading blank from completeBlockOutput: %#v", got)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "alpha") || !strings.Contains(joined, "beta") {
		t.Fatalf("unexpected soft rows: %#v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] == "" && got[i] == "" {
			t.Fatalf("soft rebuild invented double blank at %d: %#v", i, got)
		}
	}
}

func TestWriteCompleteBlockLocked_EmitsMultiLineAtomically(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	c := &chatInteractionCoordinator{writer: &buf, streamTrailingLF: true}
	c.writeCompleteBlockLocked("line-a\nline-b\n", gapNone)
	got := buf.String()
	want := "line-a\nline-b\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !c.completeBlockOutput {
		t.Fatal("expected completeBlockOutput")
	}
	if !c.streamTrailingLF {
		t.Fatal("expected streamTrailingLF after complete block")
	}
}

func TestWriteCompleteBlockLocked_ExplicitGapBlankInsertsOneSeparator(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	c := &chatInteractionCoordinator{writer: &buf, streamTrailingLF: true}
	// Pollution flags must NOT invent a gap; only the explicit gapBlank does.
	c.completeBlockOutput = true
	c.writeCompleteBlockLocked("next\n", gapNone)
	if got := buf.String(); got != "next\n" {
		t.Fatalf("gapNone must not invent blanks from flags; got %q", got)
	}

	buf.Reset()
	c = &chatInteractionCoordinator{writer: &buf, streamTrailingLF: true}
	c.writeCompleteBlockLocked("next\n", gapBlank)
	if got := buf.String(); got != "\nnext\n" {
		t.Fatalf("gapBlank should insert exactly one separator; got %q", got)
	}
}

// TestWriteCompleteBlockLocked_EditedDiffKeepsDenseRows pins the long tool
// supplement contract: multi-line "• Edited" blocks must not grow blank runs
// between consecutive content rows when written as a complete block.
func TestWriteCompleteBlockLocked_EditedDiffKeepsDenseRows(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	c := &chatInteractionCoordinator{writer: &buf, streamTrailingLF: true}
	block := strings.Join([]string{
		"• Edited demo.go (+1 -1)",
		"  10   func main() {",
		"  11 - old()",
		"  11 + new()",
		"  12   }",
	}, "\n")
	c.writeCompleteBlockLocked(block, gapNone)
	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("expected trailing row terminator, got %q", got)
	}
	rows := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(rows) != 5 {
		t.Fatalf("expected 5 dense rows, got %d: %#v", len(rows), rows)
	}
	for i, row := range rows {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("row %d is blank; complete block must not inject holes: %#v", i, rows)
		}
	}
}
