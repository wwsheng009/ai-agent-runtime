package ui

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/viewport"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// BottomRowsSnapshot materializes the current fixed bottom reserve as terminal
// cells, top-to-bottom. It is read-only: the legacy immediate-mode renderer
// remains the production writer while P5 validates owned-viewport composition.
//
// Rows have terminal width and include every reserved blank margin/gap. Styled
// application documents are rendered through the same paint plans and theme
// adapters as the legacy writer and then reconstructed by vt.Screen; no ANSI
// stripping or content-plane inference is involved.
func (s *FixedBottomSurface) BottomRowsSnapshot() [][]vt.Cell {
	if s == nil || s.terminal == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bottomRowsSnapshotLocked()
}

// HistoryRowsSnapshot materializes the retained logical history at the current
// terminal width. ANSI is interpreted by vt.Screen so styles and wide-cell
// continuations survive wrapping.
func (s *FixedBottomSurface) HistoryRowsSnapshot() [][]vt.Cell {
	if s == nil || s.terminal == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.historyRowsSnapshotLocked()
}

// ComposedFrameForTest builds the owned viewport frame without emitting
// terminal bytes. Production uses the same history/bottom snapshots when a
// reserve shrink can be restored entirely from application-owned history.
func (s *FixedBottomSurface) ComposedFrameForTest() [][]vt.Cell {
	if s == nil || s.terminal == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	width, height := s.terminal.Width(), s.terminal.Height()
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	return viewport.Compose(
		width,
		height,
		s.historyRowsWithCursorBlankLocked(),
		s.bottomRowsSnapshotLocked(),
	)
}

// renderOwnedViewportLocked materializes the complete application-owned frame
// and reconciles it through the shared double-buffer backend. Callers hold both
// the surface lock and the terminal write lock.
func (s *FixedBottomSurface) renderOwnedViewportLocked() {
	if s == nil || s.terminal == nil || !s.enabled || !s.ownedViewport {
		return
	}
	width, height := s.terminal.Width(), s.terminal.Height()
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	if s.viewportBackend == nil {
		s.viewportBackend = viewport.New(width, height)
		s.viewportBackend.Invalidate()
	} else if backendWidth, backendHeight := s.viewportBackend.Size(); backendWidth != width || backendHeight != height {
		s.viewportBackend.Resize(width, height)
	}

	// Keep the old bookkeeping fields coherent for cursor placement and for
	// capability fallback after a lifecycle transition. They no longer drive
	// terminal scrolling on the owned path.
	state := s.bottomPaneStateLocked()
	popupPlan := s.popupPaintPlanLocked(state, height)
	s.popupRenderedRows = popupPlan.reservedRows
	s.popupRenderedGapRows = popupPlan.gapRows
	s.popupRenderedStartRow = popupPlan.startRow
	promptPlan := s.promptPaintPlanLocked(state, width)
	if promptPlan.skip || promptPlan.empty {
		s.promptRenderedStartRow = 0
		s.promptRenderedRows = 0
	} else {
		s.promptRenderedStartRow = promptPlan.startRow
		s.promptRenderedRows = promptPlan.areaRows
	}

	s.viewportBackend.StageFrame(viewport.Compose(
		width,
		height,
		s.historyRowsWithCursorBlankLocked(),
		s.bottomRowsSnapshotLocked(),
	))
	if diff := s.viewportBackend.Flush(); diff != "" {
		fmt.Print(diff)
	}
}

type fixedBottomPaintRow struct {
	row  int
	text string
}

type fixedBottomPopupPaintPlan struct {
	rows         []fixedBottomPaintRow
	reservedRows int
	gapRows      int
	startRow     int
}

type fixedBottomPromptPaintPlan struct {
	skip     bool
	empty    bool
	startRow int
	areaRows int
	rows     []fixedBottomPaintRow
}

func (s *FixedBottomSurface) historyRowsSnapshotLocked() [][]vt.Cell {
	if len(s.historyWindow) == 0 {
		return nil
	}
	width := s.terminal.Width()
	if width < 1 {
		width = 80
	}
	// Size the scratch screen conservatively. DisplayWidth treats terminal
	// control sequences as zero-width, while the extra row per logical line
	// accounts for its explicit CRLF separator.
	height := 1
	for _, line := range s.historyWindow {
		displayWidth := DisplayWidth(line)
		if displayWidth < 1 {
			displayWidth = 1
		}
		height += (displayWidth+width-1)/width + 1
	}
	screen := vt.NewScreen(width, height)
	for _, line := range s.historyWindow {
		screen.Feed(line)
		screen.Feed("\r\n")
	}
	end := screen.CursorRow() - 1
	if end < 1 {
		return nil
	}
	return screen.CellRows(1, end)
}

