package ui

import (
	"strings"

	uidiff "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/diff"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// AppScreenRow is one row in the pure, plain-text screen-layout shadow. It is
// deliberately not a terminal cell row: it has no ANSI, front-buffer state,
// cursor movement, or write result. CellID and TranscriptGap preserve the
// semantic identity needed by later Compose and HistoryCommit stages.
type AppScreenRow struct {
	Row           int
	Owner         renderengine.RowOwner
	Text          string
	CellID        scene.CellID
	TranscriptGap bool
	// UserMessage 标记该行属于用户 prompt 消息块。布局文本（Text）保持纯
	// 内容（与 legacy retained parity 一致），渲染层通过该标记在输出时加
	// "> " 前缀，用于区分用户信息与 LLM 信息。
	UserMessage bool
	// RenderLine is populated only when a semantic transcript cell needs a
	// structured presentation (currently assistant Markdown). Text remains the
	// plain physical-row projection used by layout and cursor calculations.
	RenderLine render.Line
}

// AppScreenLayout combines the committed transcript tail and BottomPane row
// plan into one viewport-sized, terminal-neutral layout. The unified primary
// presenter consumes the frame derived from this layout; this type itself
// deliberately remains free of ANSI bytes, front-buffer mutation, cursor I/O,
// and terminal writes.
//
// A mutable Active cell is deliberately excluded from Transcript rows. During
// the migration it is still displayed by the explicitly marked legacy band
// projection, so treating it as retained transcript here would duplicate a
// semantic range in a future full-screen Compose.
type AppScreenLayout struct {
	Revision                uint64
	LayoutGeneration        uint64
	Geometry                GeometryState
	Lease                   LeaseState
	Rows                    []AppScreenRow
	OutputBottomRow         int
	CursorFocus             BottomFocus
	Active                  ActiveCellState
	ActiveBand              ActiveBandProjection
	LegacyBandProjection    bool
	ActiveProjectionPending bool
	// bottom is retained so Compose can derive cursor intent without running
	// the full bottom-pane layout a second time.
	bottom BottomPaneLayout
}

// LayoutAppScreen derives a complete plain screen-row layout from one
// immutable AppState snapshot. It must never inspect FixedBottomSurface,
// ScreenModel, terminal state, or effect/projection progress.
func LayoutAppScreen(state AppState) AppScreenLayout {
	layout := LayoutAppState(state)
	height := layout.Geometry.Height
	result := AppScreenLayout{
		Revision:                layout.Revision,
		LayoutGeneration:        layout.LayoutGeneration,
		Geometry:                layout.Geometry,
		Lease:                   layout.Lease,
		CursorFocus:             layout.Bottom.CursorFocus,
		Active:                  layout.Active.Clone(),
		ActiveBand:              layout.ActiveBand.Clone(),
		LegacyBandProjection:    layout.Bottom.LegacyBandProjection,
		ActiveProjectionPending: layout.Active.Phase != ActiveCellInactive,
		bottom:                  layout.Bottom,
	}
	if height < 1 {
		return result
	}

	width := layout.Geometry.Width
	if width < 1 {
		width = 80
	}
	result.Rows = makeAppScreenRows(height)
	result.OutputBottomRow = height
	for _, bottom := range layout.Bottom.RowPlan.Rows {
		if bottom.Row < 1 || bottom.Row > height {
			continue
		}
		result.Rows[bottom.Row-1] = AppScreenRow{
			Row:   bottom.Row,
			Owner: bottom.Owner,
			Text:  bottom.Text,
		}
	}
	// Zero is a valid output boundary when the entire one-row terminal is
	// occupied by the fixed status reserve.
	result.OutputBottomRow = layout.Bottom.RowPlan.OutputBottomRow
	if result.OutputBottomRow < 0 {
		result.OutputBottomRow = 0
	}
	if result.OutputBottomRow > height {
		result.OutputBottomRow = height
	}

	excluded := transcriptSuffixCellIDsFromFirstMutable(state.Transcript)
	transcript := layoutTranscriptScreenRows(layout.Transcript, transcriptCellsByID(state.Transcript), excluded, width, state.Theme)
	if len(transcript) > result.OutputBottomRow {
		transcript = transcript[len(transcript)-result.OutputBottomRow:]
	}
	startRow := result.OutputBottomRow - len(transcript) + 1
	for index, row := range transcript {
		row.Row = startRow + index
		if row.Row < 1 || row.Row > result.OutputBottomRow {
			continue
		}
		result.Rows[row.Row-1] = row
	}
	return result
}

func makeAppScreenRows(height int) []AppScreenRow {
	rows := make([]AppScreenRow, height)
	for index := range rows {
		rows[index] = AppScreenRow{Row: index + 1, Owner: renderengine.RowOwnerGap}
	}
	return rows
}

func mutableTranscriptCellIDs(transcript TranscriptState) map[scene.CellID]struct{} {
	var ids map[scene.CellID]struct{}
	for _, cell := range transcript.Cells {
		if cell.Phase != scene.CellMutable {
			continue
		}
		if ids == nil {
			ids = make(map[scene.CellID]struct{})
		}
		ids[cell.ID] = struct{}{}
	}
	return ids
}

// transcriptSuffixCellIDsFromFirstMutable keeps the inline viewport behind the
// same canonical ordering barrier as native-history commits. Once a mutable
// cell is encountered, that cell and every later cell stay out of the retained
// transcript projection until the barrier finalizes.
func transcriptSuffixCellIDsFromFirstMutable(transcript TranscriptState) map[scene.CellID]struct{} {
	var ids map[scene.CellID]struct{}
	blocked := false
	for _, cell := range transcript.Cells {
		if !blocked && cell.Phase == scene.CellMutable {
			blocked = true
		}
		if !blocked {
			continue
		}
		if ids == nil {
			ids = make(map[scene.CellID]struct{})
		}
		ids[cell.ID] = struct{}{}
	}
	return ids
}

func transcriptCellsByID(transcript TranscriptState) map[scene.CellID]scene.TranscriptCell {
	if len(transcript.Cells) == 0 {
		return nil
	}
	cells := make(map[scene.CellID]scene.TranscriptCell, len(transcript.Cells))
	for _, cell := range transcript.Cells {
		cells[cell.ID] = cell
	}
	return cells
}

func layoutTranscriptScreenRows(rows []scene.LayoutRow, cells map[scene.CellID]scene.TranscriptCell, mutable map[scene.CellID]struct{}, width int, themes ...style.ThemeContext) []AppScreenRow {
	if len(rows) == 0 {
		return nil
	}
	result := make([]AppScreenRow, 0, len(rows))
	renderedStructured := make(map[scene.CellID]struct{})
	theme := style.ThemeContext{}
	if len(themes) > 0 {
		theme = themes[0]
	}
	fp := themeFingerprint(theme)
	cache := sharedCellRows
	for index := 0; index < len(rows); index++ {
		row := rows[index]
		if _, excluded := mutable[row.CellID]; excluded {
			continue
		}
		if row.Gap > 0 {
			for count := 0; count < int(row.Gap); count++ {
				result = append(result, AppScreenRow{
					// A semantic boundary is an empty transcript row, not
					// unowned bottom-pane headroom. Keeping its physical owner
					// as Transcript matches the existing owned viewport while
					// TranscriptGap retains the semantic distinction needed by
					// later HistoryCommit handling.
					Owner:         renderengine.RowOwnerTranscript,
					CellID:        row.CellID,
					TranscriptGap: true,
				})
			}
			continue
		}
		if _, rendered := renderedStructured[row.CellID]; rendered {
			continue
		}
		if cell, found := cells[row.CellID]; found && cellUsesStructuredPresentation(cell) {
			key := cellLayoutKeyFor(cell, width, fp)
			cached := cache.get(key)
			if cached == nil {
				cached = structuredTranscriptScreenRows(cell, width, theme)
				cache.put(key, cached)
			}
			result = appendCachedCellRows(result, row.CellID, cached)
			renderedStructured[row.CellID] = struct{}{}
			continue
		}
		// Plain cell：收集该 cell 的连续语义行（不含 gap），整体 wrap 并
		// 按内容键缓存，避免每个 cell 每次 reduce 重复 wrap。
		start := index
		for index < len(rows) && rows[index].CellID == row.CellID && rows[index].Gap == 0 {
			index++
		}
		var cellRows []AppScreenRow
		if cell, found := cells[row.CellID]; found {
			key := cellLayoutKeyFor(cell, width, fp)
			cached := cache.get(key)
			if cached == nil {
				cached = wrapPlainCellRows(rows[start:index], row.CellID, width, cell.Kind == scene.KindUser)
				cache.put(key, cached)
			}
			cellRows = cached
		} else {
			cellRows = wrapPlainCellRows(rows[start:index], row.CellID, width, false)
		}
		result = appendCachedCellRows(result, row.CellID, cellRows)
		index-- // 补偿 for 步进：index 已指向下一个不同 cell 或末尾
	}
	return result
}

func appendCachedCellRows(result []AppScreenRow, cellID scene.CellID, rows []AppScreenRow) []AppScreenRow {
	start := len(result)
	result = append(result, rows...)
	for index := start; index < len(result); index++ {
		result[index].CellID = cellID
	}
	return result
}

// userMessagePrefix 是统一渲染器中用户消息块的每行前缀（与旧路径
// FormatUserMessage 的 chrome 前缀同为引用风格 "> "），用于在视觉上
// 区分用户信息与 LLM 信息。
const userMessagePrefix = "> "

// wrapPlainCellRows 把 cell 的连续语义行逐行 wrap 成 AppScreenRow。输出与
// 逐行 wrap 完全一致（同一 wrapAppScreenText 语义），仅用于缓存封装。
// userMessage 为 true 时（用户 prompt 消息）内容按 width-2 预算 wrap，为
// 渲染层每行追加的 "> " 前缀预留 2 列，避免满宽行加前缀后被终端截断；
// 文本仍保持纯内容片段（无前缀），parity 与 history planner 校验不变。
func wrapPlainCellRows(layoutRows []scene.LayoutRow, cellID scene.CellID, width int, userMessage bool) []AppScreenRow {
	var rows []AppScreenRow
	for _, row := range layoutRows {
		wrapWidth := width
		if userMessage {
			wrapWidth = width - 2
			if wrapWidth < 1 {
				wrapWidth = 1
			}
		}
		for _, line := range wrapAppScreenText(row.Text, wrapWidth) {
			rows = append(rows, AppScreenRow{
				Owner:       renderengine.RowOwnerTranscript,
				Text:        line,
				CellID:      cellID,
				UserMessage: userMessage,
			})
		}
	}
	return rows
}

func cellUsesStructuredPresentation(cell scene.TranscriptCell) bool {
	if cell.Presentation.Kind != scene.PresentationPlain {
		return true
	}
	if cell.Kind == scene.KindReasoning {
		// Reasoning is always a structured projection because its opening and
		// terminal dividers are derived chrome, not bytes in cell.Source.
		return true
	}
	if cell.Kind == scene.KindSupplement {
		return markdown.LooksLikeMarkdown(cell.Source)
	}
	return cell.Kind == scene.KindAssistant && markdown.LooksLikeMarkdown(cell.Source)
}

func structuredTranscriptScreenRows(cell scene.TranscriptCell, width int, theme style.ThemeContext) []AppScreenRow {
	if cell.Kind == scene.KindReasoning {
		return reasoningScreenRows(cell, width, theme)
	}
	var doc render.Document
	switch cell.Presentation.Kind {
	case scene.PresentationDocument:
		doc = cell.Presentation.Document.Clone()
	case scene.PresentationDiffSupplement:
		// 带标签的 supplement 文本自带 "• Edited/• Diff" 头部，空 DiffLabel
		// 走渲染器默认；raw unified diff 使用编码器按工具语义标注的动词，
		// 保证只读 git diff 查看不显示 "Edited"。
		opts := uidiff.DefaultRenderOptions(width, theme)
		if cell.Presentation.DiffLabel != "" {
			opts.HeaderLabel = cell.Presentation.DiffLabel
		}
		doc = uidiff.RenderText(cell.Source, opts)
	case scene.PresentationAssistantMarkdown:
		doc, _ = renderengine.SharedRenderCache().Render("assistant", cell.Source, markdown.AssistantBodyOptions(width, theme))
		doc = doc.Clone()
	default:
		doc, _ = renderengine.SharedRenderCache().Render("assistant", cell.Source, markdown.AssistantBodyOptions(width, theme))
		doc = doc.Clone()
	}
	return documentScreenRows(doc, cell, width, theme)
}

// documentScreenRows 把结构化文档 layout 成 AppScreenRow 序列（带
// RenderLine，供 Compose/HistoryCommit 保留样式）。
func documentScreenRows(doc render.Document, cell scene.TranscriptCell, width int, theme style.ThemeContext) []AppScreenRow {
	if len(doc.Blocks) == 0 {
		return nil
	}
	buffer := render.BufferBackend{Width: width}
	lines := buffer.Layout(doc)
	rows := make([]AppScreenRow, 0, len(lines))
	for _, line := range lines {
		rendered := cloneAppRenderLine(line)
		rows = append(rows, AppScreenRow{
			Owner: renderengine.RowOwnerTranscript, Text: render.PlainBackend{}.Render(render.LinesDoc(rendered)),
			CellID: cell.ID, RenderLine: rendered,
		})
	}
	return rows
}

// reasoningScreenRows derives presentation chrome from semantic kind/lifecycle.
// cell.Source is always the provider body verbatim: no divider, wrapping, trim,
// Markdown normalization, or inferred whitespace is persisted back into it.
func reasoningScreenRows(cell scene.TranscriptCell, width int, _ style.ThemeContext) []AppScreenRow {
	return reasoningProjectionScreenRows(
		reasoningProjectionLines(cell.Source, width, cell.Phase != scene.CellMutable),
		cell.ID,
	)
}

// reasoningProjectionLines is the single live/committed reasoning projector.
// Provider newlines define logical rows; terminal width only creates visual
// wraps. In particular, markdown-looking reasoning remains literal so a parser
// cannot collapse leading, interior, or trailing provider-owned blank lines.
func reasoningProjectionLines(source string, width int, terminal bool) []render.Line {
	lines := reasoningDividerBandLines(reasoningChromeLine("reasoning"), width)
	if source != "" {
		bodyRows := activeCellBandRows(source, width)
		if terminal && strings.HasSuffix(source, "\n") && len(bodyRows) > 0 {
			// The last split row represents the cursor position created by the
			// final LF, not an additional provider-owned blank line. The closing
			// divider occupies that row. With N trailing LFs this removes only
			// the cursor row and preserves the preceding N-1 real blank rows.
			bodyRows = bodyRows[:len(bodyRows)-1]
		}
		for _, text := range bodyRows {
			lines = append(lines, render.Line{Spans: []render.Span{{
				Text: text, Style: render.Style{Role: string(style.RoleReasoning)},
			}}})
		}
	}
	if terminal {
		lines = append(lines, reasoningDividerBandLines(reasoningChromeLine("end reasoning"), width)...)
	}
	return lines
}

func reasoningProjectionScreenRows(lines []render.Line, cellID scene.CellID) []AppScreenRow {
	rows := make([]AppScreenRow, 0, len(lines))
	for _, line := range lines {
		rendered := cloneAppRenderLine(line)
		rows = append(rows, AppScreenRow{
			Owner:  renderengine.RowOwnerTranscript,
			Text:   render.PlainBackend{}.Render(render.LinesDoc(rendered)),
			CellID: cellID, RenderLine: rendered,
		})
	}
	return rows
}

// reasoningChromeLine matches the legacy 72-column chat divider while keeping
// that decoration wholly inside the presentation layer.
func reasoningChromeLine(label string) string {
	const width = 72
	content := " " + label + " "
	contentWidth := len([]rune(content))
	left := (width - contentWidth) / 2
	if left < 0 {
		left = 0
	}
	right := width - contentWidth - left
	if right < 0 {
		right = 0
	}
	return strings.Repeat("─", left) + content + strings.Repeat("─", right)
}

// supplementDividerScreenRows 把 supplement 分隔线按 width wrap 成带
// reasoning 角色的结构化行（对齐旧版 dividerRoleForLine 的 reasoning 分支）。
func supplementDividerScreenRows(text string, cellID scene.CellID, width int) []AppScreenRow {
	return reasoningProjectionScreenRows(reasoningDividerBandLines(text, width), cellID)
}

// reasoningDividerBandLines wraps derived chrome using the same VT width model
// as body rows and assigns the reasoning role without adding source bytes.
func reasoningDividerBandLines(text string, width int) []render.Line {
	segments := wrapAppScreenText(text, width)
	lines := make([]render.Line, 0, len(segments))
	for _, segment := range segments {
		lines = append(lines, render.Line{Spans: []render.Span{{
			Text: segment, Style: render.Style{Role: string(style.RoleReasoning)},
		}}})
	}
	return lines
}

// wrapAppScreenText expands one semantic source line with the same pure VT
// model used by the legacy owned viewport. This avoids a second, subtly
// incompatible width algorithm for deferred wrap, wide runes, leading
// combining marks, tab stops, and SGR/control-sequence parsing. It remains a
// pure in-memory operation: no live terminal, surface, or projection cache is
// read. Trailing blank cells are omitted because AppScreenRow.Text represents
// visible glyphs rather than a terminal-cell buffer.
func wrapAppScreenText(text string, width int) []string {
	if width < 1 {
		width = 80
	}
	if rows, ok := wrapPlainAppScreenText(text, width); ok {
		return rows
	}
	return wrapVTAppScreenText(text, width)
}

// wrapPlainAppScreenText handles the overwhelmingly common transcript case
// without constructing a width-by-height VT cell matrix. It deliberately
// declines control sequences, tabs, and runes wider than the viewport because
// those cases depend on the full VT state machine below.
func wrapPlainAppScreenText(text string, width int) ([]string, bool) {
	if text == "" {
		return nil, false
	}
	if isPlainASCII(text) {
		rows := make([]string, 0, (len(text)+width-1)/width)
		for start := 0; start < len(text); start += width {
			end := start + width
			if end > len(text) {
				end = len(text)
			}
			rows = append(rows, text[start:end])
		}
		return rows, true
	}

	rows := make([]string, 0, 1)
	line := strings.Builder{}
	used := 0
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return nil, false
		}
		runeWidth := render.RuneWidth(r)
		if runeWidth > width {
			return nil, false
		}
		if runeWidth == 0 {
			// VT drops a leading combining mark because there is no cell to
			// attach it to. Otherwise the mark remains part of the prior cell.
			if line.Len() > 0 {
				line.WriteRune(r)
			}
			continue
		}
		if used > 0 && used+runeWidth > width {
			rows = append(rows, line.String())
			line = strings.Builder{}
			used = 0
		}
		line.WriteRune(r)
		used += runeWidth
	}
	if line.Len() > 0 || len(rows) == 0 {
		rows = append(rows, line.String())
	}
	return rows, true
}

