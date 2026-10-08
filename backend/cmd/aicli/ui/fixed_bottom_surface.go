package ui

import (
	"fmt"
	"io"
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
	// collapse the margin before sacrificing usable content. The permanent
	// second status row (session ID / --pprof / --debug) reserves one more row
	// than the historical single-status layout, so the gap threshold is raised
	// to keep at least seven retained history rows on a 15-row terminal instead
	// of letting the separator shrink the visible transcript tail to six.
	activeBandTopGapRows      = 1
	activeBandTopGapMinHeight = 16
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
	// physicalWritesEnabled is the surface-level writer fence used by the
	// unified renderer cutover. A disabled fence keeps all semantic/layout
	// state in this compatibility adapter up to date, but prevents it from
	// submitting terminal bytes that belong to the unified presenter.
	// physicalWritesConfigured preserves zero-value compatibility for tests and
	// small synthetic surfaces: before the first explicit setter call, the
	// historical default remains enabled.
	physicalWritesEnabled    bool
	physicalWritesConfigured bool
	// physicalWritesLockedOff is installed after a unified presenter has
	// attached. It makes the session fence one-way so compatibility code cannot
	// revive a second primary terminal writer later in the chat lifecycle.
	physicalWritesLockedOff bool
	// leaseID != 0 while an alternate-screen lease (ScreenLease) suspends
	// primary flushing; leaseMode records the granted screen mode.
	leaseID   uint64
	leaseMode ScreenMode
	// leaseWaitBudget 是「撞上在途租约时」的等待预算（P2-4b）：0 = 立即
	// 返回 ErrScreenLeaseBusy（历史行为）；>0 = 在预算内轮询等待释放。
	// 由忙时宿主在 S 档命令执行期间设置，使既有 AcquireAlternateScreen
	// 调用点无需改动即可获得等待能力。
	leaseWaitBudget time.Duration
	// alternateWriter is the byte sink for the DEC 1049 enter/exit sequences
	// the lease owns. nil means os.Stdout (production). Tests inject a buffer
	// to assert the sequence boundary around the picker frame.
	alternateWriter io.Writer
	// alternateTransport is installed only after the unified primary presenter
	// has become the sole physical writer. With the surface fence disabled,
	// ScreenLease delegates DEC 1049 and fullscreen frame bytes to this
	// transport instead of retaining a second terminal writer.
	alternateTransport AlternateScreenLeaseTransport
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
	sessionIDLine          string
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
	// fallback surfaces, but RenderEngine owns its complete state and planning.
	legacyReserve renderengine.LegacyReserveState
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
	// uiPoster 是 UI actor 投递入口（Phase 1，实施指南任务 4）：非 nil 时
	// facade 组内部只投递 action；reducer 消费该 action（surface Apply 已随
	// L3-3 退役）。
	uiPoster func(UIAction) bool
	// activeBandGeneration stamps facade band actions; finalization advances it
	// before committing permanent history. The surface-side fence (Apply) is
	// retired in L3-3; unified frames gate facade payloads via
	// SemanticActiveCellProjection in the reducer.
	activeBandGeneration uint64
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
		terminal:                 term,
		physicalWritesEnabled:    true,
		physicalWritesConfigured: true,
		handoffFrontier:          renderengine.NewHandoffFrontier(),
		activeBandGeneration:     1,
		statusModel: &style.StatusLineModel{
			State: style.RunReady,
		},
	}
}

// PhysicalWritesEnabled reports whether this surface may submit bytes to the
// terminal. The default is true, including for a zero-value surface created by
// tests or compatibility code. Unified production mode sets this to false once
// TerminalSession becomes the sole physical writer.
func (s *FixedBottomSurface) PhysicalWritesEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.physicalWritesEnabledLocked()
}

// SetPhysicalWritesEnabled toggles the legacy surface writer fence. Disabling
// the fence intentionally does not clear retained transcript, active-band,
// prompt, or geometry state; those values remain available to compatibility
// snapshots while all physical terminal effects are delegated elsewhere.
func (s *FixedBottomSurface) SetPhysicalWritesEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if enabled && s.physicalWritesLockedOff {
		s.mu.Unlock()
		return
	}
	s.physicalWritesEnabled = enabled
	s.physicalWritesConfigured = true
	s.mu.Unlock()
}

// FencePhysicalWrites permanently disables this surface's terminal writer for
// the rest of its lifetime. It is called only after TerminalSessionPresenter
// is live; the surface still retains compatibility state and lease metadata.
func (s *FixedBottomSurface) FencePhysicalWrites() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.physicalWritesEnabled = false
	s.physicalWritesConfigured = true
	s.physicalWritesLockedOff = true
	s.mu.Unlock()
}

// SetAlternateScreenLeaseTransport installs the unified terminal authority
// used while PhysicalWritesEnabled is false. Passing nil intentionally makes
// fullscreen acquisition fail closed instead of silently sending pager bytes
// to os.Stdout beside the primary TerminalSession.
func (s *FixedBottomSurface) SetAlternateScreenLeaseTransport(transport AlternateScreenLeaseTransport) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.alternateTransport = transport
	s.mu.Unlock()
}

func (s *FixedBottomSurface) physicalWritesEnabledLocked() bool {
	if s == nil {
		return false
	}
	if !s.physicalWritesConfigured {
		return true
	}
	return s.physicalWritesEnabled
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

// SetPaintTraceEnabled toggles the render-engine paint reconciliation probe
// (/debug on|off). The probe is observational only: it never influences
// layout or terminal output. Disabling keeps the accumulated counters so an
// operator can reproduce a symptom first and inspect the report afterwards
// with /debug display. Without an engine (synthetic surfaces) this is a
// no-op.
func (s *FixedBottomSurface) SetPaintTraceEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	engine := s.engine
	s.mu.Unlock()
	if engine != nil {
		engine.Trace().SetEnabled(enabled)
	}
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
	// L3-1: the legacy first-frame paint block (DECSTBM reset + DEC 2026
	// framing + initial composite) is retired. Production enables the surface
	// with physical writes fenced, so the block was unreachable; Enable only
	// establishes state now.
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
		leaseID := s.leaseID
		// The compatibility facade may be torn down while a unified pager is
		// open. It still must ask TerminalSession to leave DEC 1049; doing
		// nothing here would strand the process in the alternate buffer.
		if transport := s.alternateTransport; transport != nil {
			_ = transport.ExitAlternateScreen(leaseID)
		}
		s.leaseID = 0
		s.leaseMode = ScreenModePrimary
	}
	// L3-3: the legacy teardown paint (scroll-region reset + popup/status row
	// cleanup) is retired with the physical paint family; teardown is
	// state-only and the unified presenter owns the final frame.
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
	s.legacyReserve = renderengine.LegacyReserveState{}
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

