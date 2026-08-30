package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

var (
	ErrInvalidTerminalFrame  = errors.New("invalid terminal frame")
	ErrStaleTerminalFrame    = errors.New("stale terminal frame generation")
	ErrTerminalWriterMissing = errors.New("terminal session writer is unavailable")
	ErrTerminalWriterPanic   = errors.New("terminal session writer panicked")
	ErrInvalidHistoryHandoff = errors.New("invalid terminal history handoff")
	// ErrTerminalAlternateScreenBusy proves that a different fullscreen owner
	// still controls this physical terminal session. The caller must release
	// that lease rather than emit a second DEC 1049 enter sequence.
	ErrTerminalAlternateScreenBusy = errors.New("terminal alternate-screen lease is already active")
	// ErrTerminalAlternateScreenLease rejects a stale or fabricated lease id
	// before it can write into a fullscreen buffer it does not own.
	ErrTerminalAlternateScreenLease = errors.New("terminal alternate-screen lease is not active")
)

// TerminalFramePlan is the physical-frame input derived from exactly one
// AppState snapshot. It carries no terminal cache and never contains history
// ownership progress: those remain respectively in TerminalSession and the
// reducer-owned HistoryEffectQueue.
//
// In unified interactive mode this is the only primary-frame input consumed
// by TerminalSessionPresenter. FixedBottomSurface may still retain compatibility
// state, but its physical writer is fenced before the presenter attaches.
type TerminalFramePlan struct {
	LayoutGeneration uint64
	Theme            style.ThemeContext
	Geometry         GeometryState
	Lease            LeaseState
	OutputBottomRow  int
	Rows             []AppScreenRow
	// RenderRows is the structured counterpart of Rows. It is either empty
	// for a legacy/test plain plan or has exactly one render.Line per physical
	// row. Keeping both during the migration makes text parity explicit while
	// allowing the future primary presenter to retain semantic styles.
	RenderRows []render.Line
	Cursor     *AppCursor
}

// ComposeTerminalFramePlan derives an immutable physical-frame plan from a
// single AppState value. AppTextLayout already reuses LayoutAppScreen, so this
// path has no second row-allocation or text-wrap algorithm.
func ComposeTerminalFramePlan(state AppState) TerminalFramePlan {
	frame := ComposeAppRenderFrame(state)
	rows := make([]AppScreenRow, len(frame.Rows))
	renderRows := make([]render.Line, len(frame.Rows))
	for index, row := range frame.Rows {
		rows[index] = row.Screen
		renderRows[index] = cloneAppRenderLine(row.Line)
	}
	return TerminalFramePlan{
		LayoutGeneration: frame.LayoutGeneration,
		Theme:            cloneThemeContext(state.Theme),
		Geometry:         frame.Geometry,
		Lease:            frame.Lease,
		OutputBottomRow:  frame.OutputBottomRow,
		Rows:             rows,
		RenderRows:       renderRows,
		Cursor:           cloneTerminalCursor(frame.Cursor),
	}
}

func (p TerminalFramePlan) Valid() bool {
	return p.LayoutGeneration != 0 && p.Geometry.Width > 0 && p.Geometry.Height > 0 &&
		p.Geometry.Generation == p.LayoutGeneration && p.OutputBottomRow >= 0 &&
		p.OutputBottomRow <= p.Geometry.Height && len(p.Rows) == p.Geometry.Height &&
		(len(p.RenderRows) == 0 || len(p.RenderRows) == p.Geometry.Height)
}

// TerminalTransactionPlan is the immutable physical output of one presenter
// turn. History, when present, is emitted before the frame diff and cursor in
// the same terminal write. A history effect is deliberately optional because a
// recovery frame must be allowed to establish a Known viewport before an older
// scrollback range can be handed off safely.
//
// The plan has no ownership progress. Claim/Ack/Fail remains reducer-owned in
// HistoryEffectQueue; this value merely gives the physical writer one coherent
// transaction boundary for an already-claimed HistoryCommit and one AppState
// derived viewport frame.
type TerminalTransactionPlan struct {
	Frame            TerminalFramePlan
	History          *HistoryCommit
	BootstrapHistory []HistoryCommit
	// ResetScrollback requests a source-backed replacement of an uncertain
	// native-scrollback projection in the current layout generation.
	ResetScrollback bool
	// TerminalEpoch is the reducer-confirmed scrollback generation. A replaced
	// TerminalSession advances from this value instead of restarting at one.
	TerminalEpoch uint64
}

// ComposeTerminalTransactionPlan derives an immutable frame and optionally
// attaches one already-claimed HistoryCommit. The commit is cloned because its
// render lines are reducer-owned payload until the eventual Ack.
func ComposeTerminalTransactionPlan(state AppState, history *HistoryCommit, bootstrapHistory ...[]HistoryCommit) TerminalTransactionPlan {
	return composeTerminalTransactionPlan(ComposeTerminalFramePlan(state), state, history, bootstrapHistory...)
}

// composeTerminalViewportTransactionPlan is the unified primary-session hot
// path. The exported composer retains its complete-frame contract for debug,
// tests and non-session callers; only the native-scrollback owner may omit the
// finalized transcript from the mutable viewport.
func composeTerminalViewportTransactionPlan(state AppState, history *HistoryCommit, bootstrapHistory ...[]HistoryCommit) TerminalTransactionPlan {
	return composeTerminalTransactionPlan(composeTerminalViewportFramePlan(state), state, history, bootstrapHistory...)
}

func composeTerminalTransactionPlan(frame TerminalFramePlan, state AppState, history *HistoryCommit, bootstrapHistory ...[]HistoryCommit) TerminalTransactionPlan {
	plan := TerminalTransactionPlan{
		Frame:         frame,
		TerminalEpoch: state.HistoryEffects.TerminalEpoch,
	}
	if history != nil {
		clone := history.Clone()
		plan.History = &clone
	}
	if len(bootstrapHistory) > 0 && len(bootstrapHistory[0]) > 0 {
		plan.BootstrapHistory = cloneHistoryCommits(bootstrapHistory[0])
	}
	return plan
}

// composeTerminalViewportFramePlan omits finalized transcript cells from the
// mutable primary-frame projection. In unified mode those rows are delivered
// exclusively by tokenized HistoryCommit effects and retained by the terminal
// itself; laying out the complete transcript again for every streaming delta
// only allocates a discarded top region. Active, prompt, popup and status state
// remain intact because they live outside TranscriptState.
func composeTerminalViewportFramePlan(state AppState) TerminalFramePlan {
	state.Transcript = TranscriptState{}
	return ComposeTerminalFramePlan(state)
}

func ComposeScrollbackReconciliationPlan(state AppState) TerminalTransactionPlan {
	plan := ComposeTerminalTransactionPlan(state, nil)
	plan.ResetScrollback = true
	return plan
}

func composeTerminalViewportScrollbackReconciliationPlan(state AppState) TerminalTransactionPlan {
	plan := composeTerminalViewportTransactionPlan(state, nil)
	plan.ResetScrollback = true
	return plan
}

func (p TerminalTransactionPlan) Valid() bool {
	if !p.Frame.Valid() || (p.History != nil && !p.History.Valid()) {
		return false
	}
	for _, commit := range p.BootstrapHistory {
		if !commit.Valid() {
			return false
		}
	}
	return true
}

func (p TerminalTransactionPlan) Clone() TerminalTransactionPlan {
	p.Frame.Theme = cloneThemeContext(p.Frame.Theme)
	p.Frame.Rows = cloneTerminalFrameRows(p.Frame.Rows)
	p.Frame.RenderRows = cloneRenderLines(p.Frame.RenderRows)
	p.Frame.Cursor = cloneTerminalCursor(p.Frame.Cursor)
	if p.History != nil {
		clone := p.History.Clone()
		p.History = &clone
	}
	p.BootstrapHistory = cloneHistoryCommits(p.BootstrapHistory)
	return p
}

