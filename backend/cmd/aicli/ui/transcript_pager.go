package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"golang.org/x/term"
)

// TranscriptPagerSnapshot is the read-only semantic input to the transcript
// overlay. Cells are committed history; Active is a separate mutable live tail.
// It deliberately contains no ScreenModel, ActiveBand, or terminal-row cache.
type TranscriptPagerSnapshot struct {
	Transcript TranscriptState
	Active     ActiveCellState
}

func (s TranscriptPagerSnapshot) Clone() TranscriptPagerSnapshot {
	s.Transcript = s.Transcript.Clone()
	s.Active = s.Active.Clone()
	return s
}

// TranscriptPagerView is one coherent actor-owned read for the alternate
// screen. It intentionally keeps semantic content and user-owned pager state
// together: reading them from separate AppState snapshots can pair a new
// anchor with an older transcript during a concurrent runtime update.
//
// PagerKnown is false while a lease has not yet opened its overlay or after it
// has closed. The pager then retains transient local state only for that
// interval; it never writes that state back outside a durable actor action.
type TranscriptPagerView struct {
	Snapshot   TranscriptPagerSnapshot
	Pager      TranscriptPagerState
	PagerKnown bool
}

func (v TranscriptPagerView) Clone() TranscriptPagerView {
	v.Snapshot = v.Snapshot.Clone()
	v.Pager = v.Pager.Clone()
	return v
}

// TranscriptPagerModel is a detached projection of semantic transcript state.
// Mutable Scene cells are represented only by LiveTail, so finalization cannot
// duplicate a cell between committed history and the active band projection.
type TranscriptPagerModel struct {
	Revision uint64
	Cells    []scene.TranscriptCell
	LiveTail *TranscriptPagerLiveTail
}

type TranscriptPagerLiveTail struct {
	CellID           scene.CellID
	Revision         uint64
	Kind             scene.CellKind
	Source           string
	ChainKey         string
	BoundaryGroupKey string
	Boundary         boundary.BoundaryClass
}

func (m TranscriptPagerModel) Clone() TranscriptPagerModel {
	m.Cells = append([]scene.TranscriptCell(nil), m.Cells...)
	if m.LiveTail != nil {
		live := *m.LiveTail
		m.LiveTail = &live
	}
	return m
}

// NewTranscriptPagerModel builds a pager model from a semantic snapshot.
// Non-mutable cells remain in history even after native-scrollback handoff;
// their Scene phase is not a visibility decision for the alternate screen.
func NewTranscriptPagerModel(snapshot TranscriptPagerSnapshot) TranscriptPagerModel {
	model := TranscriptPagerModel{
		Revision: snapshot.Transcript.Revision,
		Cells:    make([]scene.TranscriptCell, 0, len(snapshot.Transcript.Cells)),
	}
	for _, cell := range snapshot.Transcript.Cells {
		if cell.Phase == scene.CellMutable {
			continue
		}
		model.Cells = append(model.Cells, cloneTranscriptCell(cell))
	}
	if snapshot.Active.Phase == ActiveCellMutable || snapshot.Active.Phase == ActiveCellFinalizing {
		metadata := transcriptPagerActiveCellMetadata(snapshot.Transcript, snapshot.Active.CellID)
		model.LiveTail = &TranscriptPagerLiveTail{
			CellID:           snapshot.Active.CellID,
			Revision:         snapshot.Active.Revision,
			Kind:             metadata.Kind,
			Source:           snapshot.Active.Source,
			ChainKey:         metadata.ChainKey,
			BoundaryGroupKey: metadata.BoundaryGroupKey,
			Boundary:         metadata.Boundary,
		}
	}
	return model
}

func transcriptPagerActiveCellMetadata(transcript TranscriptState, id scene.CellID) scene.TranscriptCell {
	for index := len(transcript.Cells) - 1; index >= 0; index-- {
		if transcript.Cells[index].ID == id {
			return transcript.Cells[index]
		}
	}
	return scene.TranscriptCell{ID: id, Kind: scene.KindAssistant}
}

// TranscriptPagerAnchor identifies the top content row semantically. Row is
// local to the cell rather than a global offset, and LayoutGeneration records
// the layout that produced it. Offset is retained only as a fallback when a
// history replacement removes the anchored cell.
type TranscriptPagerAnchor struct {
	CellID           scene.CellID
	Row              int
	LayoutGeneration uint64
	Offset           int
}