// TerminalGeometry returns the most recently known primary-terminal geometry
// without emitting bytes or applying layout. It is the only surface datum the
// unified presenter may read while the surface is retained as a compatibility
// state facade.
func (s *FixedBottomSurface) TerminalGeometry() (width, height int, ok bool) {
	if s == nil || s.terminal == nil {
		return 0, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return 0, 0, false
	}
	width, height = s.terminal.Width(), s.terminal.Height()
	return width, height, width > 0 && height > 0
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
	// State-only maintenance hook: keep geometry/debt bookkeeping coherent for
	// the compatibility surface. Physical paints (owned full-frame flush or the
	// legacy cursor park) were retired in L3-2; TerminalSession owns the writer.
	s.applyLayoutLocked()
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

// MeasuredGeometry returns the most recently applied terminal dimensions
// without issuing a terminal query or writing any bytes. It is the narrow
// legacy-adapter bridge used to report a completed geometry probe back to the
// UI actor; Layout must consume the resulting Resize action, never this method.
func (s *FixedBottomSurface) MeasuredGeometry() (width, height int, ok bool) {
	if s == nil || s.terminal == nil {
		return 0, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return 0, 0, false
	}
	width, height = s.terminal.Width(), s.terminal.Height()
	return width, height, width > 0 && height > 0
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
	width, height, err := s.terminal.driver.ProbeSize()
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
		editorStatusRows = 1
	)
	statusRows := 1
	if strings.TrimSpace(s.sessionIDLine) != "" && s.composerLine == "" && len(s.popupLines) == 0 {
		statusRows = 2
	}
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
		// transcript. BeginOutput remains a state-only compatibility hook for
		// callers that only need to dismiss transient UI.
		return
	}
	// Legacy cursor park retired in L3-2: the unified presenter owns the screen.
}

