package cell

import (
	"fmt"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// expandedMaxLines is the hard ceiling for an expanded cell: expansion removes
// the display budget, not the renderer's safety limit. A result past this many
// lines is a dump no interactive view can show at once, so the head-only fold
// (with the same display-only wording) reappears at the ceiling.
const expandedMaxLines = 20000

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
	// Expanded renders the complete result: no head/tail fold and no byte cap,
	// so no omission marker is emitted. Per-line display width still applies,
	// because one very long line would otherwise stall the layout. This is the
	// state behind the pager's "expand full output" toggle.
	Expanded bool
	// Hint is an optional caller-supplied affordance appended to an omission
	// marker (typically the interactive key that opens the full transcript).
	// The cell package renders it verbatim and never assumes a keybinding, so
	// non-interactive projections (web/JSON/export) leave it empty and the
	// marker stays exactly as before.
	Hint string
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

// ToolDisplayPreviewOptions returns the production budget for a finished tool
// result cell in the transcript.
//
// The initial screen must read as folded: the whole projection is capped at
// four rows (3 head + 1 tail), and the display-only notice rides on the end of
// that tail row, so a 60-line grep/view result can never occupy more transcript
// than a short answer. The previous 10+1+4 budget sat in the dead zone between
// the two goals — wide enough to push the rest of the turn off screen, narrow
// enough that nearly every real result still hit the marker — and read to users
// as "there is no folding at all".
//
// The fold stays display-only: cell.Source keeps the full body, and callers
// that can afford the whole result (see PreviewOptions.Expanded, the Ctrl+T
// transcript pager) render it verbatim.
func ToolDisplayPreviewOptions() PreviewOptions {
	return PreviewOptions{
		MaxLines:     4,
		HeadLines:    3,
		TailLines:    1,
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
	if opts.Expanded {
		// Expansion must not silently keep the display budget: a caller that
		// expands a cell is asking for the whole result, and a leftover marker
		// would recreate the dead end the marker wording warns about. Only the
		// renderer's hard ceiling stays.
		opts.MaxLines = expandedMaxLines
		opts.HeadLines = expandedMaxLines
		opts.TailLines = 0
		opts.MaxBytes = 0
	}
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
	if !opts.Expanded && opts.MaxBytes > 0 && len(text) > opts.MaxBytes {
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

	out := make([]render.Line, 0, len(selected))
	for _, line := range selected {
		if opts.MaxLineWidth > 0 {
			line = render.Truncate(line, opts.MaxLineWidth, "…")
		}
		out = append(out, line)
	}

	// One marker per projection, riding on the last row. Scope it to this
	// preview: the head/tail split is a transcript rendering choice, not a
	// tool-result truncation. An unqualified "N lines omitted" reads like the
	// model-visible fold notice and invites false "the tool is truncating"
	// reports.
	if omitted > 0 {
		marker := fmt.Sprintf("… %d lines omitted from this preview (display only)", omitted) + previewHintSuffix(opts.Hint)
		out = appendPreviewMarker(out, marker)
	}

	// The byte marker remains the only signal when the omission is invisible by
	// line count (a single very long line). It replaces the fold marker rather
	// than joining it: a second notice would make the folded block taller
	// without telling the reader anything new about where the body went.
	if byteTrunc && omitted == 0 {
		marker := fmt.Sprintf("… preview limited to the first %d bytes (display only)", opts.MaxBytes) + previewHintSuffix(opts.Hint)
		out = appendPreviewMarker(out, marker)
	}

	return PreviewResult{
		Lines:         out,
		TotalLines:    total,
		OmittedLines:  omitted,
		ByteTruncated: byteTrunc,
	}
}

// appendPreviewMarker 把显示层的省略标记追加到最后一行行尾。
//
// 标记独占一行时它必然夹在 head 与 tail 之间：用户先读到省略提示，再读到一行
// 尾部正文，于是把折叠理解成「结果在中间断了」。标记描述的是「这一行之后还有
// 正文没显示」，只有贴在尾行末尾才与它描述的位置一致：
//
//	└  统计: 24 个文件, 17 个目录 … 42 lines omitted from this preview (display only); Ctrl+T 查看完整文本
//
// 折叠块因此保持「head 若干行 + 带标记的尾行」，标记不再多占一行，也不会把尾部
// 正文挤到标记下面。
//
// 尾行超宽时标记会被渲染器折到下一行，这是终端软折行，不是第二行标记：标记的
// 逻辑归属仍然是尾行，投影的行数也不会因此增长（标记宽度不计入 MaxLineWidth，
// 截断尾行给标记让位会让尾部正文被吃掉，比软折行更糟）。
func appendPreviewMarker(lines []render.Line, marker string) []render.Line {
	span := render.Span{
		Text:  " " + marker,
		Style: render.Style{Role: string(style.RoleTextMuted), Dim: true, Italic: true},
	}
	if len(lines) == 0 {
		// 没有可依附的正文行（空投影 + 字节上限）：宁可多一行也不能丢掉恢复
		// 路径，因此这里保留独立标记行。
		return []render.Line{{Spans: []render.Span{span}}}
	}
	last := len(lines) - 1
	// 复制 span 切片：输入行可能与其他投影共享底层数组。
	spans := make([]render.Span, 0, len(lines[last].Spans)+1)
	spans = append(spans, lines[last].Spans...)
	spans = append(spans, span)
	lines[last].Spans = spans
	return lines
}

// previewHintSuffix renders the optional caller hint inside an omission marker.
// An empty hint yields an empty suffix, so every projection that has no
// interactive recovery path keeps the plain "(display only)" wording.
func previewHintSuffix(hint string) string {
	if hint == "" {
		return ""
	}
	return "; " + hint
}

// selectHeadTail 选出投影要显示的行：省略发生在 head 与 tail 之间，而描述这次
// 省略的标记由调用方贴在 tail 末尾（见 appendPreviewMarker），因此这里不再为标记
// 预留一行。
func selectHeadTail(lines []render.Line, head, tail, max int) ([]render.Line, int) {
	n := len(lines)
	if n == 0 {
		return nil, 0
	}
	if n <= max || head+tail >= n {
		return lines, 0
	}
	if head+tail > max {
		tail = max - head
		if tail < 0 {
			tail = 0
		}
	}
	omitted := n - head - tail
	items := make([]render.Line, 0, head+tail)
	for i := 0; i < head; i++ {
		items = append(items, lines[i])
	}
	for i := n - tail; i < n; i++ {
		items = append(items, lines[i])
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