func cloneHistoryCommits(commits []HistoryCommit) []HistoryCommit {
	if len(commits) == 0 {
		return nil
	}
	clone := make([]HistoryCommit, len(commits))
	for index, commit := range commits {
		clone[index] = commit.Clone()
	}
	return clone
}

// TerminalProjectionState is the TerminalSession-owned physical cache
// snapshot. It deliberately contains no semantic transcript, bottom state, or
// history source; callers can observe validity without treating ScreenModel as
// a business-data source.
type TerminalProjectionState struct {
	Geometry                  GeometryState
	Viewport                  ViewportArea
	HistoryRows               int
	HistoryKnown              bool
	LayoutGeneration          uint64
	Lease                     LeaseState
	OutputBottomRow           int
	Frame                     uint64
	TerminalEpoch             uint64
	ScrollbackResetCount      uint64
	LastScrollbackResetReason string
	Validity                  renderengine.ProjectionValidity
	Cursor                    *AppCursor
}

// ViewportArea is the only mutable primary-screen region owned by
// TerminalSession. Top is a 1-based physical terminal row. Finalized history
// lives in rows 1..Top-1 and is inserted there through the terminal's native
// scroll mechanism; it is deliberately absent from ScreenModel.
type ViewportArea struct {
	Top    int
	Height int
	Width  int
}

func (a ViewportArea) bottom() int {
	return a.Top + a.Height - 1
}

func (a ViewportArea) validFor(geometry GeometryState) bool {
	return a.Width == geometry.Width && a.Width > 0 && a.Height >= 0 &&
		a.Top >= 1 && a.Top <= geometry.Height+1 && a.bottom() <= geometry.Height
}

// TerminalFrameResult reports the outcome of one viewport transaction. A
// Deferred result means the primary screen was leased to alternate-screen UI
// and no bytes were attempted. A failed write leaves Projection Unknown.
type TerminalFrameResult struct {
	Frame       uint64
	Deferred    bool
	FullRepaint bool
	Err         error
}

// TerminalTransactionResult separates viewport delivery from optional history
// delivery. A history Deferred result proves no history bytes were attempted;
// the frame may still have completed a recovery repaint in that transaction.
type TerminalTransactionResult struct {
	Frame           TerminalFrameResult
	History         *HistoryCommitResult
	ScrollbackReset bool
	TerminalEpoch   uint64
}

// terminalPreparedTransaction contains pure presentation work derived from one
// immutable plan. It is built without holding TerminalSession.mu so a large
// bootstrap history batch cannot block projection diagnostics or shutdown
// state inspection. transactionMu still orders theme/lease transitions with
// the eventual physical write.
type terminalPreparedTransaction struct {
	theme        style.ThemeContext
	viewportRows [][]vt.Cell
	viewportErr  error
	history      *terminalPreparedHistory
}

type terminalPreparedHistory struct {
	commits []HistoryCommit
	rows    []string
	err     error
}

// terminalPreparedHistoryCache retains at most the current pending batch. It is
// guarded by TerminalSession.transactionMu and is cleared after a successful or
// possibly-partial history write. Exact presentation equality, width, and theme
// are revalidated before reuse, so a same-token pending rebase cannot return
// stale terminal bytes.
type terminalPreparedHistoryCache struct {
	commits []HistoryCommit
	theme   style.ThemeContext
	width   int
	rows    []string
}

// TerminalSession owns one physical bottom-inline viewport projection cache. The
// mutex serializes mutable front/back/cursor state; Presenter additionally
// serializes each terminal write with the repository-wide terminal lock. The
// unified chat path installs TerminalSession through TerminalSessionPresenter;
// FixedBottomSurface remains only a compatibility state facade in that mode.
type TerminalSession struct {
	// transactionMu preserves call order across the prepare-without-state-lock
	// phase and the physical write. AbortTerminalWrite intentionally bypasses
	// it so a blocked target remains interruptible.
	transactionMu sync.Mutex
	mu            sync.Mutex
	writer        io.Writer
	aborter       TerminalWriteAborter
	// output 是非 nil 时，所有 terminal bytes 经 gateway port 提交
	// （Phase 1 统一接入；NewTerminalSession 仍走 writer 直写兼容路径）。
	// port 的 admission/序列化由 gateway 保证；此字段只在锁内读写。
	output    outputpkg.RenderOutputPort
	presenter *renderengine.Presenter
	screen    *renderengine.ScreenModel
	geometry  GeometryState
	viewport  ViewportArea
	// viewportBoundaryKnown proves that viewport was confirmed on a terminal
	// with viewportTerminalHeight rows. It is separate from the cell cache:
	// theme/lease recovery can invalidate content while DEC 1049 still preserves
	// the primary boundary, whereas a partial primary write invalidates both.
	viewportBoundaryKnown  bool
	viewportTerminalHeight int
	generation             uint64
	lease                  LeaseState
	outputBottom           int
	// historyTailRows is the bounded physical projection currently resident in
	// rows 1..outputBottom, oldest first. It excludes native scrollback and,
	// critically, excludes unused blank headroom. This is display-only cache:
	// semantic recovery still comes from AppState/Scene.
	historyTailRows []string
	// historyTopAligned becomes sticky after this session has moved a semantic
	// row into native scrollback. From that point the resident tail must begin
	// at physical row one, otherwise later viewport contraction would insert
	// blank headroom between scrollback and the same semantic message. Before
	// the first overflow, bottom alignment keeps a short transcript near the
	// composer without creating non-semantic scrollback.
	historyTopAligned         bool
	historyProjectionKnown    bool
	historyProjectionStarted  bool
	frame                     uint64
	terminalEpoch             uint64
	scrollbackResetCount      uint64
	lastScrollbackResetReason string
	cursor                    *AppCursor
	// alternateLeaseID records actual DEC 1049 transport ownership. It is
	// deliberately independent of AppState.Lease because the physical enter
	// completes before LeaseAcquired is posted to the actor, and the physical
	// exit completes before LeaseReleased is reduced. FlushTransaction checks
	// it directly so no stale AppState snapshot can write a primary frame into
	// the alternate buffer during either transition.
	alternateLeaseID     uint64
	colorProfile         render.ColorProfile
	frameTheme           style.ThemeContext
	preparedHistory      *terminalPreparedHistoryCache
	historyPrepareHits   uint64
	historyPrepareMisses uint64
}

var _ HistoryCommitSink = (*TerminalSession)(nil)

func NewTerminalSession(writer io.Writer) *TerminalSession {
	screen := renderengine.NewScreenModel(1, 1)
	// A fresh session has no proof that its blank front matches the terminal.
	// The first primary transaction must therefore be a recovery repaint.
	screen.Invalidate()
	abortable := newAbortableTerminalWriter(writer)
	return &TerminalSession{
		writer:     abortable,
		aborter:    abortable,
		presenter:  renderengine.NewPresenter(),
		frameTheme: ThemeContextForProfile(style.ColorProfile{ColorProfile: render.NoColorProfile()}),
		// A real geometry must arrive through TerminalFramePlan before a
		// visible frame. The one-cell initial cache is never used as source.
		screen: screen,
	}
}

// NewTerminalSessionWithOutput 构造 gateway-backed session：所有 terminal
// bytes 经 port 提交（Phase 1 统一接入入口，供 command setup、测试与嵌入
// 调用）。writer/presenter 字段保持 nil——不会创建第二个物理 sink。
func NewTerminalSessionWithOutput(port outputpkg.RenderOutputPort) *TerminalSession {
	screen := renderengine.NewScreenModel(1, 1)
	screen.Invalidate()
	return &TerminalSession{
		output:     port,
		presenter:  renderengine.NewPresenter(),
		frameTheme: ThemeContextForProfile(style.ColorProfile{ColorProfile: render.NoColorProfile()}),
		screen:     screen,
	}
}

