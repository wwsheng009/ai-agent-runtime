package markdown

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

type renderer struct {
	opts Options
	src  []byte
}

func (r *renderer) render(source string) render.Document {
	r.src = []byte(source)
	docNode := parseCached(source)

	var blocks []render.Block
	for node := docNode.FirstChild(); node != nil; node = node.NextSibling() {
		blocks = append(blocks, r.renderBlock(node)...)
	}
	// Blank lines in the source are block delimiters that Goldmark consumes, so
	// vertical rhythm is re-established here by the layout stage.
	return render.ApplyBlockSpacing(render.Document{Blocks: blocks}, r.opts.Spacing.Policy())
}

func (r *renderer) renderBlock(node ast.Node) []render.Block {
	switch n := node.(type) {
	case *ast.Heading:
		return []render.Block{r.renderHeading(n)}
	case *ast.Paragraph:
		return []render.Block{r.renderParagraph(n)}
	case *ast.FencedCodeBlock:
		return r.renderFencedCode(n)
	case *ast.CodeBlock:
		return []render.Block{r.renderIndentedCode(n)}
	case *ast.Blockquote:
		return r.renderBlockquote(n)
	case *ast.List:
		return r.renderList(n, 0)
	case *ast.ThematicBreak:
		return []render.Block{r.renderRule()}
	case *ast.HTMLBlock:
		return []render.Block{r.renderHTMLBlock(n)}
	case *extast.Table:
		return []render.Block{r.renderTable(n)}
	default:
		// Unknown block: try children as paragraphs.
		var out []render.Block
		for c := node.FirstChild(); c != nil; c = c.NextSibling() {
			out = append(out, r.renderBlock(c)...)
		}
		return out
	}
}

func (r *renderer) renderHeading(n *ast.Heading) render.Block {
	level := n.Level
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	prefix := headingPrefix(level)
	inline := r.collectInline(n)
	spans := append([]render.Span{{
		Text:  prefix,
		Style: render.Style{Role: string(style.RoleAccent), Bold: true},
	}}, inline...)
	// Ensure bold on heading text.
	for i := range spans {
		if i == 0 {
			continue
		}
		spans[i].Style.Bold = true
		if spans[i].Style.Role == "" {
			spans[i].Style.Role = string(style.RoleAccent)
		}
	}
	lines := r.wrapSpans(spans, r.opts.Width)
	return render.Block{Kind: render.BlockParagraph, Lines: lines}
}

func headingPrefix(level int) string {
	switch level {
	case 1:
		return "▶ "
	case 2:
		return "▷ "
	case 3:
		return "◉ "
	default:
		return strings.Repeat("#", level) + " "
	}
}

func (r *renderer) renderParagraph(n *ast.Paragraph) render.Block {
	spans := r.collectInline(n)
	lines := r.wrapSpans(spans, r.opts.Width)
	return render.Block{Kind: render.BlockParagraph, Lines: lines}
}

func (r *renderer) renderFencedCode(n *ast.FencedCodeBlock) []render.Block {
	lang := ""
	if n.Info != nil {
		seg := n.Info.Segment
		lang = string(seg.Value(r.src))
	}
	var buf bytes.Buffer
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		buf.Write(seg.Value(r.src))
	}
	code := strings.TrimRight(buf.String(), "\n")

	// Legacy aicli behavior: ```markdown / ```md fences unwrap and render as
	// nested markdown so assistant-wrapped tables/lists stay readable.
	norm := syntax.NormalizeLanguage(lang)
	if norm == "markdown" && !r.opts.PreserveMarkup {
		inner := Render(code, r.opts)
		if len(inner.Blocks) > 0 {
			return inner.Blocks
		}
	}
	return []render.Block{r.codeBlock(code, lang)}
}

func (r *renderer) renderIndentedCode(n *ast.CodeBlock) render.Block {
	var buf bytes.Buffer
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		buf.Write(seg.Value(r.src))
	}
	code := strings.TrimRight(buf.String(), "\n")
	return r.codeBlock(code, "")
}

