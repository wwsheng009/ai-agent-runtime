package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
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
	return s.composerLocked().Compose(
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
	if s.leaseID != 0 {
		// Alternate-screen lease active: primary flush is suspended; state
		// is retained and replayed by the release repaint.
		return
	}
	s.stageOwnedFrameLocked()
	if diff := s.viewportBackend.Flush(); diff != "" {
		_ = s.flushHoldingLock(os.Stdout, func(w io.Writer) {
			_, _ = io.WriteString(w, diff)
		})
		s.ownedFrameFlushCount++
	}
}

// stageOwnedFrameLocked materializes the complete application-owned frame
// into the double-buffer back plane without emitting bytes. It is shared by
// the full-frame paint (renderOwnedViewportLocked) and the direct-scroll
// append path (appendOwnedDirectPaintLocked), which stages the same frame but
// commits the already-scrolled history rows silently before flushing so only
// the bottom pane delta is emitted. Callers hold the surface lock and the
// terminal write lock.
func (s *FixedBottomSurface) stageOwnedFrameLocked() {
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
		s.viewportBackend = renderengine.NewScreenModel(width, height)
		s.viewportBackend.Invalidate()
	} else if backendWidth, backendHeight := s.viewportBackend.Size(); backendWidth != width || backendHeight != height {
		s.viewportBackend.Resize(width, height)
	}
	if s.engine != nil && s.engine.Trace() != nil {
		// The reconciliation probe is owned by the engine and shared; this
		// attach is idempotent and survives backend rebuilds on resize.
		s.viewportBackend.AttachTrace(s.engine.Trace())
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

	plan := s.composedPlanLocked(width, height)
	s.lastRowOwners = planOwnersCopy(plan)
	s.viewportBackend.StageFrame(renderengine.PlanCells(plan))
}

// composedPlanLocked builds the full-screen owned frame (history + bottom
// reserve) with per-row ownership annotations, the single authoritative
// layout of the owned path.
func (s *FixedBottomSurface) composedPlanLocked(width, height int) []renderengine.PlanRow {
	history := s.historyRowsWithCursorBlankLocked()
	historyPlan := make([]renderengine.PlanRow, len(history))
	for i := range history {
		historyPlan[i] = renderengine.PlanRow{Owner: renderengine.RowOwnerTranscript, Cells: history[i]}
	}
	return s.composerLocked().ComposePlan(width, height, historyPlan, s.bottomRowsWithOwnersLocked())
}

func (s *FixedBottomSurface) composerLocked() *renderengine.Composer {
	if s != nil && s.engine != nil && s.engine.Composer() != nil {
		return s.engine.Composer()
	}
	return renderengine.NewComposer()
}

func planOwnersCopy(plan []renderengine.PlanRow) []renderengine.RowOwner {
	if len(plan) == 0 {
		return nil
	}
	owners := make([]renderengine.RowOwner, len(plan))
	for i, row := range plan {
		owners[i] = row.Owner
	}
	return owners
}

// reconcileOwnedViewportLocked forces a full-frame repaint so the physical
// terminal converges on the composed scene even when a previous frame was
// written outside the double buffer (legacy popup clearing, host-side
// scroll, geometry transitions). Callers hold the surface lock and the
// terminal write lock.
func (s *FixedBottomSurface) reconcileOwnedViewportLocked() {
	if s == nil || !s.enabled || !s.ownedViewport {
		return
	}
	if s.viewportBackend != nil {
		s.viewportBackend.Invalidate()
	}
	s.renderOwnedViewportLocked()
}

