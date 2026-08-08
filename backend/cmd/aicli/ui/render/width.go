package render

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Align describes horizontal padding alignment.
type Align int

const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// WrapOptions controls line wrapping behavior.
type WrapOptions struct {
	// BreakWord allows splitting mid-word when a single token exceeds width.
	BreakWord bool
	// TabWidth expands tabs before measuring (0 defaults to 4).
	TabWidth int
}

// Width returns the terminal cell width of plain text.
// Style and link metadata are never counted; callers must pass visible text only.
func Width(text string) int {
	if text == "" {
		return 0
	}
	total := 0
	gr := uniseg.NewGraphemes(text)
	for gr.Next() {
		total += graphemeWidth(gr.Runes())
	}
	return total
}

// SpanWidth returns the visible width of a span (ignores style/link).
func SpanWidth(span Span) int {
	return Width(span.Text)
}

// RuneWidth returns the terminal cell width of a single rune. It is exactly
// equivalent to Width(string(r)) but avoids the per-call string allocation and
// the full grapheme iteration uniseg runs even for one rune. Hot paths that
// wrap or measure text rune by rune (transcript layout, status line wrapping)
// should use this instead of Width(string(r)).
func RuneWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if r < 0x20 || r == 0x7f {
		return 0
	}
	if r < 0x80 {
		return 1
	}
	// Marks and format controls occupy no cell (matches uniseg Extend/ZWJ and
	// the pre-uniseg zero-width fallback).
	if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
		return 0
	}
	// Common East Asian Wide/Fullwidth ranges (matches uniseg's
	// propertyEastAsianWidth W/F classification). Kept conservative: anything
	// not listed falls through to uniseg so ambiguous/regional-indicator/emoji
	// runes keep uniseg's exact width.
	if eastAsianWideRune(r) {
		return 2
	}
	return uniseg.StringWidth(string(r))
}

// eastAsianWideRune reports whether r is in a high-confidence East Asian
// Wide/Fullwidth interval. Every interval here is classified W or F by
// Unicode EastAsianWidth, so returning 2 can never disagree with uniseg.
// Conservative by design: intervals with any non-W/F character (e.g. the
// 0x2E80-0x303E symbols block or KATAKANA MIDDLE DOT U+30FB, both Ambiguous)
// are left to the uniseg fallback in RuneWidth — missing the fast path only
// costs a little time, never correctness.
func eastAsianWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115f: // Hangul Jamo
		return true
	case r >= 0x3041 && r <= 0x3096: // Hiragana (marks 0x3099-0x309A handled before this)
		return true
	case r >= 0x309b && r <= 0x309f: // Hiragana digraphs
		return true
	case r >= 0x30a1 && r <= 0x30fa: // Katakana, excluding U+30FB (Ambiguous)
		return true
	case r >= 0x3400 && r <= 0x4dbf: // CJK Unified Ideographs Extension A
		return true
	case r >= 0x4e00 && r <= 0x9fff: // CJK Unified Ideographs
		return true
	case r >= 0xac00 && r <= 0xd7a3: // Hangul Syllables
		return true
	case r >= 0xf900 && r <= 0xfaff: // CJK Compatibility Ideographs
		return true
	case r >= 0xff01 && r <= 0xff60: // Fullwidth Forms (U+FF00 is unassigned)
		return true
	case r >= 0xffe0 && r <= 0xffe6: // Fullwidth Signs
		return true
	case r >= 0x20000 && r <= 0x2fffd: // CJK Unified Ideographs Extension B+
		return true
	case r >= 0x30000 && r <= 0x3fffd:
		return true
	}
	return false
}

// ExpandTabs replaces tab characters with spaces up to the next tab stop.
// Callers that measure or wrap content with tabs should normalize first so
// column math matches what the terminal finally prints.
func ExpandTabs(text string, tabWidth int) string {
	if tabWidth <= 0 {
		tabWidth = 4
	}
	return expandTabs(text, tabWidth)
}

