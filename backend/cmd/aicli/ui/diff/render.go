package diff

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

// RenderOptions configure diff layout.
type RenderOptions struct {
	Width           int
	Theme           style.ThemeContext
	HeaderLabel     string
	ShowLineNo      bool
	SyntaxHighlight bool
	SyntaxTheme     string
	// HighlightLimits caps Chroma work per hunk. Zero values use the
	// diff-specific defaults rather than the larger final-code-block budget.
	HighlightLimits syntax.Limits
	// GutterWidth is the minimum width of the single line-number column.
	// Document widens it when a document contains wider numbers so digits are
	// never clipped.
	GutterWidth int
	Highlighter syntax.Highlighter
}

// DefaultRenderOptions builds options from theme + width.
func DefaultRenderOptions(width int, theme style.ThemeContext) RenderOptions {
	if width <= 0 {
		width = 80
	}
	return RenderOptions{
		Width:           width,
		Theme:           theme,
		HeaderLabel:     "Edited",
		ShowLineNo:      true,
		SyntaxHighlight: true,
		SyntaxTheme:     theme.SyntaxName,
		HighlightLimits: DefaultHighlightLimits(),
		GutterWidth:     4,
		Highlighter:     syntax.Default,
	}
}

// Document renders a FileDiff to a structured document.
func Document(fd FileDiff, opts RenderOptions) render.Document {
	if opts.Width <= 0 {
		opts.Width = 80
	}
	if opts.GutterWidth <= 0 {
		opts.GutterWidth = 4
	}
	// One shared column width per document keeps every row aligned, including
	// rows whose numbers are wider than the configured minimum.
	if opts.ShowLineNo {
		if w := maxGutterDigits(fd); w > opts.GutterWidth {
			opts.GutterWidth = w
		}
	}

	// Pre-size for the common no-wrap case: one row per diff line plus the
	// file header and one header row per hunk.
	estimated := 1 + len(fd.Hunks)
	for _, hunk := range fd.Hunks {
		estimated += len(hunk.Lines)
	}
	lines := make([]render.Line, 0, estimated)

	// File header
	path := fd.NewPath
	if path == "" {
		path = fd.OldPath
	}
	if path != "" {
		label := path
		if fd.OldPath != "" && fd.NewPath != "" && fd.OldPath != fd.NewPath {
			label = fd.OldPath + " → " + fd.NewPath
		}
		headerLabel := strings.TrimSpace(opts.HeaderLabel)
		if headerLabel == "" {
			headerLabel = "Edited"
		}
		lines = append(lines, clipDiffLine(render.Line{Spans: []render.Span{{
			Text:  "• " + headerLabel + " " + label,
			Style: render.Style{Role: string(style.RoleTool), Bold: true},
		}}}, opts.Width))
	}

	lang := fd.Language
	if lang == "" && path != "" {
		lang = syntax.NormalizeLanguage(strings.TrimPrefix(filepath.Ext(path), "."))
	}

	// Resolve the diff palette once per document so every row shares the same
	// backgrounds even if the syntax theme changes mid-render.
	backgrounds := resolveDiffBackgrounds(opts)
	prefixSpans := diffPrefixSpanCount(opts)
	// One shared run of spaces per document: the trailing background fill only
	// ever needs a prefix of it, so tinted rows do not each allocate one.
	fillPad := ""
	if backgrounds.add.IsSet() || backgrounds.del.IsSet() {
		fillPad = strings.Repeat(" ", opts.Width)
	}

	for _, hunk := range fd.Hunks {
		if hunk.Header != "" {
			lines = append(lines, clipDiffLine(render.Line{Spans: []render.Span{{
				Text:  hunk.Header,
				Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
			}}}, opts.Width))
		}

		// Optional hunk-level syntax highlight of content lines.
		tokenLines := highlightHunk(hunk, lang, opts)

		for i, dl := range hunk.Lines {
			row := renderDiffLineWith(dl, tokenLines[i], opts, backgrounds)
			lines = appendDiffRow(lines, row, prefixSpans, opts.Width, fillPad)
		}
	}

	return render.Document{Blocks: []render.Block{{
		Kind:  render.BlockParagraph,
		Lines: lines,
	}}}
}

