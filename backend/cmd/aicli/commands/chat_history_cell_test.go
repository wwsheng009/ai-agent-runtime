package commands

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// Compile-time proof that userMessageCell satisfies the historyCell contract.
var (
	_ historyCell = userMessageCell{}
	_ historyCell = assistantMessageCell{}
	_ historyCell = supplementLineCell{}
	_ historyCell = asyncDocumentCell{}
)

// TestUserMessageCell_DisplayLinesMatchLegacyPipeline pins that routing the
// user-echo block through the cell model (P4.1) is byte-identical to the legacy
// writeCompleteBlockLocked(FormatUserMessage) path, so the reroute cannot change
// what lands in the transcript.
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
		// width is reserved for wrap-aware cells; user cells must ignore it.
		for _, width := range []int{0, 40, 80, 120} {
			got := cell.DisplayLines(width)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("DisplayLines(width=%d) for %q\n got %#v\nwant %#v", width, in, got, want)
			}
		}
	}
}

// TestAssistantMessageCell_DisplayLinesMatchLegacyPipeline pins the assistant
// one-shot cell (P4.2) against writeCompleteBlockLocked(FormatAssistantRendered).
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
		want := normalizeWriteLines(ui.FormatAssistantRendered(body))
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
