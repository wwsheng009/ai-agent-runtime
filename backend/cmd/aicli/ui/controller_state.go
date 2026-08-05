package ui

import (
	"errors"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// UIControllerState is the actor-owned state published by UIController. AppState
// is embedded rather than copied into a coordinator-local ledger, so geometry
// and lease have one controller-owned representation while Phase 2 progressively
// moves transcript/active/bottom producers onto typed actions.
//
// Effects and LastDraw remain delivery diagnostics, not semantic UI state.
// EffectResult must not be read as a history acknowledgement until Phase 3/4
// connects a tokenized TerminalSession effect queue.
type UIControllerState struct {
	AppState
	Effects  EffectResultState
	LastDraw DrawRequested
}

func (s UIControllerState) Clone() UIControllerState {
	s.AppState = s.AppState.Clone()
	return s
}

// GeometryState records only explicit known geometry. A zero dimension means
// the Phase 1 adapter requested a refresh/probe rather than reporting a new
// terminal size, so it does not overwrite the last known dimension.
type GeometryState struct {
	Width      int
	Height     int
	Generation uint64
}

// LeaseState is the logical side of an alternate-screen ownership barrier.
// The physical DEC 1049 transaction still lives in ScreenLease until the
// TerminalSession migration; stale release ids never clear a newer lease.
type LeaseState struct {
	ID     uint64
	Active bool
}

// EffectResultState records action delivery only. It must not be used as a
// history/projection acknowledgement until a tokenized terminal effect queue
// makes Token ownership and partial-write recovery explicit.
type EffectResultState struct {
	Count uint64
	Last  EffectResult
}

func reduceUIControllerState(state UIControllerState, action UIAction, revision uint64) UIControllerState {
	// UIController invokes this reducer while holding exclusive ownership of its
	// state. Cloning here used to duplicate the complete transcript and history
	// ledger for every runtime event, even when the action immediately replaced
	// that snapshot. Public readers still receive a detached copy through State
	// and AppState; reducer-local transitions can safely update this owned value.
	state.AppState.Revision = revision
	switch a := action.(type) {
	case Resize:
		// Applied resize reports are causally ordered by their measured
		// generation. Never let a delayed probe roll Geometry/Layout backwards
		// and make an old history effect look current again.
		if a.Generation != 0 && a.Generation < state.Geometry.Generation {
			break
		}
		geometryChanged := false
		if a.Width > 0 {
			geometryChanged = geometryChanged || state.Geometry.Width != a.Width
			state.Geometry.Width = a.Width
		}
		if a.Height > 0 {
			geometryChanged = geometryChanged || state.Geometry.Height != a.Height
			state.Geometry.Height = a.Height
		}
		generationChanged := false
		if a.Generation > state.Geometry.Generation {
			state.Geometry.Generation = a.Generation
			state.LayoutGeneration = a.Generation
			generationChanged = true
		} else if geometryChanged {
			state.Geometry.Generation++
			state.LayoutGeneration = state.Geometry.Generation
			generationChanged = true
		}
		if geometryChanged || generationChanged {
			rebasePendingHistoryEffects(&state)
			refreshTranscriptOverlayPager(&state)
		}
	case LeaseAcquired:
		if a.LeaseID != 0 {
			state.Lease = LeaseState{ID: a.LeaseID, Active: true}
			state.HistoryEffects.Frozen = true
		}
	case LeaseReleased:
		if a.LeaseID != 0 && state.Lease.Active && state.Lease.ID == a.LeaseID {
			state.Lease = LeaseState{}
			state.HistoryEffects.Frozen = false
			if state.TranscriptOverlay.Active && state.TranscriptOverlay.LeaseID == a.LeaseID {
				state.TranscriptOverlay = TranscriptOverlayState{}
			}
		}
	case OpenTranscriptOverlay:
		if a.LeaseID != 0 && state.Lease.Active && state.Lease.ID == a.LeaseID {
			state.TranscriptOverlay.Active = true
			state.TranscriptOverlay.LeaseID = a.LeaseID
			state.TranscriptOverlay.Pager = NewTranscriptPagerState()
			refreshTranscriptOverlayPager(&state)
		}
	case CloseTranscriptOverlay:
		if a.LeaseID != 0 && state.TranscriptOverlay.Active && state.TranscriptOverlay.LeaseID == a.LeaseID {
			state.TranscriptOverlay = TranscriptOverlayState{}
		}
	case TranscriptPagerScroll:
		if state.TranscriptOverlay.Active && transcriptPagerLeaseMatches(state.TranscriptOverlay, a.LeaseID) {
			model, width, rows := transcriptOverlayPagerInputs(state)
			state.TranscriptOverlay.Pager.Scroll(model, width, rows, a.Delta)
		}
	case TranscriptPagerSetFollowBottom:
		if state.TranscriptOverlay.Active && transcriptPagerLeaseMatches(state.TranscriptOverlay, a.LeaseID) {
			model, width, rows := transcriptOverlayPagerInputs(state)
			state.TranscriptOverlay.Pager.SetFollowBottom(model, width, rows, a.Follow)
		}
	case EffectResult:
		state.Effects.Count++
		state.Effects.Last = a
	case BeginHistoryCommit:
		if a.LayoutGeneration != state.LayoutGeneration {
			state.HistoryEffects.ProjectionUnknown = true
			break
		}
		if err := state.HistoryEffects.markInFlight(a.Token, a.LayoutGeneration); errors.Is(err, ErrStaleLayoutGeneration) {
			state.HistoryEffects.ProjectionUnknown = true
		}
	case HistoryCommitAcknowledged:
		if a.LayoutGeneration != state.LayoutGeneration {
			state.HistoryEffects.ProjectionUnknown = true
			break
		}
		if err := state.HistoryEffects.ack(a.Token, a.Frame, a.LayoutGeneration); errors.Is(err, ErrStaleLayoutGeneration) {
			state.HistoryEffects.ProjectionUnknown = true
		}
	case HistoryCommitFailed:
		if a.LayoutGeneration != state.LayoutGeneration {
			state.HistoryEffects.ProjectionUnknown = true
			break
		}
		if err := state.HistoryEffects.fail(a.Token, a.LayoutGeneration, a.Err, a.MayHavePartiallyWritten); errors.Is(err, ErrStaleLayoutGeneration) {
			state.HistoryEffects.ProjectionUnknown = true
		}
	case HistoryCommitDeferred:
		// Deferred explicitly means that the terminal transaction did not
		// start. A stale deferred callback is harmless and must not clear or
		// overwrite a newer Unknown/recovery state.
		if a.LayoutGeneration == state.LayoutGeneration {
			_ = state.HistoryEffects.deferInFlight(a.Token, a.LayoutGeneration)
		}
	case HistoryProjectionRecovered:
		if !state.Lease.Active && !state.HistoryEffects.Frozen && a.LayoutGeneration == state.LayoutGeneration {
			state.HistoryEffects.markProjectionKnown()
		}
	case HistoryProjectionInvalidated:
		if a.LayoutGeneration == state.LayoutGeneration {
			state.HistoryEffects.ProjectionUnknown = true
		}
	case HistoryScrollbackReconciled:
		// A visible-frame recovery is insufficient to resolve a possibly
		// partial native-scrollback handoff. Only the terminal owner can post
		// this stronger epoch barrier after reset/replacement and a confirmed
		// source-backed recovery frame. Replan every eligible semantic range
		// under fresh tokens; never reinterpret old delivery as Acked.
		if !state.Lease.Active && !state.HistoryEffects.Frozen && !state.HistoryEffects.ProjectionUnknown &&
			a.LayoutGeneration == state.LayoutGeneration && state.HistoryEffects.reconcileScrollback(a.TerminalEpoch) {
			syncHistoryEffectsForTranscript(&state)
		}
	case DrawRequested:
		state.LastDraw = a
	case ReplaceTranscriptAction:
		state.Transcript = NewTranscriptState(a.Snapshot)
		state.Active = reconcileTranscriptActiveCell(state.Active, state.Transcript)
		syncHistoryEffectsForTranscript(&state)
		refreshTranscriptOverlayPager(&state)
	case SetActiveCellAction:
		if a.Active.Phase == ActiveCellInactive {
			state.Active = ActiveCellState{}
			refreshTranscriptOverlayPager(&state)
			break
		}
		// Mounting a new cell is a durable boundary, but it must not install a
		// malformed range ledger. Scene-derived all-zero ranges remain valid
		// during migration; populated ranges use the same invariant as updates.
		if a.Active.ValidateStreamingRanges() == nil {
			state.Active = a.Active.Clone()
			refreshTranscriptOverlayPager(&state)
		}
	case UpdateActiveCellAction:
		// Mutable stream updates are the one place where latest-wins coalescing
		// is safe. The reducer still validates the original cell/revision fence
		// and the complete source-range invariant before publishing the snapshot.
		if reduceActiveCellUpdate(&state, a) == nil {
			refreshTranscriptOverlayPager(&state)
		}
	case ClearActiveCellAction:
		if clearActiveCellMatches(state.Active, a) {
			state.Active = ActiveCellState{}
			refreshTranscriptOverlayPager(&state)
		}
	case FinalizeActiveCellAction:
		if state.Active.CellID == a.ExpectedActiveCellID &&
			state.Active.Revision == a.ExpectedActiveRevision &&
			(!a.ExpectedActiveKindKnown || state.Active.Kind == a.ExpectedActiveKind) &&
			finalizedCellInSnapshot(a.Snapshot, a.ExpectedActiveCellID, a.ExpectedActiveRevision, a.ExpectedActiveKind, a.ExpectedActiveKindKnown) {
			state.Transcript = NewTranscriptState(a.Snapshot)
			if active, ok := ActiveCellFromTranscript(state.Transcript); ok {
				state.Active = active
			} else {
				state.Active = ActiveCellState{}
			}
			syncHistoryEffectsForTranscript(&state)
			refreshTranscriptOverlayPager(&state)
		}
	case InputEvent:
		state.Bottom.PromptInput = a.Text
		state.Bottom.PromptCursor = a.Cursor
		state.Bottom.PromptCursorKnown = true
		state.Bottom.PromptRowsOverride = 0
		state.Bottom.PasteActive = a.PasteActive
		state.Bottom.Focus = BottomFocusPrompt
	case SetActiveBandAction:
		if state.SemanticActiveCellProjection {
			// Unified production frames derive their mutable body exclusively
			// from Active. Keeping a facade payload here would let a delayed
			// legacy frame replace the Scene source after the cutover.
			break
		}
		state.Bottom.ActiveBandStyled = cloneRenderLines(a.Lines)
		if a.RawLines != nil {
			state.Bottom.ActiveBandLines = append([]string(nil), a.RawLines...)
		} else {
			state.Bottom.ActiveBandLines = render.PlainBackend{}.RenderLines(render.LinesDoc(a.Lines...))
		}
	case ClearActiveBandAction:
		state.Bottom.ActiveBandLines = nil
		state.Bottom.ActiveBandStyled = nil
	case SetSemanticActiveCellProjectionAction:
		state.SemanticActiveCellProjection = a.Enabled
		if a.Enabled {
			// A frame must never retain an old facade payload across the
			// renderer authority boundary. Subsequent facade actions are
			// deliberately ignored below while semantic projection is active.
			state.Bottom.ActiveBandLines = nil
			state.Bottom.ActiveBandStyled = nil
		}
	case SetStatusModelsAction:
		state.Bottom.StatusModel = normalizeControllerStatusModel(a.Status)
		state.Bottom.DynamicStatusModel = normalizeControllerDynamicStatusModel(a.Dynamic)
	case SetStatusModelAction:
		state.Bottom.StatusModel = normalizeControllerStatusModel(a.Status)
	case SetDynamicStatusModelAction:
		state.Bottom.DynamicStatusModel = normalizeControllerDynamicStatusModel(a.Dynamic)
	case ShowPromptAction:
		state.Bottom.PromptLine = a.Line
		state.Bottom.PromptInput = ""
		state.Bottom.PromptCursor = 0
		state.Bottom.PromptCursorKnown = true
		state.Bottom.PromptCursorAbsoluteRow = 0
		state.Bottom.PromptCursorRow = 0
		state.Bottom.PromptCursorCol = 0
		state.Bottom.PromptTotalRows = 1
		state.Bottom.PromptViewportStart = 0
		state.Bottom.PromptRowsOverride = 0
		state.Bottom.PromptReservedRows = 1
		state.Bottom.PromptVisible = true
		state.Bottom.Focus = BottomFocusPrompt
	case ClearPromptRowsAction:
		clearControllerPromptState(&state.Bottom)
	case SetPromptStateAction:
		applyControllerPromptState(&state.Bottom, state.Geometry, a.Line, a.Input, a.Rows, a.CursorRow, a.CursorCol)
	case TrackPromptInputAction:
		applyControllerPromptState(&state.Bottom, state.Geometry, a.Line, a.Input, a.Rows, a.CursorRow, a.CursorCol)
	case ResetPromptAction:
		line := strings.TrimRight(SanitizeTerminalText(a.Line), "\r\n")
		state.Bottom.PromptLine = line
		state.Bottom.PromptInput = ""
		state.Bottom.PromptCursor = 0
		state.Bottom.PromptCursorKnown = true
		state.Bottom.PromptCursorAbsoluteRow = 0
		state.Bottom.PromptCursorRow = 0
		state.Bottom.PromptCursorCol = 0
		state.Bottom.PromptTotalRows = 1
		state.Bottom.PromptViewportStart = 0
		state.Bottom.PromptRowsOverride = 0
		state.Bottom.PromptReservedRows = 1
		state.Bottom.PromptVisible = true
		state.Bottom.Focus = BottomFocusPrompt
	case SetPromptRowsAction:
		rows := a.Rows
		if rows < 1 {
			rows = 1
		}
		state.Bottom.PromptReservedRows = rows
		state.Bottom.PromptRowsOverride = rows
		state.Bottom.PromptVisible = strings.TrimSpace(state.Bottom.PromptLine) != ""
	case SetPromptNoticeAction:
		state.Bottom.PromptNoticeLine = strings.TrimRight(SanitizeTerminalText(a.Line), "\r\n")
	case SetPromptEditorStatusAction:
		state.Bottom.PromptEditorStatusLine = strings.TrimRight(SanitizeTerminalText(a.Line), "\r\n")
	case SetComposerPreviewAction:
		state.Bottom.ComposerLine = strings.TrimRight(SanitizeTerminalText(a.Line), "\r\n")
		state.Bottom.PopupBelowPrompt = false
		state.Bottom.PopupReservedRows = 0
		state.Bottom.PromptNoticeLine = ""
		state.Bottom.PromptEditorStatusLine = ""
		clearControllerPromptState(&state.Bottom)
		if strings.TrimSpace(state.Bottom.ComposerLine) != "" || len(state.Bottom.PopupLines) > 0 {
			state.Bottom.Focus = BottomFocusPopup
		} else {
			state.Bottom.Focus = BottomFocusNone
		}
	case ClearComposerPreviewAction:
		state.Bottom.ComposerLine = ""
		clearControllerPromptState(&state.Bottom)
		if len(state.Bottom.PopupLines) > 0 {
			state.Bottom.Focus = BottomFocusPopup
		} else {
			state.Bottom.Focus = BottomFocusNone
		}
	case ShowPopupAction:
		state.Bottom.applyPopupShow(a, state.Geometry.Height)
	case ClearPopupAction:
		state.Bottom.applyPopupClear(a)
	case UpdatePopupAction:
		state.Bottom.applyPopupUpdate(a)
	}
	return state
}

// reconcileTranscriptActiveCell merges a semantic Scene snapshot with the
// migration-only streaming ledger already published by the actor. Scene owns
// cell identity/source/revision, while Stable/Enqueued/Acked are physical
// handoff progress and are therefore absent from ReplaceTranscriptAction.
// When both views describe the same mutable source, dropping the ledger on
// every runtime-event snapshot would make a shadow update disappear before a
// presenter can consume it. Keep the newer/equal semantic active cell in that
// case; a newer Scene source or a finalized/removed cell still replaces it.
func reconcileTranscriptActiveCell(current ActiveCellState, transcript TranscriptState) ActiveCellState {
	next, ok := ActiveCellFromTranscript(transcript)
	if !ok {
		return ActiveCellState{}
	}
	if current.CellID == 0 || current.Phase == ActiveCellInactive || current.CellID != next.CellID {
		return next
	}
	if current.Revision > next.Revision {
		return current.Clone()
	}
	if current.Revision == next.Revision &&
		current.Kind == next.Kind && current.Source == next.Source {
		return current.Clone()
	}
	return next
}

func finalizedCellInSnapshot(snapshot *scene.Snapshot, id scene.CellID, expectedRevision uint64, expectedKind scene.CellKind, expectedKindKnown bool) bool {
	if snapshot == nil || id == 0 {
		return false
	}
	for _, cell := range snapshot.Cells {
		// ActiveCellState.Revision is a reducer-side source fence. During the
		// migration a shadow update may consume the same numeric revision as
		// the final Scene mutation, so equality is valid here. The exact active
		// fence is checked by the caller before this helper runs; a strictly
		// older Scene cell remains stale.
		if cell == nil || cell.ID != id || cell.Revision < expectedRevision ||
			(expectedKindKnown && cell.Kind != expectedKind) {
			continue
		}
		switch cell.Phase {
		case scene.CellCommitted, scene.CellPartiallyHandedOff, scene.CellHandedOff:
			return true
		}
	}
	return false
}

func clearActiveCellMatches(active ActiveCellState, action ClearActiveCellAction) bool {
	if action.ExpectedCellID != 0 && active.CellID != action.ExpectedCellID {
		return false
	}
	return !action.ExpectedKindKnown || active.Kind == action.ExpectedKind
}

func cloneControllerStatusModel(model *style.StatusLineModel) *style.StatusLineModel {
	if model == nil {
		return nil
	}
	clone := *model
	clone.Segments = append([]style.StatusSegment(nil), model.Segments...)
	return &clone
}

func normalizeControllerStatusModel(model style.StatusLineModel) *style.StatusLineModel {
	model = sanitizeStatusLineModel(model)
	if strings.TrimSpace(style.StatusLineDocument(model, 0).PlainText()) == "" {
		model = style.StatusLineModel{State: style.RunReady}
	}
	return cloneControllerStatusModel(&model)
}

func normalizeControllerDynamicStatusModel(model *style.StatusLineModel) *style.StatusLineModel {
	if model == nil {
		return nil
	}
	value := sanitizeStatusLineModel(*model)
	if strings.TrimSpace(style.StatusLineDocument(value, 0).PlainText()) == "" {
		return nil
	}
	return cloneControllerStatusModel(&value)
}

func applyControllerPromptState(bottom *BottomPaneState, geometry GeometryState, line string, input string, rows int, cursorRow int, cursorCol int) {
	if bottom == nil {
		return
	}
	line, input, rows, cursorRow, cursorCol = normalizeFixedPromptInputState(line, input, rows, cursorRow, cursorCol)
	previousInput := bottom.PromptInput
	preserveLogicalCursor := bottom.PromptCursorKnown && previousInput == input && bottom.PromptCursor >= 0 && bottom.PromptCursor <= len([]rune(input))
	bottom.PromptLine = line
	bottom.PromptInput = input
	bottom.PromptReservedRows = rows
	bottom.PromptTotalRows = rows
	bottom.PromptRowsOverride = 0
	bottom.PromptViewportStart = 0
	bottom.PromptCursorAbsoluteRow = cursorRow
	bottom.PromptCursorRow = cursorRow
	bottom.PromptCursorCol = cursorCol
	if preserveLogicalCursor {
		// InputEvent already supplied the authoritative rune offset for this
		// unchanged source. Do not reverse-map a visual cursor against a stale
		// pre-probe width; Layout will remeasure it at the current generation.
	} else if geometry.Width > 0 {
		if cursor, ok := interactiveInputCursorAtVisualRow([]rune(input), terminalVisibleWidth(line), geometry.Width, cursorRow, cursorCol); ok {
			bottom.PromptCursor = cursor
			bottom.PromptCursorKnown = true
		} else {
			bottom.PromptCursorKnown = false
		}
	} else {
		// The facade action carries visual coordinates but no rune offset. Until
		// a measured geometry exists they remain display-state only; guessing
		// with an 80-column fallback would poison future resize reflow.
		bottom.PromptCursorKnown = false
	}
	bottom.PromptVisible = true
	bottom.Focus = BottomFocusPrompt
}

func clearControllerPromptState(bottom *BottomPaneState) {
	if bottom == nil {
		return
	}
	bottom.PromptLine = ""
	bottom.PromptInput = ""
	bottom.PromptCursor = 0
	bottom.PromptCursorKnown = false
	bottom.PromptCursorAbsoluteRow = 0
	bottom.PromptCursorRow = 0
	bottom.PromptCursorCol = 0
	bottom.PromptTotalRows = 0
	bottom.PromptViewportStart = 0
	bottom.PromptRowsOverride = 0
	bottom.PromptVisible = false
	bottom.PromptReservedRows = 0
}

func refreshTranscriptOverlayPager(state *UIControllerState) {
	if state == nil || !state.TranscriptOverlay.Active {
		return
	}
	model, width, rows := transcriptOverlayPagerInputs(*state)
	state.TranscriptOverlay.Pager.Reconcile(model, width, rows)
}

func transcriptOverlayPagerInputs(state UIControllerState) (TranscriptPagerModel, int, int) {
	width := state.Geometry.Width
	if width < 1 {
		width = 80
	}
	height := state.Geometry.Height
	if height < 1 {
		height = minFullScreenListHeight
	}
	return NewTranscriptPagerModel(TranscriptPagerSnapshot{
		Transcript: state.Transcript,
		Active:     state.Active,
	}), width, transcriptPagerViewportRows(height)
}

// transcriptPagerLeaseMatches accepts a zero lease only for migration tests
// and compatibility callers. The live alternate-screen pager always supplies
// its lease id, so stale input cannot mutate a newer overlay.
func transcriptPagerLeaseMatches(overlay TranscriptOverlayState, leaseID uint64) bool {
	return leaseID == 0 || overlay.LeaseID == leaseID
}