// RenderANSI encodes a file diff.
func RenderANSI(fd FileDiff, opts RenderOptions) string {
	return style.RenderDocument(Document(fd, opts), opts.Theme)
}

// RenderPlain encodes without color.
func RenderPlain(fd FileDiff, opts RenderOptions) string {
	return render.PlainBackend{}.Render(Document(fd, opts))
}

// RenderText is a convenience that parses unified or edited-supplement text.
func RenderText(text string, opts RenderOptions) render.Document {
	if supplements := ParseSupplementBlocks(text); len(supplements) > 0 {
		return SupplementDocument(supplements, opts)
	}
	files := ParseUnified(text, DefaultParseOptions())
	if len(files) == 0 {
		return fallbackDocument(text, opts.Width)
	}
	var blocks []render.Block
	for _, fd := range files {
		doc := Document(fd, opts)
		blocks = append(blocks, doc.Blocks...)
	}
	return render.Document{Blocks: blocks}
}

// SupplementDocument renders multiple transcript diff blocks without
// collapsing their paths, languages or hunk-level highlighting budgets.
func SupplementDocument(supplements []Supplement, opts RenderOptions) render.Document {
	var lines []render.Line
	for i, supplement := range supplements {
		if i > 0 {
			lines = append(lines, render.Line{})
		}
		fileOpts := opts
		fileOpts.HeaderLabel = supplement.Label
		doc := Document(supplement.Diff, fileOpts)
		for _, block := range doc.Blocks {
			lines = append(lines, block.Lines...)
		}
	}
	if len(lines) == 0 {
		return render.Document{}
	}
	return render.Document{Blocks: []render.Block{{
		Kind:  render.BlockParagraph,
		Lines: lines,
	}}}
}

func fallbackDocument(text string, width int) render.Document {
	if text == "" {
		return render.Document{}
	}
	plain := render.ANSIToPlain(text)
	parts := strings.Split(plain, "\n")
	lines := make([]render.Line, 0, len(parts))
	for _, part := range parts {
		line := render.Line{Spans: []render.Span{{
			Text:  part,
			Style: render.Style{Role: string(style.RoleTextMuted)},
		}}}
		if width > 0 && render.LineWidth(line) > width {
			line = render.Truncate(line, width, "…")
		}
		lines = append(lines, line)
	}
	return render.Document{Blocks: []render.Block{{
		Kind:  render.BlockParagraph,
		Lines: lines,
	}}}
}

func highlightHunk(hunk Hunk, lang string, opts RenderOptions) [][]render.Span {
	out := make([][]render.Span, len(hunk.Lines))
	if !opts.SyntaxHighlight || lang == "" || lang == "plaintext" {
		for i, dl := range hunk.Lines {
			out[i] = []render.Span{{Text: dl.Text}}
		}
		return out
	}

	// Collect source rows first. Oversized hunks keep diff semantics but skip
	// token lexing, avoiding a long synchronous Chroma pass in the transcript.
	indexMap := make([]int, 0, len(hunk.Lines))
	sourceBytes := 0
	for i, dl := range hunk.Lines {
		if dl.Kind == LineMeta || dl.Kind == LineHeader || dl.Kind == LineHunk {
			out[i] = []render.Span{{Text: dl.Text}}
			continue
		}
		indexMap = append(indexMap, i)
		sourceBytes += len(dl.Text) + 1
	}
	if len(indexMap) == 0 {
		return out
	}
	limits := normalizedHighlightLimits(opts.HighlightLimits)
	if sourceBytes > limits.MaxBytes || len(indexMap) > limits.MaxLines {
		for _, idx := range indexMap {
			out[idx] = []render.Span{{Text: hunk.Lines[idx].Text}}
		}
		return out
	}

	// Build contiguous source with newlines so lexer state survives rows.
	var b strings.Builder
	b.Grow(sourceBytes)
	for _, idx := range indexMap {
		b.WriteString(hunk.Lines[idx].Text)
		b.WriteByte('\n')
	}
	code := b.String()

	h := opts.Highlighter
	if h == nil {
		h = syntax.Default
	}
	hlLines, meta := h.Highlight(syntax.HighlightRequest{
		// Keep the synthetic final separator. Chroma consumes it while emitting
		// exactly one row per diff source row, including a trailing blank row.
		Code:     code,
		Language: lang,
		Theme:    opts.SyntaxTheme,
	})
	// Require an exact row-for-row match. A lexer that emits a different line
	// count would otherwise shift every following row's colors silently, so
	// the whole hunk degrades to plain text instead.
	if !meta.Highlighted || len(hlLines) != len(indexMap) {
		for _, idx := range indexMap {
			out[idx] = []render.Span{{Text: hunk.Lines[idx].Text}}
		}
		return out
	}
	for j, idx := range indexMap {
		out[idx] = hlLines[j].Spans
	}
	return out
}