// AbortTerminalWrite is the shutdown cancellation gate for a blocked physical
// write. It never takes the session mutex, so it can interrupt a write that is
// already holding that mutex. After abort, every subsequent frame is rejected.
// Gateway-backed sessions（output != nil）委托 port.Close（bounded shutdown）。
func (s *TerminalSession) AbortTerminalWrite() error {
	if s == nil {
		return nil
	}
	port := s.output
	if port != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return port.Close(ctx)
	}
	if s.aborter == nil {
		return nil
	}
	return s.aborter.AbortTerminalWrite()
}

// ProjectionState returns a detached physical-cache summary. It is intended
// for diagnostics and tests, not for deriving semantic content.
func (s *TerminalSession) ProjectionState() TerminalProjectionState {
	if s == nil {
		return TerminalProjectionState{Validity: renderengine.ProjectionUnknown}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projectionStateLocked()
}

func (s *TerminalSession) projectionStateLocked() TerminalProjectionState {
	validity := renderengine.ProjectionUnknown
	if s.screen != nil {
		validity = s.screen.ProjectionValidity()
	}
	return TerminalProjectionState{
		Geometry:                  s.geometry,
		Viewport:                  s.viewport,
		HistoryRows:               len(s.historyTailRows),
		HistoryKnown:              s.historyProjectionKnown,
		LayoutGeneration:          s.generation,
		Lease:                     s.lease,
		OutputBottomRow:           s.outputBottom,
		Frame:                     s.frame,
		TerminalEpoch:             s.terminalEpoch,
		ScrollbackResetCount:      s.scrollbackResetCount,
		LastScrollbackResetReason: s.lastScrollbackResetReason,
		Validity:                  validity,
		Cursor:                    cloneTerminalCursor(s.cursor),
	}
}

// InvalidateProjection requests a recovery full repaint from the next frame
// plan. It never reads content back from the terminal or the front buffer.
func (s *TerminalSession) InvalidateProjection() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.screen != nil {
		s.screen.Invalidate()
	}
	s.cursor = nil
}

// AlternateScreenLeaseID reports the current physical DEC 1049 owner. It is
// an operational diagnostic only; semantic lease truth remains in AppState.
func (s *TerminalSession) AlternateScreenLeaseID() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alternateLeaseID
}

// EnterAlternateScreen moves the one terminal writer into DEC 1049 alternate
// screen mode. It is intentionally part of TerminalSession, not
// FixedBottomSurface: once the unified renderer is selected, all terminal
// control bytes must pass through this owner and its Presenter lock.
//
// The caller posts LeaseAcquired only after this method succeeds. The
// transaction mutex keeps this physical transition ordered with primary/history
// preparation, while the session mutex closes the race where an old actor
// snapshot could otherwise flush a primary frame after DEC 1049 entered but
// before the lease barrier reached the reducer.
func (s *TerminalSession) EnterAlternateScreen(leaseID uint64) error {
	if s == nil || leaseID == 0 {
		return ErrTerminalAlternateScreenLease
	}
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.writerAvailable() || s.screen == nil {
		return ErrTerminalWriterMissing
	}
	if s.alternateLeaseID != 0 {
		return fmt.Errorf("%w: lease id=%d", ErrTerminalAlternateScreenBusy, s.alternateLeaseID)
	}

	enter := s.writeTerminalBytesKindLocked(outputpkg.TransactionAlternateEnter, "\x1b[?1049h\x1b[r\x1b[?25l\x1b[2J\x1b[H")
	if enter.Err != nil {
		rollback := s.writeTerminalBytesKindLocked(outputpkg.TransactionAlternateExit, "\x1b[?25h\x1b[r\x1b[?1049l")
		// A proven zero-byte enter followed by a complete rollback preserves the
		// primary buffer. Any partial transport loses buffer ownership proof.
		if enter.MayHavePartiallyWritten || (rollback.Err != nil && rollback.MayHavePartiallyWritten) {
			if s.screen != nil {
				s.screen.Invalidate()
			}
			s.viewportBoundaryKnown = false
			s.historyTailRows = nil
			s.historyTopAligned = false
			s.historyProjectionKnown = false
			s.historyProjectionStarted = true
		}
		s.cursor = nil
		return errors.Join(enter.Err, rollback.Err)
	}

	s.alternateLeaseID = leaseID
	s.lease = LeaseState{ID: leaseID, Active: true}
	// The primary front buffer belongs to the restored primary screen after
	// exit, not to the freshly-cleared alternate screen. Never diff against it.
	s.screen.Invalidate()
	s.cursor = nil
	return nil
}

// WriteAlternateScreen writes fullscreen presenter bytes through the same
// TerminalSession writer and global presenter lock as every primary frame.
// It rejects writes outside the active lease so pager/list code cannot become
// a raw stdout bypass after its modal lifecycle has ended.
func (s *TerminalSession) WriteAlternateScreen(leaseID uint64, bytes string) error {
	if s == nil || leaseID == 0 {
		return ErrTerminalAlternateScreenLease
	}
	if bytes == "" {
		return nil
	}
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alternateLeaseID != leaseID {
		return fmt.Errorf("%w: want id=%d active id=%d", ErrTerminalAlternateScreenLease, leaseID, s.alternateLeaseID)
	}
	write := s.writeTerminalBytesKindLocked(outputpkg.TransactionAlternateWrite, bytes)
	if write.Err != nil {
		if s.screen != nil {
			s.screen.Invalidate()
		}
		s.cursor = nil
	}
	return write.Err
}

// ExitAlternateScreen restores the primary buffer and invalidates the cached
// primary projection. The next AppState-derived transaction must therefore be
// a full recovery repaint. A proven zero-byte failure retains the lease for an
// exact retry. A partial write clears local ownership and makes the primary
// projection Unknown because blindly retrying DEC 1049 could corrupt a later
// fullscreen session.
func (s *TerminalSession) ExitAlternateScreen(leaseID uint64) error {
	if s == nil || leaseID == 0 {
		return ErrTerminalAlternateScreenLease
	}
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alternateLeaseID == 0 {
		return nil
	}
	if s.alternateLeaseID != leaseID {
		return fmt.Errorf("%w: want id=%d active id=%d", ErrTerminalAlternateScreenLease, leaseID, s.alternateLeaseID)
	}

	write := s.writeTerminalBytesKindLocked(outputpkg.TransactionAlternateExit, "\x1b[?25h\x1b[r\x1b[?1049l")
	if write.Err != nil && !write.MayHavePartiallyWritten {
		// No exit byte reached the host, so the same physical alternate lease is
		// still active and may be retried without guessing buffer ownership.
		return write.Err
	}
	s.alternateLeaseID = 0
	s.lease = LeaseState{}
	if s.screen != nil {
		s.screen.Invalidate()
	}
	s.cursor = nil
	if write.Err != nil {
		s.viewportBoundaryKnown = false
		s.historyTailRows = nil
		s.historyTopAligned = false
		s.historyProjectionKnown = false
		s.historyProjectionStarted = true
	}
	return write.Err
}

// SetColorProfile replaces the negotiated ANSI encoding profile used for
// HistoryCommit payloads. A profile change changes physical bytes, so the
// existing frame cache becomes unsafe for incremental presentation until the
// next source-backed flush completes.
func (s *TerminalSession) SetColorProfile(profile render.ColorProfile) {
	if s == nil {
		return
	}
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.colorProfile == profile {
		return
	}
	s.colorProfile = profile
	s.frameTheme.Terminal.ColorProfile = profile
	s.frameTheme.UseHyperlink = profile.Hyperlinks
	// Prepared history contains already-resolved ANSI for the old profile.
	// Exact validation would reject it later, but clearing now releases the
	// retained batch even when no further history transaction is scheduled.
	s.preparedHistory = nil
	if s.screen != nil {
		s.screen.Invalidate()
	}
	s.cursor = nil
}