func (r *renderer) codeBlock(code, lang string) render.Block {
	h := r.opts.Highlighter
	if h == nil {
		h = syntax.Default
	}
	hlLines, meta := h.Highlight(syntax.HighlightRequest{
		Code:     code,
		Language: lang,
		Theme:    r.opts.SyntaxTheme,
	})
	outLines := make([]render.Line, 0, len(hlLines)+1)
	// Do not emit fence markers (```); they pollute copy/paste and break
	// legacy transcript expectations. Optional muted language hint only when
	// highlight fell back due to limits.
	if !meta.Highlighted && meta.FallbackReason != "" && !r.opts.HideHighlightFallback {
		label := strings.TrimSpace(lang)
		if label == "" {
			label = meta.Language
		}
		if label == "" {
			label = "code"
		}
		outLines = append(outLines, render.Line{
			Spans: []render.Span{{
				Text:  fmt.Sprintf("(%s: %s)", label, meta.FallbackReason),
				Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
			}},
		})
	}
	for _, line := range hlLines {
		// Soft-wrap long code lines to viewport.
		if r.opts.Width > 0 && render.LineWidth(line) > r.opts.Width {
			wrapped := render.Wrap(line, r.opts.Width, render.WrapOptions{BreakWord: true})
			outLines = append(outLines, wrapped...)
		} else {
			outLines = append(outLines, line)
		}
	}
	return render.Block{Kind: render.BlockCode, Lines: outLines}
}

func (r *renderer) renderBlockquote(n *ast.Blockquote) []render.Block {
	var out []render.Block
	const quotePrefix = "│ "
	prefix := render.Span{Text: quotePrefix, Style: render.Style{Role: string(style.RoleTextMuted), Dim: true}}
	prefixWidth := render.Width(quotePrefix)
	// Wrap the inner text at the budget left after the prefix, then prepend
	// the marker to every resulting line (including wrap continuations); the
	// old approach wrapped prefix+content together and lost the marker on
	// continuation lines.
	budget := r.opts.Width - prefixWidth
	if budget < 8 {
		// Too narrow to keep a marker: fall back to wrapping at full width.
		budget = r.opts.Width
		prefixWidth = 0
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		inner := r.renderBlock(c)
		for _, b := range inner {
			var lines []render.Line
			for _, line := range b.Lines {
				if budget > 0 && render.LineWidth(line) > budget {
					lines = append(lines, render.Wrap(line, budget, render.WrapOptions{BreakWord: true})...)
				} else {
					lines = append(lines, line)
				}
			}
			prefixed := make([]render.Line, 0, len(lines))
			for _, line := range lines {
				spans := make([]render.Span, 0, len(line.Spans)+1)
				if prefixWidth > 0 {
					spans = append(spans, prefix)
				}
				spans = append(spans, line.Spans...)
				pl := render.Line{Spans: spans}
				if prefixWidth > 0 && render.LineWidth(pl) > r.opts.Width {
					pl = render.Truncate(pl, r.opts.Width, "…")
				}
				prefixed = append(prefixed, pl)
			}
			out = append(out, render.Block{Kind: render.BlockQuote, Lines: prefixed})
		}
	}
	return out
}

func (r *renderer) renderList(n *ast.List, depth int) []render.Block {
	var out []render.Block
	index := n.Start
	if index <= 0 {
		index = 1
	}
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		listItem, ok := item.(*ast.ListItem)
		if !ok {
			continue
		}
		marker := "• "
		if n.IsOrdered() {
			marker = fmt.Sprintf("%d. ", index)
			index++
		}
		indent := strings.Repeat("  ", depth)
		// Task list checkbox may be the first child or nested under a paragraph.
		if task := findTaskCheckBox(listItem); task != nil {
			if task.IsChecked {
				marker = "[x] "
			} else {
				marker = "[ ] "
			}
		}
		prefix := indent + marker
		prefixWidth := render.Width(prefix)

		// Collect item body blocks.
		var bodySpans []render.Span
		var nested []render.Block
		first := true
		for c := listItem.FirstChild(); c != nil; c = c.NextSibling() {
			switch child := c.(type) {
			case *ast.Paragraph:
				spans := r.collectInline(child)
				if first {
					bodySpans = spans
					first = false
				} else {
					// Additional paragraphs as continuation blocks.
					cont := append([]render.Span{{Text: strings.Repeat(" ", prefixWidth)}}, spans...)
					lines := r.wrapSpans(cont, r.opts.Width)
					nested = append(nested, render.Block{Kind: render.BlockList, Lines: lines})
				}
			case *ast.List:
				nested = append(nested, r.renderList(child, depth+1)...)
			case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.Blockquote:
				nested = append(nested, r.renderBlock(c)...)
			default:
				spans := r.collectInline(c)
				if len(spans) > 0 {
					if first {
						bodySpans = spans
						first = false
					}
				}
			}
		}
		itemSpans := append([]render.Span{{
			Text:  prefix,
			Style: render.Style{Role: string(style.RoleAccent)},
		}}, bodySpans...)
		lines := r.wrapListItem(itemSpans, prefixWidth, r.opts.Width)
		out = append(out, render.Block{Kind: render.BlockList, Lines: lines})
		out = append(out, nested...)
	}
	return out
}