func (s *FixedBottomSurface) PromptCursorPrefix(rowOffset, col int) (string, bool) {
	if s == nil || s.terminal == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || !s.physicalWritesEnabledLocked() {
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
	if !s.physicalWritesEnabledLocked() {
		// The unified presenter receives the corresponding InputEvent and owns
		// the following frame/cursor update. Report this as handled so the line
		// editor never falls back to an unsynchronized raw stdout write.
		return true
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
	// State-only semantics (physical paint retired in L3-2): treat the semantic
	// write as consumed while retaining exactly the state needed by
	// snapshots/recovery. No caller-provided writer is touched: a bytes.Buffer
	// is still a physical observation point for tests and must stay fenced just
	// like os.Stdout.
	output := normalizeFixedSurfaceOutputText(text)
	if trackSoft {
		s.noteSoftOutputLocked(text)
	} else {
		s.invalidateSoftOutputLocked()
	}
	s.appendHistoryWindowLocked(text)
	// Eager state-only handoff (L3-2): rows older than the visible output
	// region advance the logical frontier and soft-trim the dual-retained
	// window, exactly as the retired direct-scroll append did. No bytes are
	// emitted; the unified presenter owns scrollback rendering.
	if s.ownedViewport {
		s.commitExcessHistoryToScrollbackLocked()
	}
	s.legacyReserve.CursorOnBlankRow = strings.HasSuffix(output, "\n")
	return len(output), nil, true
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
		// Source-backed reflow remains committed in the logical history; the
		// unified presenter will render the replacement from its next frame.
		return true
	}
	// Unified mode: keep ownership metadata synchronized without allowing this
	// legacy rewrite path to emit cursor/clear/write bytes into the unified
	// screen.
	s.replaceOwnedHistorySuffixLocked(softLines, normalized)
	s.softOutput.Replace(normalized)
	s.legacyReserve.CursorOnBlankRow = false
	return true
}

// ClearCommittedHistoryForReplay wipes the visible rows of the committed
// history region (the whole output region above the bottom pane) and resets
// all history bookkeeping, so a post-backtrack replay starts from a clean
// slate instead of stacking on ghost rows of removed turns.
//
// Rows already handed off into native scrollback are physically irreversible
// and stay where they are; the caller's archive marker is the only
// distinction for those. A full-region wipe is safe because the replay
// re-prints every surviving canonical message afterwards.
//
// Returns false when the surface is disabled or nothing is committed (fresh
// session / already cleared). The erase uses the terminal's own writer, like
// the RewriteSoftOutputTail clear loop.
func (s *FixedBottomSurface) ClearCommittedHistoryForReplay() bool {
	if s == nil || s.terminal == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	if len(s.historyWindow) == 0 && !s.softOutput.Valid() {
		return false
	}
	height := s.lastHeight
	if height <= 0 {
		height = s.terminal.Height()
	}
	bottom := outputBottomRowForHeight(height, s.effectiveBottomRowsLocked(height))
	if bottom < 1 {
		return false
	}
	// Unified mode: clear retained-history projection without emitting erase
	// bytes into the unified screen.
	s.resetOwnedHistoryLocked()
	s.invalidateSoftOutputLocked()
	s.legacyReserve = renderengine.LegacyReserveState{}
	return true
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

// LegacyReserveStateForTest returns a copy of the render-engine-owned
// compatibility fallback state without exposing mutable surface fields.
func (s *FixedBottomSurface) LegacyReserveStateForTest() renderengine.LegacyReserveState {
	if s == nil {
		return renderengine.LegacyReserveState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.legacyReserve
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

// SetUIActorPoster 注入 UI actor 投递入口（Phase 1，实施指南任务 4）。
// 非 nil 时，所有 public BottomPane facade（band/status/prompt/editor/
// composer/popup）内部只投递 action（不直接 mutation），由 reducer 经
// Apply 调用同步实现生成相同输出（任务 5）。
// nil 恢复同步路径（legacy 测试与未接线环境行为不变）。
func (s *FixedBottomSurface) SetUIActorPoster(poster func(UIAction) bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uiPoster = poster
}

// postFacadeAction 是 facade 组的统一投递入口：未接线（uiPoster == nil）
// 时返回 false，调用方回退同步实现。
func (s *FixedBottomSurface) postFacadeAction(a UIAction) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	poster := s.uiPoster
	s.mu.Unlock()
	if poster == nil {
		return false
	}
	return poster(a)
}

func (s *FixedBottomSurface) ShowPrompt(line string) bool {
	if s.postFacadeAction(ShowPromptAction{Line: line}) {
		return true
	}
	return s.showPromptImpl(line)
}

// showPromptImpl 是 ShowPrompt 的同步实现（legacy adapter 的调用目标）。
func (s *FixedBottomSurface) showPromptImpl(line string) bool {
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
	// Logical prompt state committed above; the unified presenter renders from
	// retained state (legacy byte emission retired in L3-2).
	s.refreshPaintBookkeepingLocked()
	return true
}

func (s *FixedBottomSurface) ResetPrompt(line string, rows int) bool {
	return s.ResetPromptVersioned(line, rows, 0)
}

// ResetPromptVersioned is the lifecycle-fenced reset adapter used by the
// unified coordinator. A non-zero sequence invalidates editor snapshots
// measured before the reset while allowing a genuinely newer snapshot to win
// if queue coalescing moves it ahead of this action.
func (s *FixedBottomSurface) ResetPromptVersioned(line string, rows int, sequence uint64) bool {
	if s.postFacadeAction(ResetPromptAction{Line: line, Rows: rows, Sequence: sequence}) {
		return true
	}
	return s.resetPromptImpl(line, rows)
}

// resetPromptImpl is the reducer-side synchronous ResetPrompt adapter.
func (s *FixedBottomSurface) resetPromptImpl(line string, rows int) bool {
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
	// Unified presenter renders from retained state; still commit the logical
	// reset so snapshots/recovery stay coherent (legacy byte emission retired).
	s.promptLine = line
	s.promptInput = ""
	s.promptReservedRows = 1
	s.promptViewportStart = 0
	s.setPromptCursorToLineEndLocked(line)
	s.refreshPaintBookkeepingLocked()
	return true
}

func (s *FixedBottomSurface) SetPromptRows(rows int) bool {
	if s.postFacadeAction(SetPromptRowsAction{Rows: rows}) {
		return true
	}
	return s.setPromptRowsImpl(rows)
}

// setPromptRowsImpl is the reducer-side synchronous SetPromptRows adapter.
func (s *FixedBottomSurface) setPromptRowsImpl(rows int) bool {
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
	// Unified presenter renders from retained state; commit the new row budget
	// logically only (legacy byte emission retired in L3-2).
	s.promptReservedRows = rows
	s.refreshPaintBookkeepingLocked()
	return true
}

func (s *FixedBottomSurface) SetPromptNoticeLine(line string) bool {
	if s.postFacadeAction(SetPromptNoticeAction{Line: line}) {
		return true
	}
	return s.setPromptNoticeLineImpl(line)
}

// setPromptNoticeLineImpl is the reducer-side synchronous notice adapter.
func (s *FixedBottomSurface) setPromptNoticeLineImpl(line string) bool {
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
	// Unified presenter renders from retained state (legacy byte emission
	// retired in L3-2).
	s.refreshPaintBookkeepingLocked()
	return true
}

// SetActiveBand updates the in-progress stream viewport above the prompt/status.
// Lines are sanitized and capped to the adaptive row budget. Empty clears it.
// This never writes into scrollback; callers commit final content separately.
func (s *FixedBottomSurface) SetActiveBand(lines []string) bool {
	if s.postFacadeAction(s.newSetActiveBandAction(nil, lines)) {
		return true
	}
	return s.setActiveBandImpl(lines)
}

// setActiveBandImpl 是 SetActiveBand 的同步实现（legacy adapter 调用目标）。
func (s *FixedBottomSurface) setActiveBandImpl(lines []string) bool {
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
	if s.postFacadeAction(s.newSetActiveBandAction(lines, nil)) {
		return true
	}
	return s.setActiveBandStyledImpl(lines)
}

func (s *FixedBottomSurface) newSetActiveBandAction(lines []render.Line, rawLines []string) SetActiveBandAction {
	if s == nil {
		return SetActiveBandAction{Lines: lines, RawLines: rawLines}
	}
	s.mu.Lock()
	generation := s.activeBandGeneration
	s.mu.Unlock()
	return SetActiveBandAction{Lines: lines, RawLines: rawLines, Generation: generation}
}

// setActiveBandStyledImpl 是 SetActiveBandStyled 的同步实现。
func (s *FixedBottomSurface) setActiveBandStyledImpl(lines []render.Line) bool {
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
	s.activeBandLines = normalized
	s.activeBandStyled = cloneRenderLines(styled)
	s.reflowPromptViewportLocked()
	// L3-3: no physical repaint remains; the band state above is authoritative
	// and the unified presenter recomposes the frame. repaintActiveBandLocked
	// may return false under an active lease (state is retained, rendering
	// deferred); the update itself still succeeded.
	s.repaintActiveBandLocked()
	return true
}

// RefreshActiveBand refreshes the stored band state after a theme or terminal
// capability change, even when its structured content is unchanged. The
// unified presenter recomposes the frame from retained state.
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
	if !s.physicalWritesEnabledLocked() {
		// Active-band state was already updated by setActiveBand; suppress only
		// the legacy repaint side effect. The unified frame pump will render the
		// same state through TerminalSession.
		return true
	}
	// L3-3: the physical repaint (cursor save/hide + row emission) is retired
	// with the paint family. Keep the layout/paint bookkeeping coherent for
	// cursor placement and capability fallback; nothing is written here.
	s.applyLayoutLocked()
	s.refreshPaintBookkeepingLocked()
	return true
}

// ClearActiveBand removes the in-progress stream viewport.
func (s *FixedBottomSurface) ClearActiveBand() bool {
	if s.postFacadeAction(s.newClearActiveBandAction()) {
		return true
	}
	return s.clearActiveBand()
}

func (s *FixedBottomSurface) newClearActiveBandAction() ClearActiveBandAction {
	if s == nil {
		return ClearActiveBandAction{}
	}
	s.mu.Lock()
	s.activeBandGeneration++
	generation := s.activeBandGeneration
	s.mu.Unlock()
	return ClearActiveBandAction{Generation: generation}
}

// clearActiveBand releases the active viewport state (L3-3 state-only): the
// unified presenter recomposes the screen from retained history + the shrunk
// bottom reserve; no terminal bytes are emitted here.
func (s *FixedBottomSurface) clearActiveBand() bool {
	if s == nil || s.terminal == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.activeBandLines) == 0 && len(s.activeBandStyled) == 0 {
		return s.enabled
	}

	s.activeBandLines = nil
	s.activeBandStyled = nil
	s.reflowPromptViewportLocked()
	if !s.enabled {
		return false
	}

	if s.ownedViewport {
		// Re-assert trailing blank so shrink restore works. historyPartial is
		// false when the last write ended with a newline.
		if !s.historyPartial && len(s.historyWindow) > 0 {
			s.legacyReserve.CursorOnBlankRow = true
		} else {
			s.legacyReserve.CursorOnBlankRow = false
		}
	}
	return s.repaintActiveBandLocked()
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
	if s.postFacadeAction(SetPromptEditorStatusAction{Line: line}) {
		return true
	}
	return s.setPromptEditorStatusLineImpl(line)
}

// setPromptEditorStatusLineImpl is the reducer-side synchronous editor-status
// adapter. The public facade only posts its semantic intent when an actor is
// attached.
func (s *FixedBottomSurface) setPromptEditorStatusLineImpl(line string) bool {
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
	// Unified presenter renders from retained state (legacy byte emission
	// retired in L3-2).
	s.refreshPaintBookkeepingLocked()
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
	return s.TrackPromptInputStateVersioned(line, input, rows, cursorRow, cursorCol, 0)
}

// TrackPromptInputStateVersioned publishes display metadata together with the
// editor snapshot revision it was measured from. Unified rendering uses the
// revision to reject a delayed facade projection after newer keyboard input.
// The synchronous legacy adapter ignores sequence and preserves its FIFO
// behavior.
func (s *FixedBottomSurface) TrackPromptInputStateVersioned(line string, input string, rows int, cursorRow int, cursorCol int, sequence uint64) bool {
	if s.postFacadeAction(TrackPromptInputAction{
		Line: line, Input: input, Rows: rows, CursorRow: cursorRow, CursorCol: cursorCol, Sequence: sequence,
	}) {
		return true
	}
	return s.trackPromptInputStateImpl(line, input, rows, cursorRow, cursorCol)
}

// trackPromptInputStateImpl keeps the legacy incremental editor repaint
// policy. SetPromptInputState remains the explicit full prompt projection.
func (s *FixedBottomSurface) trackPromptInputStateImpl(line string, input string, rows int, cursorRow int, cursorCol int) bool {
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
	// Logical prompt state committed above; unified presenter renders from
	// retained state (legacy byte emission retired in L3-2).
	s.refreshPaintBookkeepingLocked()
	return true
}

func (s *FixedBottomSurface) SetPromptInputState(line string, input string, rows int, cursorRow int, cursorCol int) bool {
	return s.SetPromptInputStateVersioned(line, input, rows, cursorRow, cursorCol, 0)
}

// SetPromptInputStateVersioned is the full-paint counterpart of
// TrackPromptInputStateVersioned. See that method for the sequence contract.
func (s *FixedBottomSurface) SetPromptInputStateVersioned(line string, input string, rows int, cursorRow int, cursorCol int, sequence uint64) bool {
	if s.postFacadeAction(SetPromptStateAction{Line: line, Input: input, Rows: rows, CursorRow: cursorRow, CursorCol: cursorCol, Sequence: sequence}) {
		return true
	}
	return s.setPromptInputStateImpl(line, input, rows, cursorRow, cursorCol)
}

// setPromptInputStateImpl 是 SetPromptInputState 的同步实现。
func (s *FixedBottomSurface) setPromptInputStateImpl(line string, input string, rows int, cursorRow int, cursorCol int) bool {
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
	// Logical prompt state committed above; unified presenter renders from
	// retained state (legacy byte emission retired in L3-2).
	s.refreshPaintBookkeepingLocked()
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
	if !s.physicalWritesEnabledLocked() {
		// Unified presenter renders from retained cursor state.
		return true
	}
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
	// ShowPrompt 走 actor 队列后，紧随的 ClearPromptRows 必须同队列排队，
	// 否则同步清除对尚未渲染的 prompt 无效（mid-stream 残留 prompt 行）。
	if s.postFacadeAction(ClearPromptRowsAction{Rows: rows}) {
		return true
	}
	return s.clearPromptRowsImpl(rows)
}

// clearPromptRowsImpl 是 ClearPromptRows 的同步实现（legacy adapter 调用目标）。
func (s *FixedBottomSurface) clearPromptRowsImpl(rows int) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return false
	}
	// Unified presenter renders from retained prompt state (legacy byte
	// emission retired in L3-2).
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
	s.refreshPaintBookkeepingLocked()
	return true
}

