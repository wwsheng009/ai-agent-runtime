package ui

import (
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
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
	// 逐帧路径只需要视口装得下的最后若干行：只布局尾部窗口，见
	// layoutTranscriptTailScreenRows。
	transcript := layoutTranscriptTailScreenRows(layout.Transcript, transcriptCellsByID(state.Transcript), excluded, width, result.OutputBottomRow, state.Theme)
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
	theme := style.ThemeContext{}
	if len(themes) > 0 {
		theme = themes[0]
	}
	result, _ := layoutTranscriptScreenRowsWithin(rows, cells, mutable, width, time.Time{}, theme)
	return result
}

// layoutBudgetCheckRows 是预算检查的采样间隔。逐行调用 time.Now() 会给
// 146k 行的遍历加上毫秒级固定开销；每 N 行采样一次把开销压到可忽略，同时
// 仍让预算以远小于单帧的粒度生效。
const layoutBudgetCheckRows = 4096

// layoutTranscriptScreenRowsWithin 是带预算的布局实现。deadline 为零值表示
// 不设预算（逐帧渲染路径：帧必须完整，截断会画错屏）。
//
// 预算耗尽时返回**已经布局好的前缀**并把 complete 置为 false。前缀是安全的
// 部分结果：消费方只有在 complete 为真时才把它当作完整布局（例如
// syncHistoryEffectsForTranscript 只在完整规划后才记录 memo），否则下一次
// reduce 会重试。这条路径让 historyCommitPlanningBudget 真正覆盖布局本身 ——
// 此前预算只在 commit 循环里建立，昂贵的布局在预算建立之前就已经跑完了。
func layoutTranscriptScreenRowsWithin(rows []scene.LayoutRow, cells map[scene.CellID]scene.TranscriptCell, mutable map[scene.CellID]struct{}, width int, deadline time.Time, theme style.ThemeContext) ([]AppScreenRow, bool) {
	if len(rows) == 0 {
		return nil, true
	}
	result := make([]AppScreenRow, 0, len(rows))
	renderedStructured := make(map[scene.CellID]struct{})
	fp := themeFingerprint(theme)
	cache := sharedCellRows
	// 最近一次折叠：首屏只有它带 Ctrl+T 提示，pager 初始帧也只有它展开。
	foldTarget := toolFoldTargetRows(rows, cells, mutable)
	for index := 0; index < len(rows); index++ {
		if !deadline.IsZero() && index%layoutBudgetCheckRows == 0 && time.Now().After(deadline) {
			return result, false
		}
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
		if toolCell, found := cells[row.CellID]; found && cellUsesFoldedToolPresentation(toolCell) {
			// 带提示与不带提示是两种投影内容，必须各自占用缓存条目：否则
			// 同一份 source 的复用会把提示泄漏到更早的折叠上（或反过来把
			// 最近一次折叠的提示吃掉）。
			showHint := row.CellID == foldTarget
			key := cellLayoutKeyFor(toolCell, width, fp)
			key.foldHint = showHint
			cached := cache.get(key)
			if cached == nil {
				cached = foldedToolChainScreenRows(toolCell, width, theme, showHint)
				cache.put(key, cached)
			}
			result = appendCachedCellRows(result, row.CellID, cached)
			renderedStructured[row.CellID] = struct{}{}
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
	return result, true
}

// layoutTranscriptTailScreenRows 只布局 transcript 的尾部，返回结果与「全量布局
// 后截取末 maxRows 行」逐行一致。
//
// 逐帧路径只需要视口装得下的最后若干行。全量布局再截断会让每一帧的分配量与
// 历史长度成正比：生产恢复会话实测 3951 个 cell / 146535 个布局行，每帧光
// AppScreenRow 就要重新分配十几 MB，外加 146k 次循环。这是 UI 锁被长时间持有、
// FramePump 停止出帧的直接来源。
//
// 与全量布局的等价性依赖两个事实：
//   - scene.LayoutTranscript 让每个 cell 的布局行连续（gap row 归属后继 cell），
//     因此窗口只要对齐到 cell 边界，每个 cell 的 wrap / 结构化投影就与全量布局
//     逐字节相同；
//   - foldTarget 取的是「最后一次折叠」。若它在窗口内，窗口自身的扫描会得到
//     同一个 ID；若它在窗口之前，全量布局也只是把提示挂在会被截掉的那些行上，
//     可见输出同样没有提示。
func layoutTranscriptTailScreenRows(rows []scene.LayoutRow, cells map[scene.CellID]scene.TranscriptCell, mutable map[scene.CellID]struct{}, width, maxRows int, theme style.ThemeContext) []AppScreenRow {
	if maxRows <= 0 || len(rows) == 0 {
		return nil
	}
	start := transcriptTailStartIndex(rows, mutable, maxRows)
	var laid []AppScreenRow
	for {
		laid = layoutTranscriptScreenRows(rows[start:], cells, mutable, width, theme)
		if len(laid) >= maxRows || start == 0 {
			break
		}
		// 初始窗口按「每个非 gap 行至少产出一行」估计，对 wrap 与结构化投影
		// （markdown/chroma 渲染）都偏小。按几何增长扩大窗口重试；重试时窗口内
		// 的 cell 全部命中布局缓存，代价只有缓存查询。
		next := start - (len(rows) - start)
		if next <= 0 {
			next = 0
		} else {
			// 扩张后的起点同样必须对齐到 cell 边界。布局缓存按 cell 内容寻址，
			// 从 cell 中间开始的窗口会把「残缺行集合」写进该 cell 的缓存条目，
			// 之后的全量布局命中同一条目就会拿到不完整的行。
			next = alignTranscriptCellStart(rows, next)
		}
		start = next
	}
	if len(laid) > maxRows {
		laid = laid[len(laid)-maxRows:]
	}
	return laid
}

// transcriptTailStartIndex 返回尾部窗口的起始下标，使得从该下标开始布局得到的
// 输出行数不少于 maxRows。
//
// 返回值保证对齐到 cell 边界：从某个 cell 的非 gap 连续段中间开始，会让 plain
// 路径只拿到该 cell 的残缺行集合，wrap 结果与全量布局不一致（而结构化路径读的
// 是整 cell 的缓存条目，两者会互相矛盾）。
func transcriptTailStartIndex(rows []scene.LayoutRow, mutable map[scene.CellID]struct{}, maxRows int) int {
	if maxRows <= 0 {
		return len(rows)
	}
	lowerBound := 0
	start := len(rows)
	for start > 0 {
		start--
		row := rows[start]
		if _, excluded := mutable[row.CellID]; excluded {
			// 未提交 cell 由 active band 渲染，布局阶段整块跳过（产出 0 行）。
			continue
		}
		if row.Gap > 0 {
			lowerBound += int(row.Gap)
		} else {
			lowerBound++
		}
		if lowerBound >= maxRows {
			break
		}
	}
	return alignTranscriptCellStart(rows, start)
}

// alignTranscriptCellStart 把下标回退到所在 cell 非 gap 连续段的起点。gap row
// 归属后继 cell，因此这个循环不会跨到前一个 cell。
//
// 为什么必须对齐：布局缓存按 cell 内容寻址（cellLayoutKeyFor），值却是「这个
// cell 在某个窗口里被 wrap 出来的行」。从 cell 中间开始，缓存里就会存下残缺行
// 集合，而结构化投影读的是整 cell 的条目 —— 两条路径会互相矛盾，后续全量布局
// 命中同一条目时也会拿到不完整的行。
func alignTranscriptCellStart(rows []scene.LayoutRow, start int) int {
	for start > 0 && rows[start-1].CellID == rows[start].CellID && rows[start-1].Gap == 0 {
		start--
	}
	return start
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

// cellUsesFoldedToolPresentation 判定一个已提交单元格是否为「纯文本工具链」
// ——工具结果正文直接存在 cell.Source 里的那种形态。这类单元格必须经过显示
// 预算投影，否则超长结果会把整段正文原样铺进 transcript：既没有折叠标记，
// 也没有任何「其余内容还在」的提示。
func cellUsesFoldedToolPresentation(toolCell scene.TranscriptCell) bool {
	return toolCell.Kind == scene.KindToolChain && toolCell.Presentation.Kind == scene.PresentationPlain
}

// foldedToolChainScreenRows 用 cell.ToolDisplayPreviewOptions 投影一个已提交
// 的工具结果单元格（head/tail 折叠 + 仅显示标记）。整个投影最多 4 行（3 行头 +
// 1 行尾），省略标记贴在尾行行尾，不额外占一行。
//
// 折叠是纯显示层的选择：cell.Source 始终保留完整正文，pager（Ctrl+T）渲染
// 整份 source，因此标记不会变成结果的唯一副本。
//
// showHint 只有在 toolCell 是最近一次折叠时才为 true：Ctrl+T 的提示必须与
// Ctrl+T 的实际行为一致，否则每个折叠都承诺一次「查看完整文本」，而 pager
// 只会展开其中最新的一次。
func foldedToolChainScreenRows(toolCell scene.TranscriptCell, width int, theme style.ThemeContext, showHint bool) []AppScreenRow {
	hint := ""
	if showHint {
		hint = toolFoldHint
	}
	preview := cell.BuildPreview(toolCell.Source, toolFoldOptions(hint))
	if len(preview.Lines) == 0 {
		return nil
	}
	// BuildPreview 的消毒路径把每行统一成 RoleTextMuted：正文需要回到工具
	// 角色（与未折叠时一致），只有折叠标记保持弱化斜体。
	lines := make([]render.Line, 0, len(preview.Lines))
	for _, line := range preview.Lines {
		styled := render.Line{Spans: make([]render.Span, 0, len(line.Spans))}
		for _, span := range line.Spans {
			if !span.Style.Italic {
				span.Style = render.Style{Role: string(style.RoleTool)}
			}
			styled.Spans = append(styled.Spans, span)
		}
		lines = append(lines, styled)
	}
	doc := render.Document{Blocks: []render.Block{{
		Kind:  render.BlockParagraph,
		Lines: lines,
	}}}
	return documentScreenRows(doc, toolCell, width, theme)
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