func normalizedHighlightLimits(limits syntax.Limits) syntax.Limits {
	defaults := DefaultHighlightLimits()
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxLines <= 0 {
		limits.MaxLines = defaults.MaxLines
	}
	return limits
}

// renderDiffLine renders one row using freshly resolved backgrounds.
// Document uses renderDiffLineWith to resolve them once per document.
func renderDiffLine(dl DiffLine, tokens []render.Span, opts RenderOptions) render.Line {
	return renderDiffLineWith(dl, tokens, opts, resolveDiffBackgrounds(opts))
}

func renderDiffLineWith(dl DiffLine, tokens []render.Span, opts RenderOptions, backgrounds resolvedBackgrounds) render.Line {
	gw := opts.GutterWidth
	// gutter + sign + one span per token + trailing background fill.
	spans := make([]render.Span, 0, len(tokens)+3)

	sign := " "
	signRole := style.RoleTextMuted
	lineRole := style.RoleTextPrimary
	var bg render.Color
	dimContent := false

	switch dl.Kind {
	case LineAdd:
		sign = "+"
		signRole = style.RoleSuccess
		lineRole = style.RoleSuccess
		bg = backgrounds.add
	case LineDelete:
		sign = "-"
		signRole = style.RoleError
		lineRole = style.RoleError
		dimContent = true
		bg = backgrounds.del
	case LineMeta, LineHeader, LineHunk:
		sign = " "
		signRole = style.RoleTextMuted
		lineRole = style.RoleTextMuted
	default:
		sign = " "
	}

	// The row tint covers the gutter and sign columns too, so a changed line
	// reads as one continuous band instead of a tinted code fragment with an
	// untinted line number in front of it.
	if opts.ShowLineNo {
		gutterStyle := render.Style{Role: string(style.RoleTextMuted), Dim: true}
		if bg.IsSet() {
			gutterStyle.Background = bg
		}
		spans = append(spans, render.Span{
			Text:  gutter(displayLineNo(dl), gw) + " ",
			Style: gutterStyle,
		})
	}

	// Always show +/- so mono/NoColor remains readable.
	signStyle := render.Style{Role: string(signRole), Bold: dl.Kind == LineAdd || dl.Kind == LineDelete}
	if bg.IsSet() {
		signStyle.Background = bg
	}
	spans = append(spans, render.Span{Text: sign + " ", Style: signStyle})

	contentSpans := tokens
	if len(contentSpans) == 0 {
		contentSpans = []render.Span{{Text: dl.Text}}
	}
	// Sanitize untrusted content: drop cursor/OSC, keep optional SGR as spans.
	contentSpans = sanitizeContentSpans(contentSpans)
	for _, sp := range contentSpans {
		st := sp.Style
		if !st.Foreground.IsSet() && st.Role == "" {
			st.Role = string(lineRole)
		}
		if dimContent {
			st.Dim = true
		}
		if bg.IsSet() {
			st.Background = bg
		}
		// On ANSI16/NoColor, AdaptStyle later drops bg; sign remains.
		// Tabs are expanded up front so gutter alignment and wrap math match
		// what the terminal prints.
		spans = append(spans, render.Span{
			Text:  render.ExpandTabs(sp.Text, DiffTabWidth),
			Style: st,
			Link:  sp.Link,
		})
	}

	row := render.Line{Spans: spans}
	if bg.IsSet() {
		// Row-level background lets wrapped continuation rows and the trailing
		// fill inherit the tint without re-deriving it per span.
		row.Style = render.Style{Background: bg}
	}
	return row
}