func (s *FixedBottomSurface) ShowPopup(lines []string) {
	if s.postFacadeAction(ShowPopupAction{Lines: lines}) {
		return
	}
	s.showPopupImpl(lines)
}

// showPopupImpl 是 ShowPopup 的同步实现（legacy adapter 调用目标）。
func (s *FixedBottomSurface) showPopupImpl(lines []string) {
	if s == nil || s.terminal == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setActivePopupStateLocked(cloneAndSanitizePopupLines(lines), "", "", false)
	if !s.enabled {
		return
	}
	// Unified presenter renders from retained popup state (legacy byte
	// emission retired in L3-2).
	s.refreshPaintBookkeepingLocked()
}

func (s *FixedBottomSurface) ShowPopupPreserveCursor(lines []string) {
	if s.postFacadeAction(ShowPopupAction{Lines: lines, PreserveCursor: true}) {
		return
	}
	s.ShowPopupPreserveCursorForOwner(lines, "")
}

func (s *FixedBottomSurface) ShowPopupPreserveCursorForOwner(lines []string, owner string) {
	if s.postFacadeAction(ShowPopupAction{Lines: lines, PreserveCursor: true, Owner: owner}) {
		return
	}
	s.showPopupPreserveCursorForOwner(lines, owner, false)
}

func (s *FixedBottomSurface) ShowPopupPreserveCursorForOwnerBelowPrompt(lines []string, owner string) {
	if s.postFacadeAction(ShowPopupAction{Lines: lines, PreserveCursor: true, Owner: owner, BelowPrompt: true}) {
		return
	}
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
	// Unified presenter renders from retained popup state (legacy byte
	// emission retired in L3-2).
	s.refreshPaintBookkeepingLocked()
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
	s.nextPopupInstance++
	if s.nextPopupInstance == 0 {
		s.nextPopupInstance++
	}
	handle := PopupHandle{owner: owner, instance: s.nextPopupInstance}
	s.mu.Unlock()

	// The handle is allocated before posting. This preserves the synchronous
	// API without letting an external popup caller mutate the surface directly:
	// later Update/Clear actions carry the same token and therefore stay FIFO
	// behind this begin action.
	if s.postFacadeAction(ShowPopupAction{
		Lines:    lines,
		Owner:    owner,
		Prompt:   prompt,
		Input:    true,
		Handle:   &handle,
		Viewport: clonePopupViewportSpec(viewport),
	}) {
		return handle
	}
	_ = s.beginPopupInputForHandleImpl(lines, prompt, handle, viewport)
	return handle
}

// beginPopupInputForHandleImpl is the reducer-side counterpart of
// beginPopupInputForOwner. The token has already been allocated at the facade
// boundary, so this function never generates identity or re-enters the actor.
func (s *FixedBottomSurface) beginPopupInputForHandleImpl(lines []string, prompt string, handle PopupHandle, viewport *PopupViewportSpec) bool {
	if s == nil || s.terminal == nil || !handle.Valid() {
		return false
	}
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.beginPopupInstanceLocked(cloneAndSanitizePopupLines(lines), prompt, handle, viewport) || !s.enabled {
		return true
	}
	// Unified presenter renders from retained popup state (legacy byte
	// emission retired in L3-2).
	s.refreshPaintBookkeepingLocked()
	return true
}

