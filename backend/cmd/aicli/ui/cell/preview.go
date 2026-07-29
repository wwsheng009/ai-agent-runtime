package cell

import (
	"fmt"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// PreviewOptions controls head/tail truncation of tool/shell output.
type PreviewOptions struct {
	// MaxLines is the maximum number of content lines to show (head+tail).
	MaxLines int
	// HeadLines is how many lines to keep from the start when omitting.
	HeadLines int
	// TailLines is how many lines to keep from the end when omitting.
	TailLines int
	// MaxLineWidth truncates individual lines by terminal display cells.
	MaxLineWidth int
	// MaxBytes soft-limits total source before line split (UTF-8 safe).
	MaxBytes int
	// AllowANSI keeps SGR via ANSIToSpans when true (local terminal output only).
	AllowANSI bool
}

// DefaultPreviewOptions returns production head/tail defaults.
func DefaultPreviewOptions() PreviewOptions {
	return PreviewOptions{
		MaxLines:     8,
		HeadLines:    4,
		TailLines:    2,
		MaxLineWidth: 200,
		MaxBytes:     8 * 1024,
		AllowANSI:    false,
	}
}

// PreviewResult is the structured preview of a blob of tool output.
type PreviewResult struct {
	Lines         []render.Line
	TotalLines    int
	OmittedLines  int
	ByteTruncated bool
}

// BuildPreview sanitizes and truncates output into render lines.
//
// Default path strips all controls (plain text). When AllowANSI is true,
// only safe SGR is retained via ANSIToSpans — still no cursor/OSC/title.
func BuildPreview(raw string, opts PreviewOptions) PreviewResult {
	if opts.MaxLines <= 0 {
		opts.MaxLines = DefaultPreviewOptions().MaxLines
	}
	if opts.HeadLines <= 0 {
		opts.HeadLines = opts.MaxLines / 2
		if opts.HeadLines <= 0 {
			opts.HeadLines = 1
		}
	}
	if opts.TailLines < 0 {
		opts.TailLines = 0
	}
	if opts.HeadLines+opts.TailLines > opts.MaxLines {
		opts.TailLines = opts.MaxLines - opts.HeadLines
		if opts.TailLines < 0 {
			opts.TailLines = 0
			opts.HeadLines = opts.MaxLines
		}
	}
	if opts.MaxLineWidth <= 0 {
		opts.MaxLineWidth = 200
	}

	text := raw
	byteTrunc := false
	if opts.MaxBytes > 0 && len(text) > opts.MaxBytes {
		text = truncateUTF8(text, opts.MaxBytes)
		byteTrunc = true
	}

	sourceLines := render.ANSIToLines(text)
	if !opts.AllowANSI {
		for i, line := range sourceLines {
			plain := render.PlainBackend{}.Render(render.LinesDoc(line))
			sourceLines[i] = render.Line{Spans: []render.Span{{
				Text:  plain,
				Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
			}}}
		}
	}

	// Drop trailing empty lines produced by terminal line endings.
	for len(sourceLines) > 0 && render.LineWidth(sourceLines[len(sourceLines)-1]) == 0 {
		sourceLines = sourceLines[:len(sourceLines)-1]
	}

	total := len(sourceLines)
	selected, omitted := selectHeadTail(sourceLines, opts.HeadLines, opts.TailLines, opts.MaxLines)

	out := make([]render.Line, 0, len(selected)+1)
	for _, item := range selected {
		if item.omission {
			msg := fmt.Sprintf("… %d lines omitted", item.omitted)
			out = append(out, render.Line{Spans: []render.Span{{
				Text:  msg,
				Style: render.Style{Role: string(style.RoleTextMuted), Dim: true, Italic: true},
			}}})
			continue
		}
		line := item.line
		if opts.MaxLineWidth > 0 {
			line = render.Truncate(line, opts.MaxLineWidth, "…")
		}
		out = append(out, line)
	}

	if byteTrunc {
		out = append(out, render.Line{Spans: []render.Span{{
			Text:  "… (byte limit)",
			Style: render.Style{Role: string(style.RoleTextMuted), Dim: true, Italic: true},
		}}})
	}

	return PreviewResult{
		Lines:         out,
		TotalLines:    total,
		OmittedLines:  omitted,
		ByteTruncated: byteTrunc,
	}
}

type previewItem struct {
	line     render.Line
	omission bool
	omitted  int
}

func selectHeadTail(lines []render.Line, head, tail, max int) ([]previewItem, int) {
	n := len(lines)
	if n == 0 {
		return nil, 0
	}
	if n <= max || head+tail >= n {
		items := make([]previewItem, 0, n)
		for _, l := range lines {
			items = append(items, previewItem{line: l})
		}
		return items, 0
	}
	if head+tail > max {
		tail = max - head
		if tail < 0 {
			tail = 0
		}
	}
	omitted := n - head - tail
	items := make([]previewItem, 0, head+tail+1)
	for i := 0; i < head; i++ {
		items = append(items, previewItem{line: lines[i]})
	}
	if omitted > 0 {
		items = append(items, previewItem{omission: true, omitted: omitted})
	}
	for i := n - tail; i < n; i++ {
		items = append(items, previewItem{line: lines[i]})
	}
	return items, omitted
}

func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.ValidString(s[:maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