func sanitizeContentSpans(spans []render.Span) []render.Span {
	var out []render.Span
	for _, sp := range spans {
		if sp.Text == "" {
			continue
		}
		if !strings.ContainsRune(sp.Text, '\x1b') {
			out = append(out, sp)
			continue
		}
		// Re-parse text through restricted ANSI parser; drop non-SGR.
		parsed := render.ANSIToSpans(sp.Text)
		for _, p := range parsed {
			if p.Text == "\n" {
				continue
			}
			// Prefer original token style when parser yields plain.
			merged := p
			if !p.Style.Foreground.IsSet() && sp.Style.Foreground.IsSet() {
				merged.Style = sp.Style
			}
			out = append(out, merged)
		}
	}
	if len(out) == 0 {
		return []render.Span{{Text: ""}}
	}
	return out
}

func gutter(n, width int) string {
	if n <= 0 {
		return strings.Repeat(" ", width)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) > width {
		return s[len(s)-width:]
	}
	return strings.Repeat(" ", width-len(s)) + s
}

// displayLineNo picks the one number shown for a row.
//
// Rows are numbered like the file on disk after the edit: additions and
// context use the new-file number, deletions the old-file one. Context rows
// carry both sides, so rendering an old and a new column printed the same
// value twice ("267  267") for every unchanged row while stealing content
// columns; a single column keeps each row unambiguous.
func displayLineNo(dl DiffLine) int {
	switch dl.Kind {
	case LineAdd:
		return dl.NewLineNo
	case LineDelete:
		return dl.OldLineNo
	case LineMeta, LineHeader, LineHunk:
		return 0
	}
	if dl.NewLineNo > 0 {
		return dl.NewLineNo
	}
	return dl.OldLineNo
}

// maxGutterDigits reports the digit count of the widest rendered line number.
func maxGutterDigits(fd FileDiff) int {
	widest := 0
	for _, hunk := range fd.Hunks {
		for _, dl := range hunk.Lines {
			if n := displayLineNo(dl); n > widest {
				widest = n
			}
		}
	}
	if widest <= 0 {
		return 0
	}
	return len(fmt.Sprintf("%d", widest))
}

// diffPrefixSpanCount reports how many leading spans belong to the gutter and
// sign columns, i.e. the part that must not be re-flowed when wrapping.
func diffPrefixSpanCount(opts RenderOptions) int {
	if opts.ShowLineNo {
		return 2
	}
	return 1
}

// clipDiffLine trims a non-content row (file/hunk header) to the frame width.
func clipDiffLine(line render.Line, width int) render.Line {
	if width <= 0 || render.LineWidth(line) <= width {
		return line
	}
	return render.Truncate(line, width, "…")
}

// appendDiffRow appends one diff row, re-flowing it instead of clipping when
// it exceeds the frame width.
//
// The gutter/sign prefix stays on the first visual line; continuation lines
// repeat a blank prefix of the same width so content stays column-aligned.
// Token styles survive the split because render.Wrap carries span styles
// across break points.
//
// The width scan happens exactly once per row: rows emitted here are already
// within budget, so callers must not re-measure the whole document afterwards.
// The measured width is handed to fillDiffRow for the same reason.
func appendDiffRow(dst []render.Line, line render.Line, prefixSpans, width int, pad string) []render.Line {
	if width <= 0 {
		return append(dst, line)
	}
	if lineWidth := render.LineWidth(line); lineWidth <= width {
		return append(dst, fillDiffRow(line, width, lineWidth, pad))
	}
	if prefixSpans > len(line.Spans) {
		prefixSpans = len(line.Spans)
	}
	prefix := line.Spans[:prefixSpans]
	content := line.Spans[prefixSpans:]
	prefixWidth := 0
	for _, sp := range prefix {
		prefixWidth += render.SpanWidth(sp)
	}
	available := width - prefixWidth
	if len(content) == 0 || available < minDiffContentCols {
		// No usable content column left: keep the single clipped row rather
		// than emitting a column of one-character fragments.
		clipped := render.Truncate(line, width, "…")
		return append(dst, fillDiffRow(clipped, width, render.LineWidth(clipped), pad))
	}

	wrapped := render.Wrap(
		render.Line{Spans: content, Style: line.Style},
		available,
		render.WrapOptions{BreakWord: true, TabWidth: DiffTabWidth},
	)
	continuation := render.Span{
		Text:  strings.Repeat(" ", prefixWidth),
		Style: prefix[0].Style,
	}

	for i, part := range wrapped {
		row := render.Line{Style: line.Style}
		if i == 0 {
			row.Spans = append(row.Spans, prefix...)
		} else {
			row.Spans = append(row.Spans, continuation)
		}
		row.Spans = append(row.Spans, part.Spans...)
		// render.Wrap keeps each part within `available`, and the prefix is
		// exactly prefixWidth wide, so the row cannot exceed width. The
		// minDiffContentCols floor above rules out the degenerate case where a
		// single wide grapheme could overflow a 1-2 column budget.
		dst = append(dst, fillDiffRow(row, width, prefixWidth+render.LineWidth(part), pad))
	}
	return dst
}