// Reconcile forces the owned viewport to a full-frame repaint on the next
// write lock acquisition, repairing any divergence between the double-buffer
// front frame and the physical terminal. It is the public hook for
// reconciliation timings that live outside the surface (finalize, phase
// transitions); surface-internal timings (resize, lease release, popup
// close) already reconcile inline.
func (s *FixedBottomSurface) Reconcile() {
	if s == nil || !s.enabled || !s.ownedViewport {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	WithTerminalWriteLock(func() {
		s.reconcileOwnedViewportLocked()
		s.restoreStoredPromptCursorLocked()
	})
}

type fixedBottomPaintRow struct {
	row  int
	text string
	// owner annotates the component that claims this row. It is consumed by
	// the owned RowPlan (stage C); legacy paint paths ignore it.
	owner renderengine.RowOwner
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
	return s.expandHistoryLinesLocked(s.historyWindow)
}

// expandHistoryLinesLocked materializes logical history lines as terminal
// physical rows at the current width. ANSI is interpreted by vt.Screen so
// styles and wide-cell continuations survive wrapping. This is the single
// expansion primitive shared by the composed frame, the segment single-row
// gate, and wrapped-segment handoff.
func (s *FixedBottomSurface) expandHistoryLinesLocked(lines []string) [][]vt.Cell {
	if len(lines) == 0 {
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
	for _, line := range lines {
		displayWidth := DisplayWidth(line)
		if displayWidth < 1 {
			displayWidth = 1
		}
		height += (displayWidth+width-1)/width + 1
	}
	screen := vt.NewScreen(width, height)
	for _, line := range lines {
		screen.Feed(line)
		screen.Feed("\r\n")
	}
	end := screen.CursorRow() - 1
	if end < 1 {
		return nil
	}
	return screen.CellRows(1, end)
}

// expandHistorySegmentToPhysicalTextLocked expands a handoff segment whose
// logical lines wrap at the current width into physical-row text. Each
// returned row occupies exactly one terminal row, so the DECSTBM \r\n scroll
// count stays 1:1 with the terminal. The expansion goes through vt.Screen so
// wide runes and ANSI-deferred wrapping match real terminal behavior. Filler
// rows are repainted by the owned full-frame render immediately after handoff,
// so plain physical text (without re-emitted SGR) is sufficient here.
func (s *FixedBottomSurface) expandHistorySegmentToPhysicalTextLocked(segment []string) []string {
	if len(segment) == 0 {
		return nil
	}
	width := s.terminal.Width()
	if width < 1 {
		return nil
	}
	rows := s.expandHistoryLinesLocked(segment)
	if len(rows) == 0 {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, cells := range rows {
		out = append(out, historyCellsToPlainText(cells))
	}
	return out
}

// historyCellsToPlainText flattens one physical row of reconstructed cells to
// plain text, mirroring vt.Screen.Line: wide-run continuation columns are
// skipped, blank cells become spaces, and trailing blanks are trimmed. The
// result is exactly one terminal row, so the handoff scroll count stays 1:1.
func historyCellsToPlainText(cells []vt.Cell) string {
	var b strings.Builder
	for _, c := range cells {
		if c.Cont {
			continue
		}
		if c.Text == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Text)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func (s *FixedBottomSurface) historyRowsWithCursorBlankLocked() [][]vt.Cell {
	rows := s.historyRowsSnapshotLocked()
	if !s.legacyReserve.CursorOnBlankRow {
		return rows
	}
	// The ActiveBand owns an explicit semantic separator from retained history.
	// Do not materialize the output cursor's parking blank at the same time, or
	// the composed frame would show two empty rows before Running/progress.
	if s.bottomPaneStateLocked().activeBandTopGapRowCount() > 0 {
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

func (s *FixedBottomSurface) bottomRowsSnapshotLocked() [][]vt.Cell {
	return renderengine.PlanCells(s.bottomRowsWithOwnersLocked())
}

// bottomRowsWithOwnersLocked materializes the bottom reserve as owned rows
// (cells + per-row owner). Rows run top-to-bottom from the first reserve row
// to the status row. Gap rows (margins, popup gaps) are annotated RowOwnerGap
// so the composed plan covers every physical row with exactly one owner.
func (s *FixedBottomSurface) bottomRowsWithOwnersLocked() []renderengine.PlanRow {
	width, height := s.terminal.Width(), s.terminal.Height()
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}

	state := s.bottomPaneStateLocked()
	promptPlan := s.promptPaintPlanLocked(state, width)
	popupPlan := s.popupPaintPlanLocked(state, height)
	var frame strings.Builder
	// Only non-empty paint rows are emitted. Reserved blank margins/gaps stay as
	// true empty cells (Text=""), matching Compose's blankGrid and Backend's EL
	// clear path. Do not seed spaces here: residual ' ' glyphs diverge from the
	// owned frame and reintroduce the band/popup shrink cell-mismatch.
	appendBottomPaintRows(&frame, promptPlan.rows, height)
	appendBottomPaintRows(&frame, popupPlan.rows, height)
	appendBottomPaintRows(&frame, []fixedBottomPaintRow{{
		row:   s.statusRowLocked(),
		text:  s.statusPaintTextLocked(state, width),
		owner: renderengine.RowOwnerStatus,
	}}, height)

	screen := vt.NewScreen(width, height)
	screen.Feed(frame.String())
	bottomRows := s.effectiveBottomRowsLocked(height)
	cells := screen.CellRows(height-bottomRows+1, height)
	owners := s.bottomOwnerMapLocked(height, popupPlan, promptPlan)
	plan := make([]renderengine.PlanRow, len(cells))
	for i, row := range cells {
		rowNo := height - bottomRows + 1 + i
		plan[i] = renderengine.PlanRow{Owner: owners[rowNo], Cells: row}
	}
	return plan
}

// bottomOwnerMapLocked maps physical row numbers (1-based) to the component
// that owns them. Paint rows carry their owner; reserved blank segments
// (popup gaps, prompt margins, band top gap) are annotated Gap. Rows outside
// every declared segment are left unset; the caller treats them as Gap.
func (s *FixedBottomSurface) bottomOwnerMapLocked(height int, popupPlan fixedBottomPopupPaintPlan, promptPlan fixedBottomPromptPaintPlan) map[int]renderengine.RowOwner {
	owners := make(map[int]renderengine.RowOwner)
	owners[s.statusRowLocked()] = renderengine.RowOwnerStatus
	for _, p := range popupPlan.rows {
		owners[p.row] = renderengine.RowOwnerPopup
	}
	for _, p := range promptPlan.rows {
		if p.owner != renderengine.RowOwnerGap {
			owners[p.row] = p.owner
		}
	}
	if popupPlan.reservedRows > 0 {
		for r := popupPlan.startRow; r < popupPlan.startRow+popupPlan.reservedRows+popupPlan.gapRows && r <= height; r++ {
			if _, ok := owners[r]; !ok {
				owners[r] = renderengine.RowOwnerGap
			}
		}
	}
	if promptPlan.areaRows > 0 {
		for r := promptPlan.startRow; r < promptPlan.startRow+promptPlan.areaRows && r <= height; r++ {
			if _, ok := owners[r]; !ok {
				owners[r] = renderengine.RowOwnerGap
			}
		}
	}
	return owners
}

// RowOwnersForTest returns a copy of the most recent composed frame's
// per-row owner table (row 0 = screen row 1). Nil when the owned viewport has
// never composed a frame or is disabled.
func (s *FixedBottomSurface) RowOwnersForTest() []renderengine.RowOwner {
	if s == nil || s.terminal == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || !s.ownedViewport {
		return nil
	}
	width, height := s.terminal.Width(), s.terminal.Height()
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	return planOwnersCopy(s.composedPlanLocked(width, height))
}

// RowPlanDebugString renders the current row-ownership table for /debug
// display and diagnostics. Empty when the owned viewport is inactive.
func (s *FixedBottomSurface) RowPlanDebugString() string {
	if s == nil || s.terminal == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || !s.ownedViewport {
		return ""
	}
	width, height := s.terminal.Width(), s.terminal.Height()
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	plan := s.composedPlanLocked(width, height)
	var b strings.Builder
	fmt.Fprintf(&b, "Row Ownership (%dx%d):\n", width, height)
	for i, row := range plan {
		preview := strings.TrimSpace(render.ANSIToPlain(cellRowPlainText(row.Cells)))
		if len(preview) > 12 {
			preview = preview[:12]
		}
		fmt.Fprintf(&b, "%3d  %-10s  %s\n", i+1, row.Owner.String(), preview)
	}
	return b.String()
}

// PaintTraceDebugString renders the paint reconciliation report collected by
// the engine-owned probe since the last /debug on. It classifies per row how
// often it was emitted, how often those emits were white repaints (content
// unchanged), and whether any content change was left unpainted (missing
// coverage). Empty when no engine is wired or no events were recorded.
func (s *FixedBottomSurface) PaintTraceDebugString() string {
	if s == nil || s.terminal == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || !s.ownedViewport || s.engine == nil || s.engine.Trace() == nil {
		return ""
	}
	return s.engine.Trace().DebugString(s.lastRowOwners)
}

func cellRowPlainText(cells []vt.Cell) string {
	var b strings.Builder
	for _, cell := range cells {
		if cell.Cont {
			continue
		}
		b.WriteString(cell.Text)
	}
	return b.String()
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
			row:   row,
			text:  truncateFixedPopupLine(line, s.terminal.Width()),
			owner: renderengine.RowOwnerPopup,
		})
	}
	if composer := state.composerLineText(); composer != "" {
		row := plan.startRow + len(visibleLines)
		if row < s.statusRowLocked() {
			plan.rows = append(plan.rows, fixedBottomPaintRow{
				row:   row,
				text:  truncateFixedPopupLine(composer, s.terminal.Width()),
				owner: renderengine.RowOwnerPopup,
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
	activeTopGapRows := state.activeBandTopGapRowCount()
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
	activeTopGapStart := activeStart - activeTopGapRows
	plan := fixedBottomPromptPaintPlan{
		startRow: activeTopGapStart,
		areaRows: activeTopGapRows + activeRows + noticeRows + dynamicRows + topMarginRows + promptRows + bottomMarginRows,
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
				row:   activeStart + i,
				text:  formatActiveBandPaintRow(band[i], styledLine, width, themeContext),
				owner: renderengine.RowOwnerBand,
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
			plan.rows = append(plan.rows, fixedBottomPaintRow{
				row:   noticeStart + i,
				text:  text,
				owner: renderengine.RowOwnerPrompt,
			})
		}
	}
	if dynamicRows > 0 && state.DynamicStatusModel != nil {
		plan.rows = append(plan.rows, fixedBottomPaintRow{
			row:   dynamicStart,
			text:  formatFixedStatusModelWithContext(*state.DynamicStatusModel, width, themeContext),
			owner: renderengine.RowOwnerStatus,
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
			owner: renderengine.RowOwnerPrompt,
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
