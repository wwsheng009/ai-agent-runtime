package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

const (
	// ActiveBandMinRows keeps the in-progress stream viewport usable on short
	// terminals; it matches the historical fixed band height.
	ActiveBandMinRows = 6
	// ActiveBandMaxRows is the hard ceiling for the stream viewport on very
	// tall terminals so scrollback keeps most of the screen.
	ActiveBandMaxRows = 14
	// activeBandReservedRows is the space kept for scrollback output, composer,
	// transient activity, notices and status rows when sizing the band.
	activeBandReservedRows = 12
	// activeBandHeightDivisor gives the band roughly one third of the screen
	// before the ceiling and reserve clamps apply.
	activeBandHeightDivisor = 3
	// Keep the main chat composer visually separated from transient activity
	// above it and the persistent footer below it.
	chatComposerTopMarginRows           = 1
	chatComposerBottomMarginRows        = 1
	chatComposerVerticalMarginMinHeight = 12
	// DefaultGeometryProbeMinInterval caps how often the live stream paint path
	// re-probes terminal size. Active cells paint up to ~30 FPS; probing every
	// frame is unnecessary because human resize events are far slower.
	DefaultGeometryProbeMinInterval = 100 * time.Millisecond
	// SoftOutputTailMaxLines bounds the rewriteable committed tail kept at the
	// bottom of the output region. Older lines fall out of the soft window and
	// become irreversible scrollback history. Coordinator soft ownership uses
	// the same cap so source-backed reflow stays 1:1 with the surface window.
	SoftOutputTailMaxLines = 64
)

// ActiveBandRows returns the adaptive row budget for the in-progress stream
// viewport. It grows with terminal height, never drops below the historical
// six rows, and always leaves room for output, composer and status rows.
func ActiveBandRows(terminalHeight int) int {
	if terminalHeight <= 0 {
		return ActiveBandMinRows
	}
	rows := terminalHeight / activeBandHeightDivisor
	if rows > ActiveBandMaxRows {
		rows = ActiveBandMaxRows
	}
	if limit := terminalHeight - activeBandReservedRows; rows > limit {
		rows = limit
	}
	if rows < ActiveBandMinRows {
		rows = ActiveBandMinRows
	}
	return rows
}

func chatComposerVerticalMargins(terminalHeight int) (top, bottom int) {
	if terminalHeight < chatComposerVerticalMarginMinHeight {
		return 0, 0
	}
	return chatComposerTopMarginRows, chatComposerBottomMarginRows
}

// FixedBottomSurface reserves the last terminal row for lightweight status
// while normal chat output scrolls in the region above it.
type FixedBottomSurface struct {
	terminal               *Terminal
	mu                     sync.Mutex
	enabled                bool
	testMode               bool
	statusModel            *style.StatusLineModel
	dynamicStatusModel     *style.StatusLineModel
	popupLines             []string
	popupOwner             string
	popupInstance          uint64
	nextPopupInstance      uint64
	popupViewport          *PopupViewportSpec
	popupBelowPrompt       bool
	popupStack             []fixedBottomPopupState
	composerLine           string
	promptNoticeLine       string
	promptEditorStatusLine string
	// activeBandLines is the Phase 5 in-progress stream viewport (not scrollback).
	activeBandLines        []string
	activeBandStyled       []render.Line
	promptLine             string
	promptInput            string
	promptReservedRows     int
	promptViewportStart    int
	promptCursorRow        int
	promptCursorCol        int
	promptRenderedStartRow int
	promptRenderedRows     int
	popupRenderedRows      int
	popupRenderedGapRows   int
	popupRenderedStartRow  int
	popupReservedRows      int
	scrollCompensatedRows  int
	pendingScrollDownRows  int
	// outputCursorOnBlankRow is true when the last WriteOutput ended on a
	// trailing newline, leaving the output-region cursor on an empty row.
	// Band/popup growth must consume that blank instead of scrolling it into
	// a permanent gap above the reserved bottom pane.
	outputCursorOnBlankRow bool
	// softOutputLines is the most recent committed output still sitting at the
	// bottom of the output region. Source-backed resize reflow may rewrite it
	// in place; geometry changes or overflow trim invalidate safe reflow.
	softOutputLines   []string
	softOutputValid   bool
	softOutputTrimmed bool
	lastWidth         int
	lastHeight        int
	lastBottomRows    int
	// lastGeometryProbeAt records the last SyncTerminalGeometry* size probe so
	// the paint path can throttle GetSize syscalls without missing resizes for
	// longer than DefaultGeometryProbeMinInterval.
	lastGeometryProbeAt time.Time
}

type fixedBottomPopupState struct {
	lines             []string
	owner             string
	instance          uint64
	viewport          *PopupViewportSpec
	composerLine      string
	popupBelowPrompt  bool
	popupReservedRows int
}

type PopupHandle struct {
	owner    string
	instance uint64
}

type PopupViewportSpec struct {
	HeaderLines []string
	BodyLines   []string
	FooterLines []string
	Anchor      int
}

func (h PopupHandle) Valid() bool {
	return strings.TrimSpace(h.owner) != "" && h.instance != 0
}

func NewFixedBottomSurface(term *Terminal) *FixedBottomSurface {
	if term == nil {
		term = NewTerminal()
	}
	return &FixedBottomSurface{
		terminal: term,
		statusModel: &style.StatusLineModel{
			State: style.RunReady,
		},
	}
}

func (s *FixedBottomSurface) Enable() bool {
	if s == nil || s.terminal == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.canEnableLocked() {
		return false
	}
	s.enabled = true
	s.testMode = false
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.moveToOutputLocked()
	})
	return true
}

// EnableForTest forces the surface on with a synthetic geometry for unit tests.
// It skips TTY capability probes and does not paint (callers drive Set* APIs).
func (s *FixedBottomSurface) EnableForTest(width, height int) {
	if s == nil {
		return
	}
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		s.terminal = &Terminal{width: width, height: height, theme: GetTheme(ThemeAuto)}
	}
	// Pin the geometry: probing a non-TTY writer always reports 80x24, which
	// would silently discard the requested height on the next layout pass.
	s.terminal.SetSizeForTest(width, height)
	s.enabled = true
	s.testMode = true
	s.lastWidth = width
	s.lastHeight = height
	s.lastBottomRows = 1
}

// DynamicStatusTicksEnabled reports whether wall-clock activity updates should
// be scheduled. Synthetic surfaces are driven explicitly by tests and must not
// leave background timers running after a test returns.
func (s *FixedBottomSurface) DynamicStatusTicksEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled && !s.testMode
}

func (s *FixedBottomSurface) Disable() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.terminal.SaveCursor()
		s.terminal.ResetScrollRegion()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.terminal.MoveTo(s.statusRowLocked(), 1)
		s.terminal.ClearLine()
		s.terminal.RestoreCursor()
	})
	s.clearPopupRenderStateLocked()
	s.clearPopupStateLocked(true)
	s.clearComposerStateLocked()
	s.clearPromptStateLocked(true)
	s.activeBandLines = nil
	s.activeBandStyled = nil
	s.dynamicStatusModel = nil
	s.enabled = false
	s.testMode = false
	s.scrollCompensatedRows = 0
	s.pendingScrollDownRows = 0
	s.outputCursorOnBlankRow = false
	s.invalidateSoftOutputLocked()
}

func (s *FixedBottomSurface) clearPopupStateLocked(clearStack bool) {
	if s == nil {
		return
	}
	s.popupLines = nil
	s.popupOwner = ""
	s.popupInstance = 0
	s.popupViewport = nil
	s.popupBelowPrompt = false
	s.popupReservedRows = 0
	if clearStack {
		s.popupStack = nil
	}
}

func (s *FixedBottomSurface) clearPopupRenderStateLocked() {
	if s == nil {
		return
	}
	s.popupRenderedRows = 0
	s.popupRenderedGapRows = 0
	s.popupRenderedStartRow = 0
}

func (s *FixedBottomSurface) clearComposerStateLocked() {
	if s == nil {
		return
	}
	s.composerLine = ""
}

func (s *FixedBottomSurface) clearPromptStateLocked(clearNotice bool) {
	if s == nil {
		return
	}
	if clearNotice {
		s.promptNoticeLine = ""
	}
	s.promptEditorStatusLine = ""
	s.promptLine = ""
	s.promptInput = ""
	s.promptReservedRows = 0
	s.promptViewportStart = 0
	s.promptCursorRow = 0
	s.promptCursorCol = 0
	s.promptRenderedStartRow = 0
	s.promptRenderedRows = 0
}

func (s *FixedBottomSurface) setPromptStateLocked(line string, input string, rows int, cursorRow int, cursorCol int) {
	if s == nil {
		return
	}
	s.refreshTerminalDimensionsLocked()
	s.promptLine = line
	s.promptInput = input
	visibleRows := rows
	if maxRows := s.promptInputMaxVisibleRowsLocked(); visibleRows > maxRows {
		visibleRows = maxRows
	}
	if visibleRows < 1 {
		visibleRows = 1
	}
	s.promptViewportStart = boundedInteractiveInputViewportStart(rows, cursorRow, visibleRows, s.promptViewportStart)
	s.promptReservedRows = visibleRows
	s.promptCursorRow = cursorRow - s.promptViewportStart
	s.promptCursorCol = cursorCol
}

func (s *FixedBottomSurface) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SyncTerminalGeometry re-probes terminal size and applies scroll-region layout
// when width, height, or reserved bottom rows change. Returns true when the
// physical terminal width or height differs from the last applied layout cache
// (lastWidth/lastHeight). Soft rewrite ownership is intentionally preserved so
// callers can source-reflow the soft committed tail in place.
//
// Explicit refresh paths (theme, command, tests) should call this unthrottled
// form. The live stream paint path should prefer SyncTerminalGeometryThrottled.
func (s *FixedBottomSurface) SyncTerminalGeometry() (sizeChanged bool) {
	sizeChanged, _ = s.syncTerminalGeometry(0)
	return sizeChanged
}

// SyncTerminalGeometryThrottled is the paint-path variant of
// SyncTerminalGeometry. When minInterval has not elapsed since the last probe,
// it returns (false, false) without touching the terminal. A zero/negative
// interval forces a probe (same as SyncTerminalGeometry).
func (s *FixedBottomSurface) SyncTerminalGeometryThrottled(minInterval time.Duration) (sizeChanged, probed bool) {
	return s.syncTerminalGeometry(minInterval)
}

func (s *FixedBottomSurface) syncTerminalGeometry(minInterval time.Duration) (sizeChanged, probed bool) {
	if s == nil || s.terminal == nil {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false, false
	}
	now := time.Now()
	if minInterval > 0 && !s.lastGeometryProbeAt.IsZero() && now.Sub(s.lastGeometryProbeAt) < minInterval {
		return false, false
	}
	s.lastGeometryProbeAt = now

	prevW, prevH := s.lastWidth, s.lastHeight
	width, height := s.terminal.RefreshSize()
	if width <= 0 {
		if prevW > 0 {
			width = prevW
		} else {
			width = s.terminal.Width()
		}
	}
	if height <= 0 {
		if prevH > 0 {
			height = prevH
		} else {
			height = s.terminal.Height()
		}
	}
	// Compare against the last applied layout, not the pre-refresh terminal
	// cache: tests may pin a new size via SetSizeForTest while lastWidth still
	// describes the previous scroll region.
	sizeChanged = (prevW > 0 && width > 0 && width != prevW) ||
		(prevH > 0 && height > 0 && height != prevH)
	// Reuse the just-probed size so applyLayout does not call RefreshSize again
	// under the same lock hold. Soft tail stays valid across a true resize.
	s.applyLayoutWithSizeLocked(width, height)
	return sizeChanged, true
}

// PromptInputMaxVisibleRows returns the editor viewport budget that keeps one
// output row, the status row, runtime notices, and a possible editor status
// visible. Disabled surfaces retain the regular composer limit.
func (s *FixedBottomSurface) PromptInputMaxVisibleRows() int {
	if s == nil || s.terminal == nil {
		return ChatComposerMaxVisibleRows
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return ChatComposerMaxVisibleRows
	}
	s.refreshTerminalDimensionsLocked()
	return s.promptInputMaxVisibleRowsLocked()
}

func (s *FixedBottomSurface) refreshTerminalDimensionsLocked() {
	if s == nil || s.terminal == nil || s.terminal.driver == nil || s.terminal.driver.stdout == nil {
		return
	}
	width, height, err := s.terminal.driver.Size()
	if err != nil || width <= 0 || height <= 0 {
		return
	}
	s.terminal.width = width
	s.terminal.height = height
}