// TranscriptPagerState is the user-owned view state of the alternate screen.
// FollowBottom is explicit: incoming history moves a bottom-following pager,
// while a user who has scrolled up remains anchored to the inspected cell.
type TranscriptPagerState struct {
	Anchor       TranscriptPagerAnchor
	FollowBottom bool
	Width        int
}

func NewTranscriptPagerState() TranscriptPagerState {
	return TranscriptPagerState{FollowBottom: true}
}

func (s TranscriptPagerState) Clone() TranscriptPagerState { return s }

type TranscriptPagerRow struct {
	CellID       scene.CellID
	CellRevision uint64
	CellRow      int
	Live         bool
	Text         string
}

// Rows derives visual rows entirely from semantic source. It is intentionally
// small and plain at this migration point: rich document rendering can replace
// this formatter without changing pager state, scrolling, or lease ownership.
func (m TranscriptPagerModel) Rows(width int) []TranscriptPagerRow {
	if width < 1 {
		width = 1
	}
	rows := make([]TranscriptPagerRow, 0, len(m.Cells)*3)
	var previous *scene.TranscriptCell
	for index := range m.Cells {
		cell := &m.Cells[index]
		if previous != nil && boundary.ResolveGap(previous.BoundaryMeta(), cell.BoundaryMeta()) == boundary.GapOne {
			rows = append(rows, TranscriptPagerRow{Text: ""})
		}
		rows = appendTranscriptPagerCellRows(rows, *cell, width, false)
		previous = cell
	}
	if m.LiveTail != nil {
		liveCell := scene.TranscriptCell{
			ID: m.LiveTail.CellID, Revision: m.LiveTail.Revision,
			Kind: m.LiveTail.Kind, Source: m.LiveTail.Source,
			ChainKey: m.LiveTail.ChainKey, BoundaryGroupKey: m.LiveTail.BoundaryGroupKey,
			Boundary: m.LiveTail.Boundary, Phase: scene.CellMutable,
		}
		if previous != nil && boundary.ResolveGap(previous.BoundaryMeta(), liveCell.BoundaryMeta()) == boundary.GapOne {
			rows = append(rows, TranscriptPagerRow{Text: ""})
		}
		rows = appendTranscriptPagerCellRows(rows, liveCell, width, true)
	}
	return rows
}

func appendTranscriptPagerCellRows(rows []TranscriptPagerRow, cell scene.TranscriptCell, width int, live bool) []TranscriptPagerRow {
	cellRow := 0
	header := transcriptPagerCellLabel(cell.Kind)
	if live {
		header += " (streaming)"
	}
	rows = append(rows, TranscriptPagerRow{
		CellID: cell.ID, CellRevision: cell.Revision, CellRow: cellRow, Live: live,
		Text: header,
	})
	cellRow++
	source := strings.ReplaceAll(SanitizeTerminalText(cell.Source), "\r\n", "\n")
	for _, logical := range strings.Split(source, "\n") {
		parts := wrapTranscriptPagerText(logical, max(1, width-2))
		if len(parts) == 0 {
			parts = []string{""}
		}
		for _, part := range parts {
			rows = append(rows, TranscriptPagerRow{
				CellID: cell.ID, CellRevision: cell.Revision, CellRow: cellRow, Live: live,
				Text: "  " + part,
			})
			cellRow++
		}
	}
	return rows
}

func transcriptPagerCellLabel(kind scene.CellKind) string {
	switch kind {
	case scene.KindUser:
		return "User"
	case scene.KindAssistant:
		return "Assistant"
	case scene.KindToolChain:
		return "Tool"
	case scene.KindReasoning:
		return "Reasoning"
	case scene.KindSupplement:
		return "Supplement"
	case scene.KindCommand:
		return "Command"
	case scene.KindSystem:
		return "System"
	case scene.KindDiagnostic:
		return "Diagnostic"
	case scene.KindRuntimeEvent:
		return "Event"
	default:
		return "Transcript"
	}
}

func wrapTranscriptPagerText(value string, width int) []string {
	if width < 1 {
		return []string{""}
	}
	if value == "" {
		return []string{""}
	}
	lines := make([]string, 0, 1)
	remaining := value
	for remaining != "" {
		if DisplayWidth(remaining) <= width {
			lines = append(lines, remaining)
			break
		}
		head, tail := splitFullScreenText(remaining, width)
		if head == "" {
			head, tail = truncateFullScreenText(remaining, width), ""
		}
		lines = append(lines, head)
		remaining = tail
	}
	return lines
}