// fillDiffRow extends a tinted row with trailing spaces so the add/delete
// background covers the whole terminal row instead of ending at the last code
// column. Rows without a row background — context, headers, and every
// ANSI-16/NoColor terminal, where resolveDiffBackgrounds yields no tint — are
// returned untouched so plain transcripts keep their exact-width lines.
//
// lineWidth must be the caller's already-measured width, and pad a run of
// spaces at least as long as width; both keep this off the per-row hot path.
func fillDiffRow(line render.Line, width, lineWidth int, pad string) render.Line {
	bg := line.Style.Background
	if !bg.IsSet() || width <= 0 {
		return line
	}
	gap := width - lineWidth
	if gap <= 0 || gap > len(pad) {
		return line
	}
	// Copy first: wrapped rows can share a backing array with other rows.
	spans := make([]render.Span, len(line.Spans), len(line.Spans)+1)
	copy(spans, line.Spans)
	line.Spans = append(spans, render.Span{
		Text:  pad[:gap],
		Style: render.Style{Background: bg},
	})
	return line
}

// resolvedBackgrounds holds the add/delete line backgrounds for one render
// pass. Both are unset on ANSI-16 and NoColor terminals, where saturated
// backgrounds would overpower syntax tokens.
type resolvedBackgrounds struct {
	add render.Color
	del render.Color
}

// resolveDiffBackgrounds starts from the built-in tints and lets the active
// syntax theme override them when it declares diff scopes, mirroring how
// editors color inserted/deleted lines.
func resolveDiffBackgrounds(opts RenderOptions) resolvedBackgrounds {
	depth := opts.Theme.Terminal.Depth
	if depth < render.ColorANSI256 {
		return resolvedBackgrounds{}
	}
	out := resolvedBackgrounds{
		add: addBackground(opts.Theme),
		del: delBackground(opts.Theme),
	}
	scopes := syntax.DiffScopeBackgroundsFor(opts.SyntaxTheme)
	if scopes.Inserted.IsSet() {
		out.add = adaptDiffBackground(scopes.Inserted, depth)
	}
	if scopes.Deleted.IsSet() {
		out.del = adaptDiffBackground(scopes.Deleted, depth)
	}
	return out
}

// adaptDiffBackground keeps theme RGB on truecolor terminals and pre-quantizes
// to an xterm index on 256-color terminals so the IR already reflects what the
// backend will encode.
func adaptDiffBackground(color render.Color, depth render.ColorDepth) render.Color {
	if depth == render.ColorTrueColor || color.Kind != render.ColorRGB {
		return color
	}
	return render.Indexed(render.RGBToANSI256(color.R, color.G, color.B))
}

func addBackground(theme style.ThemeContext) render.Color {
	switch theme.Terminal.Depth {
	case render.ColorTrueColor:
		if theme.Palette.Variant == style.VariantLight {
			return render.RGB(220, 255, 220)
		}
		return render.RGB(20, 50, 20)
	case render.ColorANSI256:
		if theme.Palette.Variant == style.VariantLight {
			return render.Indexed(194)
		}
		return render.Indexed(22)
	}
	return render.Color{} // ANSI-16/NoColor: no background fill
}

func delBackground(theme style.ThemeContext) render.Color {
	switch theme.Terminal.Depth {
	case render.ColorTrueColor:
		if theme.Palette.Variant == style.VariantLight {
			return render.RGB(255, 220, 220)
		}
		return render.RGB(50, 20, 20)
	case render.ColorANSI256:
		if theme.Palette.Variant == style.VariantLight {
			return render.Indexed(224)
		}
		return render.Indexed(52)
	}
	return render.Color{}
}