func (s *FixedBottomSurface) promptInputMaxVisibleRowsLocked() int {
	if s == nil || s.terminal == nil {
		return ChatComposerMaxVisibleRows
	}
	height := s.terminal.Height()
	if height <= 0 {
		return ChatComposerMaxVisibleRows
	}
	const (
		outputRows       = 1
		statusRows       = 1
		editorStatusRows = 1
	)
	dynamicStatusRows := 0
	if s.dynamicStatusModel != nil {
		dynamicStatusRows = 1
	}
	topMarginRows, bottomMarginRows := chatComposerVerticalMargins(height)
	rows := height - outputRows - statusRows - editorStatusRows - topMarginRows - bottomMarginRows - dynamicStatusRows - len(promptNoticeDisplayLines(s.promptNoticeLine)) - len(s.activeBandLines)
	if rows < 1 {
		return 1
	}
	if rows > ChatComposerMaxVisibleRows {
		return ChatComposerMaxVisibleRows
	}
	return rows
}

func (s *FixedBottomSurface) reflowPromptViewportLocked() {
	if s == nil || s.terminal == nil || s.promptReservedRows < 1 {
		return
	}
	width := s.terminal.Width()
	if width <= 0 {
		width = 80
	}
	totalRows := interactiveInputDisplayRows(
		[]rune(s.promptInput),
		terminalVisibleWidth(s.promptLine),
		width,
	)
	cursorRow := s.promptViewportStart + s.promptCursorRow
	s.setPromptStateLocked(s.promptLine, s.promptInput, totalRows, cursorRow, s.promptCursorCol)
}

func (s *FixedBottomSurface) BeginOutput() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.moveToOutputLocked()
	})
}

func (s *FixedBottomSurface) PromptCursorPrefix(rowOffset, col int) (string, bool) {
	if s == nil || s.terminal == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return "", false
	}
	var builder strings.Builder
	s.appendApplyLayoutSequenceLocked(&builder)
	row, column, ok := s.promptCursorPositionLocked(rowOffset, col)
	if !ok {
		return "", false
	}
	builder.WriteString(terminalMoveToSequence(row, column))
	return builder.String(), true
}

// WritePromptEditorText resolves the prompt cursor and writes the editor ANSI
// sequence while holding both surface and terminal write locks. This prevents
// asynchronous status or popup updates from invalidating an absolute cursor
// prefix between its calculation and use.
func (s *FixedBottomSurface) WritePromptEditorText(writer io.Writer, rowOffset, col int, editorText string) bool {
	if s == nil || s.terminal == nil || writer == nil || editorText == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	handled := false
	WithTerminalWriteLock(func() {
		var layout strings.Builder
		s.appendApplyLayoutSequenceLocked(&layout)
		row, column, ok := s.promptCursorPositionLocked(rowOffset, col)
		if !ok {
			return
		}
		var builder strings.Builder
		builder.WriteString(cursorHideSequence)
		builder.WriteString(layout.String())
		builder.WriteString(terminalMoveToSequence(row, column))
		builder.WriteString(editorText)
		builder.WriteString(cursorShowSequence)
		_, _ = io.WriteString(writer, builder.String())
		handled = true
	})
	return handled
}

// WriteOutput moves the real terminal cursor into the scrollable output region
// and writes text while holding the terminal write lock. This keeps output
// writers from racing with the line editor's prompt cursor restoration.
//
// Plain output (tool results, notices, system writers) never opens a soft
// rewrite window: any existing soft tail is invalidated so foreign text cannot
// be mistaken for assistant-owned reflowable rows. Soft-committed assistant
// drain must use WriteSoftTrackedOutput instead.
func (s *FixedBottomSurface) WriteOutput(writer io.Writer, text string) (int, error, bool) {
	return s.writeOutput(writer, text, false)
}

// WriteSoftTrackedOutput is the assistant soft-commit path: identical cursor
// and layout handling to WriteOutput, but each written row is recorded into
// the soft rewrite tail so resize/reflow can replace it in place.
func (s *FixedBottomSurface) WriteSoftTrackedOutput(writer io.Writer, text string) (int, error, bool) {
	return s.writeOutput(writer, text, true)
}

func (s *FixedBottomSurface) writeOutput(writer io.Writer, text string, trackSoft bool) (int, error, bool) {
	if s == nil || s.terminal == nil || writer == nil || text == "" {
		return 0, nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return 0, nil, false
	}
	var n int
	var err error
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.flushPendingOutputScrollDownLocked()
		s.moveToOutputLocked()
		n, err = io.WriteString(writer, normalizeFixedSurfaceOutputText(text))
		if n > 0 {
			if trackSoft {
				s.noteSoftOutputLocked(text)
			} else {
				// Foreign/plain writes break 1:1 soft ownership at the surface
				// boundary so callers cannot forget a second invalidate.
				s.invalidateSoftOutputLocked()
			}
			s.markOutputWrittenLocked()
			// Trailing newline parks the cursor on a blank row at the output
			// bottom. Later bottom-reserve growth must absorb that blank or it
			// becomes a visible hole above the active band / prompt.
			normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
			s.outputCursorOnBlankRow = strings.HasSuffix(normalized, "\n")
		}
		s.restoreStoredPromptCursorLocked()
	})
	return n, err, true
}

// SoftOutputTailValid reports whether the surface still owns a rewriteable
// committed tail at the bottom of the output region.
func (s *FixedBottomSurface) SoftOutputTailValid() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled && s.softOutputValid && len(s.softOutputLines) > 0
}

// SoftOutputTailTrimmed is true when the soft window dropped older lines. The
// remaining tail no longer maps 1:1 to a contiguous source range from the
// start of the turn's committed soft region until the coordinator re-bases
// ownership onto the retained suffix (see AdoptSoftOutputTail).
func (s *FixedBottomSurface) SoftOutputTailTrimmed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.softOutputTrimmed
}

// SoftOutputTailLineCount returns the number of rewriteable committed lines.
func (s *FixedBottomSurface) SoftOutputTailLineCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.softOutputValid {
		return 0
	}
	return len(s.softOutputLines)
}

// SoftOutputTailLines returns a copy of the rewriteable committed tail.
func (s *FixedBottomSurface) SoftOutputTailLines() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.softOutputValid || len(s.softOutputLines) == 0 {
		return nil
	}
	return append([]string(nil), s.softOutputLines...)
}

// InvalidateSoftOutputTail drops the rewrite window. Irreversible scrollback
// already contains the bytes; only future commits can form a new soft tail.
func (s *FixedBottomSurface) InvalidateSoftOutputTail() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateSoftOutputLocked()
}

// AdoptSoftOutputTail replaces soft-tail bookkeeping without rewriting the
// terminal. The coordinator calls this after trimming source-backed ownership
// so the surface window stays 1:1 with the still-reflowable suffix. Older rows
// remain in irreversible scrollback; only the rewrite window shrinks.
func (s *FixedBottomSurface) AdoptSoftOutputTail(lines []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	if len(lines) == 0 {
		s.invalidateSoftOutputLocked()
		return
	}
	s.softOutputLines = append([]string(nil), lines...)
	if len(s.softOutputLines) > SoftOutputTailMaxLines {
		drop := len(s.softOutputLines) - SoftOutputTailMaxLines
		s.softOutputLines = append([]string(nil), s.softOutputLines[drop:]...)
	}
	s.softOutputValid = true
	// Rebased window is complete relative to the adopted ownership.
	s.softOutputTrimmed = false
}

// RewriteSoftOutputTail replaces the soft committed tail in place from source
// reflow. Growing the tail scrolls within the output region; shrinking clears
// leftover rows. Returns false when the soft window is missing or has scrolled
// out of the visible output region.
func (s *FixedBottomSurface) RewriteSoftOutputTail(writer io.Writer, newLines []string) bool {
	if s == nil || s.terminal == nil || writer == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || !s.softOutputValid || len(s.softOutputLines) == 0 {
		return false
	}
	oldCount := len(s.softOutputLines)
	onBlank := s.outputCursorOnBlankRow
	// Capture pre-layout geometry so we clear the rows the soft tail currently
	// occupies even when this rewrite is triggered by a terminal resize.
	prevHeight := s.lastHeight
	prevBottomRows := s.lastBottomRows
	if prevHeight <= 0 {
		prevHeight = s.terminal.Height()
	}
	if prevBottomRows <= 0 {
		prevBottomRows = s.effectiveBottomRowsLocked(prevHeight)
	}
	prevBottom := outputBottomRowForHeight(prevHeight, prevBottomRows)
	prevStart := prevBottom - oldCount
	if !onBlank {
		prevStart = prevBottom - oldCount + 1
	}
	if newLines == nil {
		newLines = []string{}
	}
	normalized := make([]string, len(newLines))
	for i, line := range newLines {
		normalized[i] = strings.TrimSuffix(strings.ReplaceAll(line, "\r", ""), "\n")
	}
	var rewritten bool
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.flushPendingOutputScrollDownLocked()
		if prevStart < 1 {
			s.invalidateSoftOutputLocked()
			rewritten = false
			return
		}
		clearEnd := prevBottom
		if clearEnd < prevStart {
			clearEnd = prevStart
		}
		for row := prevStart; row <= clearEnd; row++ {
			s.terminal.MoveTo(row, 1)
			s.terminal.ClearLine()
		}
		s.terminal.MoveTo(prevStart, 1)
		if len(normalized) == 0 {
			s.softOutputLines = nil
			s.softOutputValid = false
			s.softOutputTrimmed = false
			s.outputCursorOnBlankRow = false
			s.restoreStoredPromptCursorLocked()
			rewritten = true
			return
		}
		var builder strings.Builder
		for i, line := range normalized {
			if i > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(line)
		}
		// Match WriteOutput / writeLineLocked: always terminate so the output
		// cursor parks on a blank row at the region bottom.
		builder.WriteString("\n")
		if _, err := io.WriteString(writer, normalizeFixedSurfaceOutputText(builder.String())); err != nil {
			rewritten = false
			return
		}
		s.softOutputLines = normalized
		s.softOutputValid = true
		s.softOutputTrimmed = false
		s.outputCursorOnBlankRow = true
		s.markOutputWrittenLocked()
		s.restoreStoredPromptCursorLocked()
		rewritten = true
	})
	return rewritten
}

func (s *FixedBottomSurface) noteSoftOutputLocked(text string) {
	if s == nil {
		return
	}
	lines := splitSoftOutputLines(text)
	if len(lines) == 0 {
		return
	}
	s.softOutputLines = append(s.softOutputLines, lines...)
	if len(s.softOutputLines) > SoftOutputTailMaxLines {
		drop := len(s.softOutputLines) - SoftOutputTailMaxLines
		s.softOutputLines = append([]string(nil), s.softOutputLines[drop:]...)
		s.softOutputTrimmed = true
	}
	s.softOutputValid = true
}

func (s *FixedBottomSurface) invalidateSoftOutputLocked() {
	if s == nil {
		return
	}
	s.softOutputLines = nil
	s.softOutputValid = false
	s.softOutputTrimmed = false
}

func splitSoftOutputLines(text string) []string {
	if text == "" {
		return nil
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	// writeLineLocked always appends a trailing newline. Keep intentional blank
	// lines, but do not treat the terminator itself as an extra soft row.
	trimmed := strings.HasSuffix(normalized, "\n")
	if trimmed {
		normalized = strings.TrimSuffix(normalized, "\n")
	}
	if normalized == "" {
		if trimmed {
			return []string{""}
		}
		return nil
	}
	return strings.Split(normalized, "\n")
}

func (s *FixedBottomSurface) ShowPrompt(line string) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	s.promptLine = line
	s.promptInput = ""
	s.promptReservedRows = 1
	s.promptViewportStart = 0
	s.setPromptCursorToLineEndLocked(line)
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.restoreStoredPromptCursorLocked()
	})
	return true
}

func (s *FixedBottomSurface) ResetPrompt(line string, rows int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	if rows < 1 {
		rows = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.clearPromptRowsLocked(rows)
		s.promptLine = line
		s.promptInput = ""
		s.promptReservedRows = 1
		s.promptViewportStart = 0
		s.setPromptCursorToLineEndLocked(line)
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.restoreStoredPromptCursorLocked()
	})
	return true
}

func (s *FixedBottomSurface) SetPromptRows(rows int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	if rows < 1 {
		rows = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	if s.promptReservedRows == rows {
		return true
	}
	restorePromptCursor := s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		if s.popupRenderedRows > 0 {
			s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
			s.clearPopupRenderStateLocked()
		}
		s.promptReservedRows = rows
		s.promptViewportStart = 0
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.flushPendingOutputScrollDownLocked()
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
	})
	return true
}