// SetThemeContext replaces the already-negotiated theme used to encode
// TerminalFramePlan.RenderRows. Theme changes alter terminal cells even when
// the plain text is unchanged, so the next source-backed frame must repaint.
// Callers supply a snapshot built from the terminal owner's capabilities;
// TerminalSession never probes global terminal state while flushing.
func (s *TerminalSession) SetThemeContext(theme style.ThemeContext) {
	if s == nil {
		return
	}
	theme = cloneThemeContext(theme)
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frameTheme = theme
	s.colorProfile = theme.Terminal.ColorProfile
	s.preparedHistory = nil
	if s.screen != nil {
		s.screen.Invalidate()
	}
	s.cursor = nil
}

// FlushAppState is the convenience form for a presenter call site. It remains
// pure on the input side: all composition occurs before terminal bytes are
// prepared, and no AppState field is modified.
func (s *TerminalSession) FlushAppState(state AppState) TerminalFrameResult {
	return s.Flush(ComposeTerminalFramePlan(state))
}

// Flush emits one viewport-only transaction. Callers that have already claimed
// a HistoryCommit must use FlushTransaction so handoff, viewport diff, and
// cursor share one Presenter write.
func (s *TerminalSession) Flush(plan TerminalFramePlan) TerminalFrameResult {
	return s.FlushTransaction(TerminalTransactionPlan{Frame: plan}).Frame
}

// FlushTransaction is the physical transaction boundary:
// lease/generation -> geometry -> viewport-boundary transition -> optional
// history insert -> viewport diff -> cursor -> one complete target write ->
// front confirmation. History is independent of the mutable viewport cache,
// so a startup/resize recovery can insert both in the same atomic write.
//
// The unified chat setup installs this only after FixedBottomSurface has been
// fenced from physical output. It must never be connected beside the legacy
// historyWindow handoff path.
func (s *TerminalSession) FlushTransaction(plan TerminalTransactionPlan) TerminalTransactionResult {
	if s == nil {
		result := TerminalFrameResult{Err: ErrInvalidTerminalFrame}
		if plan.History != nil {
			history := HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
			return TerminalTransactionResult{Frame: result, History: &history}
		}
		return TerminalTransactionResult{Frame: result}
	}
	plan = plan.Clone()
	if !plan.Valid() {
		result := TerminalFrameResult{Err: ErrInvalidTerminalFrame}
		if plan.History != nil {
			history := HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
			return TerminalTransactionResult{Frame: result, History: &history}
		}
		return TerminalTransactionResult{Frame: result}
	}
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()

	for {
		s.mu.Lock()
		if !s.transactionPreparationAllowedLocked(plan) {
			result := s.flushTransactionLocked(plan, nil)
			s.mu.Unlock()
			return result
		}
		theme := plan.Frame.Theme
		usesFallbackTheme := theme.Palette.Styles == nil
		if usesFallbackTheme {
			theme = cloneThemeContext(s.frameTheme)
		}
		s.mu.Unlock()

		prepared := s.prepareTerminalTransaction(plan, theme)

		s.mu.Lock()
		if usesFallbackTheme && !themeContextEqual(theme, s.frameTheme) {
			// Theme replacement raced pure preparation. Discard only the
			// viewport preparation; the history cache revalidates theme and
			// will replace its entry on the next pass.
			s.mu.Unlock()
			continue
		}
		result := s.flushTransactionLocked(plan, prepared)
		s.mu.Unlock()
		return result
	}
}

// writerAvailable 判断本 session 是否有可用的物理写路径：直写模式要求
// writer+presenter，gateway 模式要求 output port。
func (s *TerminalSession) writerAvailable() bool {
	if s == nil {
		return false
	}
	if s.output != nil {
		return true
	}
	return s.writer != nil && s.presenter != nil
}

// transactionPreparationAllowedLocked mirrors only the early, no-presentation
// exits of flushTransactionLocked. It is an optimization, not an authorization
// decision: the complete checks run again after preparation.
func (s *TerminalSession) transactionPreparationAllowedLocked(plan TerminalTransactionPlan) bool {
	frame := plan.Frame
	if !s.writerAvailable() || s.screen == nil ||
		frame.LayoutGeneration < s.generation ||
		s.alternateLeaseID != 0 || frame.Lease.Active {
		return false
	}
	return terminalFrameViewportArea(frame).validFor(frame.Geometry)
}

func (s *TerminalSession) prepareTerminalTransaction(plan TerminalTransactionPlan, theme style.ThemeContext) *terminalPreparedTransaction {
	area := terminalFrameViewportArea(plan.Frame)
	rows, viewportErr := terminalViewportCells(plan.Frame, area, theme)
	prepared := &terminalPreparedTransaction{
		theme:        theme,
		viewportRows: rows,
		viewportErr:  viewportErr,
	}
	if plan.History == nil {
		return prepared
	}
	commits := terminalHistoryTransactionCommits(plan.History, plan.BootstrapHistory)
	historyRows, historyErr := s.prepareTerminalHistoryRows(commits, theme, plan.Frame.Geometry.Width)
	prepared.history = &terminalPreparedHistory{
		commits: commits,
		rows:    historyRows,
		err:     historyErr,
	}
	return prepared
}

func (s *TerminalSession) prepareTerminalHistoryRows(commits []HistoryCommit, theme style.ThemeContext, width int) ([]string, error) {
	if cached := s.preparedHistory; cached != nil &&
		cached.width == width &&
		themeContextEqual(cached.theme, theme) &&
		terminalHistoryCommitsPresentationEqual(cached.commits, commits) {
		s.historyPrepareHits++
		return cached.rows, nil
	}
	s.preparedHistory = nil
	s.historyPrepareMisses++
	rows, err := terminalHistoryCommitRows(commits, theme, width)
	if err != nil {
		return nil, err
	}
	s.preparedHistory = &terminalPreparedHistoryCache{
		commits: cloneHistoryCommits(commits),
		theme:   cloneThemeContext(theme),
		width:   width,
		rows:    append([]string(nil), rows...),
	}
	return rows, nil
}