func (r *renderer) wrapListItem(spans []render.Span, prefixWidth, width int) []render.Line {
	if width <= 0 {
		return []render.Line{{Spans: spans}}
	}
	wrapped := render.Wrap(render.Line{Spans: spans}, width, render.WrapOptions{BreakWord: true})
	if len(wrapped) <= 1 || prefixWidth <= 0 {
		return wrapped
	}
	// Continuation lines get indent matching the marker width.
	pad := strings.Repeat(" ", prefixWidth)
	for i := 1; i < len(wrapped); i++ {
		wrapped[i].Spans = append([]render.Span{{Text: pad}}, wrapped[i].Spans...)
		// May exceed width slightly due to pad+content; re-trim if needed.
		if render.LineWidth(wrapped[i]) > width {
			wrapped[i] = render.Truncate(wrapped[i], width, "…")
		}
	}
	return wrapped
}

func (r *renderer) renderRule() render.Block {
	w := r.opts.Width
	if w <= 0 {
		w = 40
	}
	if w > 80 {
		w = 80
	}
	return render.Block{
		Kind: render.BlockRule,
		Lines: []render.Line{{
			Spans: []render.Span{{
				Text:  strings.Repeat("─", w),
				Style: render.Style{Role: string(style.RoleBorder), Dim: true},
			}},
		}},
	}
}

func (r *renderer) renderHTMLBlock(n *ast.HTMLBlock) render.Block {
	// Never execute HTML; emit as plain escaped-ish text.
	var buf bytes.Buffer
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		buf.Write(seg.Value(r.src))
	}
	text := strings.TrimRight(buf.String(), "\n")
	// Strip tags lightly for readability.
	plain := stripSimpleTags(text)
	spans := []render.Span{{Text: plain, Style: render.Style{Role: string(style.RoleTextMuted)}}}
	return render.Block{Kind: render.BlockParagraph, Lines: r.wrapSpans(spans, r.opts.Width)}
}

func stripSimpleTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func (r *renderer) wrapSpans(spans []render.Span, width int) []render.Line {
	line := render.Line{Spans: spans}
	if width <= 0 || render.LineWidth(line) <= width {
		return []render.Line{line}
	}
	return render.Wrap(line, width, render.WrapOptions{BreakWord: true})
}