func isPlainASCII(text string) bool {
	for index := 0; index < len(text); index++ {
		if text[index] < 0x20 || text[index] > 0x7e {
			return false
		}
	}
	return true
}

func wrapVTAppScreenText(text string, width int) []string {
	// This is only a scratch-screen capacity estimate. VT remains the sole
	// physical-row expansion rule. Size by display columns rather than source
	// rune count so a long ordinary line does not allocate width*runeCount
	// cells.
	screen := vt.NewScreen(width, appScreenScratchHeight(text, width))
	screen.Feed(text)
	screen.Feed("\r\n")
	end := screen.CursorRow() - 1
	if end < 1 {
		return nil
	}
	return screen.Lines(1, end)
}

func appScreenScratchHeight(text string, width int) int {
	if width < 1 {
		width = 80
	}
	displayWidth := DisplayWidth(text)
	if displayWidth < 1 {
		displayWidth = 1
	}
	return (displayWidth+width-1)/width + 2
}

// appScreenCellRowText converts a terminal-cell row into the plain screen
// shadow. Unlike cellRowPlainText, blank cells before or between glyphs are
// materialized as spaces because they carry visible column position (for
// example a source indentation, tab stop, or cursor-relative overwrite).
// Trailing blanks remain omitted because the row is not a fixed-width cell
// buffer.
func appScreenCellRowText(cells []vt.Cell) string {
	var text strings.Builder
	for _, cell := range cells {
		if cell.Cont {
			continue
		}
		if cell.Text == "" {
			text.WriteByte(' ')
			continue
		}
		text.WriteString(cell.Text)
	}
	return strings.TrimRight(text.String(), " ")
}