func terminalHistoryCommitsPresentationEqual(left, right []HistoryCommit) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Token != right[index].Token ||
			!historyCommitPresentationEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func (s *TerminalSession) flushTransactionLocked(plan TerminalTransactionPlan, prepared *terminalPreparedTransaction) TerminalTransactionResult {
	frame := plan.Frame
	if !s.writerAvailable() || s.screen == nil {
		result := TerminalFrameResult{Frame: s.frame, Err: ErrTerminalWriterMissing}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Err: ErrTerminalWriterMissing})
	}
	if frame.LayoutGeneration < s.generation {
		result := TerminalFrameResult{Frame: s.frame, Err: ErrStaleTerminalFrame}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Deferred: true})
	}

	if s.alternateLeaseID != 0 || frame.Lease.Active {
		if s.alternateLeaseID != 0 {
			s.lease = LeaseState{ID: s.alternateLeaseID, Active: true}
		} else {
			s.lease = frame.Lease
		}
		s.cursor = nil
		result := TerminalFrameResult{Frame: s.frame, Deferred: true}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Deferred: true})
	}

	area := terminalFrameViewportArea(frame)
	if !area.validFor(frame.Geometry) {
		result := TerminalFrameResult{Frame: s.frame, Err: ErrInvalidTerminalFrame}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Deferred: true})
	}
	initializeHistoryProjection := !s.historyProjectionStarted && s.frame == 0 &&
		!s.viewportBoundaryKnown && s.viewport.Top == 0
	resizeRebuild := s.frame > 0 && s.geometry.Width > 0 && s.geometry.Height > 0 &&
		(s.geometry.Width != frame.Geometry.Width || s.geometry.Height != frame.Geometry.Height)
	forceScrollbackReset := plan.ResetScrollback
	scrollbackResetReason := terminalScrollbackResetReason(resizeRebuild, forceScrollbackReset)
	historyProjectionWritable := (s.historyProjectionKnown || initializeHistoryProjection) && !resizeRebuild && !forceScrollbackReset
	hadKnownProjection := s.screen.ProjectionValidity() == renderengine.ProjectionKnown && !s.lease.Active
	projectionKnown := hadKnownProjection
	candidateScreen := s.screen.Clone()
	if candidateScreen == nil {
		result := TerminalFrameResult{Frame: s.frame, Err: ErrTerminalWriterMissing}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Err: ErrTerminalWriterMissing})
	}
	if s.lease.Active {
		candidateScreen.Invalidate()
		projectionKnown = false
	}
	if resizeRebuild || forceScrollbackReset {
		candidateScreen.Invalidate()
		projectionKnown = false
	}
	modelHeight := area.Height
	if modelHeight < 1 {
		modelHeight = 1
	}
	if width, height := candidateScreen.Size(); width != area.Width || height != modelHeight || s.viewport != area {
		candidateScreen.Resize(area.Width, modelHeight)
		projectionKnown = false
	}

	fullRepaint := !projectionKnown
	if prepared == nil {
		result := TerminalFrameResult{Frame: s.frame, FullRepaint: fullRepaint, Err: ErrInvalidTerminalFrame}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Deferred: true})
	}
	rows, err := prepared.viewportRows, prepared.viewportErr
	if err != nil {
		result := TerminalFrameResult{Frame: s.frame, FullRepaint: fullRepaint, Err: err}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Deferred: true})
	}
	historyResult := (*HistoryCommitResult)(nil)
	historyBytes := ""
	var delivered []HistoryCommit
	historyInsertedRows := 0
	var historyInsertedPayload []string
	if plan.History != nil {
		result := HistoryCommitResult{Deferred: true}
		if historyProjectionWritable && frame.OutputBottomRow > 0 && plan.History.LayoutGeneration == frame.LayoutGeneration {
			if history := prepared.history; history == nil {
				result = HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
			} else {
				commits := history.commits
				candidateRows, historyErr := history.rows, history.err
				if historyErr != nil {
					result = HistoryCommitResult{Err: historyErr}
				} else if len(candidateRows) == 0 {
					result = HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
				} else {
					historyInsertedRows = len(candidateRows)
					historyInsertedPayload = candidateRows
					delivered = cloneHistoryCommits(commits)
					result = HistoryCommitResult{}
				}
			}
		}
		historyResult = &result
	}
	viewportBytes := ""
	if area.Height > 0 {
		candidateScreen.StageFrame(rows)
		prepared := candidateScreen.PrepareFlush()
		viewportBytes, err = terminalOffsetViewportANSI(prepared, area)
		if err != nil {
			result := TerminalFrameResult{Frame: s.frame, FullRepaint: fullRepaint, Err: err}
			return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Deferred: true})
		}
	}
	// Reconcile the boundary before history insertion. Expansion scrolls the
	// old history region only when semantic rows actually overflow the smaller
	// capacity; contraction clears former viewport rows so prompt/status can
	// never become history.
	transitionBytes := ""
	nextHistoryTopAligned := s.historyTopAligned
	// A terminal-height resize has already changed the host's physical row
	// mapping and native scrollback. Old absolute viewport coordinates are no
	// longer a valid scroll-region boundary; replaying that transition can
	// include the new prompt/status rows and leak them into history. Trust the
	// host resize, then source-repaint only the new bottom viewport.
	if resizeRebuild || forceScrollbackReset {
		transitionBytes = terminalResetScrollbackANSI()
		nextHistoryTopAligned = false
	} else if initializeHistoryProjection {
		transitionBytes = terminalClearHistoryRegionANSI(frame.OutputBottomRow)
		nextHistoryTopAligned = false
	} else if s.historyProjectionKnown && s.viewportBoundaryKnown && s.viewportTerminalHeight == frame.Geometry.Height {
		transitionBytes, nextHistoryTopAligned = terminalViewportTransitionANSI(
			s.viewport, area, frame.Geometry.Height, s.historyTailRows, nextHistoryTopAligned,
		)
	}
	// After expansion the history region may have less capacity. Retain its
	// semantic suffix before appending new commits. History insertion itself is
	// occupancy-aware: an underfilled region is repainted without scrolling;
	// overflow uses HandoffPlan only for rows that must actually cross row one.
	baseHistoryTail := terminalRetainHistoryTailRows(s.historyTailRows, frame.OutputBottomRow)
	if resizeRebuild || forceScrollbackReset {
		baseHistoryTail = nil
	}
	if historyInsertedRows > 0 {
		historyBytes, nextHistoryTopAligned = terminalHistoryInsertionANSI(
			frame.Geometry.Height, frame.OutputBottomRow, baseHistoryTail, historyInsertedPayload, nextHistoryTopAligned,
		)
	}
	bytes := transitionBytes + historyBytes + viewportBytes
	if cursor := terminalCursorSequence(frame.Cursor, frame.Geometry.Width, frame.Geometry.Height); cursor != "" {
		bytes += cursor
	}
	// history+viewport 合并事务：frame 与历史 token 同 batch（11.2 要求）。
	kind := outputpkg.TransactionFrame
	if historyBytes != "" {
		kind = outputpkg.TransactionFrameAndHistory
	}
	if write := s.writeTerminalBytesKindLocked(kind, bytes); write.Err != nil {
		if write.MayHavePartiallyWritten {
			// The target consumed an unknown prefix. Preserve no incremental
			// viewport proof, but keep the last confirmed scalar snapshot so the
			// next source frame replays the complete candidate transition.
			s.screen.Invalidate()
			s.cursor = nil
			geometryChanged := s.geometry.Width != frame.Geometry.Width || s.geometry.Height != frame.Geometry.Height
			if geometryChanged || transitionBytes != "" {
				s.viewportBoundaryKnown = false
			}
			if geometryChanged || transitionBytes != "" || historyBytes != "" {
				s.historyTailRows = nil
				s.historyTopAligned = false
				s.historyProjectionKnown = false
				s.historyProjectionStarted = true
			}
			if historyBytes != "" {
				s.preparedHistory = nil
			}
		}
		frameResult := TerminalFrameResult{Frame: s.frame, FullRepaint: fullRepaint, Err: write.Err}
		if historyResult != nil && historyBytes != "" {
			*historyResult = HistoryCommitResult{Err: write.Err, MayHavePartiallyWritten: write.MayHavePartiallyWritten}
		}
		return TerminalTransactionResult{Frame: frameResult, History: historyResult}
	}
	candidateScreen.ConfirmFlush()
	s.screen = candidateScreen
	s.geometry = frame.Geometry
	s.generation = frame.LayoutGeneration
	s.lease = frame.Lease
	s.outputBottom = frame.OutputBottomRow
	s.viewport = area
	s.viewportBoundaryKnown = true
	s.viewportTerminalHeight = frame.Geometry.Height
	if resizeRebuild || forceScrollbackReset {
		s.historyProjectionStarted = true
		s.historyProjectionKnown = true
		s.historyTailRows = nil
		if plan.TerminalEpoch > s.terminalEpoch {
			s.terminalEpoch = plan.TerminalEpoch
		}
		s.terminalEpoch++
		s.scrollbackResetCount++
		s.lastScrollbackResetReason = scrollbackResetReason
	} else if initializeHistoryProjection {
		s.historyProjectionStarted = true
		s.historyProjectionKnown = true
	}
	if s.historyProjectionKnown {
		s.historyTailRows = baseHistoryTail
		s.historyTopAligned = nextHistoryTopAligned
		if historyInsertedRows > 0 {
			s.historyTailRows = terminalAppendHistoryTailRows(s.historyTailRows, historyInsertedPayload, frame.OutputBottomRow)
		}
	}
	s.cursor = cloneTerminalCursor(frame.Cursor)
	s.frame++
	frameResult := TerminalFrameResult{Frame: s.frame, FullRepaint: fullRepaint}
	if historyResult != nil && historyBytes != "" {
		*historyResult = HistoryCommitResult{Frame: s.frame, Delivered: delivered}
		s.preparedHistory = nil
	}
	return TerminalTransactionResult{
		Frame:           frameResult,
		History:         historyResult,
		ScrollbackReset: resizeRebuild || forceScrollbackReset,
		TerminalEpoch:   s.terminalEpoch,
	}
}