func (s *FixedBottomSurface) historyRowsWithCursorBlankLocked() [][]vt.Cell {
	rows := s.historyRowsSnapshotLocked()
	if !s.outputCursorOnBlankRow {
		return rows
	}
	// Trailing blank is a cursor-parking / absorb bookkeeping marker. Paint it
	// only when the output region still has headroom; otherwise Compose would
	// drop the oldest content row to make room for an empty cell (L1 scrolled
	// off a full N-line write that ended with "\n").
	height := s.terminal.Height()
	outputRows := outputBottomRowForHeight(height, s.effectiveBottomRowsLocked(height))
	if outputRows > 0 && len(rows) >= outputRows {
		return rows
	}
	return appendBlankHistoryRow(rows, s.terminal.Width())
}

func appendBlankHistoryRow(rows [][]vt.Cell, width int) [][]vt.Cell {
	if width < 1 {
		width = 80
	}
	withBlank := make([][]vt.Cell, 0, len(rows)+1)
	withBlank = append(withBlank, rows...)
	withBlank = append(withBlank, make([]vt.Cell, width))
	return withBlank
}

// appendOwnedHistoryRestoreForShrinkLocked replaces the visible output region
// from the retained transcript instead of asking the terminal to scroll it
// down. CSI T can only insert blank rows; it cannot recover history that reserve
// growth already pushed into native scrollback.
//
// The restore is deliberately all-or-nothing. If the logical tail is partial or
// the retained physical rows cannot cover the complete output region, callers
// must keep the legacy scroll-down fallback rather than overwrite foreign or
// uncaptured terminal history.
func (s *FixedBottomSurface) appendOwnedHistoryRestoreForShrinkLocked(builder *strings.Builder) bool {
	if s == nil || s.terminal == nil || builder == nil || s.pendingScrollDownRows < 1 || s.historyPartial {
		return false
	}
	width, height := s.terminal.Width(), s.terminal.Height()
	if width < 1 {
		width = 80
	}
	if height <= 1 {
		return false
	}
	outputRows := outputBottomRowForHeight(height, s.effectiveBottomRowsLocked(height))
	if outputRows < 1 {
		return false
	}
	history := s.historyRowsSnapshotLocked()
	// Match historyRowsWithCursorBlankLocked: only materialize the parking blank
	// when it fits. A full content fill that ended with "\n" keeps the blank as
	// a logical absorb flag without pushing the oldest retained row out of the
	// restore window.
	if (s.outputCursorOnBlankRow || s.outputScrollDebtRows > 0) && len(history) < outputRows {
		history = appendBlankHistoryRow(history, width)
	}
	if len(history) < outputRows {
		return false
	}

	backend := viewport.New(width, outputRows)
	backend.StageFrame(viewport.Compose(width, outputRows, history, nil))
	backend.Invalidate()
	builder.WriteString(backend.Flush())

	if s.outputCursorOnBlankRow || s.outputScrollDebtRows > 0 {
		s.outputCursorOnBlankRow = true
		s.outputScrollDebtRows = 0
	}
	return true
}

func (s *FixedBottomSurface) bottomRowsSnapshotLocked() [][]vt.Cell {
	width, height := s.terminal.Width(), s.terminal.Height()
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}

	state := s.bottomPaneStateLocked()
	var frame strings.Builder
	// Only non-empty paint rows are emitted. Reserved blank margins/gaps stay as
	// true empty cells (Text=""), matching Compose's blankGrid and Backend's EL
	// clear path. Do not seed spaces here: residual ' ' glyphs diverge from the
	// owned frame and reintroduce the band/popup shrink cell-mismatch.
	appendBottomPaintRows(&frame, s.promptPaintPlanLocked(state, width).rows, height)
	appendBottomPaintRows(&frame, s.popupPaintPlanLocked(state, height).rows, height)
	appendBottomPaintRows(&frame, []fixedBottomPaintRow{{
		row:  s.statusRowLocked(),
		text: s.statusPaintTextLocked(state, width),
	}}, height)

	screen := vt.NewScreen(width, height)
	screen.Feed(frame.String())
	bottomRows := s.effectiveBottomRowsLocked(height)
	return screen.CellRows(height-bottomRows+1, height)
}

func appendBottomPaintRows(builder *strings.Builder, rows []fixedBottomPaintRow, height int) {
	if builder == nil {
		return
	}
	for _, paint := range rows {
		if paint.row < 1 || paint.row > height || paint.text == "" {
			continue
		}
		builder.WriteString(terminalMoveToSequence(paint.row, 1))
		builder.WriteString(paint.text)
	}
}

func (s *FixedBottomSurface) popupPaintPlanLocked(state BottomPaneState, height int) fixedBottomPopupPaintPlan {
	visibleLines := state.VisiblePopupLines(height)
	composerRows := state.composerVisibleRowCount()
	plan := fixedBottomPopupPaintPlan{
		reservedRows: len(visibleLines) + composerRows,
		gapRows:      state.popupBottomGapRowCount(),
	}
	if plan.reservedRows == 0 {
		return plan
	}
	plan.startRow = s.popupStartRowLocked(plan.reservedRows, plan.gapRows)
	if plan.startRow < 1 {
		plan.startRow = 1
	}
	for i, line := range visibleLines {
		row := plan.startRow + i
		if row >= s.statusRowLocked() {
			break
		}
		plan.rows = append(plan.rows, fixedBottomPaintRow{
			row:  row,
			text: truncateFixedPopupLine(line, s.terminal.Width()),
		})
	}
	if composer := state.composerLineText(); composer != "" {
		row := plan.startRow + len(visibleLines)
		if row < s.statusRowLocked() {
			plan.rows = append(plan.rows, fixedBottomPaintRow{
				row:  row,
				text: truncateFixedPopupLine(composer, s.terminal.Width()),
			})
		}
	}
	return plan
}

