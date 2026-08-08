package ui

import (
	"errors"
	"reflect"
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
			if state.ResumePicker.Active && state.ResumePicker.LeaseID == a.LeaseID {
				state.ResumePicker = ResumePickerState{}
			}
			if state.BacktrackPicker.Active && state.BacktrackPicker.LeaseID == a.LeaseID {
				state.BacktrackPicker = BacktrackPickerState{}
			}
			if state.ModelPicker.Active && state.ModelPicker.LeaseID == a.LeaseID {
				state.ModelPicker = ModelPickerState{}
			}
			if state.ThemePicker.Active && state.ThemePicker.LeaseID == a.LeaseID {
				state.ThemePicker = ThemePickerState{}
			}
			if state.SkillPicker.Active && state.SkillPicker.LeaseID == a.LeaseID {
				state.SkillPicker = SkillPickerState{}
			}
			if state.ExportPicker.Active && state.ExportPicker.LeaseID == a.LeaseID {
				state.ExportPicker = ExportPickerState{}
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
	case OpenResumePicker:
		if a.LeaseID != 0 && state.Lease.Active && state.Lease.ID == a.LeaseID {
			state.ResumePicker = ResumePickerState{Active: true, LeaseID: a.LeaseID}
		}
	case CloseResumePicker:
		if a.LeaseID != 0 && state.ResumePicker.Active && state.ResumePicker.LeaseID == a.LeaseID {
			state.ResumePicker = ResumePickerState{}
		}
	case OpenBacktrackPicker:
		if a.LeaseID != 0 && state.Lease.Active && state.Lease.ID == a.LeaseID {
			state.BacktrackPicker = BacktrackPickerState{Active: true, LeaseID: a.LeaseID}
		}
	case CloseBacktrackPicker:
		if a.LeaseID != 0 && state.BacktrackPicker.Active && state.BacktrackPicker.LeaseID == a.LeaseID {
			state.BacktrackPicker = BacktrackPickerState{}
		}
	case OpenModelPicker:
		if a.LeaseID != 0 && state.Lease.Active && state.Lease.ID == a.LeaseID {
			state.ModelPicker = ModelPickerState{Active: true, LeaseID: a.LeaseID}
		}
	case CloseModelPicker:
		if a.LeaseID != 0 && state.ModelPicker.Active && state.ModelPicker.LeaseID == a.LeaseID {
			state.ModelPicker = ModelPickerState{}
		}
	case OpenThemePicker:
		if a.LeaseID != 0 && state.Lease.Active && state.Lease.ID == a.LeaseID {
			state.ThemePicker = ThemePickerState{Active: true, LeaseID: a.LeaseID}
		}
	case CloseThemePicker:
		if a.LeaseID != 0 && state.ThemePicker.Active && state.ThemePicker.LeaseID == a.LeaseID {
			state.ThemePicker = ThemePickerState{}
		}
	case OpenSkillPicker:
		if a.LeaseID != 0 && state.Lease.Active && state.Lease.ID == a.LeaseID {
			state.SkillPicker = SkillPickerState{Active: true, LeaseID: a.LeaseID}
		}
	case CloseSkillPicker:
		if a.LeaseID != 0 && state.SkillPicker.Active && state.SkillPicker.LeaseID == a.LeaseID {
			state.SkillPicker = SkillPickerState{}
		}
	case OpenExportPicker:
		if a.LeaseID != 0 && state.Lease.Active && state.Lease.ID == a.LeaseID {
			state.ExportPicker = ExportPickerState{Active: true, LeaseID: a.LeaseID}
		}
	case CloseExportPicker:
		if a.LeaseID != 0 && state.ExportPicker.Active && state.ExportPicker.LeaseID == a.LeaseID {
			state.ExportPicker = ExportPickerState{}
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
		// Begin is only a reducer-side claim attempt; no terminal bytes have
		// crossed the writer yet. A resize/rebase can make its token or generation
		// stale while the action is queued. Ignore that harmless claim miss and let
		// the current schedule wake the presenter; marking ProjectionUnknown here
		// would create an endless recovery/reclaim loop for the same stale token.
		if a.LayoutGeneration == state.LayoutGeneration {
			_ = state.HistoryEffects.markInFlight(a.Token, a.LayoutGeneration)
		}
	case HistoryCommitAcknowledged:
		if a.LayoutGeneration != state.LayoutGeneration {
			state.HistoryEffects.ProjectionUnknown = true
			break
		}
		if err := state.HistoryEffects.ack(a.Token, a.Frame, a.LayoutGeneration); errors.Is(err, ErrStaleLayoutGeneration) {
			state.HistoryEffects.ProjectionUnknown = true
		} else if err == nil {
			if entry, ok := state.HistoryEffects.ledger.Entry(a.Token); ok {
				advanceActiveCellLedgerOnAck(&state, []HistoryCommit{entry.Commit})
			}
		}
	case HistoryCommitsAcknowledged:
		var ackErr error
		if a.LayoutGeneration != state.LayoutGeneration {
			ackErr = ErrStaleLayoutGeneration
		} else {
			ackErr = state.HistoryEffects.ackBatch(a.Commits, a.Frame, a.LayoutGeneration)
		}
		if ackErr != nil {
			// A batch that no longer matches the reducer snapshot may have
			// reached native scrollback in full. Quarantine every delivered
			// token so viewport recovery cannot expose a retryable suffix.
			state.HistoryEffects.markDeliveredBatchUnresolved(a.Commits, ackErr)
			state.HistoryEffects.ProjectionUnknown = true
		} else if len(a.Commits) > 0 {
			advanceActiveCellLedgerOnAck(&state, a.Commits)
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
			resetActiveHistoryProgressForTerminalEpoch(&state)
			syncHistoryEffectsForTranscript(&state)
		}
	case DrawRequested:
		state.LastDraw = a
	case SetThemeContextAction:
		next := cloneThemeContext(a.Theme)
		if !themeContextEqual(state.Theme, next) {
			state.Theme = next
			state.Geometry.Generation++
			if state.Geometry.Generation == 0 {
				state.Geometry.Generation = 1
			}
			state.LayoutGeneration = state.Geometry.Generation
			rebasePendingHistoryEffects(&state)
			refreshTranscriptOverlayPager(&state)
		}
	case ReplaceTranscriptAction:
		// RuntimeEvent currently publishes the authoritative Scene snapshot even
		// when its ChangeSet is empty. Trust the Scene's exact provenance/version
		// fence before cloning cells or replanning native history. SceneID prevents
		// replay/backtrack rebuilds with coincident revisions from being skipped.
		if transcriptSnapshotAlreadyInstalled(state.Transcript, a.Snapshot) {
			break
		}
		var nextTranscript TranscriptState
		activeOnly := false
		if state.SemanticActiveCellProjection {
			nextTranscript, activeOnly = transcriptReplacementActiveOnlySnapshot(state.Transcript, a.Snapshot, state.Active)
		}
		if !activeOnly {
			nextTranscript = NewTranscriptState(a.Snapshot)
			activeOnly = state.SemanticActiveCellProjection &&
				transcriptReplacementOnlyUpdatesActive(state.Transcript, nextTranscript, state.Active)
		}
		invalidatesAcked := false
		if activeOnly {
			invalidatesAcked = activeReplacementInvalidatesAckedHistory(state.Active, nextTranscript)
		} else {
			invalidatesAcked = transcriptReplacementInvalidatesAckedHistory(state.Transcript, nextTranscript, state.HistoryEffects)
		}
		if invalidatesAcked {
			state.HistoryEffects.ProjectionUnknown = true
			state.HistoryEffects.ReconciliationRequired = true
		}
		state.Transcript = nextTranscript
		state.Active = reconcileTranscriptActiveCell(state.Active, state.Transcript)
		if state.SemanticActiveCellProjection && state.Active.Phase == ActiveCellMutable {
			state.Active = normalizeActiveStableRange(state.Active, state.Active.Enqueued.End)
		}
		if activeOnly && !invalidatesAcked && state.Active.Phase == ActiveCellMutable {
			syncHistoryEffectsForActiveCell(&state)
		} else {
			syncHistoryEffectsForTranscript(&state)
		}
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
		next := a.Active.Clone()
		if state.SemanticActiveCellProjection {
			next = normalizeActiveStableRange(next, next.Enqueued.End)
		}
		if next.ValidateStreamingRanges() == nil {
			state.Active = next
			syncHistoryEffectsForTranscript(&state)
			refreshTranscriptOverlayPager(&state)
		}
	case UpdateActiveCellAction:
		// Mutable stream updates are the one place where latest-wins coalescing
		// is safe. The reducer still validates the original cell/revision fence
		// and the complete source-range invariant before publishing the snapshot.
		if reduceActiveCellUpdate(&state, a) == nil {
			// Updating the mounted mutable source cannot alter the finalized
			// transcript prefix. Keep the unified high-frequency stream path
			// independent of full Markdown/transcript layout; legacy projection
			// still uses the complete planner because its source may differ.
			if state.SemanticActiveCellProjection {
				syncHistoryEffectsForActiveCell(&state)
			} else {
				syncHistoryEffectsForTranscript(&state)
			}
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
			finalizedCellInSnapshot(a.Snapshot, a.ExpectedActiveCellID, a.ExpectedSceneRevision, a.ExpectedActiveKind, a.ExpectedActiveKindKnown) {
			if finalizedActiveCorrectionTouchesAckedPrefix(state.Active, a.Snapshot) {
				state.HistoryEffects.ProjectionUnknown = true
				state.HistoryEffects.ReconciliationRequired = true
			}
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
			if state.Active.Phase == ActiveCellMutable {
				state.Active = normalizeActiveStableRange(state.Active, state.Active.Enqueued.End)
				syncHistoryEffectsForTranscript(&state)
			}
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

func finalizedActiveCorrectionTouchesAckedPrefix(active ActiveCellState, snapshot *scene.Snapshot) bool {
	if snapshot == nil || active.CellID == 0 || active.Acked.End == 0 {
		return false
	}
	for _, cell := range snapshot.Cells {
		if cell == nil || cell.ID != active.CellID {
			continue
		}
		end := active.Acked.End
		return end > len(active.Source) || end > len(cell.Source) || active.Source[:end] != cell.Source[:end]
	}
	return false
}

// transcriptReplacementInvalidatesAckedHistory detects semantic insertions,
// removals, reorders, and corrections that move ahead of bytes already
// confirmed in native scrollback. A viewport repaint cannot repair that
// ordering: the terminal owner must purge the old scrollback epoch and replay
// the current Scene. Appending or extending content after the acknowledged
// prefix remains incremental and does not trigger reconciliation.
func transcriptReplacementInvalidatesAckedHistory(previous, next TranscriptState, effects HistoryEffectQueueState) bool {
	if effects.ledger == nil {
		return false
	}
	ackedByCell := make(map[scene.CellID][]HistoryCommit)
	for _, entry := range effects.ledger.byToken {
		if entry.State != HistoryCommitAcked {
			continue
		}
		ackedByCell[entry.Commit.CellID] = append(ackedByCell[entry.Commit.CellID], entry.Commit)
	}
	if len(ackedByCell) == 0 {
		return false
	}

	previousIndex := transcriptCellIndexes(previous)
	nextIndex := transcriptCellIndexes(next)
	// Prefix comparisons: for every acked cell the original code compared
	// previous.Cells[:oldAt] with next.Cells[:newAt]. When positions are
	// unchanged (oldAt == newAt) every such comparison is a prefix of the same
	// two arrays, so the largest prefix equality implies all smaller ones —
	// one comparison suffices instead of one per acked cell. Any position
	// change makes the two prefix slices differ in length, so it already
	// invalidates the acknowledged history.
	maxPrefix := -1
	for cellID, commits := range ackedByCell {
		oldAt, oldOK := previousIndex[cellID]
		newAt, newOK := nextIndex[cellID]
		if !oldOK || !newOK {
			return true
		}
		if oldAt != newAt {
			return true
		}
		if oldAt > maxPrefix {
			maxPrefix = oldAt
		}

		oldCell := previous.Cells[oldAt]
		newCell := next.Cells[newAt]
		if oldCell.Kind != newCell.Kind || !reflect.DeepEqual(oldCell.Presentation, newCell.Presentation) {
			return true
		}
		for _, commit := range commits {
			start, end := commit.SourceRange.Start, commit.SourceRange.End
			if start < 0 || end < start || end > len(oldCell.Source) || end > len(newCell.Source) ||
				oldCell.Source[start:end] != newCell.Source[start:end] {
				return true
			}
		}
	}
	if maxPrefix >= 0 && !transcriptSemanticPrefixEqual(previous.Cells[:maxPrefix], next.Cells[:maxPrefix]) {
		return true
	}
	return false
}

func transcriptCellIndexes(transcript TranscriptState) map[scene.CellID]int {
	indexes := make(map[scene.CellID]int, len(transcript.Cells))
	for index, cell := range transcript.Cells {
		indexes[cell.ID] = index
	}
	return indexes
}

func transcriptSemanticPrefixEqual(previous, next []scene.TranscriptCell) bool {
	if len(previous) != len(next) {
		return false
	}
	for index := range previous {
		left, right := previous[index], next[index]
		if left.ID != right.ID || left.Kind != right.Kind || left.Source != right.Source ||
			!reflect.DeepEqual(left.Presentation, right.Presentation) {
			return false
		}
	}
	return true
}

// transcriptReplacementActiveOnlySnapshot uses the Scene COW/version contract
// before materializing a complete TranscriptState. Every non-active cell with
// the same ID/revision belongs to the same immutable Scene object; only the one
// active cell needs to be detached into actor-owned state.
func transcriptReplacementActiveOnlySnapshot(previous TranscriptState, snapshot *scene.Snapshot, active ActiveCellState) (TranscriptState, bool) {
	if snapshot == nil || snapshot.SceneID == 0 || snapshot.SceneID != previous.SceneID ||
		active.CellID == 0 || active.Phase != ActiveCellMutable {
		return TranscriptState{}, false
	}
	activeAt := -1
	var activeCandidate *scene.TranscriptCell
	nextIndex := 0
	for _, candidate := range snapshot.Cells {
		if candidate == nil {
			continue
		}
		if nextIndex >= len(previous.Cells) {
			return TranscriptState{}, false
		}
		current := previous.Cells[nextIndex]
		if candidate.ID == active.CellID {
			if activeAt >= 0 || current.ID != active.CellID || current.Phase != scene.CellMutable ||
				candidate.Phase != scene.CellMutable || !transcriptCellStaticMetadataEqual(current, *candidate) {
				return TranscriptState{}, false
			}
			activeAt = nextIndex
			activeCandidate = candidate
		} else if !transcriptCellVersionEqual(current, *candidate) {
			return TranscriptState{}, false
		}
		nextIndex++
	}
	if activeAt < 0 || nextIndex != len(previous.Cells) {
		return TranscriptState{}, false
	}

	next := previous
	next.SceneID = snapshot.SceneID
	next.Revision = snapshot.Revision
	next.ContentVersion = snapshot.ContentVersion
	next.Cells[activeAt] = cloneTranscriptCell(*activeCandidate)
	return next, true
}

func transcriptCellVersionEqual(left, right scene.TranscriptCell) bool {
	return left.Revision == right.Revision && transcriptCellStaticMetadataEqual(left, right)
}

func transcriptCellStaticMetadataEqual(left, right scene.TranscriptCell) bool {
	if left.ID != right.ID || left.Sequence != right.Sequence || left.Kind != right.Kind ||
		left.Phase != right.Phase || left.HistoryCommitBlocked != right.HistoryCommitBlocked ||
		left.Boundary != right.Boundary || left.Provenance != right.Provenance ||
		left.ChainKey != right.ChainKey || left.CreatedAt != right.CreatedAt {
		return false
	}
	if left.FinalizedAt == nil || right.FinalizedAt == nil {
		return left.FinalizedAt == nil && right.FinalizedAt == nil
	}
	return left.FinalizedAt.Equal(*right.FinalizedAt)
}

// transcriptReplacementOnlyUpdatesActive recognizes the full Scene snapshots
// emitted after each stream delta. The snapshots are still installed as the
// semantic source of truth, but when every non-active cell is byte-for-byte
// unchanged there is no reason to lay out the finalized transcript again.
func transcriptReplacementOnlyUpdatesActive(previous, next TranscriptState, active ActiveCellState) bool {
	if active.CellID == 0 || active.Phase != ActiveCellMutable || len(previous.Cells) != len(next.Cells) {
		return false
	}
	found := false
	for index := range previous.Cells {
		left, right := previous.Cells[index], next.Cells[index]
		if left.ID != active.CellID && right.ID != active.CellID {
			if !reflect.DeepEqual(left, right) {
				return false
			}
			continue
		}
		if found || left.ID != active.CellID || right.ID != active.CellID ||
			left.Phase != scene.CellMutable || right.Phase != scene.CellMutable ||
			left.Sequence != right.Sequence || left.Kind != right.Kind ||
			left.HistoryCommitBlocked != right.HistoryCommitBlocked ||
			left.Boundary != right.Boundary || left.Provenance != right.Provenance ||
			left.ChainKey != right.ChainKey {
			return false
		}
		found = true
	}
	return found
}

func transcriptSnapshotAlreadyInstalled(current TranscriptState, snapshot *scene.Snapshot) bool {
	return snapshot != nil && snapshot.SceneID != 0 && current.SceneID == snapshot.SceneID &&
		current.Revision == snapshot.Revision && current.ContentVersion == snapshot.ContentVersion
}

func activeReplacementInvalidatesAckedHistory(active ActiveCellState, next TranscriptState) bool {
	if active.CellID == 0 || active.Acked.End == 0 {
		return false
	}
	for _, cell := range next.Cells {
		if cell.ID != active.CellID {
			continue
		}
		end := active.Acked.End
		return end > len(active.Source) || end > len(cell.Source) ||
			active.Source[:end] != cell.Source[:end]
	}
	return true
}

func resetActiveHistoryProgressForTerminalEpoch(state *UIControllerState) {
	if state == nil || state.Active.Phase != ActiveCellMutable || state.Active.CellID == 0 {
		return
	}
	if state.Active.Enqueued == (SourceRange{}) && state.Active.Acked == (SourceRange{}) {
		return
	}
	state.Active.Revision++
	state.Active.Enqueued = SourceRange{}
	state.Active.Acked = SourceRange{}
}

// advanceActiveCellLedgerOnAck moves the active source boundary only after a
// history handoff has physically crossed the writer (single HistoryCommitAcknowledged
// or the batched HistoryCommitsAcknowledged delivery). The band projection then
// resumes from the acknowledged source offset instead of repainting rows that
// already reached native scrollback. Only commits enqueued for the live mutable
// cell advance the ledger; finalized transcript deliveries are identity-distinct
// and leave the active ranges untouched (their Stable ledger is zeroed on
// finalize, so MarkActiveEnqueued rejects them by invariant).
func advanceActiveCellLedgerOnAck(state *UIControllerState, commits []HistoryCommit) {
	if state.Active.Phase != ActiveCellMutable || state.Active.CellID == 0 || len(commits) == 0 {
		return
	}
	frontier := state.Active.Acked.End
	for {
		advanced := false
		for _, commit := range commits {
			if commit.Origin != HistoryCommitActive ||
				commit.CellID != state.Active.CellID ||
				commit.SourceRange.Start > frontier ||
				commit.SourceRange.End <= frontier ||
				commit.SourceRange.End > state.Active.Enqueued.End {
				continue
			}
			frontier = commit.SourceRange.End
			advanced = true
		}
		if !advanced {
			break
		}
	}
	if frontier <= state.Active.Acked.End {
		return
	}
	next, err := MarkActiveAcked(state.Active, frontier)
	if err != nil {
		return
	}
	state.Active = next
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

func finalizedCellInSnapshot(snapshot *scene.Snapshot, id scene.CellID, expectedSceneRevision uint64, expectedKind scene.CellKind, expectedKindKnown bool) bool {
	if snapshot == nil || id == 0 {
		return false
	}
	for _, cell := range snapshot.Cells {
		if cell == nil || cell.ID != id ||
			(expectedSceneRevision != 0 && cell.Revision != expectedSceneRevision) ||
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