func terminalScrollbackResetReason(resize, reconciliation bool) string {
	switch {
	case resize && reconciliation:
		return "resize+reconciliation"
	case resize:
		return "resize"
	case reconciliation:
		return "reconciliation"
	default:
		return ""
	}
}

func terminalTransactionWithHistory(frame TerminalFrameResult, history *HistoryCommit, result HistoryCommitResult) TerminalTransactionResult {
	if history == nil {
		return TerminalTransactionResult{Frame: frame}
	}
	return TerminalTransactionResult{Frame: frame, History: &result}
}

func terminalFrameViewportArea(frame TerminalFramePlan) ViewportArea {
	return ViewportArea{
		Top:    frame.OutputBottomRow + 1,
		Height: frame.Geometry.Height - frame.OutputBottomRow,
		Width:  frame.Geometry.Width,
	}
}

func terminalViewportCells(frame TerminalFramePlan, area ViewportArea, theme style.ThemeContext) ([][]vt.Cell, error) {
	if !area.validFor(frame.Geometry) {
		return nil, ErrInvalidTerminalFrame
	}
	rows, err := terminalFrameCells(frame.Rows, frame.RenderRows, frame.Geometry.Width, frame.Geometry.Height, theme)
	if err != nil || area.Height == 0 {
		return nil, err
	}
	return rows[area.Top-1:], nil
}

// CommitHistory implements HistoryCommitSink for the unified one-writer
// primary presenter. It intentionally has no FixedBottomSurface adapter: the
// compatibility surface is physically fenced in owned interactive sessions,
// and wiring its legacy historyWindow handoff here would duplicate native
// scrollback.
//
// The commit is accepted only when this session has a confirmed projection for
// the exact layout generation. A lease, an incomplete recovery frame, or a
// generation mismatch returns Deferred without attempting terminal I/O. Writer
// errors and short writes preserve their byte fact and invalidate the physical
// cache before the result reaches the reducer.
func (s *TerminalSession) CommitHistory(commit HistoryCommit) HistoryCommitResult {
	if s == nil {
		return HistoryCommitResult{Err: ErrTerminalWriterMissing}
	}
	commit = commit.Clone()
	if !commit.Valid() {
		return HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
	}

	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()

	for {
		s.mu.Lock()
		if result, done := s.commitHistoryEarlyResultLocked(commit); done {
			s.mu.Unlock()
			return result
		}
		theme := cloneThemeContext(s.frameTheme)
		width := s.geometry.Width
		s.mu.Unlock()

		rows, err := s.prepareTerminalHistoryRows([]HistoryCommit{commit}, theme, width)
		if err != nil {
			return HistoryCommitResult{Err: err}
		}

		s.mu.Lock()
		if width != s.geometry.Width || !themeContextEqual(theme, s.frameTheme) {
			s.mu.Unlock()
			continue
		}
		if result, done := s.commitHistoryEarlyResultLocked(commit); done {
			s.mu.Unlock()
			return result
		}
		result := s.commitHistoryRowsLocked(rows)
		s.mu.Unlock()
		return result
	}
}

func (s *TerminalSession) commitHistoryEarlyResultLocked(commit HistoryCommit) (HistoryCommitResult, bool) {
	if !s.writerAvailable() || s.screen == nil {
		return HistoryCommitResult{Err: ErrTerminalWriterMissing}, true
	}
	if s.alternateLeaseID != 0 || s.lease.Active || s.generation == 0 || commit.LayoutGeneration != s.generation ||
		s.outputBottom < 1 || !s.historyProjectionKnown ||
		s.screen.ProjectionValidity() != renderengine.ProjectionKnown {
		// No primary transaction has begun. The executor may return this token
		// to Pending without inventing a retry identity or marking projection
		// failure; a later source-backed frame/recovery owns the wake-up.
		return HistoryCommitResult{Deferred: true}, true
	}
	return HistoryCommitResult{}, false
}

func (s *TerminalSession) commitHistoryRowsLocked(rows []string) HistoryCommitResult {
	// The top-anchored region ends immediately above the inline viewport. Since
	// its top margin is physical row one, overflow becomes native scrollback;
	// prompt/status rows below outputBottom never participate in the scroll.
	bytes, nextHistoryTopAligned := terminalHistoryInsertionANSI(
		s.geometry.Height, s.outputBottom, s.historyTailRows, rows, s.historyTopAligned,
	)
	if bytes == "" {
		return HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
	}
	write := s.writeTerminalBytesKindLocked(outputpkg.TransactionHistoryHandoff, bytes)
	if write.Err != nil {
		s.screen.MarkWriteFailed()
		s.cursor = nil
		if write.MayHavePartiallyWritten {
			s.viewportBoundaryKnown = false
			s.historyTailRows = nil
			s.historyTopAligned = false
			s.historyProjectionKnown = false
			s.historyProjectionStarted = true
			s.preparedHistory = nil
		}
		return HistoryCommitResult{
			Err:                     write.Err,
			MayHavePartiallyWritten: write.MayHavePartiallyWritten,
		}
	}

	s.frame++
	s.historyTailRows = terminalAppendHistoryTailRows(s.historyTailRows, rows, s.outputBottom)
	s.historyTopAligned = nextHistoryTopAligned
	s.preparedHistory = nil
	return HistoryCommitResult{Frame: s.frame}
}

// terminalHistoryHandoffRows retains the existing rich render IR through the
// same explicit ThemeContext used for viewport rows. It does not parse source
// strings or build a second formatter; HistoryCommit.Lines already are the
// selected layout-generation display rows.
func terminalHistoryHandoffRows(lines []render.Line, theme style.ThemeContext, width int) ([]string, error) {
	if width < 1 || len(lines) == 0 {
		return nil, ErrInvalidHistoryHandoff
	}
	for index, line := range lines {
		if render.LineWidth(line) > width {
			return nil, fmt.Errorf("%w: row %d exceeds width %d", ErrInvalidHistoryHandoff, index+1, width)
		}
	}
	document := style.NewResolver(theme).ResolveDocument(render.LinesDoc(lines...))
	rows := (render.ANSIBackend{Profile: theme.Terminal.ColorProfile}).RenderLines(document)
	if len(rows) != len(lines) {
		return nil, ErrInvalidHistoryHandoff
	}
	for index, row := range rows {
		if strings.ContainsAny(row, "\r\n") {
			return nil, fmt.Errorf("%w: encoded row %d contains a line control", ErrInvalidHistoryHandoff, index+1)
		}
	}
	return rows, nil
}

func terminalHistoryTransactionCommits(claimed *HistoryCommit, bootstrap []HistoryCommit) []HistoryCommit {
	if claimed == nil {
		return nil
	}
	if len(bootstrap) > 0 && bootstrap[0].Token == claimed.Token &&
		bootstrap[0].LayoutGeneration == claimed.LayoutGeneration &&
		historyCommitPresentationEqual(bootstrap[0], *claimed) {
		return bootstrap
	}
	return []HistoryCommit{*claimed}
}

func terminalHistoryCommitRows(commits []HistoryCommit, theme style.ThemeContext, width int) ([]string, error) {
	if len(commits) == 0 {
		return nil, ErrInvalidHistoryHandoff
	}
	generation := commits[0].LayoutGeneration
	previousToken := uint64(0)
	lineCount := 0
	for _, commit := range commits {
		if !commit.Valid() || commit.LayoutGeneration != generation ||
			(previousToken != 0 && commit.Token <= previousToken) {
			return nil, ErrInvalidHistoryHandoff
		}
		lineCount += len(commit.Lines)
		previousToken = commit.Token
	}
	lines := make([]render.Line, 0, lineCount)
	for _, commit := range commits {
		lines = append(lines, commit.Lines...)
	}
	return terminalHistoryHandoffRows(lines, theme, width)
}