// collectInline walks inline children into styled spans.
func (r *renderer) collectInline(node ast.Node) []render.Span {
	var spans []render.Span
	ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Text:
			seg := t.Segment
			val := string(seg.Value(r.src))
			if t.HardLineBreak() {
				val += "\n"
			}
			if val != "" {
				spans = append(spans, render.Span{
					Text:  val,
					Style: render.Style{Role: string(style.RoleTextPrimary)},
				})
			}
			if t.SoftLineBreak() {
				// A soft line break has the semantics of a space. It is added
				// as its own span so the CJK pass below can drop it when both
				// sides are CJK text (where inserting a space would read as an
				// "extra" space inside a sentence).
				spans = append(spans, render.Span{
					Text:  " ",
					Style: render.Style{Role: string(style.RoleTextPrimary)},
				})
			}
			return ast.WalkContinue, nil
		case *ast.String:
			spans = append(spans, render.Span{
				Text:  string(t.Value),
				Style: render.Style{Role: string(style.RoleTextPrimary)},
			})
			return ast.WalkContinue, nil
		case *ast.CodeSpan:
			var b strings.Builder
			for c := t.FirstChild(); c != nil; c = c.NextSibling() {
				if textNode, ok := c.(*ast.Text); ok {
					seg := textNode.Segment
					b.Write(seg.Value(r.src))
				}
			}
			spans = append(spans, render.Span{
				Text:  b.String(),
				Style: render.Style{Role: string(style.RoleCodeInline), Bold: true},
			})
			return ast.WalkSkipChildren, nil
		case *ast.Emphasis:
			inner := r.collectInlineChildren(t)
			for i := range inner {
				if t.Level >= 2 {
					inner[i].Style.Bold = true
				} else {
					inner[i].Style.Italic = true
					inner[i].Style.Dim = true
				}
			}
			spans = append(spans, inner...)
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			label := r.collectInlineChildren(t)
			dest := string(t.Destination)
			labelText := spansText(label)
			if labelText == "" {
				labelText = dest
			}
			linkStyle := render.Style{Role: string(style.RoleLink), Underline: true}
			sp := render.Span{Text: labelText, Style: linkStyle}
			if r.opts.Hyperlinks && dest != "" {
				sp.Link = dest
			}
			spans = append(spans, sp)
			if !r.opts.Hyperlinks && dest != "" && dest != labelText {
				spans = append(spans, render.Span{
					Text:  " (" + dest + ")",
					Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
				})
			}
			return ast.WalkSkipChildren, nil
		case *ast.AutoLink:
			dest := string(t.URL(r.src))
			sp := render.Span{
				Text:  dest,
				Style: render.Style{Role: string(style.RoleLink), Underline: true},
			}
			if r.opts.Hyperlinks {
				sp.Link = dest
			}
			spans = append(spans, sp)
			return ast.WalkSkipChildren, nil
		case *extast.Strikethrough:
			inner := r.collectInlineChildren(t)
			for i := range inner {
				inner[i].Style.Dim = true
				inner[i].Text = "~~" + inner[i].Text + "~~"
			}
			spans = append(spans, inner...)
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			// Drop raw HTML inline.
			return ast.WalkSkipChildren, nil
		case *ast.Image:
			alt := r.collectInlineChildren(t)
			altText := spansText(alt)
			if altText == "" {
				altText = "image"
			}
			dest := string(t.Destination)
			spans = append(spans, render.Span{
				Text:  "[image: " + altText + "]",
				Style: render.Style{Role: string(style.RoleTextMuted)},
				Link:  dest,
			})
			return ast.WalkSkipChildren, nil
		case *ast.Paragraph, *ast.Document, *ast.Heading, *ast.ListItem:
			return ast.WalkContinue, nil
		default:
			return ast.WalkContinue, nil
		}
	})
	// Drop soft-break separator spaces between CJK characters before hard
	// newline normalization (the passes are disjoint: soft breaks are their
	// own " " spans, hard breaks carry "\n" inside span text).
	spans = dropCJKSoftbreakSpaces(spans)
	// Normalize hard newlines inside span text into separate handling by replacing \n with space for paragraph wrap.
	return normalizeInlineNewlines(spans)
}

// dropCJKSoftbreakSpaces removes soft-break separator spans (" ") whose left
// and right neighbors are both CJK-ish glyphs. Markdown semantics turn a soft
// line break into a space; for Latin text that is the correct inter-word
// separator, but between CJK characters (which carry no word spacing) it
// renders as an extra space inside a sentence.
func dropCJKSoftbreakSpaces(spans []render.Span) []render.Span {
	out := make([]render.Span, 0, len(spans))
	for i, sp := range spans {
		if sp.Text != " " {
			out = append(out, sp)
			continue
		}
		if !cjkPairAroundSoftbreak(spans, i) {
			out = append(out, sp)
		}
	}
	return out
}

// cjkPairAroundSoftbreak reports whether the soft-break space at index i sits
// between two CJK-ish characters (or at the very end of the inline run, where
// a dangling space would be redundant).
func cjkPairAroundSoftbreak(spans []render.Span, i int) bool {
	left := lastRuneBefore(spans, i)
	if left == utf8.RuneError {
		return false
	}
	right := firstRuneAfter(spans, i)
	if right == utf8.RuneError {
		return true // dangling soft-break space at the run end
	}
	return isCJKish(left) && isCJKish(right)
}

func lastRuneBefore(spans []render.Span, i int) rune {
	for j := i - 1; j >= 0; j-- {
		r, _ := utf8.DecodeLastRuneInString(spans[j].Text)
		if r != utf8.RuneError {
			return r
		}
	}
	return utf8.RuneError
}

