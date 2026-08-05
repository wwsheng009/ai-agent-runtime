package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// ActiveBandProjection is the pure, source-backed display candidate for the
// mutable primary-cell tail. It deliberately retains a semantic source range
// instead of a terminal-row cursor: a future presenter can rebuild it after a
// resize or recovery without consulting an old frame or native scrollback.
//
// Lines are the unacknowledged suffix only. Once a range is acknowledged as a
// history handoff, it must not remain in the live band and become a second
// independently advancing copy of the same body.
type ActiveBandProjection struct {
	CellID      scene.CellID
	Revision    uint64
	Kind        scene.CellKind
	SourceRange SourceRange
	Lines       []render.Line
}

func (p ActiveBandProjection) Valid() bool {
	return p.CellID != 0 && p.SourceRange.Valid() && p.SourceRange.End > p.SourceRange.Start && len(p.Lines) > 0
}

func (p ActiveBandProjection) Clone() ActiveBandProjection {
	p.Lines = cloneRenderLines(p.Lines)
	return p
}

// ProjectActiveCellBand derives a bounded rich-line view of the currently
// mutable semantic cell. The range begins at Active.Acked.End, not at Stable or
// Enqueued: a queued-but-unacknowledged prefix remains visible until the
// terminal transaction has actually completed. This prevents a handoff race
// from creating a visible hole.
//
// This is a layout helper only. It uses the same pure structured Markdown
// projection as committed assistant transcript cells, but never probes a
// terminal, reads a surface cache, or advances a source range.
func ProjectActiveCellBand(active ActiveCellState, geometry GeometryState) ActiveBandProjection {
	if active.CellID == 0 || active.Phase == ActiveCellInactive || active.Source == "" {
		return ActiveBandProjection{}
	}

	start := active.Acked.End
	if (active.Acked.End > 0 && active.Acked.Start != 0) || start < 0 || start > len(active.Source) || !activeCellSourceBoundary(active.Source, start) {
		// A source range is byte-addressed. Do not silently round an invalid
		// boundary: rounding down duplicates a possibly handed-off rune, while
		// rounding up hides source that has not been acknowledged.
		return ActiveBandProjection{}
	}
	if start == len(active.Source) {
		return ActiveBandProjection{}
	}

	width := geometry.Width
	if width < 1 {
		width = 80
	}
	var lines []render.Line
	markdownSource := active.Kind == scene.KindAssistant && markdown.LooksLikeMarkdown(active.Source)
	if markdownSource {
		var projected bool
		lines, projected = activeMarkdownSuffixLines(active.Source, start, width)
		if !projected {
			// A changed block context must never downgrade the live viewport to
			// raw Markdown. Keep a rich full-source tail visible while the effect
			// lifecycle takes the conservative recovery path.
			lines = activeMarkdownBandLines(markdown.Render(active.Source, markdown.AssistantBodyOptions(width, style.ThemeContext{})))
		}
	}
	if len(lines) == 0 && !markdownSource {
		rows := activeCellBandRows(active.Source[start:], width)
		role := appTranscriptRenderRole(active.Kind)
		lines = make([]render.Line, 0, len(rows))
		for _, row := range rows {
			lines = append(lines, render.Line{Spans: []render.Span{{
				Text:  row,
				Style: render.Style{Role: string(role)},
			}}})
		}
	}
	maxRows := ActiveBandRows(geometry.Height)
	if len(lines) > maxRows {
		lines = lines[len(lines)-maxRows:]
	}
	if len(lines) == 0 {
		return ActiveBandProjection{}
	}
	return ActiveBandProjection{
		CellID:      active.CellID,
		Revision:    active.Revision,
		Kind:        active.Kind,
		SourceRange: SourceRange{Start: start, End: len(active.Source)},
		Lines:       lines,
	}
}

// activeMarkdownSuffixLines renders from the complete source so a handed-off
// prefix cannot strip list/fence/table context from the remaining rich tail.
// The stable-prefix contract requires the old rendering to remain an exact
// line prefix; if it does not, callers conservatively keep the source live.
func activeMarkdownSuffixLines(source string, start, width int) ([]render.Line, bool) {
	if start < 0 || start > len(source) || !activeCellSourceBoundary(source, start) {
		return nil, false
	}
	full := activeMarkdownBandLines(markdown.Render(source, markdown.AssistantBodyOptions(width, style.ThemeContext{})))
	if start == 0 {
		return full, true
	}
	prefix := activeMarkdownBandLines(markdown.Render(source[:start], markdown.AssistantBodyOptions(width, style.ThemeContext{})))
	if len(prefix) > len(full) || !render.LinesEqual(prefix, full[:len(prefix)]) {
		return nil, false
	}
	return cloneRenderLines(full[len(prefix):]), true
}

func activeMarkdownBandLines(doc render.Document) []render.Line {
	if len(doc.Blocks) == 0 {
		return nil
	}
	lines := make([]render.Line, 0, doc.LineCount())
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			lines = append(lines, cloneAppRenderLine(line))
		}
	}
	return lines
}

func activeCellSourceBoundary(source string, offset int) bool {
	if offset == 0 || offset == len(source) {
		return true
	}
	return offset > 0 && offset < len(source) && utf8.RuneStart(source[offset])
}

// activeCellBandRows expands each logical source row independently. The
// shared wrapAppScreenText helper is intentionally sized for one scene
// LayoutRow; passing a multi-line active source straight through it can
// under-size the scratch VT screen and truncate rows before the band budget is
// applied. Logical-row expansion preserves all source rows, then the caller
// selects the viewport tail.
func activeCellBandRows(source string, width int) []string {
	logicalRows := strings.Split(source, "\n")
	rows := make([]string, 0, len(logicalRows))
	for _, logical := range logicalRows {
		logical = strings.TrimSuffix(logical, "\r")
		expanded := wrapAppScreenText(logical, width)
		if len(expanded) == 0 {
			expanded = []string{""}
		}
		rows = append(rows, expanded...)
	}
	return rows
}