// terminalOffsetViewportANSI maps ScreenModel's viewport-relative CUP
// coordinates onto physical terminal rows. Every CUP is fenced to area even
// when its top is row one; all other complete CSI sequences pass through.
func terminalOffsetViewportANSI(value string, area ViewportArea) (string, error) {
	if value == "" {
		return value, nil
	}
	if area.Top < 1 || area.Height < 1 || area.Width < 1 {
		return "", ErrInvalidTerminalFrame
	}
	var output strings.Builder
	for index := 0; index < len(value); {
		if value[index] != '\x1b' || index+1 >= len(value) || value[index+1] != '[' {
			output.WriteByte(value[index])
			index++
			continue
		}
		end := index + 2
		for end < len(value) && (value[end] < 0x40 || value[end] > 0x7e) {
			end++
		}
		if end >= len(value) {
			return "", ErrInvalidTerminalFrame
		}
		final := value[end]
		if final != 'H' && final != 'f' {
			output.WriteString(value[index : end+1])
			index = end + 1
			continue
		}
		parameters := strings.Split(value[index+2:end], ";")
		if len(parameters) == 0 || len(parameters) > 2 {
			return "", ErrInvalidTerminalFrame
		}
		row := 1
		if parameters[0] != "" {
			parsed, err := strconv.Atoi(parameters[0])
			if err != nil || parsed < 1 || parsed > area.Height {
				return "", ErrInvalidTerminalFrame
			}
			row = parsed
		}
		column := 1
		if len(parameters) == 2 && parameters[1] != "" {
			parsed, err := strconv.Atoi(parameters[1])
			if err != nil || parsed < 1 || parsed > area.Width {
				return "", ErrInvalidTerminalFrame
			}
			column = parsed
		}
		output.WriteString("\x1b[")
		output.WriteString(strconv.Itoa(area.Top + row - 1))
		if len(parameters) == 2 {
			output.WriteByte(';')
			if parameters[1] != "" {
				output.WriteString(strconv.Itoa(column))
			}
		}
		output.WriteByte(final)
		index = end + 1
	}
	return output.String(), nil
}

