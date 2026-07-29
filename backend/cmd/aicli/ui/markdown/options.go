// Package markdown renders CommonMark/GFM source into structured render.Document
// values. It never emits ANSI directly; callers use render backends.
package markdown

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

// TableMode controls table layout strategy.
type TableMode int

const (
	// TableAuto chooses grid vs records from viewport width and column metrics.
	TableAuto TableMode = iota
	// TableGrid always renders a multi-column grid (may wrap cells).
	TableGrid
	// TableRecords always renders vertical key/value records.
	TableRecords
	// TablePlain renders pipe-separated fallback without alignment.
	TablePlain
)

// SpacingMode controls the blank-line rhythm between top-level blocks.
type SpacingMode int

const (
	// SpacingDefault separates prose blocks with one blank line and keeps list
	// items / quote continuations tight.
	SpacingDefault SpacingMode = iota
	// SpacingNone emits blocks back-to-back (legacy dense output).
	SpacingNone
)

// Policy returns the render-level spacing policy for the mode.
func (m SpacingMode) Policy() render.SpacingPolicy {
	if m == SpacingNone {
		return render.CompactSpacingPolicy()
	}
	return render.DefaultSpacingPolicy()
}

// Options configure a single markdown render pass.
type Options struct {
	Width      int
	CWD        string
	Theme      style.ThemeContext
	TableMode  TableMode
	Hyperlinks bool
	// SyntaxTheme selects a Chroma style; empty/"auto" uses highlighter default.
	SyntaxTheme string
	// Highlighter overrides the default Chroma highlighter.
	Highlighter syntax.Highlighter
	// PreserveMarkup keeps raw markers in plain/no-color transcripts when set.
	PreserveMarkup bool
	// HideHighlightFallback suppresses the technical fallback label. Active
	// viewports use this when a large block intentionally skips highlighting.
	HideHighlightFallback bool
	// Spacing controls blank lines between top-level blocks. The zero value
	// keeps one blank line between prose blocks.
	Spacing SpacingMode
}

// DefaultOptions builds options from a theme context and terminal width.
func DefaultOptions(width int, theme style.ThemeContext) Options {
	if width <= 0 {
		width = 80
	}
	return Options{
		Width:       width,
		Theme:       theme,
		TableMode:   TableAuto,
		Hyperlinks:  theme.UseHyperlink,
		SyntaxTheme: theme.SyntaxName,
		Highlighter: syntax.Default,
	}
}

// Render parses source and returns a structured document.
func Render(source string, opts Options) render.Document {
	if stringsTrimSpace(source) == "" {
		return render.Document{}
	}
	if opts.Width <= 0 {
		opts.Width = 80
	}
	if opts.Highlighter == nil {
		opts.Highlighter = syntax.Default
	}
	r := &renderer{opts: opts}
	return r.render(source)
}

// RenderPlain returns visible text only.
func RenderPlain(source string, opts Options) string {
	return render.PlainBackend{}.Render(Render(source, opts))
}

// RenderANSI resolves roles and encodes ANSI for the given theme profile.
func RenderANSI(source string, opts Options) string {
	doc := Render(source, opts)
	return style.RenderDocument(doc, opts.Theme)
}

func stringsTrimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
