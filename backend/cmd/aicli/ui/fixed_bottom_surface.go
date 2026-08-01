package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
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
	// A visible ActiveBand is a transient event block, so keep one semantic row
	// between retained history and Running/progress content. Short terminals
	// collapse the margin before sacrificing usable content.
	activeBandTopGapRows      = 1
	activeBandTopGapMinHeight = 12
	// DefaultGeometryProbeMinInterval caps how often the live stream paint path
	// re-probes terminal size. Active cells paint up to ~30 FPS; probing every
	// frame is unnecessary because human resize events are far slower.
	DefaultGeometryProbeMinInterval = 100 * time.Millisecond
	// SoftOutputTailMaxLines bounds the rewriteable committed tail kept at the
	// bottom of the output region. Older lines fall out of the soft window and
	// become irreversible scrollback history. Coordinator soft ownership uses
	// the same cap so source-backed reflow stays 1:1 with the surface window.
	SoftOutputTailMaxLines = renderengine.DefaultSoftOutputTailMaxLines
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

func activeBandTopGap(terminalHeight int) int {
	if terminalHeight < activeBandTopGapMinHeight {
		return 0
	}
	return activeBandTopGapRows
}

// FixedBottomSurface reserves the last terminal row for lightweight status
// while normal chat output scrolls in the region above it.
type FixedBottomSurface struct {
	terminal *Terminal
	mu       sync.Mutex
	enabled  bool
	testMode bool
	// leaseID != 0 while an alternate-screen lease (ScreenLease) suspends
	// primary flushing; leaseMode records the granted screen mode.
	leaseID   uint64
	leaseMode ScreenMode
	// alternateWriter is the byte sink for the DEC 1049 enter/exit sequences
	// the lease owns. nil means os.Stdout (production). Tests inject a buffer
	// to assert the sequence boundary around the picker frame.
	alternateWriter io.Writer
	// ownedFrameFlushCount counts frames actually emitted by the owned
	// viewport renderer. Exposed for lease tests that assert flush
	// suppression while an alternate-screen lease is active.
	ownedFrameFlushCount   int
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
	// Legacy immediate-mode compensation remains available for capability
	// fallback surfaces. Owned viewport rendering recomposes absolute rows and
	// intentionally keeps these counters at zero.
	scrollCompensatedRows int
	pendingScrollDownRows int
	// outputCursorOnBlankRow is true when the last WriteOutput ended on a
	// trailing newline, leaving the output-region cursor on an empty row.
	// Band/popup growth must consume that blank instead of scrolling it into
	// a permanent gap above the reserved bottom pane.
	outputCursorOnBlankRow bool
	// outputScrollDebtRows tracks a trailing blank absorbed by legacy reserve
	// growth. It is paid before the next legacy output write.
	outputScrollDebtRows int
	// historyWindow is the P5.2b/P5.3 owned-viewport foundation: the logical
	// committed transcript lines (styled source) captured from every scrollback
	// write. It is normally bounded to historyWindowMaxLines, but may temporarily
	// exceed that limit when wrapped lines make native handoff unsafe; unhanded
	// transcript data must never be discarded. Reserve shrink uses it only when
	// the retained physical rows cover the complete output region; otherwise the
	// terminal scroll fallback is retained so unknown history is never erased.
	historyWindow []string
	// historyPartial is true when the last captured write did not end in a
	// newline, so the next write continues the same logical line.
	historyPartial bool
	// handoffFrontier marks the oldest retained history lines already inserted
	// into native scrollback. It keeps the handoff boundary explicit while the
	// surface still owns legacy capability fallback behavior.
	handoffFrontier *renderengine.HandoffFrontier
	// softOutput owns the most recent committed output still sitting at the
	// bottom of the output region. The renderengine component owns partial-line
	// merging and hard-cap trim; this facade supplies history/geometry checks.
	softOutput     renderengine.SoftOutputState
	lastWidth      int
	lastHeight     int
	lastBottomRows int
	// ownedViewport is the normal production renderer. The legacy immediate
	// scroll-region path remains only as an internal capability fallback.
	ownedViewport   bool
	viewportBackend *renderengine.ScreenModel
	engine          *renderengine.Engine
	presenter       *renderengine.Presenter
	// lastRowOwners records the most recent composed frame's per-row owner
	// annotation (stage C). It is refreshed on every owned frame and exposed
	// for diagnostics (/debug display) and layout-invariant tests.
	lastRowOwners []renderengine.RowOwner
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
		terminal:        term,
		handoffFrontier: renderengine.NewHandoffFrontier(),
		statusModel: &style.StatusLineModel{
			State: style.RunReady,
		},
	}
}

// SetPresenter adopts the render engine's shared batch presenter. Keeping the
// presenter at the facade boundary lets owned viewport paints and coordinator
// invalidations use one frame accounting and one terminal output contract.
func (s *FixedBottomSurface) SetPresenter(presenter *renderengine.Presenter) {
	if s == nil || presenter == nil {
		return
	}
	s.mu.Lock()
	s.engine = nil
	s.presenter = presenter
	s.mu.Unlock()
}

// SetEngine adopts the coordinator's render engine. The surface keeps the
// Presenter pointer as a compatibility fallback, while production owned
// paints resolve it from the Engine so scheduling, dirty state and output
// accounting share one authority.
func (s *FixedBottomSurface) SetEngine(engine *renderengine.Engine) {
	if s == nil || engine == nil {
		return
	}
	s.mu.Lock()
	s.engine = engine
	s.presenter = engine.Presenter()
	s.handoffFrontier = engine.HandoffFrontier()
	s.mu.Unlock()
}

func (s *FixedBottomSurface) presenterLocked() *renderengine.Presenter {
	if s == nil {
		return nil
	}
	if s.engine != nil && s.engine.Presenter() != nil {
		return s.engine.Presenter()
	}
	return s.presenter
}