func (s *FixedBottomSurface) SetPromptNoticeLine(line string) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.promptNoticeLine == line {
		return true
	}
	s.promptNoticeLine = line
	s.reflowPromptViewportLocked()
	if !s.enabled {
		return false
	}
	restorePromptCursor := s.bottomPaneStateLocked().promptVisibleRowCount() > 0
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		} else {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
	})
	return true
}

// SetActiveBand updates the in-progress stream viewport above the prompt/status.
// Lines are sanitized and capped to the adaptive row budget. Empty clears it.
// This never writes into scrollback; callers commit final content separately.
func (s *FixedBottomSurface) SetActiveBand(lines []string) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	normalized := normalizeActiveBandLines(lines, s.terminal.Width(), s.ActiveBandRowBudget())
	return s.setActiveBand(normalized, nil)
}

// SetActiveBandStyled updates the active viewport from structured lines.
// Text is sanitized and width-limited before storage; only semantic styles
// generated by the application reach the terminal rendering adapter.
func (s *FixedBottomSurface) SetActiveBandStyled(lines []render.Line) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	styled := normalizeActiveBandStyledLines(lines, s.terminal.Width(), s.ActiveBandRowBudget())
	plain := render.PlainBackend{}.RenderLines(render.LinesDoc(styled...))
	return s.setActiveBand(plain, styled)
}

// ActiveBandRowBudget reports how many rows the stream viewport may use for the
// current terminal size. It falls back to the historical minimum when the
// terminal height is unknown.
func (s *FixedBottomSurface) ActiveBandRowBudget() int {
	if s == nil || s.terminal == nil {
		return ActiveBandMinRows
	}
	return ActiveBandRows(s.terminal.Height())
}

// ActiveBandViewportSize reports the cached terminal width and the adaptive row
// budget for the in-progress stream viewport. Producers use it to keep their
// frame buffer sized to the surface without extra terminal syscalls.
func (s *FixedBottomSurface) ActiveBandViewportSize() (width, rows int) {
	if s == nil || s.terminal == nil {
		return 0, ActiveBandMinRows
	}
	return s.terminal.Width(), ActiveBandRows(s.terminal.Height())
}

func (s *FixedBottomSurface) setActiveBand(normalized []string, styled []render.Line) bool {
	if len(normalized) == 0 {
		return s.clearActiveBand()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if activeBandLinesEqual(s.activeBandLines, normalized) && render.LinesEqual(s.activeBandStyled, styled) {
		return s.enabled
	}
	previousLines := s.activeBandLines
	previousStyled := s.activeBandStyled
	previousRows := s.bottomPaneStateLocked().activeBandVisibleRowCount()
	previousStart := s.promptRenderedStartRow
	s.activeBandLines = normalized
	s.activeBandStyled = cloneRenderLines(styled)
	s.reflowPromptViewportLocked()
	currentRows := s.bottomPaneStateLocked().activeBandVisibleRowCount()
	if previousRows == currentRows {
		if currentRows == 0 {
			return s.enabled
		}
		if previousStart > 0 {
			return s.repaintActiveBandDiffLocked(previousStart, previousLines, previousStyled)
		}
	}
	return s.repaintActiveBandLocked()
}

func (s *FixedBottomSurface) repaintActiveBandDiffLocked(start int, previousLines []string, previousStyled []render.Line) bool {
	if !s.enabled || start < 1 {
		return false
	}
	restorePromptCursor := s.bottomPaneStateLocked().promptVisibleRowCount() > 0
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		} else {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		themeContext := s.activeBandThemeContextLocked()
		for i, plain := range s.activeBandLines {
			if activeBandRowEqual(previousLines, previousStyled, s.activeBandLines, s.activeBandStyled, i) {
				continue
			}
			s.renderActiveBandRowLocked(start+i, i, plain, themeContext)
		}
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		} else {
			s.moveToOutputLocked()
		}
	})
	return true
}

func activeBandRowEqual(previousLines []string, previousStyled []render.Line, currentLines []string, currentStyled []render.Line, index int) bool {
	if index >= len(previousLines) || index >= len(currentLines) || previousLines[index] != currentLines[index] {
		return false
	}
	previousHasStyle := index < len(previousStyled)
	currentHasStyle := index < len(currentStyled)
	if previousHasStyle != currentHasStyle {
		return false
	}
	if !previousHasStyle {
		return true
	}
	return render.LinesEqual(previousStyled[index:index+1], currentStyled[index:index+1])
}

func (s *FixedBottomSurface) renderActiveBandRowLocked(row, index int, plain string, themeContext style.ThemeContext) {
	if row < 1 {
		return
	}
	s.terminal.MoveTo(row, 1)
	s.terminal.ClearLine()
	if index < len(s.activeBandStyled) {
		line := s.activeBandStyled[index]
		if render.LineWidth(line) > s.terminal.Width() {
			line = render.Truncate(line, s.terminal.Width(), "…")
		}
		fmt.Print(style.RenderDocument(render.LinesDoc(line), themeContext))
		return
	}
	plain = truncateFixedPopupLine(plain, s.terminal.Width())
	if plain != "" {
		fmt.Print(style.RenderDocument(render.SingleLineDoc(render.Span{
			Text:  plain,
			Style: render.Style{Role: string(style.RoleInfo)},
		}), themeContext))
	}
}

// RefreshActiveBand repaints the stored frame after a theme or terminal
// capability change, even when its structured content is unchanged.
func (s *FixedBottomSurface) RefreshActiveBand() bool {
	if s == nil || s.terminal == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repaintActiveBandLocked()
}

func (s *FixedBottomSurface) repaintActiveBandLocked() bool {
	if !s.enabled {
		return false
	}
	restorePromptCursor := s.bottomPaneStateLocked().promptVisibleRowCount() > 0
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		} else {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		// Freed band rows are already cleared here, so a deferred shrink
		// compensation can safely pull the transcript down to the prompt.
		s.flushPendingOutputScrollDownLocked()
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		} else {
			s.moveToOutputLocked()
		}
	})
	return true
}

// ClearActiveBand removes the in-progress stream viewport.
func (s *FixedBottomSurface) ClearActiveBand() bool {
	return s.clearActiveBand()
}

// clearActiveBand releases the active viewport as one terminal-space
// transaction. The old pixels must be erased before the enlarged output region
// is scrolled down; repainting the old coordinates afterwards could erase the
// transcript that was just moved into those rows.
func (s *FixedBottomSurface) clearActiveBand() bool {
	if s == nil || s.terminal == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.activeBandLines) == 0 && len(s.activeBandStyled) == 0 {
		return s.enabled
	}

	oldStart := s.promptRenderedStartRow
	oldRows := s.promptRenderedRows
	s.activeBandLines = nil
	s.activeBandStyled = nil
	s.reflowPromptViewportLocked()
	if !s.enabled {
		return false
	}

	restorePromptCursor := s.bottomPaneStateLocked().promptVisibleRowCount() > 0
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		} else {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}

		// Emit geometry contraction, stale-pixel cleanup and scroll-down as one
		// write. This avoids exposing a cleared 14-row band to a later output
		// write before the transcript has been pulled into the released space.
		var transition strings.Builder
		s.appendApplyLayoutSequenceLocked(&transition)
		appendClearRowsSequence(&transition, oldStart, oldRows)
		s.appendPendingOutputScrollDownLocked(&transition)
		if transition.Len() > 0 {
			fmt.Print(transition.String())
		}

		// The old coordinates are invalid after scroll-down. The following
		// repaint must only track rows belonging to the new bottom-pane state.
		s.promptRenderedStartRow = 0
		s.promptRenderedRows = 0
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		} else {
			s.moveToOutputLocked()
		}
	})
	return true
}

// ActiveBandLines returns a copy of the current active band.
func (s *FixedBottomSurface) ActiveBandLines() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.activeBandLines) == 0 {
		return nil
	}
	return append([]string(nil), s.activeBandLines...)
}

func normalizeActiveBandLines(lines []string, width, maxRows int) []string {
	if len(lines) == 0 {
		return nil
	}
	if maxRows <= 0 {
		maxRows = ActiveBandMinRows
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
		// Keep blank lines as spacers inside the band; drop fully empty
		// leading/trailing rows so they cannot inflate the reserved height
		// into a visible hole above the first real content line.
		if width > 0 {
			line = truncateFixedPopupLine(line, width)
		}
		out = append(out, line)
	}
	// Trim leading blank lines.
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	// Trim trailing blank lines.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return nil
	}
	if len(out) > maxRows {
		// Keep the newest tail — streaming focus is the end of the active cell.
		out = out[len(out)-maxRows:]
	}
	return out
}

func normalizeActiveBandStyledLines(lines []render.Line, width, maxRows int) []render.Line {
	if len(lines) == 0 {
		return nil
	}
	if maxRows <= 0 {
		maxRows = ActiveBandMinRows
	}
	out := make([]render.Line, 0, len(lines))
	for _, line := range lines {
		clean := line
		clean.Spans = make([]render.Span, 0, len(line.Spans))
		for _, span := range line.Spans {
			span.Text = strings.ReplaceAll(SanitizeTerminalText(span.Text), "\r", " ")
			span.Text = strings.ReplaceAll(span.Text, "\n", " ")
			span.Link = sanitizeStatusLineText(span.Link)
			if span.Text != "" || span.Link != "" {
				clean.Spans = append(clean.Spans, span)
			}
		}
		if width > 0 && render.LineWidth(clean) > width {
			clean = render.Truncate(clean, width, "…")
		}
		out = append(out, clean)
	}
	for len(out) > 0 && strings.TrimSpace((render.PlainBackend{}).Render(render.LinesDoc(out[0]))) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace((render.PlainBackend{}).Render(render.LinesDoc(out[len(out)-1]))) == "" {
		out = out[:len(out)-1]
	}
	if len(out) > maxRows {
		out = out[len(out)-maxRows:]
	}
	return out
}

func activeBandLinesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneRenderLines(lines []render.Line) []render.Line {
	if len(lines) == 0 {
		return nil
	}
	clone := make([]render.Line, len(lines))
	for i, line := range lines {
		clone[i] = line
		clone[i].Spans = append([]render.Span(nil), line.Spans...)
	}
	return clone
}

// SetPromptEditorStatusLine shows compact, editor-owned context above a
// multiline draft. It is kept separate from runtime notices so queue and
// approval feedback cannot be overwritten by cursor movement.
func (s *FixedBottomSurface) SetPromptEditorStatusLine(line string) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.promptEditorStatusLine == line {
		return true
	}
	s.promptEditorStatusLine = line
	s.reflowPromptViewportLocked()
	if !s.enabled {
		return false
	}
	WithTerminalWriteLock(func() {
		s.terminal.HideCursor()
		defer s.terminal.ShowCursor()
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.restoreStoredPromptCursorLocked()
	})
	return true
}

func normalizeFixedPromptInputState(line string, input string, rows int, cursorRow int, cursorCol int) (string, string, int, int, int) {
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	input = SanitizeTerminalText(input)
	if rows < 1 {
		rows = 1
	}
	if cursorRow < 0 {
		cursorRow = 0
	}
	if cursorCol < 0 {
		cursorCol = 0
	}
	return line, input, rows, cursorRow, cursorCol
}

func (s *FixedBottomSurface) TrackPromptInputState(line string, input string, rows int, cursorRow int, cursorCol int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line, input, rows, cursorRow, cursorCol = normalizeFixedPromptInputState(line, input, rows, cursorRow, cursorCol)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	previousRows := s.promptReservedRows
	previousViewportStart := s.promptViewportStart
	s.setPromptStateLocked(line, input, rows, cursorRow, cursorCol)
	needsRender := previousRows != s.promptReservedRows || previousViewportStart != s.promptViewportStart
	if needsRender {
		WithTerminalWriteLock(func() {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
			s.applyLayoutLocked()
			s.renderPopupLocked()
			s.renderStatusLocked()
			s.renderPromptRowsLocked(true)
			s.restoreStoredPromptCursorLocked()
		})
	}
	return true
}

