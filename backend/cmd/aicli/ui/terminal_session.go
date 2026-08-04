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
)

// TerminalFramePlan is the physical-frame input derived from exactly one
// AppState snapshot. It carries no terminal cache and never contains history
// ownership progress: those remain respectively in TerminalSession and the
// reducer-owned HistoryEffectQueue.
//
// This is intentionally a Phase 3 seam. It is not installed into the legacy
// FixedBottomSurface yet, because doing so before the one-writer cutover would
// make the old historyWindow and this session write the primary terminal.
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
	Frame   TerminalFramePlan
	History *HistoryCommit
}

// ComposeTerminalTransactionPlan derives an immutable frame and optionally
// attaches one already-claimed HistoryCommit. The commit is cloned because its
// render lines are reducer-owned payload until the eventual Ack.
func ComposeTerminalTransactionPlan(state AppState, history *HistoryCommit) TerminalTransactionPlan {
	plan := TerminalTransactionPlan{Frame: ComposeTerminalFramePlan(state)}
	if history != nil {
		clone := history.Clone()
		plan.History = &clone
	}
	return plan
}

func (p TerminalTransactionPlan) Valid() bool {
	return p.Frame.Valid() && (p.History == nil || p.History.Valid())
}

func (p TerminalTransactionPlan) Clone() TerminalTransactionPlan {
	p.Frame.Rows = cloneTerminalFrameRows(p.Frame.Rows)
	p.Frame.RenderRows = cloneRenderLines(p.Frame.RenderRows)
	p.Frame.Cursor = cloneTerminalCursor(p.Frame.Cursor)
	if p.History != nil {
		clone := p.History.Clone()
		p.History = &clone
	}
	return p
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
// serializes the eventual terminal write with the repository-wide terminal
// lock. TerminalSession is currently testable but deliberately uninstalled in
// production while FixedBottomSurface remains the legacy writer.
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
	colorProfile render.ColorProfile
	frameTheme   style.ThemeContext
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

// FlushAppState is the convenience form for the eventual presenter call site.
// It remains pure on the input side: all composition occurs before terminal
// bytes are prepared, and no AppState field is modified.
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
// This remains a non-production seam until FixedBottomSurface is replaced in
// one cutover. In particular it must not be connected beside the legacy
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

	if frame.Lease.Active {
		s.geometry = frame.Geometry
		s.generation = frame.LayoutGeneration
		s.lease = frame.Lease
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
	historyResult := (*HistoryCommitResult)(nil)
	historyBytes := ""
	var historyCells [][]vt.Cell
	if plan.History != nil {
		result := HistoryCommitResult{Deferred: true}
		if projectionKnown && s.outputBottom > 0 && plan.History.LayoutGeneration == s.generation {
			rows, cells, err := terminalHistoryHandoffRows(plan.History.Lines, s.frameTheme, s.geometry.Width)
			if err != nil {
				result = HistoryCommitResult{Err: err}
			} else if handoff := renderengine.NewHandoffPlan(s.geometry.Height, s.outputBottom, rows); handoff.Empty() {
				result = HistoryCommitResult{Err: ErrInvalidHistoryHandoff}
			} else {
				historyBytes = handoff.ANSI()
				historyCells = cells
				result = HistoryCommitResult{}
			}
		}
		historyResult = &result
	}

	rows, err := terminalFrameCells(frame.Rows, frame.RenderRows, frame.Geometry.Width, frame.Geometry.Height, s.frameTheme)
	if err != nil {
		result := TerminalFrameResult{Frame: s.frame, FullRepaint: fullRepaint, Err: err}
		return TerminalTransactionResult{Frame: result, History: historyResult}
	}
	if historyBytes != "" {
		// Match the handoff before diffing the next semantic viewport. This
		// cache mutation is tentative until the one assembled write succeeds;
		// any error below marks it Unknown and forces source-backed recovery.
		s.screen.ApplyRegionAppend(1, s.outputBottom, historyCells)
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
		*historyResult = HistoryCommitResult{Frame: s.frame}
	}
	return TerminalTransactionResult{Frame: frameResult, History: historyResult}
}

func terminalTransactionWithHistory(frame TerminalFrameResult, history *HistoryCommit, result HistoryCommitResult) TerminalTransactionResult {
	if history == nil {
		return TerminalTransactionResult{Frame: frame}
	}
	return TerminalTransactionResult{Frame: frame, History: &result}
}

// CommitHistory implements HistoryCommitSink for the eventual one-writer
// primary presenter. It intentionally has no FixedBottomSurface adapter: that
// surface still owns legacy historyWindow handoff in production, and wiring
// both paths would duplicate native scrollback.
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
	plan := renderengine.NewHandoffPlan(s.geometry.Height, s.outputBottom, rows)
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
