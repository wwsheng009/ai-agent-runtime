package style

import (
	"strings"
	"testing"
)

// TestStatusLineBlankMatchesDocument pins StatusLineBlank to the document path
// it replaces. Row planning (statusVisibleRowCount / dynamicStatusVisibleRowCount
// and the controller status normalizers) used to ask
//
//	strings.TrimSpace(StatusLineDocument(model, 0).PlainText()) == ""
//
// once per call and paid a full document render for it; StatusLineBlank is the
// allocation-free form of the same question. If the document builder ever gains
// a default span, an implicit separator, or a different blank-segment rule,
// this test fails and both sides must be reconciled.
func TestStatusLineBlankMatchesDocument(t *testing.T) {
	cases := []struct {
		name  string
		model StatusLineModel
	}{
		{"zero-value-model", StatusLineModel{}},
		{"default-state", StatusLineModel{State: RunReady}},
		{"explicit-state-text", StatusLineModel{State: RunStreaming, StateText: "Working"}},
		{"hide-state-no-segments", StatusLineModel{HideState: true}},
		{"hide-state-blank-segments", StatusLineModel{HideState: true, Segments: []StatusSegment{
			{Kind: StatusSegMeta, Text: ""},
			{Kind: StatusSegPath, Text: "   "},
		}}},
		{"hide-state-with-segment", StatusLineModel{HideState: true, Segments: []StatusSegment{
			{Kind: StatusSegMeta, Text: "model gpt-4.1"},
		}}},
		{"whitespace-state-text", StatusLineModel{State: RunStreaming, StateText: "   "}},
		{"whitespace-state-text-with-segment", StatusLineModel{
			State:     RunStreaming,
			StateText: " ",
			Segments:  []StatusSegment{{Kind: StatusSegUsage, Text: "12%"}},
		}},
		{"empty-state-keeps-ready-prefix", StatusLineModel{Segments: []StatusSegment{
			{Kind: StatusSegMeta, Text: "model gpt-4.1"},
		}}},
		{"blank-segments-only", StatusLineModel{Segments: []StatusSegment{
			{Kind: StatusSegMeta, Text: ""},
			{Kind: StatusSegBalance, Text: " \t "},
		}}},
		{"custom-separator", StatusLineModel{Separator: " | ", Segments: []StatusSegment{
			{Kind: StatusSegMeta, Text: "model gpt-4.1"},
			{Kind: StatusSegPath, Text: "main"},
		}}},
		{"segment-with-inner-space", StatusLineModel{HideState: true, Segments: []StatusSegment{
			{Kind: StatusSegPath, Text: "  ~/repo with space  "},
		}}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			documentPlain := strings.TrimSpace(StatusLineDocument(testCase.model, 0).PlainText())
			wantBlank := documentPlain == ""
			if got := StatusLineBlank(testCase.model); got != wantBlank {
				t.Fatalf("StatusLineBlank=%v, document blank=%v (plain=%q, model=%+v)",
					got, wantBlank, documentPlain, testCase.model)
			}
		})
	}
}

// TestStatusLineBlankNeverDisagreesAtAnyWidth checks the documented contract
// beyond width 0: a model that StatusLineBlank calls non-blank must produce
// visible text at the widths the chat actually renders.
func TestStatusLineBlankNeverDisagreesAtAnyWidth(t *testing.T) {
	models := []StatusLineModel{
		{State: RunReady},
		{State: RunWaiting, StateText: "Waiting for answer"},
		{HideState: true, Segments: []StatusSegment{{Kind: StatusSegMeta, Text: "model gpt-4.1"}}},
	}
	for _, width := range []int{1, 8, 20, 40, 120} {
		for _, model := range models {
			if StatusLineBlank(model) {
				t.Fatalf("fixture unexpectedly blank: %+v", model)
			}
			if strings.TrimSpace(StatusLineDocument(model, width).PlainText()) == "" {
				t.Fatalf("width %d folded a non-blank model to empty: %+v", width, model)
			}
		}
	}
}

// TestStatusLineDocumentPaintsSomethingAtAnyWidth pins the narrow-terminal
// invariant behind the fold fix: when nothing fits, the status row is still
// reserved by the width-0 rule, so it must paint something rather than an empty
// row. render.Truncate degrades to the marker prefix on degenerate widths.
func TestStatusLineDocumentPaintsSomethingAtAnyWidth(t *testing.T) {
	model := StatusLineModel{HideState: true, Segments: []StatusSegment{
		{Kind: StatusSegMeta, Text: "model gpt-4.1", Priority: 3},
	}}
	for width := 1; width <= 20; width++ {
		if plain := strings.TrimSpace(StatusLineDocument(model, width).PlainText()); plain == "" {
			t.Fatalf("width %d painted an empty status row", width)
		}
	}
}

// TestStatusLineDocumentKeepsStatePrefixIntactAtNarrowWidth covers the other
// branch of the same rule: a state prefix that fits must not be clipped to make
// room for a marker of a segment that cannot fit at all.
func TestStatusLineDocumentKeepsStatePrefixIntactAtNarrowWidth(t *testing.T) {
	model := StatusLineModel{
		State:     RunWaiting,
		StateText: "Waiting",
		Segments:  []StatusSegment{{Kind: StatusSegUsage, Text: "Context 14% used", Priority: 1}},
	}
	if plain := StatusLineDocument(model, 12).PlainText(); strings.TrimSpace(plain) != "Waiting" {
		t.Fatalf("narrow fold = %q, want %q", plain, "Waiting")
	}
}