func (s *FixedBottomSurface) flushHoldingLock(writer io.Writer, render func(io.Writer)) error {
	if s == nil {
		return nil
	}
	if s.engine != nil {
		return s.engine.FlushHoldingLock(writer, render)
	}
	if presenter := s.presenterLocked(); presenter != nil {
		return presenter.FlushHoldingLock(writer, render)
	}
	// Owned viewport writes must always use the frame presenter, even when a
	// synthetic surface was assembled without the normal Engine wiring. This
	// keeps the fallback deterministic and avoids reintroducing direct writes.
	s.presenter = renderengine.NewPresenter()
	return s.presenter.FlushHoldingLock(writer, render)
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
	if s.leaseID != 0 {
		return false
	}
	s.enabled = true
	s.testMode = false
	s.ownedViewport = true
	s.viewportBackend = renderengine.NewScreenModel(s.terminal.Width(), s.terminal.Height())
	s.viewportBackend.Invalidate()
	if s.presenterLocked() == nil {
		s.presenter = renderengine.NewPresenter()
	}
	// The legacy surface may have left DECSTBM restricted to the output
	// region. Owned frames address the whole screen, so reset before the first
	// frame is composed.
	s.terminal.ResetScrollRegion()
	// Codex wraps every frame in a synchronized update; mirror that for the real
	// interactive surface so multi-step repaints (layout + scroll + content) land
	// atomically. Test surfaces go through EnableForTest and never flip this on,
	// keeping byte-exact rendering assertions unchanged.
	SetTerminalSynchronizedFrames(s.terminal.Capabilities().SynchronizedOutput)
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
	s.ownedViewport = true
	s.viewportBackend = renderengine.NewScreenModel(width, height)
	s.viewportBackend.Invalidate()
	if s.presenterLocked() == nil {
		s.presenter = renderengine.NewPresenter()
	}
	s.terminal.ResetScrollRegion()
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
	// Process teardown during an active lease must not paint into the
	// alternate screen; the pending Release becomes a no-op afterwards.
	leased := s.leaseID != 0
	if leased {
		writer := s.alternateWriter
		if writer == nil {
			if s.testMode {
				writer = io.Discard
			} else {
				writer = os.Stdout
			}
		}
		// Disable owns teardown when the surface is shut down while a modal
		// lease is open. Do not invalidate the lease id before emitting the
		// exit sequence, otherwise the handle's later Release cannot clean up.
		WithTerminalWriteLock(func() {
			_ = writeLeaseSequencesLocked(writer, "\x1b[?25h", "\x1b[r", "\x1b[?1049l")
		})
		s.leaseID = 0
		s.leaseMode = ScreenModePrimary
	}
	if !leased {
		WithTerminalWriteLock(func() {
			s.terminal.SaveCursor()
			s.terminal.ResetScrollRegion()
			s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
			s.terminal.MoveTo(s.statusRowLocked(), 1)
			s.terminal.ClearLine()
			s.terminal.RestoreCursor()
		})
	}
	s.clearPopupRenderStateLocked()
	s.clearPopupStateLocked(true)
	s.clearComposerStateLocked()
	s.clearPromptStateLocked(true)
	s.activeBandLines = nil
	s.activeBandStyled = nil
	s.dynamicStatusModel = nil
	s.enabled = false
	s.testMode = false
	s.ownedViewport = false
	s.viewportBackend = nil
	s.presenter = nil
	// Stop framing writes once the surface is torn down so any later plain output
	// path is not wrapped in a dangling synchronized update.
	SetTerminalSynchronizedFrames(false)
	s.outputCursorOnBlankRow = false
	s.scrollCompensatedRows = 0
	s.pendingScrollDownRows = 0
	s.outputScrollDebtRows = 0
	s.invalidateSoftOutputLocked()
	s.resetOwnedHistoryLocked()
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

// SettleOutputDebt clears layout debt before replaying already-final transcript
// (history / resume).
//
// On the production owned path this is a pure recompose: historyWindow + bottom
// band are painted together and no CSI-T shrink / absorb-scroll debt exists.
// On the legacy capability-fallback path it still re-applies layout and parks
// the cursor at the output region so ClearPrompt layout debt is not attached to
// the first content WriteOutput.
func (s *FixedBottomSurface) SettleOutputDebt() {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	if s.leaseID != 0 {
		// Alternate-screen lease active: primary flush is suspended; the
		// release repaint recomposes the frame from retained state.
		return
	}
	WithTerminalWriteLock(func() {
		if s.ownedViewport {
			// Owned frames recompose from historyWindow; there is no CSI-T
			// shrink debt or absorb-scroll debt to flush.
			s.applyLayoutLocked()
			s.renderOwnedViewportLocked()
			s.restoreStoredPromptCursorLocked()
			return
		}
		s.applyLayoutLocked()
		s.flushPendingOutputScrollDownLocked()
		s.flushOutputScrollDebtLocked()
		s.moveToOutputLocked()
	})
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
	// under the same lock hold. Geometry may hand rows to native scrollback, so
	// keep the entire transition inside the terminal write lock.
	WithTerminalWriteLock(func() {
		s.applyLayoutWithSizeLocked(width, height)
	})
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
	if s.ownedViewport {
		// Permanent output must go through WriteOutput so it enters the retained
		// transcript. BeginOutput remains a compatibility hook for callers that
		// only need to dismiss transient UI.
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		// Raw writers may land on a row occupied by a legacy absorbed tail.
		s.flushOutputScrollDebtLocked()
		// Raw writers (fmt.Println after beginDirectInteractiveOutput) paint at
		// the cursor this call parks.
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
	if s.leaseID != 0 {
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
	if s.leaseID != 0 {
		// The alternate presenter owns physical output. Report handled so
		// callers do not fall back to an unsynchronized raw editor write.
		return true
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
		if s.ownedViewport {
			output := normalizeFixedSurfaceOutputText(text)
			if trackSoft {
				s.noteSoftOutputLocked(text)
			} else {
				s.invalidateSoftOutputLocked()
			}
			s.appendHistoryWindowLocked(text)
			n = len(output)
			s.outputCursorOnBlankRow = strings.HasSuffix(output, "\n")
			if s.leaseID != 0 {
				// Lease active: retain state only; the release repaint
				// flushes the frame.
				return
			}
			if s.shouldAppendDirectLocked() {
				// History now exceeds the visible region: write through the native
				// scroll region (log-style natural scrolling) instead of a
				// full-frame repaint. If the rewriteable soft tail no longer fits
				// the scroll region, drop soft ownership so reflow cannot rewrite
				// rows that scrolled off screen.
				if trackSoft && s.softOutput.Valid() {
					height := s.terminal.Height()
					if height < 1 {
						height = 24
					}
					if s.softOutput.LineCount() > s.directScrollRegionRowsLocked(height) {
						s.invalidateSoftOutputLocked()
					}
				}
				s.appendOwnedDirectPaintLocked(writer, output)
			} else {
				s.renderOwnedViewportLocked()
			}
			// Live TTY paint already went through the double-buffer above. Mirror
			// plain text only to non-stdout writers so buffer/file capture paths
			// (and tests that assert irreversible scrollback bytes) still see
			// the drained head without double-painting the interactive screen.
			if writer != os.Stdout {
				if wn, werr := io.WriteString(writer, output); werr != nil {
					err = werr
					n = wn
				}
			}
			s.restoreStoredPromptCursorLocked()
			return
		}
		if s.leaseID != 0 {
			// Lease active: absorb legacy-mode writes without emitting bytes.
			return
		}
		s.applyLayoutLocked()
		s.flushPendingOutputScrollDownLocked()
		s.flushOutputScrollDebtLocked()
		s.moveToOutputLocked()
		output := normalizeFixedSurfaceOutputText(text)
		n, err = io.WriteString(writer, output)
		if n > 0 {
			s.markOutputWrittenLocked()
			fullWrite := err == nil && n == len(output)
			if fullWrite {
				if trackSoft {
					s.noteSoftOutputLocked(text)
				} else {
					// Foreign/plain writes break 1:1 soft ownership at the surface
					// boundary so callers cannot forget a second invalidate.
					s.invalidateSoftOutputLocked()
				}
				s.appendHistoryWindowLocked(text)
				// Trailing newline parks the cursor on a blank row at the output
				// bottom. Later bottom-reserve growth must absorb that blank or it
				// becomes a visible hole above the active band / prompt.
				s.outputCursorOnBlankRow = strings.HasSuffix(output, "\n")
			} else {
				// A short/erroring writer leaves only an unknown byte prefix on
				// screen. Restart ownership from future writes; once a complete
				// output region has accumulated, owned restore becomes safe again.
				s.invalidateSoftOutputLocked()
				s.resetOwnedHistoryLocked()
				written := output
				if n < len(written) {
					written = written[:n]
				}
				s.outputCursorOnBlankRow = strings.HasSuffix(written, "\n")
			}
		}
		s.restoreStoredPromptCursorLocked()
	})
	return n, err, true
}

// shouldAppendDirectLocked reports whether the committed history now exceeds
// the visible scroll region, in which case newly written rows should go
// through the native scroll region (log-style natural scrolling) instead of a
// full-frame repaint. The history model must already include the pending
// write (appendHistoryWindowLocked ran before this check).
func (s *FixedBottomSurface) shouldAppendDirectLocked() bool {
	if s == nil || s.terminal == nil {
		return false
	}
	height := s.terminal.Height()
	if height < 1 {
		height = 24
	}
	return len(s.expandHistoryLinesLocked(s.historyWindow)) > s.directScrollRegionRowsLocked(height)
}

// directScrollRegionRowsLocked returns the number of rows available for
// natural log scrolling: the output region minus the ActiveBand rows that
// stay anchored above the prompt. The band is excluded from the scroll region
// so a commit scroll never displaces the live stream viewport.
func (s *FixedBottomSurface) directScrollRegionRowsLocked(height int) int {
	regionBottom := outputBottomRowForHeight(height, s.bottomRowsLocked()) - s.bottomPaneStateLocked().activeBandVisibleRowCount()
	if regionBottom < 1 {
		regionBottom = 1
	}
	return regionBottom
}

// appendOwnedDirectPaintLocked writes committed rows through the native
// scroll region so the terminal scrolls naturally like a log once history
// exceeds the visible region: each \r\n parks at the region bottom and
// scrolls the region up one row before the row content is written, exactly
// like insertHistoryLinesLocked. After the scroll the full frame is staged
// and the scrolled history rows are committed silently (CommitRange) so the
// following flush only emits the bottom-pane delta (ActiveBand/prompt/status)
// instead of repainting the transcript. Callers hold the surface lock and the
// terminal write lock.
func (s *FixedBottomSurface) appendOwnedDirectPaintLocked(writer io.Writer, output string) {
	if s == nil || s.terminal == nil || output == "" {
		return
	}
	height := s.terminal.Height()
	if height < 1 {
		height = 24
	}
	regionBottom := s.directScrollRegionRowsLocked(height)
	rows := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	plan := renderengine.NewHandoffPlan(height, regionBottom, rows)
	// The caller already holds terminalWriteMu through writeOutput. Reuse the
	// shared handoff plan/presenter without reacquiring the non-reentrant lock.
	_ = s.flushHoldingLock(os.Stdout, func(w io.Writer) {
		_, _ = plan.WriteTo(w)
	})
	if writer != os.Stdout {
		_, _ = plan.WriteTo(writer)
	}
	// Mirror the scroll into the double buffer: stage the full new frame,
	// then mark the already-scrolled history rows as committed so Flush only
	// emits the bottom-pane delta.
	s.stageOwnedFrameLocked()
	if s.viewportBackend != nil {
		s.viewportBackend.CommitRange(1, regionBottom)
		if diff := s.viewportBackend.Flush(); diff != "" {
			_ = s.flushHoldingLock(os.Stdout, func(w io.Writer) {
				_, _ = io.WriteString(w, diff)
			})
			s.ownedFrameFlushCount++
		}
	}
}

// SoftOutputTailValid reports whether the surface still owns a rewriteable
// committed tail at the bottom of the output region.
func (s *FixedBottomSurface) SoftOutputTailValid() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled && s.softOutput.Valid()
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
	return s.softOutput.Trimmed()
}

// SoftOutputTailLineCount returns the number of rewriteable committed lines.
func (s *FixedBottomSurface) SoftOutputTailLineCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.softOutput.LineCount()
}