func firstRuneAfter(spans []render.Span, i int) rune {
	for j := i + 1; j < len(spans); j++ {
		r, _ := utf8.DecodeRuneInString(spans[j].Text)
		if r != utf8.RuneError {
			return r
		}
	}
	return utf8.RuneError
}

// isCJKish reports whether r is a non-ASCII, non-space glyph. Latin text keeps
// the space separator (the left/right neighbor is ASCII), while CJK, Kana,
// Hangul and full-width punctuation all suppress it.
func isCJKish(r rune) bool {
	return r >= 0x80 && !unicode.IsSpace(r) && r != '\u0085' && r != '\u00a0'
}

func (r *renderer) collectInlineChildren(node ast.Node) []render.Span {
	var spans []render.Span
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		spans = append(spans, r.collectInline(c)...)
	}
	return spans
}

func spansText(spans []render.Span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

func findTaskCheckBox(item *ast.ListItem) *extast.TaskCheckBox {
	if item == nil {
		return nil
	}
	if task, ok := item.FirstChild().(*extast.TaskCheckBox); ok {
		return task
	}
	for c := item.FirstChild(); c != nil; c = c.NextSibling() {
		if task, ok := c.(*extast.TaskCheckBox); ok {
			return task
		}
		if p, ok := c.(*ast.Paragraph); ok {
			if task, ok := p.FirstChild().(*extast.TaskCheckBox); ok {
				return task
			}
		}
	}
	return nil
}

func normalizeInlineNewlines(spans []render.Span) []render.Span {
	out := make([]render.Span, 0, len(spans))
	for spanIndex, sp := range spans {
		if !strings.Contains(sp.Text, "\n") {
			out = append(out, sp)
			continue
		}
		parts := strings.Split(sp.Text, "\n")
		for i, p := range parts {
			if i > 0 {
				// The separator belongs between parts[i-1] and parts[i].
				// Inspect that exact newline rather than the next one when a
				// single span contains multiple hard breaks.
				before := strings.Join(parts[:i], "\n")
				after := strings.Join(parts[i:], "\n")
				if !cjkPairAroundHardBreak(spans, spanIndex, before, after) {
					out = append(out, render.Span{Text: " ", Style: sp.Style})
				}
			}
			// Goldmark keeps the source spaces which introduce a hard line break
			// (normally two spaces before the newline) in the Text segment. Those
			// spaces are Markdown syntax, not visible content. Leaving them here
			// makes a Chinese hard break render as several spaces after the line
			// break is flattened to the paragraph separator below.
			if i < len(parts)-1 {
				p = strings.TrimRight(p, " \t\r")
			}
			if p != "" {
				nsp := sp
				nsp.Text = p
				out = append(out, nsp)
			}
		}
	}
	return out
}

// cjkPairAroundHardBreak reports whether a hard-break newline has CJK-ish
// visible glyphs on both sides. The local before/after strings cover newlines
// within one span; neighboring spans handle breaks that coincide with a style
// boundary. Whitespace (including other newlines) is ignored while finding the
// visible neighbors because Markdown hard-break markers are not content.
func cjkPairAroundHardBreak(spans []render.Span, spanIndex int, before, after string) bool {
	left, ok := lastNonSpaceRune(before)
	if !ok {
		for i := spanIndex - 1; i >= 0 && !ok; i-- {
			left, ok = lastNonSpaceRune(spans[i].Text)
		}
	}
	right, rightOK := firstNonSpaceRune(after)
	if !rightOK {
		for i := spanIndex + 1; i < len(spans) && !rightOK; i++ {
			right, rightOK = firstNonSpaceRune(spans[i].Text)
		}
	}
	return ok && rightOK && isCJKish(left) && isCJKish(right)
}

func lastNonSpaceRune(text string) (rune, bool) {
	for len(text) > 0 {
		r, size := utf8.DecodeLastRuneInString(text)
		if size <= 0 {
			break
		}
		text = text[:len(text)-size]
		if !unicode.IsSpace(r) {
			return r, true
		}
	}
	return 0, false
}

func firstNonSpaceRune(text string) (rune, bool) {
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if size <= 0 {
			break
		}
		text = text[size:]
		if !unicode.IsSpace(r) {
			return r, true
		}
	}
	return 0, false
}