func terminalViewportTransitionANSI(previous, next ViewportArea, physicalHeight int, historyTailRows []string, topAligned bool) (string, bool) {
	if previous.Height <= 0 || previous == next || physicalHeight < 1 {
		return "", topAligned
	}
	oldCapacity, newCapacity := previous.Top-1, next.Top-1
	if oldCapacity < 1 || newCapacity < 0 {
		return "", topAligned
	}
	resident := len(historyTailRows)
	if resident > oldCapacity {
		resident = oldCapacity
	}

	if newCapacity < oldCapacity {
		if topAligned {
			overflow := resident - newCapacity
			if overflow <= 0 {
				return "", true
			}
			return renderengine.NewHandoffPlan(
				physicalHeight, oldCapacity, make([]string, overflow),
			).ANSI(), true
		}
		delta := oldCapacity - newCapacity
		blankHeadroom := oldCapacity - resident
		shift := delta
		if shift > blankHeadroom {
			shift = blankHeadroom
		}
		var output strings.Builder
		// Delete only unused headroom inside the old bounded region. CSI M
		// shifts resident rows upward but, unlike LF at row one, never appends
		// the deleted blank line to native scrollback.
		if shift > 0 && resident > 0 {
			output.WriteString(terminalHistoryDeleteLinesANSI(oldCapacity, 1, shift))
		}
		overflow := resident - newCapacity
		if overflow > 0 {
			output.WriteString(renderengine.NewHandoffPlan(
				physicalHeight, oldCapacity, make([]string, overflow),
			).ANSI())
		}
		return output.String(), overflow > 0
	}
	if newCapacity == oldCapacity {
		return "", topAligned
	}

	var output strings.Builder
	// Rows newly transferred from the inline viewport are cleared before any
	// line insertion, so prompt/status content can never become history.
	for row := oldCapacity + 1; row <= newCapacity && row <= physicalHeight; row++ {
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[0m\x1b[K", row)
	}
	if resident > 0 && !topAligned {
		start := oldCapacity - resident + 1
		output.WriteString(terminalHistoryInsertLinesANSI(newCapacity, start, newCapacity-oldCapacity))
	}
	return output.String(), topAligned
}

func terminalRetainHistoryTailRows(rows []string, capacity int) []string {
	if capacity <= 0 || len(rows) == 0 {
		return nil
	}
	if len(rows) > capacity {
		rows = rows[len(rows)-capacity:]
	}
	return append([]string(nil), rows...)
}

func terminalAppendHistoryTailRows(current, inserted []string, capacity int) []string {
	if capacity <= 0 {
		return nil
	}
	combined := make([]string, 0, len(current)+len(inserted))
	combined = append(combined, current...)
	combined = append(combined, inserted...)
	return terminalRetainHistoryTailRows(combined, capacity)
}

// terminalHistoryInsertionANSI appends semantic history without placing blank
// headroom inside one continuous native-history stream. Before the first
// overflow, a short resident tail stays bottom-aligned near the composer. Once
// overflow has reached native scrollback, the resident suffix is top-aligned;
// any later capacity growth remains below the suffix and new rows fill it in
// document order. Only rows beyond the physical capacity use LF.
func terminalHistoryInsertionANSI(height, capacity int, resident, inserted []string, topAligned bool) (string, bool) {
	if height < 1 || capacity < 1 || len(inserted) == 0 {
		return "", topAligned
	}
	if capacity > height {
		capacity = height
	}
	resident = terminalRetainHistoryTailRows(resident, capacity)
	fill := capacity - len(resident)
	if fill > len(inserted) {
		fill = len(inserted)
	}
	var output strings.Builder
	if fill > 0 {
		output.WriteString("\x1b[s")
		fmt.Fprintf(&output, "\x1b[1;%dr", capacity)
		if len(resident) > 0 && !topAligned {
			start := capacity - len(resident) - fill + 1
			fmt.Fprintf(&output, "\x1b[%d;1H\x1b[%dM", start, fill)
		}
		start := capacity - fill + 1
		if topAligned {
			start = len(resident) + 1
		}
		for index, row := range inserted[:fill] {
			fmt.Fprintf(&output, "\x1b[%d;1H\x1b[0m\x1b[K", start+index)
			output.WriteString(row)
		}
		output.WriteString("\x1b[r\x1b[u")
	}
	if fill < len(inserted) {
		output.WriteString(renderengine.NewHandoffPlan(height, capacity, inserted[fill:]).ANSI())
		topAligned = true
	}
	return output.String(), topAligned
}

func terminalHistoryDeleteLinesANSI(capacity, row, count int) string {
	if capacity < 1 || row < 1 || row > capacity || count < 1 {
		return ""
	}
	var output strings.Builder
	output.WriteString("\x1b[s")
	fmt.Fprintf(&output, "\x1b[1;%dr\x1b[%d;1H\x1b[%dM", capacity, row, count)
	output.WriteString("\x1b[r\x1b[u")
	return output.String()
}

func terminalClearHistoryRegionANSI(capacity int) string {
	if capacity < 1 {
		return ""
	}
	var output strings.Builder
	output.WriteString("\x1b[s\x1b[r")
	for row := 1; row <= capacity; row++ {
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[0m\x1b[K", row)
	}
	output.WriteString("\x1b[u")
	return output.String()
}

func terminalResetScrollbackANSI() string {
	return "\x1b[r\x1b[0m\x1b[H\x1b[2J\x1b[3J\x1b[H"
}

func terminalHistoryInsertLinesANSI(capacity, row, count int) string {
	if capacity < 1 || row < 1 || row > capacity || count < 1 {
		return ""
	}
	var output strings.Builder
	output.WriteString("\x1b[s")
	fmt.Fprintf(&output, "\x1b[1;%dr\x1b[%d;1H\x1b[%dL", capacity, row, count)
	output.WriteString("\x1b[r\x1b[u")
	return output.String()
}

type terminalWriteResult struct {
	Err                     error
	MayHavePartiallyWritten bool
}

// writeTerminalBytesLocked emits one fully assembled terminal transaction.
// Callers hold s.mu; Presenter serializes the actual terminal bytes globally.
// A writer panic is made equivalent to a possible partial write, so callers
// never retain a Known front after an interrupted transaction.
//
// Gateway-backed sessions（output != nil）把 bytes 经 port 提交，kind 默认
// frame；语义化调用点使用 writeTerminalBytesKindLocked。
func (s *TerminalSession) writeTerminalBytesLocked(bytes string) (result terminalWriteResult) {
	return s.writeTerminalBytesKindLocked(outputpkg.TransactionFrame, bytes)
}

// writeTerminalBytesKindLocked 是带事务 kind 的写入入口。output == nil 时
// 走 writer/presenter 直写兼容路径；output != nil 时经 gateway port 提交并
// 从 primary receipt 推导 result。
func (s *TerminalSession) writeTerminalBytesKindLocked(kind outputpkg.TransactionKind, bytes string) (result terminalWriteResult) {
	if bytes == "" {
		return result
	}
	port := s.output
	if port != nil {
		return s.submitWithPortLocked(port, kind, bytes)
	}
	if s == nil || s.writer == nil || s.presenter == nil {
		return terminalWriteResult{Err: ErrTerminalWriterMissing}
	}
	probe := &terminalSessionWriter{writer: s.writer}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = terminalWriteResult{
				Err:                     fmt.Errorf("%w: %v", ErrTerminalWriterPanic, recovered),
				MayHavePartiallyWritten: true,
			}
		}
	}()
	result.Err = s.presenter.Flush(probe, func(w io.Writer) {
		_, _ = io.WriteString(w, bytes)
	})
	result.MayHavePartiallyWritten = probe.bytesWritten > 0
	return result
}

// submitWithPortLocked 提交一笔 intent 并消费 primary receipt。receipt 到
// terminalWriteResult 的映射：
//   - committed → 成功（Err=nil）；
//   - failed_zero_bytes → 可证明零写：Err 保留（class 由 error 转换），
//     MayHavePartiallyWritten=false——调用方可以按零写重建；
//   - unknown_partial → 部分写：MayHavePartiallyWritten=true，Err=classified；
//   - target-level rejected/deferred → Err=classified，无部分写证明。
//
// pre-admission rejection（Primary==nil）不调用 sink，Err=classified。
func (s *TerminalSession) submitWithPortLocked(port outputpkg.RenderOutputPort, kind outputpkg.TransactionKind, bytes string) terminalWriteResult {
	intent := outputpkg.RenderIntent{
		Kind:   kind,
		Source: "terminal_session",
		Cause:  "writeTerminalBytes",
		Bytes:  []byte(bytes),
	}
	receipt := port.Submit(context.Background(), intent)
	if receipt.Primary == nil {
		// pre-admission rejection/defer：未调用 sink、零写证明。
		return terminalWriteResult{
			Err: outputpkg.NewClassifiedError(receipt.Admission.ErrorClass, receipt.Admission.Message),
		}
	}
	p := receipt.Primary
	switch p.Status {
	case outputpkg.DeliveryCommitted:
		return terminalWriteResult{}
	case outputpkg.DeliveryFailedZeroBytes:
		// 可证明零写：调用方可安全重试/重建。
		return terminalWriteResult{Err: receiptError(p.ErrorClass)}
	case outputpkg.DeliveryUnknownPartial:
		return terminalWriteResult{
			Err:                     receiptError(p.ErrorClass),
			MayHavePartiallyWritten: true,
		}
	default: // deferred/rejected
		return terminalWriteResult{Err: receiptError(p.ErrorClass)}
	}
}

// receiptError 从稳定 class 映射可读错误；gateway 的 classified error 已
// 携带 class 信息。
func receiptError(class outputpkg.DeliveryErrorClass) error {
	switch class {
	case outputpkg.DeliveryErrorCanceledBeforeIO, outputpkg.DeliveryErrorCanceledAfterStart:
		return ErrTerminalWriteAborted
	case outputpkg.DeliveryErrorClosed, outputpkg.DeliveryErrorAbandoned:
		return ErrTerminalWriterMissing
	default:
		return fmt.Errorf("terminal write failed: %s", class)
	}
}

type terminalSessionWriter struct {
	writer       io.Writer
	bytesWritten int
}

func (w *terminalSessionWriter) Write(data []byte) (int, error) {
	if w == nil || w.writer == nil {
		return 0, ErrTerminalWriterMissing
	}
	n, err := w.writer.Write(data)
	if n < 0 {
		n = 0
	}
	if n > len(data) {
		n = len(data)
	}
	w.bytesWritten += n
	return n, err
}

func cloneTerminalFrameRows(rows []AppScreenRow) []AppScreenRow {
	if len(rows) == 0 {
		return nil
	}
	return append([]AppScreenRow(nil), rows...)
}

func cloneTerminalCursor(cursor *AppCursor) *AppCursor {
	if cursor == nil {
		return nil
	}
	clone := *cursor
	return &clone
}

// terminalFrameCells converts a text-parity checked frame into a cell grid for
// the physical projection cache. Structured rows are resolved through the
// session's explicit ThemeContext before VT expansion; empty RenderRows keeps
// existing plain test/compatibility plans valid. The shared VT model remains
// the only width/SGR expansion rule.
func terminalFrameCells(rows []AppScreenRow, renderRows []render.Line, width, height int, theme style.ThemeContext) ([][]vt.Cell, error) {
	if width < 1 || height < 1 || len(rows) != height {
		return nil, ErrInvalidTerminalFrame
	}
	if len(renderRows) != 0 && len(renderRows) != height {
		return nil, ErrInvalidTerminalFrame
	}
	frame := make([][]vt.Cell, height)
	for index, row := range rows {
		if row.Row != 0 && row.Row != index+1 {
			return nil, fmt.Errorf("%w: row %d declared as %d", ErrInvalidTerminalFrame, index+1, row.Row)
		}
		text := row.Text
		if len(renderRows) > 0 {
			line := renderRows[index]
			plain := (render.PlainBackend{}).Render(render.LinesDoc(line))
			if plain != row.Text {
				// 用户 prompt 消息的渲染行允许带 "> " 前缀（区分用户信息与
				// LLM 信息），其余角色要求渲染行与布局行严格一致。
				if !(row.UserMessage && plain == userMessagePrefix+row.Text) {
					return nil, fmt.Errorf("%w: structured row %d plain text %q differs from text row %q", ErrInvalidTerminalFrame, index+1, plain, row.Text)
				}
			}
			text = style.RenderDocument(render.LinesDoc(line), theme)
		}
		if strings.ContainsAny(text, "\r\n") {
			return nil, fmt.Errorf("%w: row %d contains a line control", ErrInvalidTerminalFrame, index+1)
		}
		screen := vt.NewScreen(width, 2)
		screen.Feed(text)
		frame[index] = screen.CellRows(1, 1)[0]
	}
	return frame, nil
}

// terminalCursorSequence emits no bytes for an unknown/out-of-viewport cursor.
// AppCursor.Col==0 explicitly means unknown; leaving the physical cursor alone
// is safer than guessing a column from a renderer cache.
func terminalCursorSequence(cursor *AppCursor, width, height int) string {
	if cursor == nil || cursor.Row < 1 || cursor.Row > height || cursor.Col < 1 || cursor.Col > width {
		return ""
	}
	return fmt.Sprintf("\x1b[%d;%dH", cursor.Row, cursor.Col)
}
