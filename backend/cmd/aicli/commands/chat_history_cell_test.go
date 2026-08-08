package commands

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// Compile-time proof that userMessageCell satisfies the historyCell contract.
var (
	_ historyCell = userMessageCell{}
	_ historyCell = assistantMessageCell{}
	_ historyCell = supplementLineCell{}
	_ historyCell = asyncDocumentCell{}
	_ historyCell = toolChainCell{}
	_ historyCell = assistantStreamCell{}
)

// TestUserMessageCell_DisplayLinesMatchLegacyPipeline pins that routing the
// user-echo block through the cell model (P4.1) is byte-identical to the legacy
// writeCompleteBlockLocked(FormatUserMessage) path for widths that do not wrap.
func TestUserMessageCell_DisplayLinesMatchLegacyPipeline(t *testing.T) {
	inputs := []string{
		"hello",
		"line one\nline two",
		"  trailing spaces  ",
		"multi\n\nblank inside",
		"你好，世界",
	}
	for _, in := range inputs {
		cell := newUserMessageCell(in)
		if cell.Kind() != historyCellUser {
			t.Fatalf("Kind()=%d want historyCellUser", cell.Kind())
		}
		want := normalizeWriteLines(ui.FormatUserMessage(in))
		// width 0 and generous widths that fit keep legacy bytes.
		for _, width := range []int{0, 40, 80, 120} {
			got := cell.DisplayLines(width)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("DisplayLines(width=%d) for %q\n got %#v\nwant %#v", width, in, got, want)
			}
		}
	}
}

// TestUserMessageCell_DisplayLinesWrapsNarrowWidth pins real line breaks via
// render.Wrap for content that exceeds the requested width (P5.4-S2).
func TestUserMessageCell_DisplayLinesWrapsNarrowWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cell := newUserMessageCell("abcdefghijklmnopqrstuvwxyz")
	got := cell.DisplayLines(10)
	if len(got) < 2 {
		t.Fatalf("expected wrap into multiple rows at width=10, got %#v", got)
	}
	for i, row := range got {
		if plain := render.ANSIToPlain(row); render.Width(plain) > 10 {
			t.Fatalf("row %d wider than 10: %q (width=%d)", i, plain, render.Width(plain))
		}
	}
}

// TestAssistantMessageCell_DisplayLinesMatchLegacyPipeline pins the assistant
// one-shot cell (P4.2) against the unified plain-block chrome
// (FormatAssistantBlockChrome) — the plain stream cell and the one-shot
// reference must project the identical rows.
func TestAssistantMessageCell_DisplayLinesMatchLegacyPipeline(t *testing.T) {
	bodies := []string{
		"just text",
		"para one\n\npara two",
		"- a\n- b\n- c",
		"你好，助手",
	}
	for _, body := range bodies {
		cell := newAssistantMessageCell(body)
		if cell.Kind() != historyCellAssistant {
			t.Fatalf("Kind()=%d want historyCellAssistant", cell.Kind())
		}
		want := normalizeWriteLines(ui.FormatAssistantBlockChrome(body))
		for _, width := range []int{0, 40, 80, 120} {
			if got := cell.DisplayLines(width); !reflect.DeepEqual(got, want) {
				t.Fatalf("assistant DisplayLines(width=%d) for %q\n got %#v\nwant %#v", width, body, got, want)
			}
		}
	}
}

// TestSupplementLineCell_DisplayLinesMatchLegacyPipeline pins the async/supplement
// cell (P4.2) against writeCompleteBlockLocked(FormatAssistantSupplementBlock).
func TestSupplementLineCell_DisplayLinesMatchLegacyPipeline(t *testing.T) {
	lines := []string{
		"• Running ls",
		"• Completed ls\n  output row",
		"⚠ warning text",
	}
	for _, line := range lines {
		cell := newSupplementLineCell(line)
		if cell.Kind() != historyCellSupplement {
			t.Fatalf("Kind()=%d want historyCellSupplement", cell.Kind())
		}
		want := normalizeWriteLines(ui.FormatAssistantSupplementBlock(line))
		if got := cell.DisplayLines(80); !reflect.DeepEqual(got, want) {
			t.Fatalf("supplement DisplayLines for %q\n got %#v\nwant %#v", line, got, want)
		}
	}
}