// Reconcile applies a new semantic model while preserving the reader's
// position. The anchor is resolved after every model or layout change; a
// removed anchor falls back to the last known offset and is clamped safely.
func (s *TranscriptPagerState) Reconcile(model TranscriptPagerModel, width, viewportRows int) {
	if s == nil {
		return
	}
	if width < 1 {
		width = 1
	}
	if viewportRows < 1 {
		viewportRows = 1
	}
	if s.Width != width {
		s.Width = width
		s.Anchor.LayoutGeneration++
	}
	rows := model.Rows(width)
	maxOffset := transcriptPagerMaxOffset(len(rows), viewportRows)
	if s.FollowBottom {
		s.Anchor.Offset = maxOffset
		s.captureAnchor(rows, maxOffset)
		return
	}
	if offset, ok := findTranscriptPagerAnchor(rows, s.Anchor); ok {
		s.Anchor.Offset = offset
	} else if s.Anchor.Offset > maxOffset {
		s.Anchor.Offset = maxOffset
	}
	if s.Anchor.Offset < 0 {
		s.Anchor.Offset = 0
	}
	s.captureAnchor(rows, s.Anchor.Offset)
}

func (s *TranscriptPagerState) Scroll(model TranscriptPagerModel, width, viewportRows, delta int) {
	if s == nil {
		return
	}
	s.Reconcile(model, width, viewportRows)
	rows := model.Rows(max(1, width))
	maxOffset := transcriptPagerMaxOffset(len(rows), max(1, viewportRows))
	next := s.Anchor.Offset + delta
	if next < 0 {
		next = 0
	}
	if next > maxOffset {
		next = maxOffset
	}
	s.Anchor.Offset = next
	s.FollowBottom = next == maxOffset
	s.captureAnchor(rows, next)
}

func (s *TranscriptPagerState) SetFollowBottom(model TranscriptPagerModel, width, viewportRows int, follow bool) {
	if s == nil {
		return
	}
	s.FollowBottom = follow
	s.Reconcile(model, width, viewportRows)
}

func (s *TranscriptPagerState) captureAnchor(rows []TranscriptPagerRow, offset int) {
	if s == nil || len(rows) == 0 {
		return
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rows) {
		offset = len(rows) - 1
	}
	for index := offset; index < len(rows); index++ {
		if rows[index].CellID == 0 {
			continue
		}
		s.Anchor.CellID = rows[index].CellID
		s.Anchor.Row = rows[index].CellRow
		return
	}
	for index := offset - 1; index >= 0; index-- {
		if rows[index].CellID == 0 {
			continue
		}
		s.Anchor.CellID = rows[index].CellID
		s.Anchor.Row = rows[index].CellRow
		return
	}
}

func findTranscriptPagerAnchor(rows []TranscriptPagerRow, anchor TranscriptPagerAnchor) (int, bool) {
	if anchor.CellID == 0 {
		return 0, false
	}
	lastCellOffset := -1
	for index, row := range rows {
		if row.CellID != anchor.CellID {
			continue
		}
		lastCellOffset = index
		if row.CellRow >= anchor.Row {
			return index, true
		}
	}
	return lastCellOffset, lastCellOffset >= 0
}

func transcriptPagerMaxOffset(rowCount, viewportRows int) int {
	if viewportRows < 1 || rowCount <= viewportRows {
		return 0
	}
	return rowCount - viewportRows
}

func transcriptPagerViewportRows(height int) int {
	rows := height - 3
	if rows < 1 {
		return 1
	}
	return rows
}

func renderTranscriptPagerFrame(model TranscriptPagerModel, state TranscriptPagerState, width, height int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	viewportRows := transcriptPagerViewportRows(height)
	state.Reconcile(model, width, viewportRows)
	rows := model.Rows(width)
	start := state.Anchor.Offset
	end := min(start+viewportRows, len(rows))
	lines := make([]string, height)
	if height > 0 {
		label := "Transcript"
		if model.LiveTail != nil {
			label += "  streaming"
		}
		lines[0] = label
	}
	for index := start; index < end && index-start+1 < height; index++ {
		lines[index-start+1] = rows[index].Text
	}
	if height > 1 {
		position := "top"
		if state.FollowBottom {
			position = "bottom"
		}
		lines[height-1] = "" + position + "  " + transcriptPagerPosition(start, len(rows))
	}
	var builder strings.Builder
	builder.WriteString("\x1b[H")
	for row, line := range lines {
		builder.WriteString("\x1b[2K")
		builder.WriteString(fitFullScreenText(line, width))
		if row < len(lines)-1 {
			builder.WriteString("\r\n")
		}
	}
	return builder.String()
}