// LineWidth returns the visible width of a line.
func LineWidth(line Line) int {
	w := 0
	for _, span := range line.Spans {
		w += SpanWidth(span)
	}
	return w
}

// Truncate shortens a line to fit width, appending marker when clipped.
// Marker width is included in the budget. UTF-8 / grapheme clusters are kept intact.
func Truncate(line Line, width int, marker string) Line {
	if width <= 0 {
		return Line{}
	}
	lineWidth := LineWidth(line)
	if lineWidth <= width {
		return cloneLine(line)
	}
	markerWidth := Width(marker)
	if markerWidth >= width {
		// Degenerate budget: emit as much of the marker as fits.
		return Line{Spans: []Span{{Text: truncateText(marker, width), Style: firstSpanStyle(line)}}}
	}
	budget := width - markerWidth
	out := Line{Style: line.Style}
	remaining := budget
	for spanIndex, span := range line.Spans {
		if remaining <= 0 {
			break
		}
		sw := lineWidth
		if len(line.Spans) != 1 || spanIndex != 0 {
			sw = SpanWidth(span)
		}
		if sw <= remaining {
			out.Spans = append(out.Spans, span)
			remaining -= sw
			continue
		}
		trimmed := truncateTextOverflow(span.Text, remaining)
		if trimmed != "" {
			out.Spans = append(out.Spans, Span{Text: trimmed, Style: span.Style, Link: span.Link})
		}
		remaining = 0
		break
	}
	if marker != "" {
		style := Style{}
		if len(out.Spans) > 0 {
			style = out.Spans[len(out.Spans)-1].Style
		} else {
			style = firstSpanStyle(line)
		}
		out.Spans = append(out.Spans, Span{Text: marker, Style: style})
	}
	return out
}

// TruncateText truncates plain text to the given cell width.
func TruncateText(text string, width int, marker string) string {
	line := Line{Spans: []Span{{Text: text}}}
	return linePlain(Truncate(line, width, marker))
}

// Wrap splits a line into multiple lines that each fit width.
func Wrap(line Line, width int, opts WrapOptions) []Line {
	if width <= 0 {
		return []Line{cloneLine(line)}
	}
	if LineWidth(line) <= width {
		return []Line{cloneLine(line)}
	}
	if opts.TabWidth <= 0 {
		opts.TabWidth = 4
	}

	// Flatten to grapheme runs preserving style boundaries.
	type run struct {
		text  string
		style Style
		link  string
		w     int
	}
	var runs []run
	for _, span := range line.Spans {
		text := expandTabs(span.Text, opts.TabWidth)
		if text == "" {
			continue
		}
		gr := uniseg.NewGraphemes(text)
		for gr.Next() {
			g := string(gr.Runes())
			runs = append(runs, run{
				text:  g,
				style: span.Style,
				link:  span.Link,
				w:     graphemeWidth(gr.Runes()),
			})
		}
	}
	if len(runs) == 0 {
		return []Line{{Style: line.Style}}
	}

	var lines []Line
	var current Line
	current.Style = line.Style
	curWidth := 0

	flush := func() {
		if len(current.Spans) == 0 && curWidth == 0 {
			return
		}
		lines = append(lines, current)
		current = Line{Style: line.Style}
		curWidth = 0
	}

	appendRun := func(r run) {
		if r.w > width && opts.BreakWord {
			// Extremely wide single grapheme: emit alone.
			flush()
			current.Spans = []Span{{Text: r.text, Style: r.style, Link: r.link}}
			flush()
			return
		}
		if curWidth+r.w > width && curWidth > 0 {
			flush()
		}
		// Merge with previous span when style/link match.
		if n := len(current.Spans); n > 0 {
			last := &current.Spans[n-1]
			if last.Style == r.style && last.Link == r.link {
				last.Text += r.text
				curWidth += r.w
				return
			}
		}
		current.Spans = append(current.Spans, Span{Text: r.text, Style: r.style, Link: r.link})
		curWidth += r.w
	}

	// Word-aware wrapping: try to break on whitespace for ASCII prose.
	i := 0
	for i < len(runs) {
		// Collect a word (non-space run) or a single space cluster.
		j := i
		wordWidth := 0
		if isBreakSpace(runs[i].text) {
			appendRun(runs[i])
			i++
			continue
		}
		for j < len(runs) && !isBreakSpace(runs[j].text) {
			wordWidth += runs[j].w
			j++
		}
		if wordWidth > width && opts.BreakWord {
			for k := i; k < j; k++ {
				appendRun(runs[k])
			}
			i = j
			continue
		}
		if curWidth > 0 && curWidth+wordWidth > width {
			flush()
		}
		for k := i; k < j; k++ {
			appendRun(runs[k])
		}
		i = j
	}
	flush()
	if len(lines) == 0 {
		return []Line{{Style: line.Style}}
	}
	return lines
}