// SoftOutputTailLines returns a copy of the rewriteable committed tail.
func (s *FixedBottomSurface) SoftOutputTailLines() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.softOutput.Lines()
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
	adopted := append([]string(nil), lines...)
	if len(adopted) > SoftOutputTailMaxLines {
		drop := len(adopted) - SoftOutputTailMaxLines
		adopted = append([]string(nil), adopted[drop:]...)
	}
	// Adoption only changes ownership metadata. It must never invent history or
	// reclaim rows that have already crossed the irreversible scrollback
	// boundary.
	if _, ok := s.ownedHistorySuffixStartLocked(adopted); !ok {
		if !s.testMode {
			s.invalidateSoftOutputLocked()
			return
		}
		// Some coordinator unit tests exercise soft-window bookkeeping without
		// replaying the preceding surface writes. Keep that synthetic fixture
		// support isolated to EnableForTest; production adoption remains a
		// metadata-only operation over real retained history.
		s.historyWindow = append([]string(nil), adopted...)
		s.historyPartial = false
		s.handoffFrontier.Reset()
	}
	// Rebased window is complete relative to the adopted ownership.
	s.softOutput.Adopt(adopted)
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
	if !s.enabled || !s.softOutput.Valid() {
		return false
	}
	softLines := s.softOutput.Lines()
	if _, ok := s.ownedHistorySuffixStartLocked(softLines); !ok {
		s.invalidateSoftOutputLocked()
		return false
	}
	oldCount := len(softLines)
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
	if s.ownedViewport {
		if !s.testMode && !s.canRewriteOwnedHistorySuffixLocked(softLines, normalized) {
			s.invalidateSoftOutputLocked()
			return false
		}
		if !s.replaceOwnedHistorySuffixLocked(softLines, normalized) {
			s.invalidateSoftOutputLocked()
			return false
		}
		s.softOutput.Replace(normalized)
		s.outputCursorOnBlankRow = false
		if s.leaseID != 0 {
			// Retain the rewritten soft tail without touching the alternate
			// screen. Release will repaint the latest owned frame.
			return true
		}
		WithTerminalWriteLock(func() {
			// A growing reflow may move older, non-soft rows beyond the visible
			// output region. Hand them off before repainting; the preflight above
			// guarantees the rewritten suffix itself remains mutable.
			s.commitExcessHistoryToScrollbackLocked()
			s.renderOwnedViewportLocked()
			s.restoreStoredPromptCursorLocked()
		})
		return true
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
			s.replaceOwnedHistorySuffixLocked(softLines, nil)
			s.softOutput.Invalidate()
			s.outputCursorOnBlankRow = false
			s.outputScrollDebtRows = 0
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
		output := normalizeFixedSurfaceOutputText(builder.String())
		if n, err := io.WriteString(writer, output); err != nil || n != len(output) {
			s.invalidateSoftOutputLocked()
			s.resetOwnedHistoryLocked()
			rewritten = false
			return
		}
		s.replaceOwnedHistorySuffixLocked(softLines, normalized)
		s.softOutput.Replace(normalized)
		s.outputCursorOnBlankRow = true
		s.outputScrollDebtRows = 0
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
	s.softOutput.Note(text, s.historyPartial)
}