func transcriptPagerPosition(offset, total int) string {
	if total <= 0 {
		return "0/0"
	}
	return strings.TrimSpace(strings.Join([]string{strconv.Itoa(offset + 1), strconv.Itoa(total)}, "/"))
}

// TranscriptPagerOptions configures the alternate-screen transcript reader.
// Snapshot must return a coherent semantic snapshot and must not read terminal
// state or a legacy history-window projection.
type TranscriptPagerOptions struct {
	// View supplies one coherent AppState-derived content/scroll snapshot. The
	// production chat pager uses it so content and anchor share one actor read.
	// When provided, View takes precedence over Snapshot and ViewState.
	View func() TranscriptPagerView
	// Snapshot supplies the immutable semantic document. In the production chat
	// path prefer View; standalone callers may use this compatibility seam.
	// It must never read a surface/history-window projection.
	Snapshot func() TranscriptPagerSnapshot
	// ViewState supplies the actor-owned scroll anchor for the current lease.
	// When absent, the pager retains a local state only for standalone callers
	// and deterministic tests.
	ViewState func() (TranscriptPagerState, bool)
	// PostAction submits durable pager input to the UI actor. A configured
	// ViewState without PostAction is intentionally read-only.
	PostAction func(UIAction) bool
}

type transcriptPagerLoopHooks struct {
	refreshSize func() (int, int)
	view        func() TranscriptPagerView
	snapshot    func() TranscriptPagerSnapshot
	viewState   func() (TranscriptPagerState, bool)
	postAction  func(UIAction) bool
	leaseID     uint64
	writeFrame  func(string) error
	readKey     func(context.Context) (editorKey, bool, error)
}

type transcriptPagerLifecycle struct {
	writer       io.Writer
	restoreRaw   func() error
	leaseManaged bool
}

const transcriptPagerPollInterval = 75 * time.Millisecond

// RunTranscriptPagerWithLease opens a read-only transcript pager. When lease
// is active it owns the alternate screen transport, otherwise the pager enters
// and leaves alternate screen itself. A pager never writes into the primary
// buffer while an alternate lease is held.
func RunTranscriptPagerWithLease(ctx context.Context, terminal *Terminal, options TranscriptPagerOptions, lease ScreenLease) error {
	leaseID := uint64(0)
	leaseManaged := lease != nil && lease.Active()
	if leaseManaged {
		leaseID = lease.ID()
	}
	return runTranscriptPagerWithLease(ctx, terminal, options, os.Stdin, os.Stdout, lease, leaseManaged, leaseID)
}

func runTranscriptPager(ctx context.Context, terminal *Terminal, options TranscriptPagerOptions, reader io.Reader, writer io.Writer, leaseManaged bool, leaseID uint64) error {
	return runTranscriptPagerWithLease(ctx, terminal, options, reader, writer, nil, leaseManaged, leaseID)
}