func (s *FixedBottomSurface) UpdatePopupInputForHandle(handle PopupHandle, lines []string, prompt string, preserveCursor bool) bool {
	if s == nil || s.terminal == nil || !handle.Valid() {
		return false
	}
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	if s.postFacadeAction(UpdatePopupAction{
		Handle:         handle,
		Lines:          lines,
		Prompt:         prompt,
		PreserveCursor: preserveCursor,
	}) {
		return true
	}
	return s.updatePopupInputForHandleImpl(handle, lines, prompt, preserveCursor)
}

func (s *FixedBottomSurface) updatePopupInputForHandleImpl(handle PopupHandle, lines []string, prompt string, preserveCursor bool) bool {
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
	// Unified presenter renders from retained popup state (legacy byte
	// emission retired in L3-2).
	s.refreshPaintBookkeepingLocked()
	return true
}

func (s *FixedBottomSurface) showPopupInputForOwner(lines []string, prompt string, owner string, preserveCursor bool) {
	if s == nil || s.terminal == nil {
		return
	}
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	if s.postFacadeAction(ShowPopupAction{
		Lines:          lines,
		PreserveCursor: preserveCursor,
		Owner:          owner,
		Prompt:         prompt,
		Input:          true,
	}) {
		return
	}
	s.showPopupInputForOwnerImpl(lines, prompt, owner, preserveCursor)
}

func (s *FixedBottomSurface) showPopupInputForOwnerImpl(lines []string, prompt string, owner string, preserveCursor bool) {
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
	// Unified presenter renders from retained popup state (legacy byte
	// emission retired in L3-2).
	s.refreshPaintBookkeepingLocked()
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
	if s.postFacadeAction(ClearPopupAction{}) {
		return
	}
	s.clearPopupImpl()
}