func (s *FixedBottomSurface) invalidateSoftOutputLocked() {
	if s == nil {
		return
	}
	s.softOutput.Invalidate()
}

// historyWindowMaxLines is the normal retained-history bound. It covers the
// visible output region plus headroom for band grow/shrink and keeps memory flat
// after successful handoff. Wrapped rows may temporarily exceed it because
// discarding an unhanded transcript is worse than retaining extra source.
const historyWindowMaxLines = 400

// appendHistoryWindowLocked captures committed scrollback text into the owned
// history window, coalescing writes that continue a partial (newline-less) line
// so streaming fragments do not create spurious line breaks.
func (s *FixedBottomSurface) appendHistoryWindowLocked(text string) {
	if s == nil || text == "" {
		return
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if normalized == "" {
		return
	}
	endsWithNewline := strings.HasSuffix(normalized, "\n")
	segs := strings.Split(strings.TrimSuffix(normalized, "\n"), "\n")
	if s.historyPartial && len(s.historyWindow) > 0 && len(segs) > 0 {
		s.historyWindow[len(s.historyWindow)-1] += segs[0]
		segs = segs[1:]
	}
	if len(segs) > 0 {
		s.historyWindow = append(s.historyWindow, segs...)
	}
	s.historyPartial = !endsWithNewline
	// Owned path only: hand lines older than the visible output region into
	// native scrollback BEFORE the hard cap (dual-retain headroom for shrink).
	// Hard-cap trim alone would silently drop lines without host scrollback.
	if s.ownedViewport {
		s.commitExcessHistoryToScrollbackLocked()
	}

	// Safety net for all paths: keep the newest historyWindowMaxLines rows
	// (matches original bounds test and prevents unbounded memory growth).
	//
	// If a retained line wraps at the current width, native handoff is
	// intentionally paused because the handoff frontier is a logical-line index
	// while the terminal scrolls physical rows. Do not trim unhanded lines in
	// that state: doing so would silently discard transcript data without ever
	// putting it in host scrollback. Already handed-off rows may still be
	// discarded from the dual-retention window.
	if len(s.historyWindow) > historyWindowMaxLines {
		drop := len(s.historyWindow) - historyWindowMaxLines
		if s.ownedViewport && drop > s.handoffFrontier.Value() {
			drop = s.handoffFrontier.Value()
		}
		if drop <= 0 {
			return
		}
		s.historyWindow = append([]string(nil), s.historyWindow[drop:]...)
		s.handoffFrontier.TrimPrefix(drop, len(s.historyWindow))
	}
}

func (s *FixedBottomSurface) resetOwnedHistoryLocked() {
	if s == nil {
		return
	}
	s.historyWindow = nil
	s.historyPartial = false
	s.handoffFrontier.Reset()
}

func (s *FixedBottomSurface) replaceOwnedHistorySuffixLocked(oldLines, newLines []string) bool {
	if s == nil {
		return false
	}
	start, ok := s.ownedHistorySuffixStartLocked(oldLines)
	if !ok {
		// Soft ownership metadata may be stale, but retained history remains the
		// source of truth. A failed validation must be non-destructive.
		return false
	}
	replaced := make([]string, 0, start+len(newLines))
	replaced = append(replaced, s.historyWindow[:start]...)
	replaced = append(replaced, newLines...)
	if len(replaced) > historyWindowMaxLines {
		drop := len(replaced) - historyWindowMaxLines
		replaced = append([]string(nil), replaced[drop:]...)
		s.handoffFrontier.TrimPrefix(drop, len(replaced))
	}
	s.handoffFrontier.Clamp(len(replaced))
	s.historyWindow = replaced
	s.historyPartial = false
	return true
}

// ownedHistorySuffixStartLocked validates that lines are the still-mutable
// suffix of retained history. Rows before the handoff frontier already exist in
// native terminal scrollback and are immutable.
func (s *FixedBottomSurface) ownedHistorySuffixStartLocked(lines []string) (int, bool) {
	if s == nil || s.historyPartial || len(lines) > len(s.historyWindow) {
		return 0, false
	}
	start := len(s.historyWindow) - len(lines)
	if start < s.handoffFrontier.Value() {
		return 0, false
	}
	for i := range lines {
		if s.historyWindow[start+i] != lines[i] {
			return 0, false
		}
	}
	return start, true
}

// canRewriteOwnedHistorySuffixLocked additionally checks the post-reflow
// visibility boundary. Reflow may hand off older rows, but it must not make any
// part of the newly rewritten suffix irreversible during the same operation.
func (s *FixedBottomSurface) canRewriteOwnedHistorySuffixLocked(oldLines, newLines []string) bool {
	start, ok := s.ownedHistorySuffixStartLocked(oldLines)
	if !ok {
		return false
	}
	prospectiveLen := start + len(newLines)
	if prospectiveLen > historyWindowMaxLines {
		return false
	}
	needHandedOff := prospectiveLen - s.visibleOutputRowsLocked()
	if needHandedOff < 0 {
		needHandedOff = 0
	}
	return needHandedOff <= start
}

// HistoryWindowForTest returns a copy of the captured history window (test-only).
func (s *FixedBottomSurface) HistoryWindowForTest() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.historyWindow...)
}