func (s *FixedBottomSurface) SetPromptInputState(line string, input string, rows int, cursorRow int, cursorCol int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line, input, rows, cursorRow, cursorCol = normalizeFixedPromptInputState(line, input, rows, cursorRow, cursorCol)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	s.setPromptStateLocked(line, input, rows, cursorRow, cursorCol)
	WithTerminalWriteLock(func() {
		s.terminal.HideCursor()
		defer s.terminal.ShowCursor()
		if s.popupRenderedRows > 0 && !s.bottomPaneStateLocked().popupExpandsBelowPrompt() {
			s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
			s.clearPopupRenderStateLocked()
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.restoreStoredPromptCursorLocked()
	})
	return true
}

func (s *FixedBottomSurface) SetPromptCursor(rowOffset, col int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	if rowOffset < 0 {
		rowOffset = 0
	}
	if col < 0 {
		col = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	if _, _, ok := s.promptCursorPositionLocked(rowOffset, col); !ok {
		return false
	}
	s.promptCursorRow = rowOffset
	s.promptCursorCol = col
	return true
}

func (s *FixedBottomSurface) MoveToPromptCursor(rowOffset, col int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	if rowOffset < 0 {
		rowOffset = 0
	}
	if col < 0 {
		col = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	row, column, ok := s.promptCursorPositionLocked(rowOffset, col)
	if !ok {
		return false
	}
	s.promptCursorRow = rowOffset
	s.promptCursorCol = col
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.terminal.MoveTo(row, column)
	})
	return true
}

// ClearPromptRows clears the currently visible interactive prompt rows without
// relying on cursor-relative movement inside the active scroll region.
func (s *FixedBottomSurface) ClearPromptRows(rows int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	if rows < 1 {
		rows = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.clearPromptRowsLocked(rows)
		s.promptNoticeLine = ""
		s.promptEditorStatusLine = ""
		s.promptLine = ""
		s.promptInput = ""
		s.promptReservedRows = 0
		s.promptViewportStart = 0
		s.promptCursorRow = 0
		s.promptCursorCol = 0
		s.promptRenderedStartRow = 0
		s.promptRenderedRows = 0
		s.applyLayoutLocked()
		s.renderPromptRowsLocked(true)
		s.moveToOutputLocked()
	})
	return true
}

func (s *FixedBottomSurface) ShowPopup(lines []string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), "", "", false)
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.moveToOutputLocked()
	})
}

func (s *FixedBottomSurface) ShowPopupPreserveCursor(lines []string) {
	s.ShowPopupPreserveCursorForOwner(lines, "")
}

func (s *FixedBottomSurface) ShowPopupPreserveCursorForOwner(lines []string, owner string) {
	s.showPopupPreserveCursorForOwner(lines, owner, false)
}

func (s *FixedBottomSurface) ShowPopupPreserveCursorForOwnerBelowPrompt(lines []string, owner string) {
	s.showPopupPreserveCursorForOwner(lines, owner, true)
}

func (s *FixedBottomSurface) showPopupPreserveCursorForOwner(lines []string, owner string, belowPrompt bool) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), strings.TrimSpace(owner), "", belowPrompt) {
		return
	}
	if !s.enabled {
		return
	}
	restorePromptCursor := belowPrompt || s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
	})
}

func (s *FixedBottomSurface) ShowPopupInput(lines []string, prompt string) {
	s.showPopupInputForOwner(lines, prompt, "", false)
}

func (s *FixedBottomSurface) ShowPopupInputForOwner(lines []string, prompt string, owner string) {
	s.showPopupInputForOwner(lines, prompt, owner, false)
}

func (s *FixedBottomSurface) ShowPopupInputPreserveCursorForOwner(lines []string, prompt string, owner string) {
	s.showPopupInputForOwner(lines, prompt, owner, true)
}

func (s *FixedBottomSurface) BeginPopupInputForOwner(lines []string, prompt string, owner string) PopupHandle {
	return s.beginPopupInputForOwner(lines, prompt, owner, nil)
}

func (s *FixedBottomSurface) BeginPopupInputForOwnerWithViewport(lines []string, prompt string, owner string, viewport PopupViewportSpec) PopupHandle {
	return s.beginPopupInputForOwner(lines, prompt, owner, &viewport)
}

func (s *FixedBottomSurface) beginPopupInputForOwner(lines []string, prompt string, owner string, viewport *PopupViewportSpec) PopupHandle {
	if s == nil || s.terminal == nil {
		return PopupHandle{}
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return PopupHandle{}
	}
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextPopupInstance++
	if s.nextPopupInstance == 0 {
		s.nextPopupInstance++
	}
	handle := PopupHandle{owner: owner, instance: s.nextPopupInstance}
	if !s.beginPopupInstanceLocked(cloneAndSanitizePopupLines(lines), prompt, handle, viewport) || !s.enabled {
		return handle
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.moveToPopupInputLocked()
	})
	return handle
}

func (s *FixedBottomSurface) UpdatePopupInputForHandle(handle PopupHandle, lines []string, prompt string, preserveCursor bool) bool {
	if s == nil || s.terminal == nil || !handle.Valid() {
		return false
	}
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.updatePopupInstanceLocked(handle, cloneAndSanitizePopupLines(lines), prompt)
	if !active || !s.enabled {
		return active
	}
	WithTerminalWriteLock(func() {
		if preserveCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		if !preserveCursor {
			s.moveToPopupInputLocked()
		}
	})
	return true
}

func (s *FixedBottomSurface) showPopupInputForOwner(lines []string, prompt string, owner string, preserveCursor bool) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	if !s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), strings.TrimSpace(owner), prompt, false) {
		return
	}
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		if preserveCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		if !preserveCursor {
			s.moveToPopupInputLocked()
		}
	})
}

func (s *FixedBottomSurface) ShowPopupInputPreserveCursor(lines []string, prompt string) {
	s.showPopupInputForOwner(lines, prompt, "", true)
}

func (s *FixedBottomSurface) ShowPendingPastePreview(lines int, text string) {
	if s == nil || s.terminal == nil {
		return
	}
	text = NormalizePastedText(text)
	lines = maxInt(0, lines)
	preview := buildPendingPastePreviewLines(lines, text)
	s.ShowPopupPreserveCursorForOwner(preview, "pending_paste")
}

func (s *FixedBottomSurface) ClearPendingPastePreview() {
	if s == nil || s.terminal == nil {
		return
	}
	s.ClearPopupForOwnerPreserveCursor("pending_paste")
}

func (s *FixedBottomSurface) ClearPopup() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.popupLines) == 0 && s.popupRenderedRows == 0 && strings.TrimSpace(s.composerLine) == "" {
		return
	}
	s.clearPopupStateLocked(true)
	s.clearComposerStateLocked()
	if !s.enabled {
		s.clearPopupRenderStateLocked()
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.clearPopupRenderStateLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.moveToOutputLocked()
	})
}

func (s *FixedBottomSurface) ClearPopupPreserveCursor() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.popupLines) == 0 && s.popupRenderedRows == 0 && strings.TrimSpace(s.composerLine) == "" {
		return
	}
	restorePromptCursor := s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	s.clearPopupStateLocked(true)
	s.clearComposerStateLocked()
	if !s.enabled {
		s.clearPopupRenderStateLocked()
		return
	}
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.clearPopupRenderStateLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
	})
}

func (s *FixedBottomSurface) ClearPopupForOwnerPreserveCursor(owner string) {
	if s == nil || s.terminal == nil {
		return
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.popupOwner != owner {
		s.removePopupStateFromStackLocked(owner)
		return
	}
	if len(s.popupLines) == 0 && s.popupRenderedRows == 0 && strings.TrimSpace(s.composerLine) == "" {
		return
	}
	wasBelowPrompt := s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	previousRows := s.popupRenderedRows
	previousGapRows := s.popupRenderedGapRows
	s.restorePopupStateFromStackLocked()
	if !s.enabled {
		return
	}
	restorePromptCursor := wasBelowPrompt || s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(previousRows, previousGapRows)
		s.clearPopupRenderStateLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
	})
}

func (s *FixedBottomSurface) ClearPopupHandlePreserveCursor(handle PopupHandle) {
	if s == nil || s.terminal == nil || !handle.Valid() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.popupOwner != handle.owner || s.popupInstance != handle.instance {
		s.removePopupInstanceFromStackLocked(handle)
		return
	}
	wasBelowPrompt := s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	previousRows := s.popupRenderedRows
	previousGapRows := s.popupRenderedGapRows
	s.restorePopupStateFromStackLocked()
	if !s.enabled {
		return
	}
	restorePromptCursor := wasBelowPrompt || s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		} else {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(previousRows, previousGapRows)
		s.clearPopupRenderStateLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
	})
}