func (s *FixedBottomSurface) statusPaintTextLocked(state BottomPaneState, width int) string {
	model := style.StatusLineModel{State: style.RunReady}
	if state.StatusModel != nil {
		model = *state.StatusModel
	}
	return formatFixedStatusModelWithContext(model, width, s.activeBandThemeContextLocked())
}

func (s *FixedBottomSurface) promptPaintPlanLocked(state BottomPaneState, width int) fixedBottomPromptPaintPlan {
	if state.composerVisibleRowCount() > 0 {
		return fixedBottomPromptPaintPlan{skip: true}
	}
	promptRows := state.promptVisibleRowCount()
	noticeRows := state.promptNoticeVisibleRowCount()
	dynamicRows := state.dynamicStatusVisibleRowCount()
	activeRows := state.activeBandVisibleRowCount()
	topMarginRows := state.promptTopMarginRowCount()
	bottomMarginRows := state.promptBottomMarginRowCount()
	if promptRows < 1 && noticeRows < 1 && dynamicRows < 1 && activeRows < 1 {
		return fixedBottomPromptPaintPlan{empty: true}
	}
	if promptRows > 0 {
		if maxRows := s.promptMaxVisibleRowsLocked(); maxRows > 0 && promptRows > maxRows {
			promptRows = maxRows
		}
	}
	bottom := s.promptBottomRowLocked()
	if bottom < 1 {
		bottom = s.statusRowLocked() - 1
	}
	if bottom < 1 {
		return fixedBottomPromptPaintPlan{empty: true}
	}

	promptStart := bottom + bottomMarginRows + 1
	if promptRows > 0 {
		promptStart = bottom - promptRows + 1
	}
	topMarginStart := promptStart - topMarginRows
	dynamicStart := topMarginStart - dynamicRows
	noticeStart := dynamicStart - noticeRows
	activeStart := noticeStart - activeRows
	plan := fixedBottomPromptPaintPlan{
		startRow: activeStart,
		areaRows: activeRows + noticeRows + dynamicRows + topMarginRows + promptRows + bottomMarginRows,
	}
	if plan.startRow < 1 {
		plan.startRow = 1
	}
	themeContext := s.activeBandThemeContextLocked()

	if activeRows > 0 {
		band := state.ActiveBandLines
		styled := state.ActiveBandStyled
		if len(band) > activeRows {
			band = band[len(band)-activeRows:]
		}
		if len(styled) > activeRows {
			styled = styled[len(styled)-activeRows:]
		}
		for i := 0; i < activeRows && i < len(band); i++ {
			var styledLine *render.Line
			if i < len(styled) {
				styledLine = &styled[i]
			}
			plan.rows = append(plan.rows, fixedBottomPaintRow{
				row:  activeStart + i,
				text: formatActiveBandPaintRow(band[i], styledLine, width, themeContext),
			})
		}
	}
	if noticeRows > 0 {
		noticeLines := state.promptNoticeLines()
		for i := 0; i < noticeRows && i < len(noticeLines); i++ {
			notice := truncateFixedPopupLine(noticeLines[i], width)
			var text string
			if notice != "" {
				text = style.RenderDocument(render.SingleLineDoc(render.Span{
					Text:  notice,
					Style: render.Style{Role: string(style.RoleTextMuted)},
				}), themeContext)
			}
			plan.rows = append(plan.rows, fixedBottomPaintRow{row: noticeStart + i, text: text})
		}
	}
	if dynamicRows > 0 && state.DynamicStatusModel != nil {
		plan.rows = append(plan.rows, fixedBottomPaintRow{
			row:  dynamicStart,
			text: formatFixedStatusModelWithContext(*state.DynamicStatusModel, width, themeContext),
		})
	}
	if promptRows > 0 {
		plan.rows = append(plan.rows, fixedBottomPaintRow{
			row: promptStart,
			text: renderInteractiveInputViewport(
				s.promptLine,
				[]rune(s.promptInput),
				width,
				s.promptViewportStart,
				promptRows,
			),
		})
	}
	return plan
}

func formatActiveBandPaintRow(plain string, styled *render.Line, width int, themeContext style.ThemeContext) string {
	if styled != nil {
		line := *styled
		if render.LineWidth(line) > width {
			line = render.Truncate(line, width, "…")
		}
		return style.RenderDocument(render.LinesDoc(line), themeContext)
	}
	plain = truncateFixedPopupLine(plain, width)
	if plain == "" {
		return ""
	}
	return style.RenderDocument(render.SingleLineDoc(render.Span{
		Text:  plain,
		Style: render.Style{Role: string(style.RoleInfo)},
	}), themeContext)
}