// clearPopupImpl 是 ClearPopup 的同步实现（legacy adapter 调用目标）。
func (s *FixedBottomSurface) clearPopupImpl() {
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
	if !s.physicalWritesEnabledLocked() {
		// Logical popup state cleared above; unified presenter renders from
		// retained state. Suppress legacy erase bytes.
		s.clearPopupRenderStateLocked()
		return
	}
	WithTerminalWriteLock(func() {
		s.applyLayoutLocked()
		if s.ownedViewport {
			s.refreshPaintBookkeepingLocked()
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
	if s.postFacadeAction(ClearPopupAction{PreserveCursor: true}) {
		return
	}
	s.clearPopupPreserveCursorImpl()
}

// clearPopupPreserveCursorImpl 是 ClearPopupPreserveCursor 的同步实现。
func (s *FixedBottomSurface) clearPopupPreserveCursorImpl() {
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
	if !s.physicalWritesEnabledLocked() {
		// Logical popup state cleared above; unified presenter renders from
		// retained state. Suppress legacy erase bytes.
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
			s.refreshPaintBookkeepingLocked()
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
	if s.postFacadeAction(ClearPopupAction{PreserveCursor: true, Owner: owner}) {
		return
	}
	s.clearPopupForOwnerPreserveCursorImpl(owner)
}

func (s *FixedBottomSurface) clearPopupForOwnerPreserveCursorImpl(owner string) {
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
	if !s.physicalWritesEnabledLocked() {
		// Popup stack restored above; unified presenter renders from retained
		// state. Suppress legacy erase-repaint sequence.
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
			s.refreshPaintBookkeepingLocked()
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
	if s.postFacadeAction(ClearPopupAction{PreserveCursor: true, Handle: &handle}) {
		return
	}
	s.clearPopupHandlePreserveCursorImpl(handle)
}

func (s *FixedBottomSurface) clearPopupHandlePreserveCursorImpl(handle PopupHandle) {
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
	if !s.physicalWritesEnabledLocked() {
		// Popup stack restored above; unified presenter renders from retained
		// state. Suppress legacy erase-repaint sequence.
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
			s.refreshPaintBookkeepingLocked()
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
	if s.postFacadeAction(SetStatusModelAction{Status: model}) {
		return
	}
	s.setStatusModelImpl(model)
}

// setStatusModelImpl is the reducer-side synchronous persistent-status
// adapter. It deliberately leaves the dynamic row unchanged.
func (s *FixedBottomSurface) setStatusModelImpl(model style.StatusLineModel) {
	if s == nil || s.terminal == nil {
		return
	}
	model = sanitizeStatusLineModel(model)
	if style.StatusLineBlank(model) {
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
	if s.postFacadeAction(SetDynamicStatusModelAction{Dynamic: cloneStatusLineModel(model)}) {
		return
	}
	s.setDynamicStatusModelImpl(model)
}

// setDynamicStatusModelImpl is the reducer-side synchronous dynamic-status
// adapter. It deliberately leaves the persistent footer unchanged.
func (s *FixedBottomSurface) setDynamicStatusModelImpl(model *style.StatusLineModel) {
	if s == nil || s.terminal == nil {
		return
	}
	var normalized *style.StatusLineModel
	if model != nil {
		value := sanitizeStatusLineModel(*model)
		if !style.StatusLineBlank(value) {
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
	if s.postFacadeAction(SetStatusModelsAction{Status: status, Dynamic: dynamic}) {
		return
	}
	s.setStatusModelsImpl(status, dynamic)
}

// SetSessionIDLine sets the plain-text session ID line shown on the second
// status row. It is a persistent label that is hidden while a popup or composer
// is active.
func (s *FixedBottomSurface) SetSessionIDLine(line string) bool {
	if s.postFacadeAction(SetSessionIDLineAction{Line: line}) {
		return true
	}
	return s.setSessionIDLineImpl(line)
}

// setSessionIDLineImpl is the synchronous implementation for SetSessionIDLine.
func (s *FixedBottomSurface) setSessionIDLineImpl(line string) bool {
	if s == nil || s.terminal == nil {
		return false
	}
	line = strings.TrimRight(SanitizeTerminalText(line), "\r\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionIDLine == line {
		return true
	}
	s.sessionIDLine = line
	s.reflowPromptViewportLocked()
	s.repaintStatusUpdateLocked()
	return true
}

// setStatusModelsImpl 是 SetStatusModels 的同步实现（legacy adapter 调用目标）。
func (s *FixedBottomSurface) setStatusModelsImpl(status style.StatusLineModel, dynamic *style.StatusLineModel) {
	if s == nil || s.terminal == nil {
		return
	}
	status = sanitizeStatusLineModel(status)
	if style.StatusLineBlank(status) {
		status = style.StatusLineModel{State: style.RunReady}
	}
	var normalizedDynamic *style.StatusLineModel
	if dynamic != nil {
		value := sanitizeStatusLineModel(*dynamic)
		if !style.StatusLineBlank(value) {
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
	if !s.physicalWritesEnabledLocked() {
		// Status model already updated by the caller; unified presenter
		// renders the status row from retained state.
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
	if s.postFacadeAction(SetComposerPreviewAction{Line: line}) {
		return
	}
	s.setComposerPreviewImpl(line)
}

// setComposerPreviewImpl is the reducer-side synchronous composer-preview
// adapter. The preview is an overlay transition, never transcript content.
func (s *FixedBottomSurface) setComposerPreviewImpl(line string) {
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
	if !s.physicalWritesEnabledLocked() {
		// Unified presenter renders from retained composer state.
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
	if s.postFacadeAction(ClearComposerPreviewAction{}) {
		return
	}
	s.clearComposerPreviewImpl()
}

// clearComposerPreviewImpl is the reducer-side synchronous composer cleanup
// adapter. It intentionally retains any active or suspended popup layer.
func (s *FixedBottomSurface) clearComposerPreviewImpl() {
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
	if !s.physicalWritesEnabledLocked() {
		// Unified presenter renders from retained composer state.
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

// ReleaseActiveBandForFinalizedOutput synchronously removes the transient
// projection (state-only since L3-3; the legacy queued-action fence retired
// with the Apply sink). It is intentionally narrower than a generic facade
// escape hatch: only the terminal transaction that immediately commits a
// finalized transcript cell may use it.
func (s *FixedBottomSurface) ReleaseActiveBandForFinalizedOutput() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	s.activeBandGeneration++
	s.mu.Unlock()
	return s.clearActiveBand()
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
	width, height := s.terminal.RefreshSize()
	s.applyLayoutWithSizeLocked(width, height)
}

// applyLayoutWithSizeLocked applies layout using a size that was already probed
// in the same lock hold (e.g. syncTerminalGeometry). Callers that have not just
// refreshed must use applyLayoutLocked so geometry stays current.
func (s *FixedBottomSurface) applyLayoutWithSizeLocked(width, height int) {
	if s.ownedViewport {
		s.applyOwnedViewportGeometryLocked(width, height)
		return
	}
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	bottomRows := s.effectiveBottomRowsLocked(height)
	sizeChanged := width != s.lastWidth || height != s.lastHeight
	s.lastWidth = width
	s.lastHeight = height
	s.lastBottomRows = bottomRows
	// Retired legacy path: keep layout bookkeeping coherent for the
	// compatibility surface, but no scroll-region sequence is emitted.
	s.legacyReserve.ScrollCompensatedRows = 0
	s.legacyReserve.PendingScrollDownRows = 0
	if sizeChanged {
		s.legacyReserve.OutputScrollDebtRows = 0
	}
}

func (s *FixedBottomSurface) appendApplyLayoutSequenceLocked(builder *strings.Builder) {
	if builder == nil {
		return
	}
	width, height := s.terminal.RefreshSize()
	s.applyLayoutWithSizeLocked(width, height)
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
	if height <= 0 {
		height = 24
	}
	bottomRows := s.effectiveBottomRowsLocked(height)
	sizeChanged := width != s.lastWidth || height != s.lastHeight
	s.lastWidth = width
	s.lastHeight = height
	s.lastBottomRows = bottomRows
	// Retired legacy path: keep layout bookkeeping coherent for the
	// compatibility surface, but no scroll-region sequence is emitted.
	s.legacyReserve.ScrollCompensatedRows = 0
	s.legacyReserve.PendingScrollDownRows = 0
	if sizeChanged {
		s.legacyReserve.OutputScrollDebtRows = 0
	}
}

func (s *FixedBottomSurface) applyOwnedViewportGeometryLocked(width, height int) {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	bottomRows := s.effectiveBottomRowsLocked(height)
	firstLayout := s.lastWidth == 0 || s.lastHeight == 0
	sizeChanged := width != s.lastWidth || height != s.lastHeight
	bottomChanged := bottomRows != s.lastBottomRows
	s.lastWidth = width
	s.lastHeight = height
	s.lastBottomRows = bottomRows
	// Owned frames recompose absolute rows; legacy compensation state must not
	// leak across a capability-path transition.
	s.legacyReserve.ScrollCompensatedRows = 0
	s.legacyReserve.PendingScrollDownRows = 0
	if sizeChanged {
		s.legacyReserve.OutputScrollDebtRows = 0
	}
	// Only a real terminal resize invalidates the trailing-blank marker:
	// band/popup grow-shrink must keep it so Compose can restore the owned
	// transcript tail. A first-ever geometry application is initialization,
	// not a resize: L3-2 retires eager prompt-time layout, so the first
	// application can arrive on the first post-write repaint.
	if sizeChanged && !firstLayout {
		s.legacyReserve.CursorOnBlankRow = false
	}
	// Owned frames address absolute rows and must not inherit a narrower legacy
	// DECSTBM region. Reset before the first/resize repaint so the status row and
	// other bottom-pane rows are writable in the host terminal and vt.Screen.
	if sizeChanged || s.viewportBackend == nil {
		if s.leaseID == 0 && s.physicalWritesEnabledLocked() {
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

func (s *FixedBottomSurface) renderStatusLocked() {
	if !s.enabled {
		return
	}
	if !s.physicalWritesEnabledLocked() {
		// Unified presenter owns the screen; suppress legacy status paint.
		return
	}
	if s.leaseID != 0 {
		return
	}
	if s.ownedViewport {
		s.refreshPaintBookkeepingLocked()
		return
	}
	// Legacy body removed (Phase 6): owned mode handles status rendering via
	// renderOwnedViewportLocked or TerminalSession presenter. This codepath
	// was only reachable in legacy test mode (ownedViewport=false).
}

func (s *FixedBottomSurface) renderPopupLocked() {
	if !s.enabled {
		return
	}
	if !s.physicalWritesEnabledLocked() {
		// Unified presenter owns the screen; suppress legacy popup paint.
		return
	}
	if s.leaseID != 0 {
		return
	}
	if s.ownedViewport {
		s.refreshPaintBookkeepingLocked()
		return
	}
	// Legacy body removed (Phase 6): owned mode handles popup rendering via
	// renderOwnedViewportLocked or TerminalSession presenter. This codepath
	// was only reachable in legacy test mode (ownedViewport=false).
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
	// Move to the last row of the popup block. The popup start must use the
	// same bottom gap as popupPaintPlanLocked, otherwise a body-only popup
	// (merged ask_user_question answer) is targeted past its own tail, on the
	// prompt-area rows that now sit below it.
	row := s.popupStartRowLocked(len(visibleLines)+composerRows, state.popupBottomGapRowCount()) + len(visibleLines) + composerRows - 1
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
		row := s.statusRowLocked() - 1 - state.sessionStatusVisibleRowCount() - state.promptBottomMarginRowCount()
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
	rows := 1 + state.sessionStatusVisibleRowCount() + state.popupVisibleRowCount(s.terminal.Height())
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
	if !s.physicalWritesEnabledLocked() {
		// Unified presenter owns the screen; suppress legacy prompt rows paint.
		return
	}
	if s.leaseID != 0 {
		return
	}
	if s.ownedViewport {
		s.refreshPaintBookkeepingLocked()
		return
	}
	// Legacy body removed (Phase 6): owned mode handles prompt rendering via
	// renderOwnedViewportLocked or TerminalSession presenter. This codepath
	// was only reachable in legacy test mode (ownedViewport=false).
}

// OwnedViewport reports whether the production owned-viewport renderer is active.
func (s *FixedBottomSurface) OwnedViewport() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownedViewport
}

// HasActivePopup reports whether another bottom-pane interaction currently
// owns input. Fullscreen transcript entry uses it to avoid stealing approval,
// completion, or modal composer input from the primary screen.
func (s *FixedBottomSurface) HasActivePopup() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.popupLines) > 0 || s.popupOwner != "" || s.popupInstance != 0 || len(s.popupStack) > 0
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
	StatusModel        *style.StatusLineModel
	DynamicStatusModel *style.StatusLineModel
	// ReserveDynamicStatusRow pins the dynamic status row into the composer band
	// independently of the current model content. The unified composer must keep
	// that row stable: if the reservation follows "model is non-nil and non-blank",
	// the band height — and with it OutputBottomRow and the history capacity —
	// changes with transient content, the history region takes over the boundary
	// row, and the dynamic status row is washed out by history rendering.
	// Legacy surface layouts leave this false and keep their historical behavior.
	ReserveDynamicStatusRow bool
	// SessionIDLine is the plain text shown on the second status row (directly
	// above the persistent status row). It is hidden while a popup or composer
	// is active so those overlays keep the same reserved bottom area.
	SessionIDLine string
	// Prompt fields are semantic overlay inputs. Row allocation remains pure
	// BottomPaneState derivation; physical cursor placement belongs to the
	// presenter. They are also populated in AppState during the Phase 2 bridge.
	PromptLine  string
	PromptInput string
	// PromptInputSequence is the newest versioned editor snapshot accepted by
	// the AppState reducer. PromptInputClearedThrough is an explicit submission
	// fence: delayed full-state projections at or below it cannot resurrect text
	// that was already submitted. Zero-valued legacy facade actions remain FIFO.
	PromptInputSequence       uint64
	PromptInputClearedThrough uint64
	// PromptCursor is the logical rune offset supplied by InputEvent. The
	// visual cursor fields below are derived from it and geometry by Layout.
	PromptCursor            int
	PromptCursorKnown       bool
	PromptCursorAbsoluteRow int
	PromptCursorRow         int
	PromptCursorCol         int
	PromptTotalRows         int
	PromptViewportStart     int
	// PromptRowsOverride is a legacy explicit reservation request. Zero means
	// derive the viewport from PromptInput; a positive value is retained across
	// resize so Layout does not mistake a clipped display allocation for source.
	PromptRowsOverride int
	PromptVisible      bool
	PasteActive        bool
	Focus              BottomFocus
	PopupLines         []string
	PopupOwner         string
	// PopupInstance identifies a BeginPopupInputForOwner* token. The active
	// popup is represented by the fields above; PopupStack retains suspended
	// layers so closing a modal can be derived from AppState rather than old
	// terminal pixels.
	PopupInstance          uint64
	PopupStack             []PopupLayer
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

// PopupLayer is the semantic state of one suspended bottom-pane overlay. It
// contains no physical row/cursor cache, so it can be restored by pure Layout
// after another popup closes or a terminal repaint/resize occurs.
type PopupLayer struct {
	Lines        []string
	Owner        string
	Instance     uint64
	Viewport     *PopupViewportSpec
	ComposerLine string
	BelowPrompt  bool
	ReservedRows int
}

func clonePopupLayers(layers []PopupLayer) []PopupLayer {
	if len(layers) == 0 {
		return nil
	}
	clone := make([]PopupLayer, len(layers))
	for index, layer := range layers {
		clone[index] = layer
		clone[index].Lines = append([]string(nil), layer.Lines...)
		clone[index].Viewport = clonePopupViewportSpec(layer.Viewport)
	}
	return clone
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

func (s BottomPaneState) statusVisibleRowCount() int {
	if s.StatusModel == nil {
		return 0
	}
	if style.StatusLineBlank(*s.StatusModel) {
		return 0
	}
	return 1
}

func (s BottomPaneState) sessionStatusVisibleRowCount() int {
	if strings.TrimSpace(s.SessionIDLine) == "" {
		return 0
	}
	if s.composerVisibleRowCount() > 0 {
		return 0
	}
	if len(s.PopupLines) > 0 || s.PopupReservedRows > 0 {
		return 0
	}
	return 1
}

func (s BottomPaneState) promptNoticeVisibleRowCount() int {
	if s.composerVisibleRowCount() > 0 || s.promptReservedRowCount() < 1 {
		return 0
	}
	return s.promptNoticeLinesRowCount()
}

// promptNoticeLinesRowCount reports len(promptNoticeLines()) without
// materializing the line slice. The row-count chain asks for this several times
// per frame (popupBottomGapRowCount → … → here) and the width planner asks for
// the ungated count, so both paths share this one counter instead of each
// rebuilding the lines only to measure them.
func (s BottomPaneState) promptNoticeLinesRowCount() int {
	count := promptNoticeDisplayLineCount(s.PromptNoticeLine)
	if strings.TrimSpace(s.PromptEditorStatusLine) != "" {
		count++
	}
	return count
}

func (s BottomPaneState) dynamicStatusVisibleRowCount() int {
	if s.composerVisibleRowCount() > 0 {
		return 0
	}
	if s.DynamicStatusModel == nil || style.StatusLineBlank(*s.DynamicStatusModel) {
		// 常驻预留（统一 composer）：模型为空/blank 时该行仍然占位并渲染为空行。
		// 预留一旦跟随内容变化，band 高度就会抖动，OutputBottomRow 与历史容量随之
		// 变化，边界行在「历史」与「composer」之间来回切换——用户看到的就是动态状态
		// 栏被历史消息覆盖冲刷（现场字节流里 band 高度在 38/39 间漂移）。
		// Clone/DeriveBottomPaneState 按值复制标量字段，因此该标记会随状态快照传播。
		if !s.ReserveDynamicStatusRow {
			return 0
		}
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
	line = normalizePromptNoticeLine(line)
	if line == "" {
		return nil
	}
	return strings.Split(line, "\n")
}

// promptNoticeDisplayLineCount reports how many rows promptNoticeDisplayLines
// would return, without allocating the slice.
func promptNoticeDisplayLineCount(line string) int {
	line = normalizePromptNoticeLine(line)
	if line == "" {
		return 0
	}
	return strings.Count(line, "\n") + 1
}

// normalizePromptNoticeLine canonicalizes newlines and trims trailing ones. It
// returns "" when the notice has no visible text. Both the line list and the
// count derive from it, so they cannot disagree.
func normalizePromptNoticeLine(line string) string {
	line = strings.ReplaceAll(line, "\r\n", "\n")
	line = strings.ReplaceAll(line, "\r", "\n")
	line = strings.TrimRight(line, "\n")
	if strings.TrimSpace(line) == "" {
		return ""
	}
	return line
}

func (s *FixedBottomSurface) bottomPaneStateLocked() BottomPaneState {
	topMarginRows, bottomMarginRows := chatComposerVerticalMargins(s.terminal.Height())
	state := BottomPaneState{
		StatusModel:             cloneStatusLineModel(s.statusModel),
		DynamicStatusModel:      cloneStatusLineModel(s.dynamicStatusModel),
		SessionIDLine:           s.sessionIDLine,
		PromptLine:              s.promptLine,
		PromptInput:             s.promptInput,
		PromptCursorAbsoluteRow: s.promptViewportStart + s.promptCursorRow,
		PromptCursorRow:         s.promptCursorRow,
		PromptCursorCol:         s.promptCursorCol,
		PromptViewportStart:     s.promptViewportStart,
		PromptVisible:           strings.TrimSpace(s.promptLine) != "" && s.promptReservedRows > 0,
		PasteActive:             false,
		PopupLines:              append([]string(nil), s.popupLines...),
		PopupOwner:              s.popupOwner,
		PopupInstance:           s.popupInstance,
		PopupBelowPrompt:        s.popupBelowPrompt,
		PopupReservedRows:       s.popupReservedRows,
		PopupViewport:           clonePopupViewportSpec(s.popupViewport),
		ComposerLine:            s.composerLine,
		PromptNoticeLine:        s.promptNoticeLine,
		PromptEditorStatusLine:  s.promptEditorStatusLine,
		ActiveBandLines:         append([]string(nil), s.activeBandLines...),
		ActiveBandStyled:        cloneRenderLines(s.activeBandStyled),
		ActiveBandMaxRows:       s.ActiveBandRowBudget(),
		ActiveBandTopGapRows:    activeBandTopGap(s.terminal.Height()),
		PromptReservedRows:      s.promptReservedRows,
		PromptTopMarginRows:     topMarginRows,
		PromptBottomMarginRows:  bottomMarginRows,
	}
	if len(s.popupStack) > 0 {
		state.PopupStack = make([]PopupLayer, 0, len(s.popupStack))
		for _, popup := range s.popupStack {
			state.PopupStack = append(state.PopupStack, PopupLayer{
				Lines:        append([]string(nil), popup.lines...),
				Owner:        popup.owner,
				Instance:     popup.instance,
				Viewport:     clonePopupViewportSpec(popup.viewport),
				ComposerLine: popup.composerLine,
				BelowPrompt:  popup.popupBelowPrompt,
				ReservedRows: popup.popupReservedRows,
			})
		}
	}
	// 与 controller/BottomPaneState 用同一条归属规则：只有拥有输入行的 popup
	// 才接管光标；仅渲染正文的 popup（提问正文）让底部 prompt 持有光标。
	state.Focus = bottomFocusForPopup(PopupLayer{
		Lines:        state.PopupLines,
		Owner:        state.PopupOwner,
		Instance:     state.PopupInstance,
		ComposerLine: state.ComposerLine,
	}, state.PromptVisible)
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
		w := render.RuneWidth(r)
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
		// popup 行是“一条 = 一个物理行”的契约：正文里的硬换行必须切成多条。
		// 行内换行只被 TrimRight 掉尾部、宽度测量又把它记为 0 列，于是提问正文
		// （LLM 给出的多段文本）会以“一行”进入底区行计划，终端却把它渲染成
		// 多行——盒子边框只出现在折行首/尾，盒子高度与下方 Running/Waiting/
		// prompt 行整体错位，卡片在物理屏上无法成形。
		if !strings.ContainsAny(line, "\r\n") {
			out = append(out, line)
			continue
		}
		for _, segment := range strings.Split(strings.ReplaceAll(line, "\r\n", "\n"), "\n") {
			if strings.TrimSpace(segment) == "" {
				out = append(out, "")
				continue
			}
			out = append(out, segment)
		}
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
		width := render.RuneWidth(r)
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
// commitExcessHistoryToScrollbackLocked hands off any history rows older than
// the current visible output region into native scrollback (once) and reports
// whether a handoff actually happened. A true result means the terminal's
// output region scrolled; callers that then repaint through a diffing Flush
// without CommitRange must Invalidate the viewport backend so the stale front
// buffer cannot produce a wrong delta.
func (s *FixedBottomSurface) commitExcessHistoryToScrollbackLocked() bool {
	if s == nil || s.terminal == nil || len(s.historyWindow) == 0 {
		return false
	}
	if s.leaseID != 0 {
		// Alternate-screen lease active: primary flush is suspended and the
		// leased alternate screen owns the output region. Native scrollback
		// handoff bytes must not be emitted here; the release repaint replays
		// retained state and commits pending history then.
		return false
	}
	if s.terminal.width < 1 || s.terminal.height < 1 {
		// State-only stand-in for the retired physical insert: a terminal with
		// invalid geometry cannot accept a handoff, so the logical boundary
		// must not advance (a failed insert must never lose transcript rows).
		return false
	}
	handedOff := false
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
		// L3-2: physical scrollback emission retired (state-only). Advance the
		// logical handoff frontier and drop soft-output ownership without
		// emitting bytes; the retained window keeps the dual-retain accounting.
		s.handoffFrontier.AdvanceTo(needHandedOff, len(s.historyWindow))
		if s.softOutput.Valid() && (!softSuffixOwned || s.handoffFrontier.Value() > softStart) {
			// Native scrollback is immutable. Once handoff reaches any part of
			// the rewrite window, abandon that ownership before a later resize
			// can replace already-emitted history with a different rendering.
			s.invalidateSoftOutputLocked()
		}
		handedOff = true
	}

	s.softTrimRetainedHistoryLocked(keepForRestore)
	return handedOff
}

// softTrimRetainedHistoryLocked drops the oldest retained rows past keep,
// limited to rows already handed into native scrollback. Never trim an
// unhanded row: when physical-row handoff is deferred for a wrapped line,
// those rows are the only durable copy.
func (s *FixedBottomSurface) softTrimRetainedHistoryLocked(keep int) {
	if s == nil || keep < 0 {
		return
	}
	if len(s.historyWindow) <= keep {
		return
	}
	drop := len(s.historyWindow) - keep
	if drop > s.handoffFrontier.Value() {
		drop = s.handoffFrontier.Value()
	}
	if drop <= 0 {
		return
	}
	s.historyWindow = append([]string(nil), s.historyWindow[drop:]...)
	s.handoffFrontier.TrimPrefix(drop, len(s.historyWindow))
}
