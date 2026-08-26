package ui

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
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

// activeBandMarkdownOptions builds assistant-body options for live ActiveBand
// rendering: the restricted active highlighter plus hidden fallback labels so
// a large block degrades silently instead of showing a technical notice.
func activeBandMarkdownOptions(width int, theme style.ThemeContext, highlighter syntax.Highlighter) markdown.Options {
	opts := markdown.AssistantBodyOptions(width, theme)
	opts.Highlighter = highlighter
	opts.HideHighlightFallback = true
	return opts
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
	return ProjectActiveCellBandWithTheme(active, geometry, style.ThemeContext{})
}

func ProjectActiveCellBandWithTheme(active ActiveCellState, geometry GeometryState, theme style.ThemeContext) ActiveBandProjection {
	if active.CellID == 0 || active.Phase == ActiveCellInactive || active.Source == "" {
		return ActiveBandProjection{}
	}

	// 工具执行中（Scene tool chain 处于 mutable 阶段）：无论 Acked 边界
	// 如何，ActiveBand 投影一行 "• Running <命令摘要>"。这对齐旧版
	// chat_tool_rendering 的 Running 行，让"开始执行工具"在真实 TUI
	// （semantic active-cell projection）里重新可见；工具完成后 cell 提交，
	// ActiveCellFromTranscript 不再返回它，Running 行随之消失（保持
	// viewport-only 语义，不进 transcript）。
	if active.Kind == scene.KindToolChain && active.Phase == ActiveCellMutable {
		head := activeCellRunningToolBandHead(active.Source)
		if head == "" {
			return ActiveBandProjection{}
		}
		width := geometry.Width
		if width < 1 {
			width = 80
		}
		// head 首行在 started 阶段已带 "• Running " 前缀（数据层 Running
		// 标记），投影不重复加；旧 head（无前缀）仍按原逻辑补齐。
		row := head
		if !strings.HasPrefix(row, "• Running ") {
			row = "• Running " + row
		}
		row = TruncateVisible(row, width, "…")
		return ActiveBandProjection{
			CellID:      active.CellID,
			Revision:    active.Revision,
			Kind:        active.Kind,
			SourceRange: SourceRange{Start: 0, End: len(active.Source)},
			Lines: []render.Line{{Spans: []render.Span{{
				Text:  row,
				Style: render.Style{Role: string(style.RoleTextSecondary), Bold: true},
			}}}},
		}
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
	highlighter := newActiveBandHighlighter()
	var lines []render.Line
	// Reasoning owns a semantic kind and body-only source. Its divider is
	// derived here, using the same projection as committed transcript rows.
	// This keeps live/final rows identical without putting chrome into source
	// offsets or asking Markdown to interpret the divider as body text.
	reasoning := active.Kind == scene.KindReasoning
	markdownSource := active.Kind == scene.KindAssistant && markdown.LooksLikeMarkdown(active.Source)
	structuredSource := reasoning || markdownSource
	if structuredSource {
		var projected bool
		if reasoning {
			lines, projected = activeReasoningSuffixLines(active.Source, start, width, theme, highlighter)
		} else {
			lines, projected = activeMarkdownSuffixLines(active.Source, start, width, theme, highlighter)
		}
		if !projected {
			// A changed block context must never downgrade the live viewport to
			// raw Markdown. Keep a rich full-source tail visible while the effect
			// lifecycle takes the conservative recovery path.
			if reasoning {
				lines = activeReasoningBandLines(active.Source, width, theme, highlighter)
			} else {
				lines = activeMarkdownBandLines(markdown.Render(active.Source, activeBandMarkdownOptions(width, theme, highlighter)))
			}
		}
	}
	if len(lines) == 0 && !structuredSource {
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

// activeCellRunningToolBandHead 提取 tool cell Source 的首行作为 ActiveBand
// Running 行的命令摘要（progress/output 行不进入 Running 行）。
func activeCellRunningToolBandHead(source string) string {
	head := strings.TrimSpace(source)
	if newline := strings.IndexByte(head, '\n'); newline >= 0 {
		head = strings.TrimSpace(head[:newline])
	}
	return head
}

// activeMarkdownSuffixLines renders from the complete source so a handed-off// prefix cannot strip list/fence/table context from the remaining rich tail.
// The stable-prefix contract requires the old rendering to remain an exact
// line prefix; if it does not, callers conservatively keep the source live.
func activeMarkdownSuffixLines(source string, start, width int, theme style.ThemeContext, highlighter syntax.Highlighter) ([]render.Line, bool) {
	if start < 0 || start > len(source) || !activeCellSourceBoundary(source, start) {
		return nil, false
	}
	full := activeMarkdownBandLines(markdown.Render(source, activeBandMarkdownOptions(width, theme, highlighter)))
	if start == 0 {
		return full, true
	}
	prefix := activeMarkdownBandLines(markdown.Render(source[:start], activeBandMarkdownOptions(width, theme, highlighter)))
	if len(prefix) > len(full) || !render.LinesEqual(prefix, full[:len(prefix)]) {
		return nil, false
	}
	return cloneRenderLines(full[len(prefix):]), true
}

// activeReasoningSuffixLines is the reasoning counterpart of the assistant
// suffix projector. Both live and finalized reasoning use the same literal
// source-line projection, including derived opening chrome, so an acknowledged
// prefix cannot make finalization replay the complete body.
func activeReasoningSuffixLines(source string, start, width int, theme style.ThemeContext, highlighter syntax.Highlighter) ([]render.Line, bool) {
	if start < 0 || start > len(source) || !activeCellSourceBoundary(source, start) {
		return nil, false
	}
	full := activeReasoningBandLines(source, width, theme, highlighter)
	if start == 0 {
		return full, true
	}
	prefix := activeReasoningBandLines(source[:start], width, theme, highlighter)
	prefix = trimReasoningContinuationCursor(prefix, source[:start], start < len(source))
	if len(prefix) > len(full) || !render.LinesEqual(prefix, full[:len(prefix)]) {
		return nil, false
	}
	return cloneRenderLines(full[len(prefix):]), true
}

// A prefix ending at '\n' has a cursor row that is not yet a provider-owned
// blank line when more source follows. Exclude only that transient last row
// from handoff comparison; two newlines still retain one real blank row.
func trimReasoningContinuationCursor(lines []render.Line, prefix string, continues bool) []render.Line {
	if !continues || !strings.HasSuffix(prefix, "\n") || len(lines) == 0 {
		return lines
	}
	return lines[:len(lines)-1]
}

// activeReasoningBandLines deliberately ignores Markdown presentation hints.
// Reasoning is provider prose: preserve every logical newline and only apply
// terminal-width wrapping. The closing divider appears only after finalization.
func activeReasoningBandLines(source string, width int, _ style.ThemeContext, _ syntax.Highlighter) []render.Line {
	return reasoningProjectionLines(source, width, false)
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

// suffixProjector memoizes the expensive pieces of activeMarkdownSuffixLines /
// activeReasoningSuffixLines across the many suffix queries one
// handoff-planning pass makes. The prefix rendering (source[:start]) is
// identical for every query, and the full-source render (live) is queried at
// most once, so without memoization planning N handoff rows re-renders the
// whole markdown source — chroma lexing included — 2×(N+2) times per state
// reduce, which is O(rows × full-source) work on every streamed chunk.
type suffixProjector struct {
	source      string
	start       int
	width       int
	theme       style.ThemeContext
	reasoning   bool
	highlighter syntax.Highlighter

	prefixOnce  sync.Once
	prefixLines []render.Line

	liveOnce  sync.Once
	liveLines []render.Line
	liveOK    bool

	lastEnd   int
	lastLines []render.Line
	lastOK    bool
}

func newSuffixProjector(source string, start, width int, theme style.ThemeContext, reasoning bool, highlighter syntax.Highlighter) *suffixProjector {
	if highlighter == nil {
		highlighter = newActiveBandHighlighter()
	}
	return &suffixProjector{source: source, start: start, width: width, theme: theme, reasoning: reasoning, highlighter: highlighter}
}

// band renders one source span the same way the finalized commit does.
func (p *suffixProjector) band(source string) []render.Line {
	if p.reasoning {
		return activeReasoningBandLines(source, p.width, p.theme, p.highlighter)
	}
	return activeMarkdownBandLines(markdown.Render(source, activeBandMarkdownOptions(p.width, p.theme, p.highlighter)))
}

// prefix renders source[:start] once and memoizes it.
func (p *suffixProjector) prefix() []render.Line {
	p.prefixOnce.Do(func() {
		if p.start > 0 {
			p.prefixLines = p.band(p.source[:p.start])
			if p.reasoning {
				p.prefixLines = trimReasoningContinuationCursor(
					p.prefixLines, p.source[:p.start], p.start < len(p.source),
				)
			}
		}
	})
	return p.prefixLines
}

// suffix returns the rendered rows of source[start:end] as they appear in a
// full render of source[:end], mirroring activeMarkdownSuffixLines semantics:
// the full render's first prefix rows must equal the independent prefix
// render, otherwise the projection is rejected (nil, false). The returned
// lines are a fresh clone; internal memoization never escapes.
func (p *suffixProjector) suffix(end int) ([]render.Line, bool) {
	if end == p.lastEnd {
		if !p.lastOK {
			return nil, false
		}
		return cloneRenderLines(p.lastLines), true
	}
	if end < p.start || end > len(p.source) {
		return nil, false
	}
	full := p.band(p.source[:end])
	if p.reasoning {
		full = trimReasoningContinuationCursor(full, p.source[:end], end < len(p.source))
	}
	prefix := p.prefix()
	if len(prefix) > len(full) || !render.LinesEqual(prefix, full[:len(prefix)]) {
		p.lastEnd, p.lastLines, p.lastOK = end, nil, false
		return nil, false
	}
	lines := cloneRenderLines(full[len(prefix):])
	p.lastEnd, p.lastLines, p.lastOK = end, lines, true
	return lines, true
}

// live renders the full-source suffix once.
func (p *suffixProjector) live() ([]render.Line, bool) {
	p.liveOnce.Do(func() {
		p.liveLines, p.liveOK = p.suffix(len(p.source))
	})
	if !p.liveOK {
		return nil, false
	}
	return cloneRenderLines(p.liveLines), true
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