// SetStatusModel updates the status row from structured semantic data.
func (s *FixedBottomSurface) SetStatusModel(model style.StatusLineModel) {
	if s == nil || s.terminal == nil {
		return
	}
	model = sanitizeStatusLineModel(model)
	line := strings.TrimSpace(style.StatusLineDocument(model, 0).PlainText())
	if line == "" {
		model = style.StatusLineModel{State: style.RunReady}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusModel = cloneStatusLineModel(&model)
	s.repaintStatusUpdateLocked()
}

// SetDynamicStatusModel sets the transient activity row rendered immediately
// above the prompt. Passing nil removes the row; the persistent diagnostics
// remain in the terminal's final status row.
func (s *FixedBottomSurface) SetDynamicStatusModel(model *style.StatusLineModel) {
	if s == nil || s.terminal == nil {
		return
	}
	var normalized *style.StatusLineModel
	if model != nil {
		value := sanitizeStatusLineModel(*model)
		if strings.TrimSpace(style.StatusLineDocument(value, 0).PlainText()) != "" {
			normalized = cloneStatusLineModel(&value)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dynamicStatusModel = normalized
	s.reflowPromptViewportLocked()
	s.repaintStatusUpdateLocked()
}

// SetStatusModels updates the persistent footer and transient activity row in
// one paint, avoiding a visible intermediate frame during state transitions.
func (s *FixedBottomSurface) SetStatusModels(status style.StatusLineModel, dynamic *style.StatusLineModel) {
	if s == nil || s.terminal == nil {
		return
	}
	status = sanitizeStatusLineModel(status)
	if strings.TrimSpace(style.StatusLineDocument(status, 0).PlainText()) == "" {
		status = style.StatusLineModel{State: style.RunReady}
	}
	var normalizedDynamic *style.StatusLineModel
	if dynamic != nil {
		value := sanitizeStatusLineModel(*dynamic)
		if strings.TrimSpace(style.StatusLineDocument(value, 0).PlainText()) != "" {
			normalizedDynamic = cloneStatusLineModel(&value)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusModel = cloneStatusLineModel(&status)
	s.dynamicStatusModel = normalizedDynamic
	s.reflowPromptViewportLocked()
	s.repaintStatusUpdateLocked()
}

func (s *FixedBottomSurface) repaintStatusUpdateLocked() {
	if !s.enabled {
		return
	}
	restorePromptCursor := s.bottomPaneStateLocked().popupExpandsBelowPrompt()
	WithTerminalWriteLock(func() {
		if restorePromptCursor {
			s.terminal.HideCursor()
			defer s.terminal.ShowCursor()
		}
		if !restorePromptCursor {
			s.terminal.SaveCursor()
			defer s.terminal.RestoreCursor()
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		// Removing the dynamic status row grows the output region just like
		// releasing ActiveBand. Flush only after its stale pixels are cleared,
		// otherwise the newly moved transcript row can be erased again.
		s.flushPendingOutputScrollDownLocked()
		if restorePromptCursor {
			s.restoreStoredPromptCursorLocked()
		}
	})
}

func sanitizeStatusLineModel(model style.StatusLineModel) style.StatusLineModel {
	model.State = style.RunState(sanitizeStatusLineText(string(model.State)))
	model.StateText = sanitizeStatusLineText(model.StateText)
	if model.Separator != "" {
		model.Separator = strings.ReplaceAll(SanitizeTerminalText(model.Separator), "\r", " ")
		model.Separator = strings.ReplaceAll(model.Separator, "\n", " ")
	}
	segments := make([]style.StatusSegment, 0, len(model.Segments))
	for _, segment := range model.Segments {
		segment.Text = sanitizeStatusLineText(segment.Text)
		if segment.Text == "" {
			continue
		}
		segment.Link = sanitizeStatusLineText(segment.Link)
		segments = append(segments, segment)
	}
	model.Segments = segments
	return model
}

func sanitizeStatusLineText(text string) string {
	text = strings.ReplaceAll(SanitizeTerminalText(text), "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.TrimSpace(text)
}

func cloneStatusLineModel(model *style.StatusLineModel) *style.StatusLineModel {
	if model == nil {
		return nil
	}
	clone := *model
	clone.Segments = append([]style.StatusSegment(nil), model.Segments...)
	return &clone
}

// SetComposerPreview 在底部固定区额外保留一行 composer 预览。
// 这是一条过渡能力，用来承载 transient prompt / future composer。
func (s *FixedBottomSurface) SetComposerPreview(line string) {
	if s == nil || s.terminal == nil {
		return
	}
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.composerLine = line
	s.popupBelowPrompt = false
	s.popupReservedRows = 0
	s.clearPromptStateLocked(true)
	if !s.enabled {
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.moveToPopupInputLocked()
	})
}

// ClearComposerPreview 清理底部 composer 预览。
func (s *FixedBottomSurface) ClearComposerPreview() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.composerLine == "" && s.popupRenderedRows == 0 {
		return
	}
	s.clearComposerStateLocked()
	s.promptReservedRows = 0
	s.promptCursorRow = 0
	s.promptCursorCol = 0
	if !s.enabled {
		s.clearPopupRenderStateLocked()
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.clearPopupRenderStateLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.moveToOutputLocked()
	})
}

func (s *FixedBottomSurface) setActivePopupStateLocked(lines []string, owner string, composerLine string, belowPrompt bool) bool {
	owner = strings.TrimSpace(owner)
	reservedRows := s.popupReservedRowsForUpdateLocked(lines, owner, belowPrompt)
	if owner == "" {
		s.popupStack = nil
		s.popupLines = lines
		s.popupOwner = ""
		s.popupInstance = 0
		s.popupViewport = nil
		s.popupBelowPrompt = belowPrompt
		s.popupReservedRows = reservedRows
		s.composerLine = composerLine
		return true
	}
	if s.popupOwner == owner {
		s.popupLines = lines
		s.popupInstance = 0
		s.popupViewport = nil
		s.popupBelowPrompt = belowPrompt
		s.popupReservedRows = reservedRows
		s.composerLine = composerLine
		return true
	}
	if s.popupOwner != "" && popupOwnerPriority(owner) < popupOwnerPriority(s.popupOwner) {
		s.upsertPopupStateInStackLocked(fixedBottomPopupState{
			lines:             lines,
			owner:             owner,
			instance:          0,
			composerLine:      composerLine,
			popupBelowPrompt:  belowPrompt,
			popupReservedRows: reservedRows,
		})
		return false
	}
	if s.popupOwner != "" || len(s.popupLines) > 0 || strings.TrimSpace(s.composerLine) != "" {
		s.upsertPopupStateInStackLocked(fixedBottomPopupState{
			lines:             append([]string(nil), s.popupLines...),
			owner:             s.popupOwner,
			instance:          s.popupInstance,
			viewport:          clonePopupViewportSpec(s.popupViewport),
			composerLine:      s.composerLine,
			popupBelowPrompt:  s.popupBelowPrompt,
			popupReservedRows: s.popupReservedRows,
		})
	}
	s.removePopupStateFromStackLocked(owner)
	s.popupLines = lines
	s.popupOwner = owner
	s.popupInstance = 0
	s.popupViewport = nil
	s.popupBelowPrompt = belowPrompt
	s.popupReservedRows = reservedRows
	s.composerLine = composerLine
	return true
}

func (s *FixedBottomSurface) beginPopupInstanceLocked(
	lines []string,
	composerLine string,
	handle PopupHandle,
	viewport *PopupViewportSpec,
) bool {
	state := fixedBottomPopupState{
		lines:        append([]string(nil), lines...),
		owner:        handle.owner,
		instance:     handle.instance,
		viewport:     clonePopupViewportSpec(viewport),
		composerLine: composerLine,
	}
	if s.popupOwner != "" && popupOwnerPriority(handle.owner) < popupOwnerPriority(s.popupOwner) {
		s.popupStack = append(s.popupStack, state)
		return false
	}
	if s.popupOwner != "" || len(s.popupLines) > 0 || strings.TrimSpace(s.composerLine) != "" {
		s.popupStack = append(s.popupStack, fixedBottomPopupState{
			lines:             append([]string(nil), s.popupLines...),
			owner:             s.popupOwner,
			instance:          s.popupInstance,
			viewport:          clonePopupViewportSpec(s.popupViewport),
			composerLine:      s.composerLine,
			popupBelowPrompt:  s.popupBelowPrompt,
			popupReservedRows: s.popupReservedRows,
		})
	}
	s.popupLines = state.lines
	s.popupOwner = state.owner
	s.popupInstance = state.instance
	s.popupViewport = clonePopupViewportSpec(state.viewport)
	s.popupBelowPrompt = false
	s.popupReservedRows = 0
	s.composerLine = state.composerLine
	return true
}

func (s *FixedBottomSurface) updatePopupInstanceLocked(handle PopupHandle, lines []string, composerLine string) bool {
	if s.popupOwner == handle.owner && s.popupInstance == handle.instance {
		s.popupLines = lines
		s.popupBelowPrompt = false
		s.popupReservedRows = 0
		s.composerLine = composerLine
		return true
	}
	for i := len(s.popupStack) - 1; i >= 0; i-- {
		if s.popupStack[i].owner == handle.owner && s.popupStack[i].instance == handle.instance {
			s.popupStack[i].lines = append([]string(nil), lines...)
			s.popupStack[i].popupBelowPrompt = false
			s.popupStack[i].popupReservedRows = 0
			s.popupStack[i].composerLine = composerLine
			return false
		}
	}
	return false
}

func (s *FixedBottomSurface) popupReservedRowsForUpdateLocked(lines []string, owner string, belowPrompt bool) int {
	if !belowPrompt || len(lines) == 0 {
		return 0
	}
	rows := len(lines)
	if s.popupBelowPrompt && s.popupOwner == owner && s.popupReservedRows > rows {
		rows = s.popupReservedRows
	}
	if maxRows := maxBottomPanePopupRows(s.terminal.Height(), s.bottomPaneStateLocked().promptReservedRowCount(), 0); maxRows > 0 && rows > maxRows {
		rows = maxRows
	}
	return rows
}

func (s *FixedBottomSurface) upsertPopupStateInStackLocked(state fixedBottomPopupState) {
	state.owner = strings.TrimSpace(state.owner)
	if state.owner == "" {
		return
	}
	state.lines = append([]string(nil), state.lines...)
	state.viewport = clonePopupViewportSpec(state.viewport)
	for i := range s.popupStack {
		sameLegacyOwner := state.instance == 0 && s.popupStack[i].instance == 0 && s.popupStack[i].owner == state.owner
		sameInstance := state.instance != 0 && s.popupStack[i].owner == state.owner && s.popupStack[i].instance == state.instance
		if sameLegacyOwner || sameInstance {
			s.popupStack[i] = state
			return
		}
	}
	s.popupStack = append(s.popupStack, state)
}

func (s *FixedBottomSurface) removePopupStateFromStackLocked(owner string) {
	owner = strings.TrimSpace(owner)
	if owner == "" || len(s.popupStack) == 0 {
		return
	}
	filtered := s.popupStack[:0]
	for _, state := range s.popupStack {
		if state.owner == owner {
			continue
		}
		filtered = append(filtered, state)
	}
	s.popupStack = filtered
}

func (s *FixedBottomSurface) removePopupInstanceFromStackLocked(handle PopupHandle) {
	if !handle.Valid() || len(s.popupStack) == 0 {
		return
	}
	filtered := s.popupStack[:0]
	for _, state := range s.popupStack {
		if state.owner == handle.owner && state.instance == handle.instance {
			continue
		}
		filtered = append(filtered, state)
	}
	s.popupStack = filtered
}

func (s *FixedBottomSurface) restorePopupStateFromStackLocked() {
	for len(s.popupStack) > 0 {
		last := s.popupStack[len(s.popupStack)-1]
		s.popupStack = s.popupStack[:len(s.popupStack)-1]
		if last.owner == "" && len(last.lines) == 0 && strings.TrimSpace(last.composerLine) == "" {
			continue
		}
		s.popupLines = append([]string(nil), last.lines...)
		s.popupOwner = last.owner
		s.popupInstance = last.instance
		s.popupViewport = clonePopupViewportSpec(last.viewport)
		s.popupBelowPrompt = last.popupBelowPrompt
		s.popupReservedRows = last.popupReservedRows
		s.composerLine = last.composerLine
		return
	}
	s.clearPopupStateLocked(false)
	s.clearComposerStateLocked()
}

func popupOwnerPriority(owner string) int {
	owner = strings.TrimSpace(owner)
	switch {
	case strings.HasPrefix(owner, "modal:priority:"):
		return 300
	case strings.HasPrefix(owner, "modal:"):
		return 200
	}
	switch owner {
	case "slash_completion":
		return 100
	case "pending_paste":
		return 90
	case "":
		return 0
	default:
		return 10
	}
}

func (s *FixedBottomSurface) canEnableLocked() bool {
	caps := s.terminal.Capabilities()
	if !caps.Interactive || !caps.ANSI || !caps.ScrollRegion {
		return false
	}
	// Zellij has known DECSTBM incompatibilities; keep the safe legacy path
	// until the full viewport fallback is implemented.
	if strings.TrimSpace(caps.MultiplexerName) != "" && strings.Contains(strings.ToLower(caps.MultiplexerName), "zellij") {
		return false
	}
	_, height := s.terminal.RefreshSize()
	return height > s.bottomRowsLocked()
}

func (s *FixedBottomSurface) applyLayoutLocked() {
	var builder strings.Builder
	s.appendApplyLayoutSequenceLocked(&builder)
	if builder.Len() > 0 {
		fmt.Print(builder.String())
	}
}

// applyLayoutWithSizeLocked applies layout using a size that was already probed
// in the same lock hold (e.g. syncTerminalGeometry). Callers that have not just
// refreshed must use applyLayoutLocked so geometry stays current.
func (s *FixedBottomSurface) applyLayoutWithSizeLocked(width, height int) {
	var builder strings.Builder
	s.appendApplyLayoutSequenceWithSizeLocked(&builder, width, height)
	if builder.Len() > 0 {
		fmt.Print(builder.String())
	}
}

func (s *FixedBottomSurface) appendApplyLayoutSequenceLocked(builder *strings.Builder) {
	if builder == nil {
		return
	}
	width, height := s.terminal.RefreshSize()
	s.appendApplyLayoutSequenceWithSizeLocked(builder, width, height)
}

func (s *FixedBottomSurface) appendApplyLayoutSequenceWithSizeLocked(builder *strings.Builder, width, height int) {
	if builder == nil {
		return
	}
	if width <= 0 {
		width = 80
	}
	if height <= 1 {
		return
	}
	bottomRows := s.effectiveBottomRowsLocked(height)
	lastWidth := s.lastWidth
	lastHeight := s.lastHeight
	lastBottomRows := s.lastBottomRows
	if width == lastWidth && height == lastHeight && bottomRows == lastBottomRows {
		return
	}
	sameSize := width == lastWidth && height == lastHeight
	compensatedRows := s.scrollCompensatedRows
	switch {
	case sameSize && compensatedRows > 0 && bottomRows > compensatedRows:
		// A pending shrink compensation cancels out an immediate re-growth:
		// content never moved down, so it must not be scrolled up twice.
		growth := bottomRows - compensatedRows
		if s.pendingScrollDownRows > 0 {
			canceled := s.pendingScrollDownRows
			if canceled > growth {
				canceled = growth
			}
			s.pendingScrollDownRows -= canceled
			growth -= canceled
		}
		scrollGrowth := growth
		// WriteOutput("...\n") leaves the cursor on an empty output row. The
		// first reserved row can occupy that blank directly; scrolling it up
		// would open a permanent hole between scrollback and the active band
		// (or popup) during a single reply — most visible when the band jumps
		// toward ActiveBandMaxRows on a tall terminal.
		if scrollGrowth > 0 && s.outputCursorOnBlankRow {
			scrollGrowth--
			s.outputCursorOnBlankRow = false
		}
		if scrollGrowth > 0 {
			// When a trailing blank is absorbed, scrollGrowth == growth-1 and
			// (bottomRows-scrollGrowth) is one past the true previous bottom:
			// the scroll region ends on the last content row so the blank row
			// becomes the first newly reserved row instead of a hole above it.
			// Without absorption, scrollGrowth == growth and this equals the
			// real previous bottomRows.
			appendOutputScrollUpForBottomReserveGrowthSequence(builder, height, bottomRows-scrollGrowth, bottomRows)
		}
		s.scrollCompensatedRows = bottomRows
	case sameSize && compensatedRows > 0 && bottomRows < compensatedRows:
		// Freed reserve rows (a released active band, a closed popup) would
		// otherwise stay blank between the last committed output line and the
		// prompt. The scroll itself is deferred until the stale rows above the
		// prompt have been cleared, so a repaint cannot erase moved content.
		s.pendingScrollDownRows += compensatedRows - bottomRows
		s.scrollCompensatedRows = bottomRows
		// Shrink repaints the freed rows; any prior trailing blank is gone.
		s.outputCursorOnBlankRow = false
	case !sameSize || compensatedRows <= 0:
		s.pendingScrollDownRows = 0
		s.scrollCompensatedRows = bottomRows
		s.outputCursorOnBlankRow = false
		// Soft tail stays valid across resize so source-backed reflow can
		// rewrite it in place. Callers invalidate explicitly when reflow is
		// impossible (trimmed window, missing source, or scroll-away).
	}
	s.lastWidth = width
	s.lastHeight = height
	s.lastBottomRows = bottomRows
	builder.WriteString(terminalScrollRegionSequence(1, outputBottomRowForHeight(height, bottomRows)))
}

// appendPendingOutputScrollDownLocked flushes a deferred bottom-reserve shrink
// compensation. Callers must invoke it only after the freed rows above the
// prompt have been cleared or repainted, otherwise the moved output lines would
// be erased again. BeginOutput must not flush: it only positions the cursor and
// may be followed by another transient popup instead of real output.
func (s *FixedBottomSurface) appendPendingOutputScrollDownLocked(builder *strings.Builder) {
	if s == nil || s.terminal == nil || builder == nil || s.pendingScrollDownRows < 1 {
		return
	}
	rows := s.pendingScrollDownRows
	s.pendingScrollDownRows = 0
	height := s.terminal.Height()
	if height <= 1 {
		return
	}
	appendOutputScrollDownForBottomReserveShrinkSequence(builder, height, s.effectiveBottomRowsLocked(height), rows)
}

func (s *FixedBottomSurface) flushPendingOutputScrollDownLocked() {
	if s == nil || s.pendingScrollDownRows < 1 {
		return
	}
	var builder strings.Builder
	s.appendPendingOutputScrollDownLocked(&builder)
	if builder.Len() > 0 {
		fmt.Print(builder.String())
	}
}

// markOutputWrittenLocked invalidates prior spare-row compensation only after
// bytes really reach the output region. BeginOutput is also used to position
// the cursor before transient command popups, so it must not call this method.
func (s *FixedBottomSurface) markOutputWrittenLocked() {
	if s == nil || s.terminal == nil {
		return
	}
	height := s.terminal.Height()
	if height <= 0 {
		_, height = s.terminal.RefreshSize()
	}
	s.scrollCompensatedRows = s.effectiveBottomRowsLocked(height)
}

func (s *FixedBottomSurface) renderStatusLocked() {
	if !s.enabled {
		return
	}
	state := s.bottomPaneStateLocked()
	s.terminal.MoveTo(s.statusRowLocked(), 1)
	s.terminal.ClearLine()
	width := s.terminal.Width()
	themeContext := s.activeBandThemeContextLocked()
	model := style.StatusLineModel{State: style.RunReady}
	if state.StatusModel != nil {
		model = *state.StatusModel
	}
	fmt.Print(formatFixedStatusModelWithContext(model, width, themeContext))
	s.terminal.ClearLine()
}

func (s *FixedBottomSurface) renderPopupLocked() {
	if !s.enabled {
		return
	}
	state := s.bottomPaneStateLocked()
	visibleLines := state.VisiblePopupLines(s.terminal.Height())
	composerRows := state.composerVisibleRowCount()
	gapRows := state.popupBottomGapRowCount()
	rows := len(visibleLines) + composerRows
	if rows == 0 {
		if s.popupRenderedRows > 0 {
			s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
			s.clearPopupRenderStateLocked()
		}
		return
	}
	if s.popupRenderedRows > 0 && (s.popupRenderedRows != rows || s.popupRenderedGapRows != gapRows) {
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.clearPopupRenderStateLocked()
	}
	startRow := s.popupStartRowLocked(rows, gapRows)
	if startRow < 1 {
		startRow = 1
	}
	for i, line := range visibleLines {
		row := startRow + i
		if row >= s.statusRowLocked() {
			break
		}
		s.terminal.MoveTo(row, 1)
		s.terminal.ClearLine()
		line = truncateFixedPopupLine(line, s.terminal.Width())
		if line != "" {
			fmt.Print(line)
		}
	}
	if composer := state.composerLineText(); composer != "" {
		row := startRow + len(visibleLines)
		if row < s.statusRowLocked() {
			s.terminal.MoveTo(row, 1)
			s.terminal.ClearLine()
			composer = truncateFixedPopupLine(composer, s.terminal.Width())
			if composer != "" {
				fmt.Print(composer)
			}
		}
	}
	s.popupRenderedRows = rows
	s.popupRenderedGapRows = gapRows
	s.popupRenderedStartRow = startRow
}

func (s *FixedBottomSurface) moveToOutputLocked() {
	s.terminal.MoveTo(s.outputBottomRowLocked(), 1)
}

func (s *FixedBottomSurface) moveToPromptLocked() {
	s.terminal.MoveTo(s.promptBottomRowLocked(), 1)
}

func (s *FixedBottomSurface) restoreStoredPromptCursorLocked() {
	if s.bottomPaneStateLocked().promptVisibleRowCount() < 1 {
		return
	}
	row, column, ok := s.promptCursorPositionLocked(s.promptCursorRow, s.promptCursorCol)
	if !ok {
		return
	}
	s.terminal.MoveTo(row, column)
}

func (s *FixedBottomSurface) setPromptCursorToLineEndLocked(line string) {
	width := s.terminal.Width()
	if width <= 0 {
		width = 80
	}
	row, col := fixedPromptLineEndPosition(line, width)
	s.promptCursorRow = row
	s.promptCursorCol = col
}

func (s *FixedBottomSurface) promptCursorPositionLocked(rowOffset, col int) (int, int, bool) {
	if rowOffset < 0 {
		rowOffset = 0
	}
	if col < 0 {
		col = 0
	}
	state := s.bottomPaneStateLocked()
	rows := state.promptVisibleRowCount()
	if rows < 1 {
		return 0, 0, false
	}
	if maxRows := s.promptMaxVisibleRowsLocked(); maxRows > 0 && rows > maxRows {
		rows = maxRows
	}
	if rowOffset >= rows {
		rowOffset = rows - 1
	}
	bottom := s.promptBottomRowLocked()
	start := bottom - rows + 1
	if start < 1 {
		start = 1
	}
	row := start + rowOffset
	if row > bottom {
		row = bottom
	}
	width := s.terminal.Width()
	if width > 0 && col >= width {
		col = width - 1
	}
	return row, col + 1, true
}

func (s *FixedBottomSurface) promptMaxVisibleRowsLocked() int {
	bottom := s.promptBottomRowLocked()
	outputBottom := s.outputBottomRowLocked()
	state := s.bottomPaneStateLocked()
	rows := bottom - outputBottom - state.dynamicStatusVisibleRowCount() - state.promptNoticeVisibleRowCount() - state.activeBandVisibleRowCount() - state.promptTopMarginRowCount()
	if rows < 1 {
		return 1
	}
	return rows
}

func (s *FixedBottomSurface) moveToPopupInputLocked() {
	state := s.bottomPaneStateLocked()
	visibleLines := state.VisiblePopupLines(s.terminal.Height())
	composerRows := state.composerVisibleRowCount()
	if len(visibleLines) == 0 && composerRows == 0 {
		s.moveToOutputLocked()
		return
	}
	row := s.popupStartRowLocked(len(visibleLines)+composerRows, state.popupInputGapRowCount()) + len(visibleLines) + composerRows - 1
	if row < 1 {
		row = 1
	}
	if row >= s.statusRowLocked() {
		row = s.statusRowLocked() - 1
	}
	if row < 1 {
		row = 1
	}
	line := ""
	if composer := state.composerLineText(); composer != "" {
		line = truncateFixedPopupLine(composer, s.terminal.Width())
	} else if len(visibleLines) > 0 {
		line = truncateFixedPopupLine(visibleLines[len(visibleLines)-1], s.terminal.Width())
	}
	col := DisplayWidth(line) + 1
	if col < 1 {
		col = 1
	}
	width := s.terminal.Width()
	if width > 0 && col > width {
		col = width
	}
	s.terminal.MoveTo(row, col)
}

func (s *FixedBottomSurface) outputBottomRowLocked() int {
	height := s.terminal.Height()
	bottom := height - s.effectiveBottomRowsLocked(height)
	if bottom < 1 {
		return 1
	}
	return bottom
}

func (s *FixedBottomSurface) promptBottomRowLocked() int {
	state := s.bottomPaneStateLocked()
	if state.popupExpandsBelowPrompt() {
		rows := state.promptAreaVisibleRowCount()
		if rows < 1 {
			return s.outputBottomRowLocked()
		}
		row := s.outputBottomRowLocked() + rows - state.promptBottomMarginRowCount()
		if row < 1 {
			return 1
		}
		if row >= s.statusRowLocked() {
			return s.statusRowLocked() - 1
		}
		return row
	}
	if state.composerVisibleRowCount() > 0 {
		visibleLines := state.VisiblePopupLines(s.terminal.Height())
		row := s.popupStartRowLocked(len(visibleLines)+state.composerVisibleRowCount(), state.popupInputGapRowCount()) + len(visibleLines)
		if row < 1 {
			return 1
		}
		if row >= s.statusRowLocked() {
			return s.statusRowLocked() - 1
		}
		return row
	}
	// A visible active band reserves rows in bottomRowsLocked even when the
	// prompt is hidden (streaming before the prompt returns). Anchoring the
	// stack to the output bottom in that case would paint the band inside the
	// scroll region and leave its reserved rows blank above the status line.
	if state.popupInputGapRowCount() > 0 || state.promptReservedRowCount() > 0 || state.dynamicStatusVisibleRowCount() > 0 || state.activeBandVisibleRowCount() > 0 {
		row := s.statusRowLocked() - 1 - state.promptBottomMarginRowCount()
		if row < 1 {
			return 1
		}
		return row
	}
	return s.outputBottomRowLocked()
}

func (s *FixedBottomSurface) statusRowLocked() int {
	row := s.terminal.Height()
	if row < 1 {
		return 1
	}
	return row
}

func (s *FixedBottomSurface) popupStartRowLocked(rows int, gapRows int) int {
	state := s.bottomPaneStateLocked()
	if state.popupExpandsBelowPrompt() {
		row := s.promptBottomRowLocked() + state.promptBottomMarginRowCount() + 1
		if row < 1 {
			return 1
		}
		if row >= s.statusRowLocked() {
			return s.statusRowLocked() - 1
		}
		return row
	}
	row := s.statusRowLocked() - gapRows - rows
	if row < 1 {
		return 1
	}
	return row
}

func (s *FixedBottomSurface) bottomRowsLocked() int {
	state := s.bottomPaneStateLocked()
	rows := 1 + state.popupVisibleRowCount(s.terminal.Height())
	if state.popupExpandsBelowPrompt() {
		rows += state.promptAreaVisibleRowCount()
	} else {
		rows += state.composerVisibleRowCount() + state.popupBottomGapRowCount()
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (s *FixedBottomSurface) effectiveBottomRowsLocked(height int) int {
	rows := s.bottomRowsLocked()
	if height <= 1 {
		return 1
	}
	maxRows := height - 1
	if rows > maxRows {
		return maxRows
	}
	if rows < 1 {
		return 1
	}
	return rows
}

func (s *FixedBottomSurface) popupVisibleRowCountLocked() int {
	if s == nil || s.terminal == nil {
		return 0
	}
	state := s.bottomPaneStateLocked()
	return state.popupVisibleRowCount(s.terminal.Height())
}

func (s *FixedBottomSurface) maxPopupRowsLocked() int {
	state := s.bottomPaneStateLocked()
	reservedRows := state.composerVisibleRowCount() + state.popupTopReservedRowCount()
	return maxBottomPanePopupRows(s.terminal.Height(), reservedRows, state.popupBottomGapRowCount())
}

func (s *FixedBottomSurface) popupVisibleLinesLocked() []string {
	state := s.bottomPaneStateLocked()
	return state.VisiblePopupLines(s.terminal.Height())
}

func (s *FixedBottomSurface) clearPromptRowsLocked(rows int) {
	if rows < 1 {
		rows = 1
	}
	bottom := s.promptBottomRowLocked()
	if bottom < 1 {
		return
	}
	state := s.bottomPaneStateLocked()
	capToVisiblePrompt := false
	if reservedRows := state.promptVisibleRowCount(); reservedRows > 0 {
		rows = reservedRows
		capToVisiblePrompt = true
	} else if state.popupInputGapRowCount() > 0 && rows > 1 {
		rows = 1
	}
	if capToVisiblePrompt {
		if maxRows := s.promptMaxVisibleRowsLocked(); maxRows > 0 && rows > maxRows {
			rows = maxRows
		}
	}
	rows += state.dynamicStatusVisibleRowCount() + state.promptNoticeVisibleRowCount() + state.promptTopMarginRowCount()
	startRow := bottom - rows + 1
	if startRow < 1 {
		startRow = 1
	}
	endRow := bottom + state.promptBottomMarginRowCount()
	if endRow >= s.statusRowLocked() {
		endRow = s.statusRowLocked() - 1
	}
	for row := startRow; row <= endRow; row++ {
		s.terminal.MoveTo(row, 1)
		s.terminal.ClearLine()
	}
}

func (s *FixedBottomSurface) renderPromptRowsLocked(clear bool) {
	if s == nil || s.terminal == nil || !s.enabled {
		return
	}
	state := s.bottomPaneStateLocked()
	if state.composerVisibleRowCount() > 0 {
		return
	}
	rows := state.promptVisibleRowCount()
	noticeRows := state.promptNoticeVisibleRowCount()
	dynamicRows := state.dynamicStatusVisibleRowCount()
	activeRows := state.activeBandVisibleRowCount()
	topMarginRows := state.promptTopMarginRowCount()
	bottomMarginRows := state.promptBottomMarginRowCount()
	if rows < 1 && noticeRows < 1 && dynamicRows < 1 && activeRows < 1 {
		if s.promptRenderedRows > 0 {
			s.clearRowsLocked(s.promptRenderedStartRow, s.promptRenderedRows)
			s.promptRenderedStartRow = 0
			s.promptRenderedRows = 0
		}
		return
	}
	if rows > 0 {
		if maxRows := s.promptMaxVisibleRowsLocked(); maxRows > 0 && rows > maxRows {
			rows = maxRows
		}
	}
	bottom := s.promptBottomRowLocked()
	if bottom < 1 {
		bottom = s.statusRowLocked() - 1
	}
	if bottom < 1 {
		return
	}
	// Layout bottom-up: [active band][notice][dynamic status][top margin]
	// [prompt][bottom margin][status]. Margins are reserved only for the main
	// chat composer, so transient popups and prompt-less streaming stay dense.
	promptStart := bottom + bottomMarginRows + 1
	if rows > 0 {
		promptStart = bottom - rows + 1
	}
	topMarginStart := promptStart - topMarginRows
	dynamicStart := topMarginStart - dynamicRows
	noticeStart := dynamicStart - noticeRows
	activeStart := noticeStart - activeRows
	start := activeStart
	if start < 1 {
		start = 1
	}
	areaRows := activeRows + noticeRows + dynamicRows + topMarginRows + rows + bottomMarginRows
	if s.promptRenderedStartRow > 0 && (s.promptRenderedStartRow != start || s.promptRenderedRows != areaRows) {
		s.clearRowsLocked(s.promptRenderedStartRow, s.promptRenderedRows)
	}
	if clear {
		s.clearRowsLocked(start, areaRows)
	}
	activeTheme := s.activeBandThemeContextLocked()
	if activeRows > 0 {
		// After a shrink the stored band can exceed the current budget; keep the
		// newest tail so streaming focus stays on the end of the active cell.
		band := state.ActiveBandLines
		styled := state.ActiveBandStyled
		if len(band) > activeRows {
			band = band[len(band)-activeRows:]
		}
		if len(styled) > activeRows {
			styled = styled[len(styled)-activeRows:]
		}
		for i := 0; i < activeRows && i < len(band); i++ {
			row := activeStart + i
			if row < 1 {
				continue
			}
			s.terminal.MoveTo(row, 1)
			if i < len(styled) {
				line := styled[i]
				if render.LineWidth(line) > s.terminal.Width() {
					line = render.Truncate(line, s.terminal.Width(), "…")
				}
				fmt.Print(style.RenderDocument(render.LinesDoc(line), activeTheme))
				continue
			}
			plain := truncateFixedPopupLine(band[i], s.terminal.Width())
			if plain != "" {
				fmt.Print(style.RenderDocument(render.SingleLineDoc(render.Span{
					Text:  plain,
					Style: render.Style{Role: string(style.RoleInfo)},
				}), activeTheme))
			}
		}
	}
	if noticeRows > 0 {
		noticeLines := state.promptNoticeLines()
		for i := 0; i < noticeRows && i < len(noticeLines); i++ {
			row := noticeStart + i
			if row < 1 {
				continue
			}
			s.terminal.MoveTo(row, 1)
			notice := truncateFixedPopupLine(noticeLines[i], s.terminal.Width())
			if notice != "" {
				fmt.Print(style.RenderDocument(render.SingleLineDoc(render.Span{
					Text:  notice,
					Style: render.Style{Role: string(style.RoleTextMuted)},
				}), activeTheme))
			}
		}
	}
	if dynamicRows > 0 && state.DynamicStatusModel != nil {
		s.terminal.MoveTo(dynamicStart, 1)
		model := *state.DynamicStatusModel
		fmt.Print(formatFixedStatusModelWithContext(model, s.terminal.Width(), activeTheme))
	}
	if rows > 0 {
		s.terminal.MoveTo(promptStart, 1)
		viewportText := renderInteractiveInputViewport(
			s.promptLine,
			[]rune(s.promptInput),
			s.terminal.Width(),
			s.promptViewportStart,
			rows,
		)
		if viewportText != "" {
			fmt.Print(viewportText)
		}
	}
	s.promptRenderedStartRow = start
	s.promptRenderedRows = areaRows
}

func (s *FixedBottomSurface) activeBandThemeContextLocked() style.ThemeContext {
	var profile style.ColorProfile
	if s.terminal != nil && s.terminal.driver != nil {
		profile = s.terminal.driver.ColorProfile()
	} else {
		profile = CurrentColorProfile()
	}
	return ThemeContextForProfile(profile)
}

func (s *FixedBottomSurface) clearRowsLocked(startRow int, rows int) {
	if rows < 1 {
		return
	}
	if startRow < 1 {
		startRow = 1
	}
	statusRow := s.statusRowLocked()
	endRow := startRow + rows - 1
	if endRow >= statusRow {
		endRow = statusRow - 1
	}
	for row := startRow; row <= endRow; row++ {
		s.terminal.MoveTo(row, 1)
		s.terminal.ClearLine()
	}
}

func (s *FixedBottomSurface) clearPopupAreaLocked(rows int, gapRows int) {
	if rows < 1 {
		return
	}
	if s.popupRenderedStartRow > 0 {
		s.clearRowsLocked(s.popupRenderedStartRow, rows)
		return
	}
	endRow := s.statusRowLocked() - gapRows
	if endRow < 1 {
		return
	}
	startRow := endRow - rows
	if startRow < 1 {
		startRow = 1
	}
	for row := startRow; row < endRow; row++ {
		s.terminal.MoveTo(row, 1)
		s.terminal.ClearLine()
	}
}

type BottomPaneState struct {
	StatusModel            *style.StatusLineModel
	DynamicStatusModel     *style.StatusLineModel
	PopupLines             []string
	PopupOwner             string
	PopupBelowPrompt       bool
	PopupReservedRows      int
	PopupViewport          *PopupViewportSpec
	ComposerLine           string
	PromptNoticeLine       string
	PromptEditorStatusLine string
	ActiveBandLines        []string
	ActiveBandStyled       []render.Line
	ActiveBandMaxRows      int
	PromptReservedRows     int
	PromptTopMarginRows    int
	PromptBottomMarginRows int
}

func (s BottomPaneState) composerLineText() string {
	return strings.TrimSpace(s.ComposerLine)
}

func (s BottomPaneState) composerVisibleRowCount() int {
	if strings.TrimSpace(s.ComposerLine) == "" {
		return 0
	}
	return 1
}

func (s BottomPaneState) promptNoticeVisibleRowCount() int {
	if s.composerVisibleRowCount() > 0 || s.promptReservedRowCount() < 1 {
		return 0
	}
	return len(s.promptNoticeLines())
}

func (s BottomPaneState) dynamicStatusVisibleRowCount() int {
	if s.composerVisibleRowCount() > 0 || s.DynamicStatusModel == nil {
		return 0
	}
	if strings.TrimSpace(style.StatusLineDocument(*s.DynamicStatusModel, 0).PlainText()) == "" {
		return 0
	}
	return 1
}

func (s BottomPaneState) promptNoticeLines() []string {
	lines := promptNoticeDisplayLines(s.PromptNoticeLine)
	if status := strings.TrimSpace(s.PromptEditorStatusLine); status != "" {
		lines = append(lines, status)
	}
	return lines
}

// activeBandVisibleRowCount is independent of prompt visibility so streaming
// can show progress while the prompt is hidden.
func (s BottomPaneState) activeBandVisibleRowCount() int {
	if s.composerVisibleRowCount() > 0 {
		return 0
	}
	n := len(s.ActiveBandLines)
	limit := s.ActiveBandMaxRows
	if limit <= 0 {
		limit = ActiveBandMaxRows
	}
	if n > limit {
		return limit
	}
	return n
}

func (s BottomPaneState) promptAreaVisibleRowCount() int {
	return s.activeBandVisibleRowCount() + s.dynamicStatusVisibleRowCount() + s.promptNoticeVisibleRowCount() + s.promptVerticalMarginRowCount() + s.promptVisibleRowCount()
}

func (s BottomPaneState) popupExpandsBelowPrompt() bool {
	return s.PopupBelowPrompt && len(s.PopupLines) > 0 && s.composerVisibleRowCount() == 0
}

func (s BottomPaneState) popupTopReservedRowCount() int {
	if !s.popupExpandsBelowPrompt() {
		return 0
	}
	rows := s.activeBandVisibleRowCount() + s.dynamicStatusVisibleRowCount() + s.promptNoticeVisibleRowCount() + s.promptVerticalMarginRowCount() + s.promptReservedRowCount()
	if rows < 0 {
		return 0
	}
	return rows
}

func (s BottomPaneState) popupInputGapRowCount() int {
	if s.popupExpandsBelowPrompt() {
		return 0
	}
	if len(s.PopupLines) == 0 || s.composerVisibleRowCount() > 0 {
		return 0
	}
	return 1
}

func (s BottomPaneState) promptReservedRowCount() int {
	if s.PromptReservedRows < 0 {
		return 0
	}
	return s.PromptReservedRows
}

func (s BottomPaneState) promptMarginsVisible() bool {
	return s.composerVisibleRowCount() == 0 && s.promptReservedRowCount() > 0
}

func (s BottomPaneState) promptTopMarginRowCount() int {
	if !s.promptMarginsVisible() || s.PromptTopMarginRows < 1 {
		return 0
	}
	return s.PromptTopMarginRows
}

func (s BottomPaneState) promptBottomMarginRowCount() int {
	if !s.promptMarginsVisible() || s.PromptBottomMarginRows < 1 {
		return 0
	}
	return s.PromptBottomMarginRows
}

func (s BottomPaneState) promptVerticalMarginRowCount() int {
	return s.promptTopMarginRowCount() + s.promptBottomMarginRowCount()
}

func (s BottomPaneState) promptVisibleRowCount() int {
	if s.composerVisibleRowCount() > 0 {
		return s.composerVisibleRowCount()
	}
	if s.popupExpandsBelowPrompt() {
		return s.promptReservedRowCount()
	}
	rows := s.promptReservedRowCount()
	if gapRows := s.popupInputGapRowCount(); gapRows > rows {
		rows = gapRows
	}
	return rows
}

func (s BottomPaneState) extraPromptReservedRowCount() int {
	if s.composerVisibleRowCount() > 0 || s.popupExpandsBelowPrompt() {
		return 0
	}
	rows := s.promptReservedRowCount()
	gapRows := s.popupInputGapRowCount()
	if rows <= gapRows {
		return 0
	}
	return rows - gapRows
}

func (s BottomPaneState) popupBottomGapRowCount() int {
	return s.activeBandVisibleRowCount() + s.dynamicStatusVisibleRowCount() + s.promptNoticeVisibleRowCount() + s.promptVerticalMarginRowCount() + s.popupInputGapRowCount() + s.extraPromptReservedRowCount()
}

func (s BottomPaneState) popupVisibleRowCount(height int) int {
	rows := s.popupLineVisibleRowCount(height)
	if !s.popupExpandsBelowPrompt() || s.PopupReservedRows <= rows {
		return rows
	}
	reservedRows := s.composerVisibleRowCount() + s.popupTopReservedRowCount()
	maxRows := maxBottomPanePopupRows(height, reservedRows, s.popupBottomGapRowCount())
	if maxRows > 0 && s.PopupReservedRows > maxRows {
		return maxRows
	}
	return s.PopupReservedRows
}

func (s BottomPaneState) VisiblePopupLines(height int) []string {
	rows := s.popupLineVisibleRowCount(height)
	if rows <= 0 || len(s.PopupLines) == 0 {
		return nil
	}
	if len(s.PopupLines) <= rows {
		return append([]string(nil), s.PopupLines...)
	}
	if s.PopupViewport != nil {
		if semanticLines := visibleSemanticPopupLines(s.PopupViewport, rows); len(semanticLines) > 0 {
			return semanticLines
		}
	}
	if popupOwnerUsesSelectionViewport(s.PopupOwner) {
		return visibleSelectionPopupLines(s.PopupLines, rows)
	}
	if rows == 1 {
		return []string{s.PopupLines[len(s.PopupLines)-1]}
	}
	if rows == 2 {
		return []string{s.PopupLines[0], s.PopupLines[len(s.PopupLines)-1]}
	}
	out := make([]string, 0, rows)
	out = append(out, s.PopupLines[0])
	out = append(out, "...")
	tailCount := rows - 2
	tailStart := len(s.PopupLines) - tailCount
	if tailStart < 1 {
		tailStart = 1
	}
	out = append(out, s.PopupLines[tailStart:]...)
	return out
}

func popupOwnerUsesSelectionViewport(owner string) bool {
	owner = strings.ToLower(strings.TrimSpace(owner))
	return owner == "slash_completion" || owner == "modal:selection"
}

func visibleSelectionPopupLines(lines []string, rows int) []string {
	if rows <= 0 || len(lines) == 0 {
		return nil
	}
	anchor := selectionPopupAnchorLine(lines)
	if rows == 1 {
		return []string{lines[anchor]}
	}

	indices := make([]int, 0, rows)
	appendUnique := func(index int) {
		if index < 0 || index >= len(lines) || len(indices) >= rows {
			return
		}
		for _, existing := range indices {
			if existing == index {
				return
			}
		}
		indices = append(indices, index)
	}

	appendUnique(0)
	appendUnique(anchor)
	warning := selectionPopupWarningLine(lines)
	appendUnique(warning)
	if (warning < 0 && rows >= 3) || (warning >= 0 && rows >= 4) {
		appendUnique(len(lines) - 1)
	}
	for distance := 1; len(indices) < rows && distance < len(lines); distance++ {
		appendUnique(anchor - distance)
		appendUnique(anchor + distance)
	}
	for index := 0; len(indices) < rows && index < len(lines); index++ {
		appendUnique(index)
	}

	sortInts(indices)
	out := make([]string, 0, len(indices))
	for _, index := range indices {
		out = append(out, lines[index])
	}
	return out
}

func selectionPopupWarningLine(lines []string) int {
	for index, line := range lines {
		normalized := strings.TrimSpace(line)
		if strings.Contains(normalized, "无效") || strings.Contains(normalized, "失败") || strings.HasPrefix(normalized, "错误") {
			return index
		}
	}
	return -1
}

func selectionPopupAnchorLine(lines []string) int {
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "> ") || strings.Contains(line, "(当前)") {
			return index
		}
	}
	for index, line := range lines {
		if strings.Contains(line, "(默认)") || strings.HasPrefix(strings.TrimLeft(line, " \t"), "[") {
			return index
		}
	}
	return 0
}

func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func clonePopupViewportSpec(spec *PopupViewportSpec) *PopupViewportSpec {
	if spec == nil {
		return nil
	}
	clone := &PopupViewportSpec{
		HeaderLines: append([]string(nil), spec.HeaderLines...),
		BodyLines:   append([]string(nil), spec.BodyLines...),
		FooterLines: append([]string(nil), spec.FooterLines...),
		Anchor:      spec.Anchor,
	}
	return clone
}

func visibleSemanticPopupLines(spec *PopupViewportSpec, rows int) []string {
	if spec == nil || rows <= 0 {
		return nil
	}
	header := cloneAndSanitizePopupLines(spec.HeaderLines)
	body := cloneAndSanitizePopupLines(spec.BodyLines)
	footer := cloneAndSanitizePopupLines(spec.FooterLines)
	if rows == 1 {
		if len(body) > 0 {
			return []string{body[clampPopupIndex(spec.Anchor, len(body))]}
		}
		if len(header) > 0 {
			return []string{header[0]}
		}
		return append([]string(nil), footer[:minPopupInt(1, len(footer))]...)
	}

	out := make([]string, 0, rows)
	if len(header) > 0 {
		out = append(out, header[0])
	}
	footerBudget := minPopupInt(len(footer), 1)
	bodyBudget := rows - len(out) - footerBudget
	if bodyBudget > 0 && len(body) > 0 {
		anchor := clampPopupIndex(spec.Anchor, len(body))
		start := anchor - bodyBudget/2
		if start < 0 {
			start = 0
		}
		if start+bodyBudget > len(body) {
			start = maxInt(0, len(body)-bodyBudget)
		}
		end := minPopupInt(len(body), start+bodyBudget)
		out = append(out, body[start:end]...)
	}
	if footerBudget > 0 && len(out) < rows {
		out = append(out, footer[len(footer)-1])
	}
	for index := 1; len(out) < rows && index < len(header); index++ {
		out = append(out, header[index])
	}
	return out
}

func clampPopupIndex(index int, length int) int {
	if length <= 0 || index < 0 {
		return 0
	}
	if index >= length {
		return length - 1
	}
	return index
}

func minPopupInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s BottomPaneState) popupLineVisibleRowCount(height int) int {
	reservedRows := s.composerVisibleRowCount() + s.popupTopReservedRowCount()
	maxRows := maxBottomPanePopupRows(height, reservedRows, s.popupBottomGapRowCount())
	if maxRows <= 0 || len(s.PopupLines) == 0 {
		return 0
	}
	if len(s.PopupLines) <= maxRows {
		return len(s.PopupLines)
	}
	return maxRows
}

func maxBottomPanePopupRows(height int, composerRows int, gapRows int) int {
	if height <= 2 {
		return 0
	}
	rows := height - 2 - composerRows - gapRows
	if rows < 0 {
		return 0
	}
	return rows
}

func promptNoticeDisplayLines(line string) []string {
	line = strings.ReplaceAll(line, "\r\n", "\n")
	line = strings.ReplaceAll(line, "\r", "\n")
	line = strings.TrimRight(line, "\n")
	if strings.TrimSpace(line) == "" {
		return nil
	}
	return strings.Split(line, "\n")
}

func (s *FixedBottomSurface) bottomPaneStateLocked() BottomPaneState {
	topMarginRows, bottomMarginRows := chatComposerVerticalMargins(s.terminal.Height())
	state := BottomPaneState{
		StatusModel:            cloneStatusLineModel(s.statusModel),
		DynamicStatusModel:     cloneStatusLineModel(s.dynamicStatusModel),
		PopupLines:             append([]string(nil), s.popupLines...),
		PopupOwner:             s.popupOwner,
		PopupBelowPrompt:       s.popupBelowPrompt,
		PopupReservedRows:      s.popupReservedRows,
		PopupViewport:          clonePopupViewportSpec(s.popupViewport),
		PromptNoticeLine:       s.promptNoticeLine,
		PromptEditorStatusLine: s.promptEditorStatusLine,
		ActiveBandLines:        append([]string(nil), s.activeBandLines...),
		ActiveBandStyled:       cloneRenderLines(s.activeBandStyled),
		ActiveBandMaxRows:      s.ActiveBandRowBudget(),
		PromptReservedRows:     s.promptReservedRows,
		PromptTopMarginRows:    topMarginRows,
		PromptBottomMarginRows: bottomMarginRows,
	}
	if strings.TrimSpace(s.composerLine) != "" {
		state.ComposerLine = s.composerLine
	}
	return state
}

func formatFixedStatusModelWithContext(model style.StatusLineModel, width int, theme style.ThemeContext) string {
	return style.RenderDocument(style.StatusLineDocument(model, width), theme)
}

func truncateFixedPopupLine(line string, width int) string {
	if width <= 0 {
		width = 80
	}
	if DisplayWidth(line) <= width {
		return line
	}
	if width <= 3 {
		return ""
	}
	var builder strings.Builder
	current := 0
	limit := width - 3
	for _, r := range line {
		w := DisplayWidth(string(r))
		if w <= 0 {
			continue
		}
		if current+w > limit {
			break
		}
		builder.WriteRune(r)
		current += w
	}
	builder.WriteString("...")
	return builder.String()
}

func cloneAndSanitizePopupLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, line)
	}
	return out
}