// TestToolChainCell_DisplayLinesDenseRunningAndCompleted pins the P5.6 single-cell
// tool chain model: Running is dense (no gap) and Completed is one final block.
func TestToolChainCell_DisplayLinesDenseRunningAndCompleted(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cell := newToolChainCell("ls", map[string]interface{}{"path": "docs"}, time.Time{})
	if cell.Kind() != historyCellTool {
		t.Fatalf("Kind()=%d want historyCellTool", cell.Kind())
	}
	running := cell.DisplayLines(80)
	if len(running) == 0 || !strings.Contains(running[0], "Running ls") {
		t.Fatalf("Running DisplayLines missing marker: %#v", running)
	}
	// Complete in place and re-render via withCompleted.
	completedCell := cell.withCompleted("README.md", nil)
	completed := completedCell.DisplayLines(80)
	joined := strings.Join(completed, "\n")
	if !strings.Contains(joined, "Completed ls") || !strings.Contains(joined, "README.md") {
		t.Fatalf("Completed DisplayLines missing content: %#v", completed)
	}
	// Dense: no blank row between header and result.
	if len(completed) > 1 && completed[1] == "" {
		t.Fatalf("Completed should be dense (no blank after header): %#v", completed)
	}
}

// TestAsyncDocumentCell_DisplayLinesMatchLegacyPipeline pins the typed-document
// cell (P4.2) against writeCompleteBlockLocked(RenderDocumentANSI).
func TestAsyncDocumentCell_DisplayLinesMatchLegacyPipeline(t *testing.T) {
	doc := render.Document{Blocks: []render.Block{
		{Kind: render.BlockParagraph, Lines: []render.Line{
			{Spans: []render.Span{{Text: "tool done"}}},
			{Spans: []render.Span{{Text: "detail row"}}},
		}},
	}}
	cell := newAsyncDocumentCell(doc)
	if cell.Kind() != historyCellTool {
		t.Fatalf("Kind()=%d want historyCellTool", cell.Kind())
	}
	want := normalizeWriteLines(ui.RenderDocumentANSI(doc))
	if got := cell.DisplayLines(80); !reflect.DeepEqual(got, want) {
		t.Fatalf("document DisplayLines\n got %#v\nwant %#v", got, want)
	}
}

// TestAssistantStreamCell_DisplayLinesMatchLegacyPipeline pins the P5.4 stream
// cell against the one-shot assistantMessageCell path for plain source.
func TestAssistantStreamCell_DisplayLinesMatchLegacyPipeline(t *testing.T) {
	bodies := []string{
		"just text",
		"para one\n\npara two",
		"你好，助手",
	}
	for _, body := range bodies {
		stream := newAssistantStreamCell(body, false)
		oneShot := newAssistantMessageCell(body)
		if stream.Kind() != historyCellAssistant {
			t.Fatalf("Kind()=%d want historyCellAssistant", stream.Kind())
		}
		for _, width := range []int{0, 40, 80, 120} {
			got := stream.DisplayLines(width)
			want := oneShot.DisplayLines(width)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("stream vs one-shot DisplayLines(width=%d) for %q\n got %#v\nwant %#v", width, body, got, want)
			}
		}
	}
}

// TestAssistantStreamCell_WidthAwareFormatterReflow pins that a supplied
// formatFn sees the requested width, then residual long rows still wrap via
// render.Wrap (P5.4-S2).
func TestAssistantStreamCell_WidthAwareFormatterReflow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	seen := make([]int, 0, 3)
	cell := newAssistantStreamCellWithFormatter("hello world from stream", true, func(source string, width int) string {
		seen = append(seen, width)
		if width > 0 && len(source) > width {
			return source[:width] + "\n" + source[width:]
		}
		return source
	})
	got := cell.DisplayLines(11)
	if len(seen) != 1 || seen[0] != 11 {
		t.Fatalf("formatFn width seen=%v want [11]", seen)
	}
	// formatFn splits at 11; residual " from stream" (12 cols) still wraps.
	want := widthAwareDisplayLines(ui.FormatAssistantRendered("hello world\n from stream"), 11)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("width-aware DisplayLines\n got %#v\nwant %#v", got, want)
	}
	if len(got) < 2 {
		t.Fatalf("expected multi-row wrap, got %#v", got)
	}
}

// TestAssistantStreamCell_PlainWidthAwareWrap pins plain (no formatFn) source
// wraps through render.Wrap at narrow widths.
func TestAssistantStreamCell_PlainWidthAwareWrap(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cell := newAssistantStreamCell("abcdefghijklmnopqrstuvwxyz", false)
	got := cell.DisplayLines(8)
	if len(got) < 3 {
		t.Fatalf("expected multi-row wrap at width=8, got %#v", got)
	}
	for i, row := range got {
		if plain := render.ANSIToPlain(row); render.Width(plain) > 8 {
			t.Fatalf("row %d wider than 8: %q (width=%d)", i, plain, render.Width(plain))
		}
	}
	// width 0 must match legacy one-shot path.
	oneShot := newAssistantMessageCell("abcdefghijklmnopqrstuvwxyz")
	if !reflect.DeepEqual(cell.DisplayLines(0), oneShot.DisplayLines(0)) {
		t.Fatalf("width=0 stream/one-shot mismatch\n stream=%#v\n oneShot=%#v", cell.DisplayLines(0), oneShot.DisplayLines(0))
	}
}
