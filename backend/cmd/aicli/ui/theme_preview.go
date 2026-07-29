package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/diff"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

// ThemePreviewOptions controls rich theme samples for /theme picker.
type ThemePreviewOptions struct {
	Width       int
	Palette     string
	Mode        string
	SyntaxTheme string
	// Profile overrides terminal color capabilities for deterministic previews.
	// Nil uses the current terminal profile and therefore respects NO_COLOR and
	// ANSI-16/256/TrueColor capability negotiation.
	Profile *style.ColorProfile
	// Compact limits rows for narrow fullscreen previews.
	Compact bool
}

// FormatThemePreviewRich builds a multi-line preview using the same renderers
// as real transcript content (syntax + diff + semantic roles).
func FormatThemePreviewRich(opts ThemePreviewOptions) string {
	doc, themeCtx := buildThemePreviewDocument(opts)
	return style.RenderDocument(doc, themeCtx)
}

// ThemePreviewDocument returns the backend-neutral preview used by /theme.
// Callers that own a terminal surface can retain its roles and token spans
// until their final ANSI/plain backend boundary.
func ThemePreviewDocument(opts ThemePreviewOptions) render.Document {
	doc, _ := buildThemePreviewDocument(opts)
	return doc
}

func buildThemePreviewDocument(opts ThemePreviewOptions) (render.Document, style.ThemeContext) {
	if opts.Width <= 0 {
		opts.Width = 72
	}
	if opts.SyntaxTheme == "" {
		opts.SyntaxTheme = CurrentSyntaxThemeName()
	}
	legacy := BuildThemePreview(opts.Palette, opts.Mode)
	var profile style.ColorProfile
	if opts.Profile != nil {
		profile = *opts.Profile
	} else {
		profile = CurrentColorProfile()
	}
	paletteName := ThemePresetFocus
	mode := style.ThemeModeDark
	if legacy != nil {
		paletteName = legacy.Name
		mode = themeModeFromType(legacy.Type)
	}

	themeCtx := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: paletteName,
		SyntaxName:  opts.SyntaxTheme,
		Mode:        mode,
	}, profile)
	if themeCtx.Palette.Name == "" {
		themeCtx.Palette = style.NewPalette(style.PaletteFocus, style.VariantDark)
	}
	// BuildThemeContext resolves syntax=auto against the preview mode.
	opts.SyntaxTheme = themeCtx.SyntaxName

	blocks := []render.Block{{
		Kind:  render.BlockParagraph,
		Lines: []render.Line{{Spans: themePreviewSampleSpans()}},
	}}

	// Code sample via Chroma.
	code := `func Hello(n int) string { // demo
  return fmt.Sprintf("hi %d", n)
}`
	if opts.Compact {
		code = `func Hello(n int) string { return "hi" } // demo`
	}
	hlLines, _ := syntax.Default.Highlight(syntax.HighlightRequest{
		Code:     code,
		Language: "go",
		Theme:    opts.SyntaxTheme,
	})
	if opts.Compact && len(hlLines) > 2 {
		hlLines = hlLines[:2]
	}
	blocks = append(blocks, render.Block{Kind: render.BlockCode, Lines: hlLines})

	// Diff sample via structured diff renderer.
	fd := diff.FileDiff{
		OldPath: "demo.go",
		NewPath: "demo.go",
		Hunks: []diff.Hunk{{
			Lines: []diff.DiffLine{
				{Kind: diff.LineDelete, OldLineNo: 1, Text: `return "bye"`},
				{Kind: diff.LineAdd, NewLineNo: 1, Text: `return "hi"`},
				{Kind: diff.LineContext, OldLineNo: 2, NewLineNo: 2, Text: "}"},
			},
		}},
	}
	diffOpts := diff.DefaultRenderOptions(opts.Width, themeCtx)
	// Keep token highlighting on: the diff sample is where users judge how a
	// syntax theme colors real code against the add/delete tints.
	diffOpts.ShowLineNo = !opts.Compact
	blocks = append(blocks, diff.Document(fd, diffOpts).Blocks...)

	maxRows := 0
	if opts.Compact {
		maxRows = 6
	}
	return constrainThemePreview(render.Document{Blocks: blocks}, opts.Width, maxRows), themeCtx
}

func themePreviewSampleSpans() []render.Span {
	samples := []struct {
		text string
		role style.Role
	}{
		{text: "user", role: style.RoleUser},
		{text: "asst", role: style.RoleAssistant},
		{text: "system", role: style.RoleSystem},
		{text: "tool", role: style.RoleTool},
		{text: "think", role: style.RoleReasoning},
		{text: "err", role: style.RoleError},
		{text: "ok", role: style.RoleSuccess},
		{text: "dim", role: style.RoleTextMuted},
	}
	spans := make([]render.Span, 0, len(samples)*2-1)
	for index, sample := range samples {
		if index > 0 {
			spans = append(spans, render.Span{Text: " "})
		}
		spans = append(spans, render.Span{
			Text:  sample.text,
			Style: render.Style{Role: string(sample.role)},
		})
	}
	return spans
}

func constrainThemePreview(doc render.Document, width, maxRows int) render.Document {
	if width <= 0 && maxRows <= 0 {
		return doc
	}
	out := render.Document{Blocks: make([]render.Block, 0, len(doc.Blocks))}
	rows := 0
	for _, block := range doc.Blocks {
		next := block
		next.Lines = nil
		for _, line := range block.Lines {
			if maxRows > 0 && rows >= maxRows {
				break
			}
			if width > 0 && render.LineWidth(line) > width {
				line = render.Truncate(line, width, "…")
			}
			next.Lines = append(next.Lines, line)
			rows++
		}
		if len(next.Lines) > 0 {
			out.Blocks = append(out.Blocks, next)
		}
		if maxRows > 0 && rows >= maxRows {
			break
		}
	}
	return out
}