func normalizeFixedSurfaceOutputText(text string) string {
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.ReplaceAll(text, "\n", "\r\n")
}

func buildPendingPastePreviewLines(lines int, text string) []string {
	title := "粘贴草稿预览"
	if lines <= 0 {
		lines = 1
	}
	out := []string{
		title,
		fmt.Sprintf("  行数: %d", lines),
		"  提示: 回车确认，Esc 取消",
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		out = append(out, "  (空内容)")
		return out
	}
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		out = append(out, "  (空内容)")
		return out
	}
	previewLines := strings.Split(text, "\n")
	maxPreviewLines := 8
	if len(previewLines) > maxPreviewLines {
		previewLines = append(append([]string(nil), previewLines[:maxPreviewLines]...), "  ...")
	}
	out = append(out, "  内容:")
	for _, line := range previewLines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "  ")
			continue
		}
		out = append(out, "  "+line)
	}
	return out
}

func terminalMoveToSequence(row, col int) string {
	if row < 1 {
		row = 1
	}
	if col < 1 {
		col = 1
	}
	return fmt.Sprintf("\x1b[%d;%dH", row, col)
}

func terminalScrollRegionSequence(top, bottom int) string {
	if top < 1 {
		top = 1
	}
	if bottom < top {
		bottom = top
	}
	return fmt.Sprintf("\x1b[%d;%dr", top, bottom) + terminalMoveToSequence(top, 1)
}