func runTranscriptPagerWithLease(ctx context.Context, terminal *Terminal, options TranscriptPagerOptions, reader io.Reader, writer io.Writer, lease ScreenLease, leaseManaged bool, leaseID uint64) error {
	if terminal == nil || !terminal.SupportsANSI() || (options.View == nil && options.Snapshot == nil) || reader == nil || writer == nil {
		return ErrFullScreenUnavailable
	}
	stdinFile, _ := reader.(*os.File)
	if stdinFile == nil || !term.IsTerminal(int(stdinFile.Fd())) {
		return ErrFullScreenUnavailable
	}
	_, height := terminal.RefreshSize()
	if height < minFullScreenListHeight {
		return fullScreenUnavailable("terminal height is too small", nil)
	}
	rawState, err := term.MakeRaw(int(stdinFile.Fd()))
	if err != nil {
		return fullScreenUnavailable("enable raw mode", err)
	}
	pending := takeInteractiveInputCarryover()
	defer func() { storeInteractiveInputCarryover(pending) }()
	hooks := transcriptPagerLoopHooks{
		refreshSize: terminal.RefreshSize,
		view:        options.View,
		snapshot:    options.Snapshot,
		viewState:   options.ViewState,
		postAction:  options.PostAction,
		leaseID:     leaseID,
		writeFrame: func(frame string) error {
			return writeLeaseManagedFullScreenText(lease, writer, frame)
		},
		readKey: func(readCtx context.Context) (editorKey, bool, error) {
			pollCtx, cancel := context.WithTimeout(readCtx, transcriptPagerPollInterval)
			defer cancel()
			key, ok, readErr := nextInteractiveKey(pollCtx, reader, &pending, stdinFile)
			if errors.Is(readErr, context.DeadlineExceeded) && readCtx.Err() == nil {
				return editorKey{}, false, nil
			}
			return key, ok, readErr
		},
	}
	lifecycle := transcriptPagerLifecycle{
		writer:       writer,
		restoreRaw:   func() error { return term.Restore(int(stdinFile.Fd()), rawState) },
		leaseManaged: leaseManaged,
	}
	if err := lifecycle.enter(); err != nil {
		return fullScreenUnavailable("enter alternate screen", errors.Join(err, lifecycle.close()))
	}
	runErr := runTranscriptPagerLoop(ctx, hooks)
	if closeErr := lifecycle.close(); closeErr != nil {
		return fullScreenUnavailable("restore terminal", errors.Join(runErr, closeErr))
	}
	return runErr
}

func (l transcriptPagerLifecycle) enter() error {
	if l.leaseManaged {
		return nil
	}
	return writeFullScreenSequences(l.writer,
		"\x1b[?1049h", "\x1b[r", "\x1b[?25l", "\x1b[2J", "\x1b[H")
}

func (l transcriptPagerLifecycle) close() error {
	var writeErr error
	if !l.leaseManaged {
		writeErr = writeFullScreenSequences(l.writer, "\x1b[?25h", "\x1b[r", "\x1b[?1049l")
	}
	if l.restoreRaw != nil {
		writeErr = errors.Join(writeErr, l.restoreRaw())
	}
	return writeErr
}

func runTranscriptPagerLoop(ctx context.Context, hooks transcriptPagerLoopHooks) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if hooks.refreshSize == nil || (hooks.view == nil && hooks.snapshot == nil) || hooks.writeFrame == nil || hooks.readKey == nil {
		return fullScreenUnavailable("transcript pager is not configured", nil)
	}
	state := NewTranscriptPagerState()
	lastWidth, lastHeight := -1, -1
	lastRevision := ^uint64(0)
	lastLiveID := scene.CellID(0)
	lastLiveRevision := ^uint64(0)
	lastLiveSource := ""
	lastViewState := TranscriptPagerState{}
	hasLastViewState := false
	dirty := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		width, height := hooks.refreshSize()
		if height < minFullScreenListHeight {
			return fullScreenUnavailable("terminal height is too small", nil)
		}
		view := TranscriptPagerView{}
		if hooks.view != nil {
			view = hooks.view().Clone()
		} else {
			view.Snapshot = hooks.snapshot().Clone()
			if hooks.viewState != nil {
				view.Pager, view.PagerKnown = hooks.viewState()
			}
		}
		snapshot := view.Snapshot
		model := NewTranscriptPagerModel(snapshot)
		viewState := view.Pager
		hasViewState := view.PagerKnown
		if hasViewState {
			state = viewState
		}
		viewStateChanged := hasViewState != hasLastViewState ||
			(hasViewState && viewState != lastViewState)
		liveID, liveRevision, liveSource := transcriptPagerLiveTailKey(model.LiveTail)
		if dirty || width != lastWidth || height != lastHeight || model.Revision != lastRevision ||
			liveID != lastLiveID || liveRevision != lastLiveRevision || liveSource != lastLiveSource ||
			viewStateChanged {
			state.Reconcile(model, width, transcriptPagerViewportRows(height))
			if err := hooks.writeFrame(renderTranscriptPagerFrame(model, state, width, height)); err != nil {
				return fullScreenUnavailable("write frame", err)
			}
			lastWidth, lastHeight, lastRevision = width, height, model.Revision
			lastLiveID, lastLiveRevision, lastLiveSource = liveID, liveRevision, liveSource
			lastViewState, hasLastViewState = viewState, hasViewState
			dirty = false
		}
		key, ok, err := hooks.readKey(ctx)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if transcriptPagerKeyCloses(key) {
			return nil
		}
		if hooks.postAction != nil {
			if action := transcriptPagerIntentForKey(hooks.leaseID, model, width, height, key); action != nil {
				if !hooks.postAction(action) {
					return fullScreenUnavailable("post transcript pager action", errors.New("ui actor is closed"))
				}
				// The actor is the production authority. The next iteration reads
				// its published anchor instead of mutating a second durable state.
				dirty = true
				continue
			}
		}
		// An actor-backed view is never allowed to fall back to a second
		// writable scroll state. If its action poster is unavailable, preserve
		// the published view until close instead of making a transient local
		// scroll that would race the next actor snapshot.
		if hooks.view != nil || hooks.viewState != nil {
			continue
		}
		if applyTranscriptPagerKey(&state, model, width, height, key) {
			return nil
		}
		dirty = true
	}
}

