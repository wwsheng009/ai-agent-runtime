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
	// The plan is the single authoritative composed frame (history + bottom
	// reserve + debug annotations), so tests observe exactly what the
	// production paint path stages.
	// The test/display frame re-annotates with the most recent frame's
	// white rows (star marker); the staged frame carries the annotation
	// without the star so the star phase never flips the white
	// classification of the next reconcile.
	return renderengine.PlanCells(s.composedPlanLocked(width, height, true))
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
	if diff := s.viewportBackend.PrepareFlush(); diff != "" {
		if err := s.flushHoldingLock(os.Stdout, func(w io.Writer) {
			_, _ = io.WriteString(w, diff)
		}); err != nil {
			s.viewportBackend.MarkWriteFailed()
		} else {
			s.viewportBackend.ConfirmFlush()
			s.ownedFrameFlushCount++
		}
	} else if s.viewportBackend.ProjectionValidity() == renderengine.ProjectionUnknown {
		// An empty recovery diff is possible only for an empty frame; it is
		// nevertheless the full projection of that frame.
		s.viewportBackend.ConfirmFlush()
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

	plan := s.composedPlanLocked(width, height, false)
	s.lastRowOwners = planOwnersCopy(plan)
	s.viewportBackend.StageFrame(renderengine.PlanCells(plan))
}

// composedPlanLocked builds the full-screen owned frame (history + bottom
// reserve) with per-row ownership annotations, the single authoritative
// layout of the owned path.
func (s *FixedBottomSurface) composedPlanLocked(width, height int, debugStars bool) []renderengine.PlanRow {
	history := s.historyRowsWithCursorBlankLocked()
	historyPlan := make([]renderengine.PlanRow, len(history))
	for i := range history {
		historyPlan[i] = renderengine.PlanRow{Owner: renderengine.RowOwnerTranscript, Cells: history[i]}
	}
	plan := s.composerLocked().ComposePlan(width, height, historyPlan, s.bottomRowsWithOwnersLocked())
	s.annotateDebugRowsLocked(plan, width, debugStars)
	return plan
}

// paintDebugTagSGR is the dim attribute applied to the per-row debug tag
// while /debug on is active, so the tag reads as metadata rather than
// message content.
const paintDebugTagSGR = "2"

// debugTagWidth is the minimum width of the per-row tag "[hhhh #NN wN]"
// (4-hex content fingerprint, 1-based screen row, cumulative white-repaint
// count for that content; a trailing "*" marks rows white-repainted by the
// most recent frame). The w counter and marker make the actual width grow;
// truncation adapts to the rendered tag length.
const debugTagWidth = 13 // "[3f9a #05 w0]"

// annotateDebugRowsLocked adds a unique per-row debug tag to the message
// stream / data-flow rows (transcript and active band) while /debug on is
// active: "[3f9a #05 w0]". The tag maps the screen directly onto the
// /debug display table (row numbers match), the fingerprint makes identical
// content instantly recognizable (duplicate rendering of the same row keeps
// the same fingerprint), and the w counter increments visibly each time that
// content is white-repainted - so repeated rendering is located on the
// message stream itself instead of requiring a HUD or table lookup. The
// counter is content-addressed (WhiteEmitsByHash): a row that scrolls to a
// new screen position keeps its own w count and does not inherit the
// position's history, and a trailing "*" marks rows white-repainted by the
// most recent frame, separating "duplicated right now" from the lifetime
// count. The tag is a pure annotation of the composed frame: history data is
// never mutated, and a row that keeps its content keeps its fingerprint, so
// the white-repaint reconciliation stays honest. Prompt/status/popup rows
// are left untouched (interaction rows, not message data). Callers hold the
// surface lock.
func (s *FixedBottomSurface) annotateDebugRowsLocked(plan []renderengine.PlanRow, width int, withStar bool) {
	if s == nil || s.engine == nil || s.engine.Trace() == nil || !s.engine.Trace().Enabled() {
		return
	}
	if width < 1 {
		width = 80
	}
	if width <= debugTagWidth {
		return
	}
	trace := s.engine.Trace()
	var lastWhite []int
	if withStar {
		lastWhite = trace.LastFrame().White
	}
	for i := range plan {
		row := &plan[i]
		if row.Owner != renderengine.RowOwnerTranscript && row.Owner != renderengine.RowOwnerBand {
			continue
		}
		hash := renderengine.RowTextHash(row.Cells)
		justWhite := false
		for _, whiteRow := range lastWhite {
			if whiteRow == i+1 {
				justWhite = true
				break
			}
		}
		tag := debugRowTag(i+1, hash, trace.WhiteEmitsByHash(hash), justWhite)
		tagCells := debugTagCells(tag)
		row.Cells = append(tagCells, truncateRowCells(row.Cells, width-len(tagCells))...)
	}
}

// debugRowTag builds the "[hhhh #NN wN]" tag for one 1-based screen row: a
// 4-hex content fingerprint (content-addressed, so scrolling never changes
// it), the screen row number, and the cumulative white-repaint count for
// that content. A trailing "*" marks rows that were white-repainted by the
// most recent recorded frame, separating "duplicated right now" from the
// lifetime count; the marker disappears once a frame records no white
// repaint on the row.
func debugRowTag(row int, hash uint32, white uint64, justWhite bool) string {
	star := ""
	if justWhite {
		star = "*"
	}
	return fmt.Sprintf("[%04x #%02d w%d%s]", hash&0xffff, row, white, star)
}

// hash4Hex returns the 4-hex-digit content fingerprint (truncated FNV-1a 32)
// of plain text. It delegates to the render-engine hash so the tag's
// fingerprint and the probe's per-content white counters share one
// implementation.
func hash4Hex(text string) string {
	return fmt.Sprintf("%04x", renderengine.TextHash4(text)&0xffff)
}