func appendClearRowsSequence(builder *strings.Builder, startRow, rows int) {
	if builder == nil || startRow < 1 || rows < 1 {
		return
	}
	for row := startRow; row < startRow+rows; row++ {
		builder.WriteString(terminalMoveToSequence(row, 1))
		builder.WriteString("\x1b[K")
	}
}

func terminalScrollDownSequence(rows int) string {
	if rows < 1 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dT", rows)
}

func appendOutputScrollUpForBottomReserveGrowthSequence(builder *strings.Builder, height int, oldBottomRows int, newBottomRows int) {
	if builder == nil || height <= 1 || newBottomRows <= oldBottomRows {
		return
	}
	oldBottomRows = effectiveBottomRowsForHeight(height, oldBottomRows)
	newBottomRows = effectiveBottomRowsForHeight(height, newBottomRows)
	delta := newBottomRows - oldBottomRows
	if delta <= 0 {
		return
	}
	oldOutputBottom := outputBottomRowForHeight(height, oldBottomRows)
	if oldOutputBottom < 1 {
		return
	}
	if delta > oldOutputBottom {
		delta = oldOutputBottom
	}
	builder.WriteString(terminalScrollRegionSequence(1, oldOutputBottom))
	builder.WriteString(terminalMoveToSequence(oldOutputBottom, 1))
	builder.WriteString(strings.Repeat("\n", delta))
}