// HistoryHandedOffForTest returns how many oldest window lines have already been
// inserted into native scrollback (test-only).
func (s *FixedBottomSurface) HistoryHandedOffForTest() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handoffFrontier.Value()
}

// visibleOutputRowsForTest exposes visibleOutputRowsLocked for tests.
func (s *FixedBottomSurface) visibleOutputRowsForTest() int {
	if s == nil {
		return 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visibleOutputRowsLocked()
}

// VisibleOutputRows returns the number of terminal rows the owned viewport can
// display before excess committed output must be handed off to native
// scrollback. Returns 0 when the surface is not enabled, so callers (for
// example command result rendering) can skip overflow hints.
func (s *FixedBottomSurface) VisibleOutputRows() int {
	if s == nil || !s.enabled {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visibleOutputRowsLocked()
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
	if s.leaseID != 0 {
		// Retain the updated prompt while the alternate presenter owns the
		// terminal. Release will repaint it on the primary screen.
		return true
	}
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
		s.flushPendingOutputScrollDownLocked()
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
	previousState := s.bottomPaneStateLocked()
	previousRows := previousState.activeBandVisibleRowCount()
	previousGapRows := previousState.activeBandTopGapRowCount()
	if previousRows > 0 && s.lastHeight > 0 {
		previousGapRows = activeBandTopGap(s.lastHeight)
	}
	previousStart := s.promptRenderedStartRow
	s.activeBandLines = normalized
	s.activeBandStyled = cloneRenderLines(styled)
	s.reflowPromptViewportLocked()
	if s.ownedViewport {
		s.repaintActiveBandLocked()
		return true
	}
	currentState := s.bottomPaneStateLocked()
	currentRows := currentState.activeBandVisibleRowCount()
	currentGapRows := currentState.activeBandTopGapRowCount()
	if previousRows == currentRows && previousGapRows == currentGapRows {
		if currentRows == 0 {
			return s.enabled
		}
		if previousStart > 0 {
			previousActiveStart := previousStart + previousGapRows
			return s.repaintActiveBandDiffLocked(previousActiveStart, previousLines, previousStyled)
		}
	}
	return s.repaintActiveBandLocked()
}

func (s *FixedBottomSurface) repaintActiveBandDiffLocked(start int, previousLines []string, previousStyled []render.Line) bool {
	if !s.enabled || start < 1 {
		return false
	}
	if s.leaseID != 0 {
		return true
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
	var styled *render.Line
	if index < len(s.activeBandStyled) {
		styled = &s.activeBandStyled[index]
	}
	if text := formatActiveBandPaintRow(plain, styled, s.terminal.Width(), themeContext); text != "" {
		fmt.Print(text)
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
	if s.leaseID != 0 {
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
		if s.ownedViewport {
			// One full recompose covers prompt/band/status and restores owned
			// history into rows freed by a shrink. Legacy multi-pass paint and
			// scroll-down compensation are not used on this path.
			s.applyLayoutLocked()
			s.renderOwnedViewportLocked()
			if restorePromptCursor {
				s.restoreStoredPromptCursorLocked()
			} else {
				s.moveToOutputLocked()
			}
			return
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
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

	if s.ownedViewport {
		// Owned frames recompose the full screen from retained history + the
		// shrunk bottom reserve. No scroll-down compensation is required.
		// Re-assert trailing blank so shrink restore works. historyPartial is
		// false when the last write ended with a newline.
		if !s.historyPartial && len(s.historyWindow) > 0 {
			s.outputCursorOnBlankRow = true
		} else {
			s.outputCursorOnBlankRow = false
		}
		return s.repaintActiveBandLocked()
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

		// Emit geometry contraction and stale-pixel cleanup as one write so a
		// later output write never paints into the cleared band rows.
		var transition strings.Builder
		s.appendApplyLayoutSequenceLocked(&transition)
		appendClearRowsSequence(&transition, oldStart, oldRows)
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
		s.flushPendingOutputScrollDownLocked()
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
		if s.ownedViewport {
			s.reconcileOwnedViewportLocked()
			return
		}
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
		if s.ownedViewport {
			s.reconcileOwnedViewportLocked()
			return
		}
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
		if s.ownedViewport {
			s.reconcileOwnedViewportLocked()
			return
		}
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
		if s.ownedViewport {
			s.reconcileOwnedViewportLocked()
			return
		}
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
	if s.leaseID != 0 {
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
	if s.ownedViewport {
		width, height := s.terminal.RefreshSize()
		s.applyOwnedViewportGeometryLocked(width, height)
		return
	}
	if s.leaseID != 0 {
		return
	}
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
	if s.ownedViewport {
		s.applyOwnedViewportGeometryLocked(width, height)
		return
	}
	if s.leaseID != 0 {
		return
	}
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
	if s.ownedViewport {
		s.applyOwnedViewportGeometryLocked(width, height)
		return
	}
	s.appendApplyLayoutSequenceWithSizeLocked(builder, width, height)
}

func (s *FixedBottomSurface) appendApplyLayoutSequenceWithSizeLocked(builder *strings.Builder, width, height int) {
	if builder == nil {
		return
	}
	if s.ownedViewport {
		s.applyOwnedViewportGeometryLocked(width, height)
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
		if scrollGrowth > 0 && s.outputCursorOnBlankRow {
			scrollGrowth--
			s.outputCursorOnBlankRow = false
			s.outputScrollDebtRows++
		}
		if scrollGrowth > 0 {
			appendOutputScrollUpForBottomReserveGrowthSequence(builder, height, bottomRows-scrollGrowth, bottomRows)
		}
		s.scrollCompensatedRows = bottomRows
	case sameSize && compensatedRows > 0 && bottomRows < compensatedRows:
		s.pendingScrollDownRows += compensatedRows - bottomRows
		s.scrollCompensatedRows = bottomRows
	case !sameSize || compensatedRows <= 0:
		s.pendingScrollDownRows = 0
		s.scrollCompensatedRows = bottomRows
		s.outputCursorOnBlankRow = false
		s.outputScrollDebtRows = 0
	}
	s.lastWidth = width
	s.lastHeight = height
	s.lastBottomRows = bottomRows
	builder.WriteString(terminalScrollRegionSequence(1, outputBottomRowForHeight(height, bottomRows)))
}

func (s *FixedBottomSurface) applyOwnedViewportGeometryLocked(width, height int) {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	previousBottomRows := s.lastBottomRows
	bottomRows := s.effectiveBottomRowsLocked(height)
	sizeChanged := width != s.lastWidth || height != s.lastHeight
	bottomChanged := bottomRows != s.lastBottomRows
	s.lastWidth = width
	s.lastHeight = height
	s.lastBottomRows = bottomRows
	// Owned frames recompose absolute rows; legacy compensation state must not
	// leak across a capability-path transition.
	s.scrollCompensatedRows = 0
	s.pendingScrollDownRows = 0
	if sizeChanged {
		s.outputScrollDebtRows = 0
	}
	if previousBottomRows > 0 && bottomRows > previousBottomRows {
		// A growing owned bottom pane (ActiveBand, popup, dynamic status, or
		// semantic gap) reduces the visible history region immediately. Hand
		// newly hidden rows to native scrollback before the full-frame repaint;
		// otherwise they exist only in the retained window and appear clipped
		// until the transient pane shrinks again.
		if s.leaseID == 0 {
			s.commitExcessHistoryToScrollbackLocked()
		}
	}
	// Only a real terminal resize invalidates the trailing-blank marker:
	// band/popup grow-shrink must keep it so Compose can restore the owned
	// transcript tail.
	if sizeChanged {
		s.outputCursorOnBlankRow = false
	}
	// Owned frames address absolute rows and must not inherit a narrower legacy
	// DECSTBM region. Reset before the first/resize repaint so the status row and
	// other bottom-pane rows are writable in the host terminal and vt.Screen.
	if sizeChanged || s.viewportBackend == nil {
		if s.leaseID == 0 {
			s.terminal.ResetScrollRegion()
		}
	}
	if s.viewportBackend == nil {
		s.viewportBackend = renderengine.NewScreenModel(width, height)
		s.viewportBackend.Invalidate()
		return
	}
	if backendWidth, backendHeight := s.viewportBackend.Size(); backendWidth != width || backendHeight != height {
		s.viewportBackend.Resize(width, height)
		return
	}
	// Geometry transitions that free or claim reserve rows must force a full
	// row repaint (EL) so blank margin/history cells stay Text="" rather than
	// residual space glyphs from the previous band/popup content.
	if sizeChanged || bottomChanged {
		s.viewportBackend.Invalidate()
	}
}

// appendPendingOutputScrollDownLocked emits deferred legacy reserve-release
// compensation. Owned frames never use this path.
func (s *FixedBottomSurface) appendPendingOutputScrollDownLocked(builder *strings.Builder) {
	if s == nil || s.terminal == nil || builder == nil || s.pendingScrollDownRows < 1 || s.ownedViewport {
		return
	}
	rows := s.pendingScrollDownRows
	s.pendingScrollDownRows = 0
	height := s.terminal.Height()
	if height > 1 {
		appendOutputScrollDownForBottomReserveShrinkSequence(builder, height, s.effectiveBottomRowsLocked(height), rows)
	}
}

func (s *FixedBottomSurface) flushPendingOutputScrollDownLocked() {
	if s == nil || s.pendingScrollDownRows < 1 || s.ownedViewport {
		return
	}
	var builder strings.Builder
	s.appendPendingOutputScrollDownLocked(&builder)
	if builder.Len() > 0 {
		fmt.Print(builder.String())
	}
}

// appendOutputScrollDebtLocked pays the row absorbed from a legacy trailing
// blank before the next write reaches the output bottom.
func (s *FixedBottomSurface) appendOutputScrollDebtLocked(builder *strings.Builder) {
	if s == nil || s.terminal == nil || builder == nil || s.outputScrollDebtRows < 1 || s.ownedViewport || s.pendingScrollDownRows > 0 {
		return
	}
	rows := s.outputScrollDebtRows
	s.outputScrollDebtRows = 0
	height := s.terminal.Height()
	if height <= 1 {
		return
	}
	bottom := outputBottomRowForHeight(height, s.effectiveBottomRowsLocked(height))
	if rows > bottom {
		rows = bottom
	}
	builder.WriteString(terminalMoveToSequence(bottom, 1))
	builder.WriteString(strings.Repeat("\n", rows))
	s.outputCursorOnBlankRow = true
}

func (s *FixedBottomSurface) flushOutputScrollDebtLocked() {
	if s == nil || s.outputScrollDebtRows < 1 || s.ownedViewport {
		return
	}
	var builder strings.Builder
	s.appendOutputScrollDebtLocked(&builder)
	if builder.Len() > 0 {
		fmt.Print(builder.String())
	}
}

func (s *FixedBottomSurface) markOutputWrittenLocked() {
	if s == nil || s.terminal == nil || s.ownedViewport {
		return
	}
	height := s.terminal.Height()
	if height <= 0 {
		_, height = s.terminal.RefreshSize()
	}
	s.scrollCompensatedRows = s.effectiveBottomRowsLocked(height)
}

func appendOutputScrollUpForBottomReserveGrowthSequence(builder *strings.Builder, height, oldBottomRows, newBottomRows int) {
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
	if delta > oldOutputBottom {
		delta = oldOutputBottom
	}
	builder.WriteString(terminalScrollRegionSequence(1, oldOutputBottom))
	builder.WriteString(terminalMoveToSequence(oldOutputBottom, 1))
	builder.WriteString(strings.Repeat("\n", delta))
}

func appendOutputScrollDownForBottomReserveShrinkSequence(builder *strings.Builder, height, bottomRows, rows int) {
	if builder == nil || height <= 1 || rows < 1 {
		return
	}
	outputBottom := outputBottomRowForHeight(height, bottomRows)
	if rows > outputBottom {
		rows = outputBottom
	}
	builder.WriteString(terminalScrollRegionSequence(1, outputBottom))
	builder.WriteString(terminalMoveToSequence(1, 1))
	builder.WriteString(terminalScrollDownSequence(rows))
}

func (s *FixedBottomSurface) renderStatusLocked() {
	if !s.enabled {
		return
	}
	if s.leaseID != 0 {
		return
	}
	if s.ownedViewport {
		s.renderOwnedViewportLocked()
		return
	}
	state := s.bottomPaneStateLocked()
	s.terminal.MoveTo(s.statusRowLocked(), 1)
	s.terminal.ClearLine()
	fmt.Print(s.statusPaintTextLocked(state, s.terminal.Width()))
	s.terminal.ClearLine()
}

func (s *FixedBottomSurface) renderPopupLocked() {
	if !s.enabled {
		return
	}
	if s.leaseID != 0 {
		return
	}
	if s.ownedViewport {
		s.renderOwnedViewportLocked()
		return
	}
	state := s.bottomPaneStateLocked()
	plan := s.popupPaintPlanLocked(state, s.terminal.Height())
	if plan.reservedRows == 0 {
		if s.popupRenderedRows > 0 {
			s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
			s.clearPopupRenderStateLocked()
		}
		return
	}
	if s.popupRenderedRows > 0 && (s.popupRenderedRows != plan.reservedRows || s.popupRenderedGapRows != plan.gapRows) {
		s.clearPopupAreaLocked(s.popupRenderedRows, s.popupRenderedGapRows)
		s.clearPopupRenderStateLocked()
	}
	for _, paint := range plan.rows {
		s.terminal.MoveTo(paint.row, 1)
		s.terminal.ClearLine()
		if paint.text != "" {
			fmt.Print(paint.text)
		}
	}
	s.popupRenderedRows = plan.reservedRows
	s.popupRenderedGapRows = plan.gapRows
	s.popupRenderedStartRow = plan.startRow
}

func (s *FixedBottomSurface) moveToOutputLocked() {
	if s.leaseID != 0 {
		return
	}
	s.terminal.MoveTo(s.outputBottomRowLocked(), 1)
}

func (s *FixedBottomSurface) moveToPromptLocked() {
	s.terminal.MoveTo(s.promptBottomRowLocked(), 1)
}

func (s *FixedBottomSurface) restoreStoredPromptCursorLocked() {
	if s.leaseID != 0 {
		return
	}
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
	rows := bottom - outputBottom - state.dynamicStatusVisibleRowCount() - state.promptNoticeVisibleRowCount() - state.activeBandLayoutRowCount() - state.promptTopMarginRowCount()
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
	if s.leaseID != 0 {
		return
	}
	if s.ownedViewport {
		s.renderOwnedViewportLocked()
		return
	}
	state := s.bottomPaneStateLocked()
	plan := s.promptPaintPlanLocked(state, s.terminal.Width())
	if plan.skip {
		return
	}
	if plan.empty {
		if s.promptRenderedRows > 0 {
			s.clearRowsLocked(s.promptRenderedStartRow, s.promptRenderedRows)
			s.promptRenderedStartRow = 0
			s.promptRenderedRows = 0
		}
		return
	}
	// Layout bottom-up: [active band][notice][dynamic status][top margin]
	// [prompt][bottom margin][status]. Margins are reserved only for the main
	// chat composer, so transient popups and prompt-less streaming stay dense.
	if s.promptRenderedStartRow > 0 && (s.promptRenderedStartRow != plan.startRow || s.promptRenderedRows != plan.areaRows) {
		s.clearRowsLocked(s.promptRenderedStartRow, s.promptRenderedRows)
	}
	if clear {
		s.clearRowsLocked(plan.startRow, plan.areaRows)
	}
	for _, paint := range plan.rows {
		if paint.row < 1 {
			continue
		}
		s.terminal.MoveTo(paint.row, 1)
		if paint.text != "" {
			fmt.Print(paint.text)
		}
	}
	s.promptRenderedStartRow = plan.startRow
	s.promptRenderedRows = plan.areaRows
}

// OwnedViewport reports whether the production owned-viewport renderer is active.
func (s *FixedBottomSurface) OwnedViewport() bool {
	if s == nil {
		return false
	}
	return s.ownedViewport
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
	if s.leaseID != 0 {
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
	if s.leaseID != 0 {
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
	ActiveBandTopGapRows   int
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

func (s BottomPaneState) activeBandTopGapRowCount() int {
	if s.activeBandVisibleRowCount() < 1 || s.ActiveBandTopGapRows < 1 {
		return 0
	}
	return s.ActiveBandTopGapRows
}

func (s BottomPaneState) activeBandLayoutRowCount() int {
	return s.activeBandTopGapRowCount() + s.activeBandVisibleRowCount()
}

func (s BottomPaneState) promptAreaVisibleRowCount() int {
	return s.activeBandLayoutRowCount() + s.dynamicStatusVisibleRowCount() + s.promptNoticeVisibleRowCount() + s.promptVerticalMarginRowCount() + s.promptVisibleRowCount()
}

func (s BottomPaneState) popupExpandsBelowPrompt() bool {
	return s.PopupBelowPrompt && len(s.PopupLines) > 0 && s.composerVisibleRowCount() == 0
}

func (s BottomPaneState) popupTopReservedRowCount() int {
	if !s.popupExpandsBelowPrompt() {
		return 0
	}
	rows := s.activeBandLayoutRowCount() + s.dynamicStatusVisibleRowCount() + s.promptNoticeVisibleRowCount() + s.promptVerticalMarginRowCount() + s.promptReservedRowCount()
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
	return s.activeBandLayoutRowCount() + s.dynamicStatusVisibleRowCount() + s.promptNoticeVisibleRowCount() + s.promptVerticalMarginRowCount() + s.popupInputGapRowCount() + s.extraPromptReservedRowCount()
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
		ActiveBandTopGapRows:   activeBandTopGap(s.terminal.Height()),
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

func terminalResetScrollRegionSequence(height int) string {
	// Bare CSI r is the portable full-screen reset; keep height for call-site
	// clarity and future hosts that need an explicit 1;height form.
	_ = height
	return "\x1b[r"
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

// visibleOutputRowsLocked returns the number of rows occupied by the current
// output region (history + active band + prompt/status margins). This is the
// maximum number of history rows that can be kept in owned historyWindow before
// excess must be handed off to native scrollback.
func (s *FixedBottomSurface) visibleOutputRowsLocked() int {
	if s == nil || s.terminal == nil {
		return 1
	}
	height := s.terminal.Height()
	if height < 1 {
		height = 24
	}
	bottomRows := s.effectiveBottomRowsLocked(height)
	outputBottom := outputBottomRowForHeight(height, bottomRows)
	return outputBottom
}

// historyWindowHeadroom is the extra rows kept in historyWindow beyond the
// current visible output region so that band grow/shrink can restore freed
// rows from the retained transcript without falling back to CSI T.
const historyWindowHeadroom = 40

// historySegmentIsSinglePhysicalRowsLocked reports whether the given logical
// history segment materializes 1:1 onto terminal rows at the current width.
// Native scrollback handoff writes one terminal row per emitted line, so a
// segment is only row-safe while this invariant holds. Unlike the previous
// whole-window gate, only the segment being handed off matters: a wrapped
// line still living in the visible region does not block older rows from
// reaching scrollback.
func (s *FixedBottomSurface) historySegmentIsSinglePhysicalRowsLocked(segment []string) bool {
	if s == nil || len(segment) == 0 {
		return true
	}
	return len(s.expandHistoryLinesLocked(segment)) == len(segment)
}

// commitExcessHistoryToScrollbackLocked hands off any history rows older than
// the current visible output region into native scrollback (once). Dual-retains
// those lines in historyWindow up to visible+headroom so band-shrink can restore
// without CSI T. Soft-trims only rows already confirmed in native scrollback;
// unsafe wrapped rows remain retained even when that temporarily exceeds the
// normal memory bound.
func (s *FixedBottomSurface) commitExcessHistoryToScrollbackLocked() {
	if s == nil || s.terminal == nil || len(s.historyWindow) == 0 {
		return
	}
	if s.leaseID != 0 {
		// Alternate-screen lease active: primary flush is suspended and the
		// leased alternate screen owns the output region. Native scrollback
		// handoff bytes must not be emitted here; the release repaint replays
		// retained state and commits pending history then.
		return
	}
	visible := s.visibleOutputRowsLocked()
	if visible < 1 {
		visible = 1
	}
	keepForRestore := visible + historyWindowHeadroom
	if keepForRestore > historyWindowMaxLines {
		keepForRestore = historyWindowMaxLines
	}

	// Any line older than the newest `visible` rows must enter scrollback once.
	// Headroom lines stay dual-retained in the window for shrink restore.
	needHandedOff := 0
	if len(s.historyWindow) > visible {
		needHandedOff = len(s.historyWindow) - visible
	}
	if needHandedOff > s.handoffFrontier.Value() {
		softStart, softSuffixOwned := 0, false
		softLines := s.softOutput.Lines()
		if len(softLines) > 0 {
			softStart, softSuffixOwned = s.ownedHistorySuffixStartLocked(softLines)
		}
		segment := s.historyWindow[s.handoffFrontier.Value():needHandedOff]
		var handoff []string
		if s.historySegmentIsSinglePhysicalRowsLocked(segment) {
			// Fast path: every logical line is exactly one terminal row, so
			// write the styled source verbatim (preserves ANSI styling in
			// native scrollback).
			handoff = append([]string(nil), segment...)
		} else {
			// Wrapped segment: expand each logical line into its physical
			// rows so the DECSTBM \r\n scroll count stays 1:1 with the
			// terminal. Rows are plain text; the owned full-frame repaint
			// immediately re-renders the visible window from styled source.
			handoff = s.expandHistorySegmentToPhysicalTextLocked(segment)
			if len(handoff) == 0 {
				return
			}
		}
		if !s.insertHistoryLinesLocked(handoff) {
			// A failed terminal write must not advance the logical boundary:
			// doing so would make these rows permanently disappear from future
			// handoff attempts.
			return
		}
		s.handoffFrontier.AdvanceTo(needHandedOff, len(s.historyWindow))
		if s.softOutput.Valid() && (!softSuffixOwned || s.handoffFrontier.Value() > softStart) {
			// Native scrollback is immutable. Once handoff reaches any part of
			// the rewrite window, abandon that ownership before a later resize
			// can replace already-emitted history with a different rendering.
			s.invalidateSoftOutputLocked()
		}
	}

	// Soft-trim oldest rows past keepForRestore (already handed off when
	// possible). Never trim an unhanded row: when physical-row handoff is
	// deferred for a wrapped line, those rows are the only durable copy.
	if len(s.historyWindow) > keepForRestore {
		drop := len(s.historyWindow) - keepForRestore
		if drop > s.handoffFrontier.Value() {
			drop = s.handoffFrontier.Value()
		}
		if drop <= 0 {
			return
		}
		s.historyWindow = append([]string(nil), s.historyWindow[drop:]...)
		s.handoffFrontier.TrimPrefix(drop, len(s.historyWindow))
	}
}

// insertHistoryLinesLocked is the single primitive for moving history into
// native scrollback. Cursor-neutral. Codex-aligned DECSTBM path:
//
//  1. Limit the scroll region to rows 1..outputBottom (above the bottom band).
//  2. Park the cursor on the last row of that region.
//  3. For each history line emit "\r\n" then the line — the LF at the region
//     bottom scrolls the top of the region into host scrollback without
//     touching the reserved bottom band.
//
// This must be the ONLY path for history to reach scrollback. CSI T (Scroll
// Down) is wrong: it does not enter scrollback and corrupts the double-buffer
// front state. Do not write at row 1 of a multi-row region either — that only
// advances the cursor downward and never scrolls until the region is full.
func (s *FixedBottomSurface) insertHistoryLinesLocked(rows []string) bool {
	if s == nil || s.terminal == nil || len(rows) == 0 {
		return false
	}
	width, height := s.terminal.Width(), s.terminal.Height()
	if width < 1 || height < 1 {
		return false
	}

	outputBottom := outputBottomRowForHeight(height, s.bottomRowsLocked())
	if outputBottom < 1 {
		outputBottom = 1
	}
	// Presenter owns the cursor-neutral DECSTBM bytes and batches them as one
	// handoff plan, so no Terminal fmt.Print call can interleave with a frame.
	plan := renderengine.NewHandoffPlan(height, outputBottom, rows)
	if err := s.flushHoldingLock(os.Stdout, func(w io.Writer) {
		_, _ = plan.WriteTo(w)
	}); err != nil {
		return false
	}

	// Host scroll mutated physical rows outside Backend; Invalidate so the
	// next Flush force-repaints instead of diffing against a stale front.
	if s.viewportBackend != nil {
		s.viewportBackend.Invalidate()
	}
	return true
}
