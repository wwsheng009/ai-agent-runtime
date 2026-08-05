package ui

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
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
}

// ComposeTerminalTransactionPlan derives an immutable frame and optionally
// attaches one already-claimed HistoryCommit. The commit is cloned because its
// render lines are reducer-owned payload until the eventual Ack.
func ComposeTerminalTransactionPlan(state AppState, history *HistoryCommit, bootstrapHistory ...[]HistoryCommit) TerminalTransactionPlan {
	plan := TerminalTransactionPlan{Frame: ComposeTerminalFramePlan(state)}
	if history != nil {
		clone := history.Clone()
		plan.History = &clone
	}
	if len(bootstrapHistory) > 0 && len(bootstrapHistory[0]) > 0 {
		plan.BootstrapHistory = cloneHistoryCommits(bootstrapHistory[0])
	}
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
	Geometry         GeometryState
	LayoutGeneration uint64
	Lease            LeaseState
	OutputBottomRow  int
	Frame            uint64
	Validity         renderengine.ProjectionValidity
	Cursor           *AppCursor
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
	Frame   TerminalFrameResult
	History *HistoryCommitResult
}

// TerminalSession owns one physical primary-viewport projection cache. The
// mutex serializes mutable front/back/cursor state; Presenter additionally
// serializes each terminal write with the repository-wide terminal lock. The
// unified chat path installs TerminalSession through TerminalSessionPresenter;
// FixedBottomSurface remains only a compatibility state facade in that mode.
type TerminalSession struct {
	mu           sync.Mutex
	writer       io.Writer
	presenter    *renderengine.Presenter
	screen       *renderengine.ScreenModel
	geometry     GeometryState
	generation   uint64
	lease        LeaseState
	outputBottom int
	frame        uint64
	cursor       *AppCursor
	// alternateLeaseID records actual DEC 1049 transport ownership. It is
	// deliberately independent of AppState.Lease because the physical enter
	// completes before LeaseAcquired is posted to the actor, and the physical
	// exit completes before LeaseReleased is reduced. FlushTransaction checks
	// it directly so no stale AppState snapshot can write a primary frame into
	// the alternate buffer during either transition.
	alternateLeaseID uint64
	colorProfile     render.ColorProfile
	frameTheme       style.ThemeContext
}

var _ HistoryCommitSink = (*TerminalSession)(nil)

