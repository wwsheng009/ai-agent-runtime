package ui

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
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
	state = state.Clone()
	state.AppState.Revision = revision
	switch a := action.(type) {
	case Resize:
		geometryChanged := false
		if a.Width > 0 {
			geometryChanged = geometryChanged || state.Geometry.Width != a.Width
			state.Geometry.Width = a.Width
		}
		if a.Height > 0 {
			geometryChanged = geometryChanged || state.Geometry.Height != a.Height
			state.Geometry.Height = a.Height
		}
		if a.Generation != 0 {
			state.Geometry.Generation = a.Generation
			state.LayoutGeneration = a.Generation
		} else if geometryChanged {
			state.Geometry.Generation++
			state.LayoutGeneration = state.Geometry.Generation
		}
	case LeaseAcquired:
		if a.LeaseID != 0 {
			state.Lease = LeaseState{ID: a.LeaseID, Active: true}
		}
	case LeaseReleased:
		if a.LeaseID != 0 && state.Lease.Active && state.Lease.ID == a.LeaseID {
			state.Lease = LeaseState{}
		}
	case EffectResult:
		state.Effects.Count++
		state.Effects.Last = a
	case DrawRequested:
		state.LastDraw = a
	case ReplaceTranscriptAction:
		state.Transcript = NewTranscriptState(a.Snapshot)
		if active, ok := ActiveCellFromTranscript(state.Transcript); ok {
			state.Active = active
		} else {
			state.Active = ActiveCellState{}
		}
	case SetActiveCellAction:
		state.Active = a.Active.Clone()
	case ClearActiveCellAction:
		state.Active = ActiveCellState{}
	case InputEvent:
		state.Bottom.PromptInput = a.Text
		state.Bottom.PromptCursor = a.Cursor
		state.Bottom.PromptCursorKnown = true
		state.Bottom.PromptRowsOverride = 0
		state.Bottom.PasteActive = a.PasteActive
		state.Bottom.Focus = BottomFocusPrompt
	case SetActiveBandAction:
		state.Bottom.ActiveBandStyled = cloneRenderLines(a.Lines)
		if a.RawLines != nil {
			state.Bottom.ActiveBandLines = append([]string(nil), a.RawLines...)
		} else {
			state.Bottom.ActiveBandLines = render.PlainBackend{}.RenderLines(render.LinesDoc(a.Lines...))
		}
	case ClearActiveBandAction:
		state.Bottom.ActiveBandLines = nil
		state.Bottom.ActiveBandStyled = nil
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