// appendOutputScrollDownForBottomReserveShrinkSequence mirrors the growth
// compensation. When reserved bottom rows are released, the output region grows
// downwards and the freed rows stay blank between the last committed line and
// the prompt. CSI Ps T scrolls the complete region down in one operation. It is
// preferable to a burst of one-row RI controls here: the transition is atomic
// from the terminal's perspective and is reliable through Windows ConPTY.
func appendOutputScrollDownForBottomReserveShrinkSequence(builder *strings.Builder, height, bottomRows, rows int) {
	if builder == nil || height <= 1 || rows < 1 {
		return
	}
	outputBottom := outputBottomRowForHeight(height, bottomRows)
	if outputBottom < 1 {
		return
	}
	if rows > outputBottom {
		rows = outputBottom
	}
	builder.WriteString(terminalScrollRegionSequence(1, outputBottom))
	builder.WriteString(terminalMoveToSequence(1, 1))
	builder.WriteString(terminalScrollDownSequence(rows))
}

func outputBottomRowForHeight(height int, bottomRows int) int {
	bottom := height - effectiveBottomRowsForHeight(height, bottomRows)
	if bottom < 1 {
		return 1
	}
	return bottom
}

func effectiveBottomRowsForHeight(height int, bottomRows int) int {
	if height <= 1 {
		return 1
	}
	maxRows := height - 1
	if bottomRows > maxRows {
		return maxRows
	}
	if bottomRows < 1 {
		return 1
	}
	return bottomRows
}

func fixedPromptLineEndPosition(line string, termWidth int) (int, int) {
	if termWidth <= 0 {
		termWidth = 80
	}
	row, col := 0, 0
	for _, r := range stripTerminalEscapeSequences(line) {
		switch r {
		case '\r', '\n':
			row++
			col = 0
			continue
		}
		width := DisplayWidth(string(r))
		if width <= 0 {
			continue
		}
		col += width
		if col >= termWidth {
			row += col / termWidth
			col %= termWidth
		}
	}
	return row, col
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