// debugTagCells converts the ASCII tag into dim cells, one cell per rune.
func debugTagCells(tag string) []vt.Cell {
	cells := make([]vt.Cell, 0, len(tag))
	for _, r := range tag {
		cells = append(cells, vt.Cell{Text: string(r), SGR: []string{paintDebugTagSGR}})
	}
	return cells
}

// truncateRowCells keeps the first max cells of a row, dropping a dangling
// continuation cell so a double-width rune is never cut in half.
func truncateRowCells(cells []vt.Cell, max int) []vt.Cell {
	if max <= 0 {
		return nil
	}
	if len(cells) <= max {
		return cells
	}
	cut := cells[:max]
	if cut[len(cut)-1].Cont {
		cut = cut[:len(cut)-1]
	}
	return cut
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
	skip        bool
	empty       bool
	startRow    int
	areaRows    int
	promptStart int
	promptRows  int
	rows        []fixedBottomPaintRow
}

func (s *FixedBottomSurface) historyRowsSnapshotLocked() [][]vt.Cell {
	// Rows already handed into native scrollback must never re-enter the
	// composed frame: a later full-frame repaint (band/popup/prompt shrink
	// restore, status refresh) would paint them a second time, leaving the
	// same row once in native scrollback and once on screen — the duplicate
	// rendering users see after a band disappears. The frame window starts at
	// the handoff frontier; scrollback is the single durable copy of older
	// rows and users reach them by scrolling.
	window := s.historyWindow
	if frontier := s.handoffFrontier.Value(); frontier > 0 {
		if frontier >= len(window) {
			return nil
		}
		window = window[frontier:]
	}
	return s.expandHistoryLinesLocked(window)
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

// expandHistoryLinesToStyledTextLocked expands a visible-tail segment whose
// logical lines wrap at the current width into physical-row text WITH
// re-emitted SGR styling. Scrollback handoff can use plain text (the owned
// full-frame repaint re-renders the visible window from styled source), but
// the direct-scroll append paints the visible tail through the native scroll
// region and only flushes the bottom-pane delta afterwards — the transcript is
// never repainted, so the rows written here must carry their own styling.
func (s *FixedBottomSurface) expandHistoryLinesToStyledTextLocked(segment []string) []string {
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
		out = append(out, historyCellsToStyledText(cells))
	}
	return out
}

// historyCellsToStyledText re-emits one physical row of reconstructed cells as
// ANSI text: SGR changes become CSI m sequences, wide-run continuation columns
// are skipped, and blank cells become spaces. Trailing blanks are trimmed and
// the row ends with an SGR reset, so the result is exactly one terminal row
// with self-contained styling (a trailing styled blank cannot bleed into the
// next emitted row).
func historyCellsToStyledText(cells []vt.Cell) string {
	high := -1
	for column, cell := range cells {
		if cell.Text != "" || cell.Cont || len(cell.SGR) > 0 {
			high = column
		}
	}
	if high < 0 {
		return ""
	}
	var b strings.Builder
	var activeSGR []string
	haveActiveSGR := false
	for column := 0; column <= high; column++ {
		cell := cells[column]
		if cell.Cont {
			continue
		}
		if !haveActiveSGR || !sgrEqual(activeSGR, cell.SGR) {
			b.WriteString("\x1b[0m")
			if len(cell.SGR) > 0 {
				b.WriteString("\x1b[")
				b.WriteString(strings.Join(cell.SGR, ";"))
				b.WriteByte('m')
			}
			activeSGR = cell.SGR
			haveActiveSGR = true
		}
		if cell.Text == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(cell.Text)
		}
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// sgrEqual reports whether two SGR code lists are identical.
func sgrEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
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
	owners := s.bottomOwnerMapLocked(height, state, popupPlan, promptPlan)
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
func (s *FixedBottomSurface) bottomOwnerMapLocked(height int, state BottomPaneState, popupPlan fixedBottomPopupPaintPlan, promptPlan fixedBottomPromptPaintPlan) map[int]renderengine.RowOwner {
	owners := make(map[int]renderengine.RowOwner)
	owners[s.statusRowLocked()] = renderengine.RowOwnerStatus
	for _, p := range popupPlan.rows {
		owners[p.row] = renderengine.RowOwnerPopup
	}
	for _, p := range promptPlan.rows {
		// A plan reserves a blank prompt-input gap while a popup is shown even
		// when the chat prompt is not active. That row is a Gap, not a Prompt
		// owner: no prompt cells were painted there. Keep the annotation tied to
		// actual text so the legacy snapshot and pure bottom row plan share the
		// same ownership contract.
		if p.owner != renderengine.RowOwnerGap && p.text != "" {
			owners[p.row] = p.owner
		}
	}
	// renderInteractiveInputViewport writes a multi-row prompt as one terminal
	// text operation containing CRLF separators. Its first row is present in
	// promptPlan.rows, but every occupied viewport row must receive the same
	// Prompt owner or the physical text/owner snapshots disagree after a long
	// draft reflows.
	if state.PromptVisible && promptPlan.promptRows > 0 {
		for row := promptPlan.promptStart; row < promptPlan.promptStart+promptPlan.promptRows && row <= height; row++ {
			if row >= 1 {
				owners[row] = renderengine.RowOwnerPrompt
			}
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
	return planOwnersCopy(s.composedPlanLocked(width, height, false))
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
	plan := s.composedPlanLocked(width, height, false)
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
		startRow:    activeTopGapStart,
		areaRows:    activeTopGapRows + activeRows + noticeRows + dynamicRows + topMarginRows + promptRows + bottomMarginRows,
		promptStart: promptStart,
		promptRows:  promptRows,
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