// Pad expands a line to width using spaces on the chosen side.
func Pad(line Line, width int, align Align) Line {
	cur := LineWidth(line)
	if cur >= width {
		return cloneLine(line)
	}
	pad := width - cur
	space := strings.Repeat(" ", pad)
	out := cloneLine(line)
	switch align {
	case AlignRight:
		out.Spans = append([]Span{{Text: space}}, out.Spans...)
	case AlignCenter:
		left := pad / 2
		right := pad - left
		if left > 0 {
			out.Spans = append([]Span{{Text: strings.Repeat(" ", left)}}, out.Spans...)
		}
		if right > 0 {
			out.Spans = append(out.Spans, Span{Text: strings.Repeat(" ", right)})
		}
	default:
		out.Spans = append(out.Spans, Span{Text: space})
	}
	return out
}

func graphemeWidth(runes []rune) int {
	if len(runes) == 0 {
		return 0
	}
	// Zero-width / non-printing controls.
	if len(runes) == 1 {
		return runeWidth(runes[0])
	}
	// Use uniseg's own width for multi-codepoint graphemes (emoji ZWJ etc.).
	w := uniseg.StringWidth(string(runes))
	if w < 0 {
		return 0
	}
	return w
}

func runeWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if r < 32 || r == 127 {
		return 0
	}
	if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
		return 0
	}
	// uniseg handles most wide characters; keep a conservative fallback.
	w := uniseg.StringWidth(string(r))
	if w < 0 {
		return 0
	}
	return w
}

func truncateText(text string, width int) string {
	if width <= 0 || text == "" {
		return ""
	}
	if Width(text) <= width {
		return text
	}
	return truncateTextOverflow(text, width)
}

func truncateTextOverflow(text string, width int) string {
	if width <= 0 || text == "" {
		return ""
	}
	var b strings.Builder
	cur := 0
	gr := uniseg.NewGraphemes(text)
	for gr.Next() {
		w := graphemeWidth(gr.Runes())
		if cur+w > width {
			break
		}
		b.WriteString(string(gr.Runes()))
		cur += w
	}
	return b.String()
}

func expandTabs(text string, tabWidth int) string {
	if tabWidth <= 0 || !strings.ContainsRune(text, '\t') {
		return text
	}
	var b strings.Builder
	col := 0
	for _, r := range text {
		if r == '\t' {
			spaces := tabWidth - (col % tabWidth)
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
			continue
		}
		b.WriteRune(r)
		col += runeWidth(r)
	}
	return b.String()
}

func isBreakSpace(g string) bool {
	if g == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(g)
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

func cloneLine(line Line) Line {
	out := Line{Style: line.Style}
	if len(line.Spans) > 0 {
		out.Spans = append([]Span(nil), line.Spans...)
	}
	return out
}

func firstSpanStyle(line Line) Style {
	if len(line.Spans) == 0 {
		return line.Style
	}
	return line.Spans[0].Style
}

func linePlain(line Line) string {
	var b strings.Builder
	for _, span := range line.Spans {
		b.WriteString(span.Text)
	}
	return b.String()
}