func NewTerminalSession(writer io.Writer) *TerminalSession {
	screen := renderengine.NewScreenModel(1, 1)
	// A fresh session has no proof that its blank front matches the terminal.
	// The first primary transaction must therefore be a recovery repaint.
	screen.Invalidate()
	return &TerminalSession{
		writer:     writer,
		presenter:  renderengine.NewPresenter(),
		frameTheme: ThemeContextForProfile(style.ColorProfile{ColorProfile: render.NoColorProfile()}),
		// A real geometry must arrive through TerminalFramePlan before a
		// visible frame. The one-cell initial cache is never used as source.
		screen: screen,
	}
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
		Geometry:         s.geometry,
		LayoutGeneration: s.generation,
		Lease:            s.lease,
		OutputBottomRow:  s.outputBottom,
		Frame:            s.frame,
		Validity:         validity,
		Cursor:           cloneTerminalCursor(s.cursor),
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
// The caller posts LeaseAcquired only after this method succeeds. Holding the
// session mutex across the transport write closes the race where an old actor
// snapshot could otherwise flush a primary frame after DEC 1049 entered but
// before the lease barrier reached the reducer.
func (s *TerminalSession) EnterAlternateScreen(leaseID uint64) error {
	if s == nil || leaseID == 0 {
		return ErrTerminalAlternateScreenLease
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer == nil || s.presenter == nil || s.screen == nil {
		return ErrTerminalWriterMissing
	}
	if s.alternateLeaseID != 0 {
		return fmt.Errorf("%w: lease id=%d", ErrTerminalAlternateScreenBusy, s.alternateLeaseID)
	}

	enter := s.writeTerminalBytesLocked("\x1b[?1049h\x1b[r\x1b[?25l\x1b[2J\x1b[H")
	if enter.Err != nil {
		// A partial enter leaves both the terminal buffer and our front cache
		// unknowable. Best-effort rollback is still useful, but cannot restore
		// a Known projection and must not grant the lease.
		if s.screen != nil {
			s.screen.Invalidate()
		}
		s.cursor = nil
		rollback := s.writeTerminalBytesLocked("\x1b[?25h\x1b[r\x1b[?1049l")
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alternateLeaseID != leaseID {
		return fmt.Errorf("%w: want id=%d active id=%d", ErrTerminalAlternateScreenLease, leaseID, s.alternateLeaseID)
	}
	write := s.writeTerminalBytesLocked(bytes)
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
// a full recovery repaint. Exit clears physical ownership even on an I/O
// error: retrying DEC 1049 blindly after a partial write could corrupt a later
// fullscreen session more severely than treating the projection as Unknown.
func (s *TerminalSession) ExitAlternateScreen(leaseID uint64) error {
	if s == nil || leaseID == 0 {
		return ErrTerminalAlternateScreenLease
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alternateLeaseID == 0 {
		return nil
	}
	if s.alternateLeaseID != leaseID {
		return fmt.Errorf("%w: want id=%d active id=%d", ErrTerminalAlternateScreenLease, leaseID, s.alternateLeaseID)
	}

	write := s.writeTerminalBytesLocked("\x1b[?25h\x1b[r\x1b[?1049l")
	s.alternateLeaseID = 0
	s.lease = LeaseState{}
	if s.screen != nil {
		s.screen.Invalidate()
	}
	s.cursor = nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.colorProfile == profile {
		return
	}
	s.colorProfile = profile
	s.frameTheme.Terminal.ColorProfile = profile
	s.frameTheme.UseHyperlink = profile.Hyperlinks
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frameTheme = theme
	s.colorProfile = theme.Terminal.ColorProfile
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

// FlushTransaction is the Phase 3 physical transaction boundary:
// lease/generation -> geometry -> optional history handoff -> viewport diff ->
// cursor -> one complete target write -> front confirmation. History is
// deferred, rather than replayed against an Unknown cache, while this call
// establishes the required source-backed recovery frame.
//
// The unified chat setup installs this only after FixedBottomSurface has been
// fenced from physical output. It must never be connected beside the legacy
// historyWindow handoff path.
func (s *TerminalSession) FlushTransaction(plan TerminalTransactionPlan) TerminalTransactionResult {
	plan = plan.Clone()
	if s == nil || !plan.Valid() {
		result := TerminalFrameResult{Err: ErrInvalidTerminalFrame}
		if plan.History != nil {
			history := HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
			return TerminalTransactionResult{Frame: result, History: &history}
		}
		return TerminalTransactionResult{Frame: result}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushTransactionLocked(plan)
}

func (s *TerminalSession) flushTransactionLocked(plan TerminalTransactionPlan) TerminalTransactionResult {
	frame := plan.Frame
	if s.writer == nil || s.presenter == nil || s.screen == nil {
		result := TerminalFrameResult{Frame: s.frame, Err: ErrTerminalWriterMissing}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Err: ErrTerminalWriterMissing})
	}
	if frame.LayoutGeneration < s.generation {
		result := TerminalFrameResult{Frame: s.frame, Err: ErrStaleTerminalFrame}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Deferred: true})
	}

	if s.alternateLeaseID != 0 || frame.Lease.Active {
		s.geometry = frame.Geometry
		s.generation = frame.LayoutGeneration
		if s.alternateLeaseID != 0 {
			s.lease = LeaseState{ID: s.alternateLeaseID, Active: true}
		} else {
			s.lease = frame.Lease
		}
		s.outputBottom = frame.OutputBottomRow
		s.cursor = nil
		result := TerminalFrameResult{Frame: s.frame, Deferred: true}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Deferred: true})
	}

	// A handoff needs an already-confirmed physical frame at the same geometry.
	// Resize and lease release make that proof unavailable, so establish a full
	// recovery frame now and leave the reducer-owned token Pending for a later
	// transaction.
	projectionKnown := s.screen.ProjectionValidity() == renderengine.ProjectionKnown
	if s.lease.Active {
		s.screen.Invalidate()
		projectionKnown = false
	}
	s.lease = frame.Lease
	if width, height := s.screen.Size(); width != frame.Geometry.Width || height != frame.Geometry.Height {
		s.screen.Resize(frame.Geometry.Width, frame.Geometry.Height)
		projectionKnown = false
	}
	s.geometry = frame.Geometry
	s.generation = frame.LayoutGeneration
	s.outputBottom = frame.OutputBottomRow

	fullRepaint := s.screen.ProjectionValidity() != renderengine.ProjectionKnown
	rows, err := terminalFrameCells(frame.Rows, frame.RenderRows, frame.Geometry.Width, frame.Geometry.Height, s.frameTheme)
	if err != nil {
		result := TerminalFrameResult{Frame: s.frame, FullRepaint: fullRepaint, Err: err}
		return terminalTransactionWithHistory(result, plan.History, HistoryCommitResult{Deferred: true})
	}
	historyResult := (*HistoryCommitResult)(nil)
	historyBytes := ""
	var historyCells [][]vt.Cell
	var delivered []HistoryCommit
	if plan.History != nil {
		result := HistoryCommitResult{Deferred: true}
		if !projectionKnown && s.outputBottom > 0 && plan.History.LayoutGeneration == s.generation && len(plan.BootstrapHistory) > 0 {
			bootstrapRows, bootstrapCells, bootstrapErr := terminalHistoryBootstrapRows(plan.BootstrapHistory, frame, s.frameTheme)
			if bootstrapErr != nil {
				result = HistoryCommitResult{Err: bootstrapErr}
			} else if handoff := renderengine.NewHandoffPlan(s.geometry.Height, s.geometry.Height, bootstrapRows); handoff.Empty() {
				result = HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
			} else {
				// A host only guarantees global terminal scrollback for a full-screen
				// scroll. Bootstrap appends semantic history followed by one complete
				// target frame, which pushes the history into native scrollback while
				// leaving the target frame as the visible physical tail.
				historyBytes = handoff.ANSI()
				historyCells = bootstrapCells
				delivered = cloneHistoryCommits(plan.BootstrapHistory)
				result = HistoryCommitResult{}
			}
		} else if projectionKnown && s.outputBottom > 0 && plan.History.LayoutGeneration == s.generation {
			candidateRows, candidateCells, err := terminalHistoryHandoffRows(plan.History.Lines, s.frameTheme, s.geometry.Width)
			if err != nil {
				result = HistoryCommitResult{Err: err}
			} else if s.screen.RegionPrefixEquals(1, s.outputBottom, candidateCells) {
				// The outgoing semantic range is already the physical prefix of the
				// current screen. Push it through the full terminal height so real
				// ConPTY hosts commit it to global scrollback; blank rows avoid
				// replaying the entering transcript tail before the source-backed
				// frame repaint below.
				blankRows, blankCells := terminalBlankHandoffRows(len(candidateRows), s.geometry.Width)
				if handoff := renderengine.NewHandoffPlan(s.geometry.Height, s.geometry.Height, blankRows); handoff.Empty() {
					result = HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
				} else {
					historyBytes = handoff.ANSI()
					historyCells = blankCells
					result = HistoryCommitResult{}
				}
			} else if len(plan.BootstrapHistory) > 0 {
				bootstrapRows, bootstrapCells, bootstrapErr := terminalHistoryBootstrapRows(plan.BootstrapHistory, frame, s.frameTheme)
				if bootstrapErr != nil {
					result = HistoryCommitResult{Err: bootstrapErr}
				} else if handoff := renderengine.NewHandoffPlan(s.geometry.Height, s.geometry.Height, bootstrapRows); handoff.Empty() {
					result = HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
				} else {
					// Bootstrap appends a full target frame after semantic history. That
					// guarantees the prefix crosses the real terminal's top boundary,
					// while the target frame itself remains visible rather than being
					// duplicated in native scrollback.
					historyBytes = handoff.ANSI()
					historyCells = bootstrapCells
					delivered = cloneHistoryCommits(plan.BootstrapHistory)
					result = HistoryCommitResult{}
				}
			} else if handoff := renderengine.NewHandoffPlan(s.geometry.Height, s.outputBottom, candidateRows); handoff.Empty() {
				// Compatibility callers that do not supply the AppState-derived
				// bootstrap batch retain the original isolated sink behavior.
				// Unified TerminalSessionExecutor always supplies a batch, so this
				// path cannot define production history ownership.
				result = HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
			} else {
				historyBytes = handoff.ANSI()
				historyCells = candidateCells
				result = HistoryCommitResult{}
			}
		}
		historyResult = &result
	}
	if historyBytes != "" {
		// Handoffs always scroll the full physical terminal. Mirror that state
		// before diffing the source-backed frame; a failed write below invalidates
		// this tentative cache mutation.
		s.screen.ApplyRegionAppend(1, s.geometry.Height, historyCells)
	}
	s.screen.StageFrame(rows)
	bytes := historyBytes + s.screen.PrepareFlush()
	if cursor := terminalCursorSequence(frame.Cursor, frame.Geometry.Width, frame.Geometry.Height); cursor != "" {
		bytes += cursor
	}
	if write := s.writeTerminalBytesLocked(bytes); write.Err != nil {
		s.screen.MarkWriteFailed()
		s.cursor = nil
		frameResult := TerminalFrameResult{Frame: s.frame, FullRepaint: fullRepaint, Err: write.Err}
		if historyResult != nil && historyBytes != "" {
			*historyResult = HistoryCommitResult{Err: write.Err, MayHavePartiallyWritten: write.MayHavePartiallyWritten}
		}
		return TerminalTransactionResult{Frame: frameResult, History: historyResult}
	}
	s.screen.ConfirmFlush()
	s.cursor = cloneTerminalCursor(frame.Cursor)
	s.frame++
	frameResult := TerminalFrameResult{Frame: s.frame, FullRepaint: fullRepaint}
	if historyResult != nil && historyBytes != "" {
		*historyResult = HistoryCommitResult{Frame: s.frame, Delivered: delivered}
	}
	return TerminalTransactionResult{Frame: frameResult, History: historyResult}
}

func terminalTransactionWithHistory(frame TerminalFrameResult, history *HistoryCommit, result HistoryCommitResult) TerminalTransactionResult {
	if history == nil {
		return TerminalTransactionResult{Frame: frame}
	}
	return TerminalTransactionResult{Frame: frame, History: &result}
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
	if !commit.Valid() {
		return HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer == nil || s.presenter == nil || s.screen == nil {
		return HistoryCommitResult{Err: ErrTerminalWriterMissing}
	}
	if s.lease.Active || s.generation == 0 || commit.LayoutGeneration != s.generation ||
		s.outputBottom < 1 || s.screen.ProjectionValidity() != renderengine.ProjectionKnown {
		// No primary transaction has begun. The executor may return this token
		// to Pending without inventing a retry identity or marking projection
		// failure; a later source-backed frame/recovery owns the wake-up.
		return HistoryCommitResult{Deferred: true}
	}

	rows, cells, err := terminalHistoryHandoffRows(commit.Lines, s.frameTheme, s.geometry.Width)
	if err != nil {
		return HistoryCommitResult{Err: err}
	}
	// Do not use the fixed-bottom subregion here. Some real terminal hosts
	// render DECSTBM correctly but do not add rows from a subregion to their
	// global scrollback. A full-screen scroll is the portable native-history
	// boundary; the next source-backed frame restores fixed bottom rows.
	plan := renderengine.NewHandoffPlan(s.geometry.Height, s.geometry.Height, rows)
	if plan.Empty() {
		return HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
	}
	write := s.writeTerminalBytesLocked(plan.ANSI())
	if write.Err != nil {
		s.screen.MarkWriteFailed()
		s.cursor = nil
		return HistoryCommitResult{
			Err:                     write.Err,
			MayHavePartiallyWritten: write.MayHavePartiallyWritten,
		}
	}

	// HandoffPlan appends each encoded display row at the bottom of the exact
	// DECSTBM region. Mirror the successful physical scroll in the cache before
	// another frame uses its front buffer. No terminal/source data is read back.
	s.screen.ApplyRegionAppend(1, s.outputBottom, cells)
	s.frame++
	return HistoryCommitResult{Frame: s.frame}
}

// terminalHistoryHandoffRows retains the existing rich render IR through the
// same explicit ThemeContext used for viewport rows. It does not parse source
// strings or build a second formatter; HistoryCommit.Lines already are the
// selected layout-generation display rows.
func terminalHistoryHandoffRows(lines []render.Line, theme style.ThemeContext, width int) ([]string, [][]vt.Cell, error) {
	if width < 1 || len(lines) == 0 {
		return nil, nil, ErrInvalidHistoryHandoff
	}
	for index, line := range lines {
		if render.LineWidth(line) > width {
			return nil, nil, fmt.Errorf("%w: row %d exceeds width %d", ErrInvalidHistoryHandoff, index+1, width)
		}
	}
	document := style.NewResolver(theme).ResolveDocument(render.LinesDoc(lines...))
	rows := (render.ANSIBackend{Profile: theme.Terminal.ColorProfile}).RenderLines(document)
	if len(rows) != len(lines) {
		return nil, nil, ErrInvalidHistoryHandoff
	}
	cells := make([][]vt.Cell, len(rows))
	for index, row := range rows {
		if strings.ContainsAny(row, "\r\n") {
			return nil, nil, fmt.Errorf("%w: encoded row %d contains a line control", ErrInvalidHistoryHandoff, index+1)
		}
		screen := vt.NewScreen(width, 2)
		screen.Feed(row)
		cells[index] = screen.CellRows(1, 1)[0]
	}
	return rows, cells, nil
}

// terminalFrameOutputLines returns the source-backed render lines in the
// primary output region. It never reads the front buffer; callers use it only
// to prepare a physical append that makes an already-proven front prefix leave
// the terminal viewport.
func terminalFrameOutputLines(frame TerminalFramePlan, count int) ([]render.Line, error) {
	if count < 1 || count > frame.OutputBottomRow || frame.OutputBottomRow > len(frame.Rows) {
		return nil, ErrInvalidHistoryHandoff
	}
	start := frame.OutputBottomRow - count
	lines := make([]render.Line, 0, count)
	for index := start; index < frame.OutputBottomRow; index++ {
		if len(frame.RenderRows) == len(frame.Rows) {
			lines = append(lines, cloneAppRenderLine(frame.RenderRows[index]))
			continue
		}
		lines = append(lines, appPlainRenderLine(frame.Rows[index].Text))
	}
	return lines, nil
}

func terminalFrameIncomingHandoffRows(frame TerminalFramePlan, count int, theme style.ThemeContext) ([]string, [][]vt.Cell, error) {
	lines, err := terminalFrameOutputLines(frame, count)
	if err != nil {
		return nil, nil, err
	}
	return terminalHistoryHandoffRows(lines, theme, frame.Geometry.Width)
}

func terminalBlankHandoffRows(count, width int) ([]string, [][]vt.Cell) {
	if count < 1 || width < 1 {
		return nil, nil
	}
	rows := make([]string, count)
	cells := make([][]vt.Cell, count)
	for index := range cells {
		cells[index] = make([]vt.Cell, width)
	}
	return rows, cells
}

// terminalHistoryBootstrapRows writes all pending semantic history before one
// complete target frame. The full frame forces the history prefix through a
// real terminal's global top boundary, while it itself remains on screen.
func terminalHistoryBootstrapRows(history []HistoryCommit, frame TerminalFramePlan, theme style.ThemeContext) ([]string, [][]vt.Cell, error) {
	if len(history) == 0 || frame.Geometry.Height < 1 || len(frame.Rows) != frame.Geometry.Height {
		return nil, nil, ErrInvalidHistoryHandoff
	}
	lines := make([]render.Line, 0)
	for _, commit := range history {
		if !commit.Valid() {
			return nil, nil, ErrInvalidHistoryHandoff
		}
		lines = append(lines, cloneRenderLines(commit.Lines)...)
	}
	tail, err := terminalFrameScreenLines(frame)
	if err != nil {
		return nil, nil, err
	}
	lines = append(lines, tail...)
	return terminalHistoryHandoffRows(lines, theme, frame.Geometry.Width)
}

func terminalFrameScreenLines(frame TerminalFramePlan) ([]render.Line, error) {
	if frame.Geometry.Width < 1 || frame.Geometry.Height < 1 || len(frame.Rows) != frame.Geometry.Height ||
		(len(frame.RenderRows) != 0 && len(frame.RenderRows) != len(frame.Rows)) {
		return nil, ErrInvalidHistoryHandoff
	}
	lines := make([]render.Line, 0, len(frame.Rows))
	for index := range frame.Rows {
		if len(frame.RenderRows) == len(frame.Rows) {
			lines = append(lines, cloneAppRenderLine(frame.RenderRows[index]))
			continue
		}
		lines = append(lines, appPlainRenderLine(frame.Rows[index].Text))
	}
	return lines, nil
}

type terminalWriteResult struct {
	Err                     error
	MayHavePartiallyWritten bool
}

// writeTerminalBytesLocked emits one fully assembled terminal transaction.
// Callers hold s.mu; Presenter serializes the actual terminal bytes globally.
// A writer panic is made equivalent to a possible partial write, so callers
// never retain a Known front after an interrupted transaction.
func (s *TerminalSession) writeTerminalBytesLocked(bytes string) (result terminalWriteResult) {
	if bytes == "" {
		return result
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
				return nil, fmt.Errorf("%w: structured row %d plain text %q differs from text row %q", ErrInvalidTerminalFrame, index+1, plain, row.Text)
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