func transcriptPagerLiveTailKey(live *TranscriptPagerLiveTail) (scene.CellID, uint64, string) {
	if live == nil {
		return 0, 0, ""
	}
	return live.CellID, live.Revision, live.Source
}

func transcriptPagerIntentForKey(leaseID uint64, model TranscriptPagerModel, width, height int, key editorKey) UIAction {
	viewportRows := transcriptPagerViewportRows(height)
	switch key.kind {
	case editorKeyUp:
		return TranscriptPagerScroll{LeaseID: leaseID, Delta: -1}
	case editorKeyDown:
		return TranscriptPagerScroll{LeaseID: leaseID, Delta: 1}
	case editorKeyPageUp, editorKeyLeft:
		return TranscriptPagerScroll{LeaseID: leaseID, Delta: -viewportRows}
	case editorKeyPageDown, editorKeyRight:
		return TranscriptPagerScroll{LeaseID: leaseID, Delta: viewportRows}
	case editorKeyHome:
		return TranscriptPagerScroll{LeaseID: leaseID, Delta: -len(model.Rows(max(1, width)))}
	case editorKeyEnd:
		return TranscriptPagerSetFollowBottom{LeaseID: leaseID, Follow: true}
	case editorKeyRune:
		switch key.r {
		case 'j':
			return TranscriptPagerScroll{LeaseID: leaseID, Delta: 1}
		case 'k':
			return TranscriptPagerScroll{LeaseID: leaseID, Delta: -1}
		case 'g':
			return TranscriptPagerScroll{LeaseID: leaseID, Delta: -len(model.Rows(max(1, width)))}
		case 'G':
			return TranscriptPagerSetFollowBottom{LeaseID: leaseID, Follow: true}
		}
	}
	return nil
}

func transcriptPagerKeyCloses(key editorKey) bool {
	switch key.kind {
	case editorKeyCancelPopup, editorKeyInterrupt, editorKeyEOF, editorKeyTranspose:
		return true
	case editorKeyRune:
		return key.r == 'q' || key.r == 'Q'
	default:
		return false
	}
}

func applyTranscriptPagerKey(state *TranscriptPagerState, model TranscriptPagerModel, width, height int, key editorKey) bool {
	if state == nil {
		return true
	}
	viewportRows := transcriptPagerViewportRows(height)
	switch key.kind {
	case editorKeyCancelPopup, editorKeyInterrupt, editorKeyEOF, editorKeyTranspose:
		return true
	case editorKeyUp:
		state.Scroll(model, width, viewportRows, -1)
	case editorKeyDown:
		state.Scroll(model, width, viewportRows, 1)
	case editorKeyPageUp, editorKeyLeft:
		state.Scroll(model, width, viewportRows, -viewportRows)
	case editorKeyPageDown, editorKeyRight:
		state.Scroll(model, width, viewportRows, viewportRows)
	case editorKeyHome:
		state.Scroll(model, width, viewportRows, -len(model.Rows(max(1, width))))
	case editorKeyEnd:
		state.SetFollowBottom(model, width, viewportRows, true)
	case editorKeyRune:
		switch key.r {
		case 'q', 'Q':
			return true
		case 'j':
			state.Scroll(model, width, viewportRows, 1)
		case 'k':
			state.Scroll(model, width, viewportRows, -1)
		case 'g':
			state.Scroll(model, width, viewportRows, -len(model.Rows(max(1, width))))
		case 'G':
			state.SetFollowBottom(model, width, viewportRows, true)
		}
	}
	return false
}
