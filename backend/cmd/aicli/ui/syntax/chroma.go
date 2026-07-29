// Package syntax provides code highlighting that emits structured render spans
// instead of pre-colored ANSI strings.
package syntax

import (
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// HighlightRequest describes a single code block to highlight.
type HighlightRequest struct {
	Code     string
	Language string
	Filename string
	Theme    string
}

// HighlightMeta describes what happened during highlighting.
type HighlightMeta struct {
	Language       string
	Theme          string
	Highlighted    bool
	FallbackReason string
}

// Highlighter turns source code into render lines with token styles.
type Highlighter interface {
	Highlight(req HighlightRequest) ([]render.Line, HighlightMeta)
}

// ChromaHighlighter uses Chroma v2 lexers/styles.
type ChromaHighlighter struct {
	Limits Limits
	// DefaultTheme is used when req.Theme is empty or "auto".
	DefaultTheme string
}

var (
	styleOnce sync.Once
	// Ensure styles package side effects are loaded once.
	_ = styles.Fallback

	// globalDefaultTheme is updated by ui.SetSyntaxTheme so formatters can
	// pick up the user selection without importing the ui package.
	globalDefaultThemeMu sync.RWMutex
	globalDefaultTheme   = "monokai"
)

// SetGlobalDefaultTheme sets the process-wide default Chroma style name.
func SetGlobalDefaultTheme(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "monokai"
	}
	globalDefaultThemeMu.Lock()
	globalDefaultTheme = name
	globalDefaultThemeMu.Unlock()
}

// GlobalDefaultTheme returns the process-wide default Chroma style name.
func GlobalDefaultTheme() string {
	globalDefaultThemeMu.RLock()
	defer globalDefaultThemeMu.RUnlock()
	if globalDefaultTheme == "" {
		return "monokai"
	}
	return globalDefaultTheme
}

// NewChromaHighlighter builds a highlighter with default limits.
func NewChromaHighlighter() *ChromaHighlighter {
	return &ChromaHighlighter{
		Limits:       DefaultLimits(),
		DefaultTheme: "auto",
	}
}

// Highlight implements Highlighter.
func (h *ChromaHighlighter) Highlight(req HighlightRequest) ([]render.Line, HighlightMeta) {
	if h == nil {
		h = NewChromaHighlighter()
	}
	meta := HighlightMeta{
		Language: NormalizeLanguage(req.Language),
		Theme:    strings.TrimSpace(req.Theme),
	}
	if meta.Theme == "" || strings.EqualFold(meta.Theme, "auto") {
		meta.Theme = h.DefaultTheme
		if meta.Theme == "" || strings.EqualFold(meta.Theme, "auto") {
			meta.Theme = GlobalDefaultTheme()
		}
	}

	code := req.Code
	if code == "" {
		return []render.Line{{Spans: []render.Span{{Text: ""}}}}, meta
	}

	if h.Limits.Exceeded(code) {
		meta.FallbackReason = "limit_exceeded"
		return plainCodeLines(code), meta
	}

	lexer := ResolveLexer(req.Language, req.Filename, code)
	if lexer != nil {
		meta.Language = strings.ToLower(lexer.Config().Name)
	}

	style := styles.Get(meta.Theme)
	if style == nil {
		style = styles.Fallback
		meta.Theme = style.Name
	}

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		meta.FallbackReason = "tokenize_error"
		return plainCodeLines(code), meta
	}

	lines := tokensToLines(iterator, style)
	meta.Highlighted = true
	return lines, meta
}

func plainCodeLines(code string) []render.Line {
	parts := strings.Split(code, "\n")
	// Preserve trailing empty line semantics of Split.
	out := make([]render.Line, 0, len(parts))
	for _, part := range parts {
		out = append(out, render.Line{
			Spans: []render.Span{{
				Text:  part,
				Style: render.Style{Role: "Code"},
			}},
		})
	}
	return out
}

func tokensToLines(it chroma.Iterator, style *chroma.Style) []render.Line {
	var lines []render.Line
	var current render.Line

	flush := func() {
		if len(current.Spans) == 0 {
			current.Spans = []render.Span{{Text: ""}}
		}
		lines = append(lines, current)
		current = render.Line{}
	}

	for _, tok := range it.Tokens() {
		text := tok.Value
		if text == "" {
			continue
		}
		entry := style.Get(tok.Type)
		styleSpan := chromaEntryToStyle(entry, tok.Type)

		// Split token text on newlines so each visual line is separate.
		for {
			idx := strings.IndexByte(text, '\n')
			if idx < 0 {
				if text != "" {
					current.Spans = append(current.Spans, render.Span{
						Text:  text,
						Style: styleSpan,
					})
				}
				break
			}
			if idx > 0 {
				current.Spans = append(current.Spans, render.Span{
					Text:  text[:idx],
					Style: styleSpan,
				})
			}
			flush()
			text = text[idx+1:]
		}
	}
	// Always emit at least the final line (may be empty after trailing newline).
	if len(current.Spans) > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

func chromaEntryToStyle(entry chroma.StyleEntry, tokType chroma.TokenType) render.Style {
	s := render.Style{Role: "Code." + tokType.String()}
	if entry.Bold == chroma.Yes {
		s.Bold = true
	}
	if entry.Italic == chroma.Yes {
		s.Italic = true
	}
	if entry.Underline == chroma.Yes {
		s.Underline = true
	}
	if color := entry.Colour; color.IsSet() {
		r, g, b := color.Red(), color.Green(), color.Blue()
		s.Foreground = render.RGB(r, g, b)
	}
	// Background from theme is intentionally ignored for terminal code blocks
	// in ANSI-16/low-depth; TrueColor path may adopt later via ThemeContext.
	return s
}

// Default is a process-wide highlighter instance.
var Default Highlighter = NewChromaHighlighter()

// Highlight is a convenience wrapper around Default.
func Highlight(req HighlightRequest) ([]render.Line, HighlightMeta) {
	if Default == nil {
		Default = NewChromaHighlighter()
	}
	return Default.Highlight(req)
}
