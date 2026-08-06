package ui

import (
	"reflect"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// AppState is the immutable-state shape consumed by the Phase 2 layout and
// presenter path. During migration UIController owns every populated field;
// legacy adapters may still be the physical presenter until Phase 3.
//
// Transcript retains semantic cells, Active retains mutable semantic source,
// and Bottom contains overlays only. Terminal/front-buffer state is deliberately
// absent: it is a physical projection cache, never UI business truth.
type AppState struct {
	Revision   uint64
	Theme      style.ThemeContext
	Transcript TranscriptState
	Active     ActiveCellState
	// SemanticActiveCellProjection makes Active the only visual source for the
	// mutable bottom band. It is enabled by the unified primary renderer; the
	// retained ActiveBand facade then remains compatibility state only and must
	// not replace or duplicate the Scene-backed active cell.
	SemanticActiveCellProjection bool
	Bottom                       BottomPaneState
	Geometry                     GeometryState
	Lease                        LeaseState
	HistoryEffects               HistoryEffectQueueState
	TranscriptOverlay            TranscriptOverlayState
	ResumePicker                 ResumePickerState
	BacktrackPicker              BacktrackPickerState
	ModelPicker                  ModelPickerState
	ThemePicker                  ThemePickerState
	SkillPicker                  SkillPickerState
	LayoutGeneration             uint64
}

// Clone returns an independent immutable snapshot suitable for layout,
// diagnostics, or tests. Callers must never receive actor-owned slices or
// status-model pointers directly.
func (s AppState) Clone() AppState {
	s.Theme = cloneThemeContext(s.Theme)
	s.Transcript = s.Transcript.Clone()
	s.Active = s.Active.Clone()
	s.Bottom = s.Bottom.Clone()
	s.HistoryEffects = s.HistoryEffects.Clone()
	s.TranscriptOverlay = s.TranscriptOverlay.Clone()
	return s
}

// TranscriptState is the semantic transcript part of AppState. It intentionally
// stores cells rather than rendered terminal rows, so resize/replay derives a
// new layout from source instead of reverse-engineering a viewport.
type TranscriptState struct {
	Revision uint64
	Cells    []scene.TranscriptCell
}

// NewTranscriptState snapshots the transcript data plane into an AppState
// value. Nil means an empty transcript, not an unknown terminal projection.
func NewTranscriptState(snapshot *scene.Snapshot) TranscriptState {
	if snapshot == nil {
		return TranscriptState{}
	}
	state := TranscriptState{
		Revision: snapshot.Revision,
		Cells:    make([]scene.TranscriptCell, 0, len(snapshot.Cells)),
	}
	for _, cell := range snapshot.Cells {
		if cell == nil {
			continue
		}
		state.Cells = append(state.Cells, cloneTranscriptCell(*cell))
	}
	return state
}

func (s TranscriptState) Clone() TranscriptState {
	clone := make([]scene.TranscriptCell, len(s.Cells))
	for index := range s.Cells {
		clone[index] = cloneTranscriptCell(s.Cells[index])
	}
	s.Cells = clone
	return s
}

// LayoutRows derives semantic transcript rows without first cloning every
// cell. scene.LayoutTranscript only reads its input and returns detached row
// values, so the temporary pointer slice is sufficient isolation for this
// synchronous layout operation.
func (s TranscriptState) LayoutRows(policyVersion uint64) []scene.LayoutRow {
	if len(s.Cells) == 0 {
		return nil
	}
	cells := make([]*scene.TranscriptCell, len(s.Cells))
	for index := range s.Cells {
		cells[index] = &s.Cells[index]
	}
	return scene.LayoutTranscript(cells, policyVersion)
}

// Snapshot converts the value back into the scene package's read-only view.
// It copies every cell so a layout consumer cannot modify AppState memory.
func (s TranscriptState) Snapshot() *scene.Snapshot {
	snapshot := &scene.Snapshot{
		Revision: s.Revision,
		Cells:    make([]*scene.TranscriptCell, 0, len(s.Cells)),
	}
	for index := range s.Cells {
		cell := cloneTranscriptCell(s.Cells[index])
		snapshot.Cells = append(snapshot.Cells, &cell)
	}
	return snapshot
}

func cloneTranscriptCell(cell scene.TranscriptCell) scene.TranscriptCell {
	if cell.FinalizedAt != nil {
		finalizedAt := *cell.FinalizedAt
		cell.FinalizedAt = &finalizedAt
	}
	cell.Presentation = cell.Presentation.Clone()
	return cell
}

func cloneThemeContext(theme style.ThemeContext) style.ThemeContext {
	if theme.Palette.Styles != nil {
		styles := make(map[style.Role]render.Style, len(theme.Palette.Styles))
		for role, value := range theme.Palette.Styles {
			styles[role] = value
		}
		theme.Palette.Styles = styles
	}
	if theme.Terminal.DefaultFG != nil {
		value := *theme.Terminal.DefaultFG
		theme.Terminal.DefaultFG = &value
	}
	if theme.Terminal.DefaultBG != nil {
		value := *theme.Terminal.DefaultBG
		theme.Terminal.DefaultBG = &value
	}
	return theme
}

func themeContextEqual(left, right style.ThemeContext) bool {
	return reflect.DeepEqual(left, right)
}

// ActiveCellPhase tracks semantic mutable-cell lifecycle. It is intentionally
// separate from transcript CellPhase because an active cell has not necessarily
// been finalized into transcript history yet.
type ActiveCellPhase uint8

const (
	ActiveCellInactive ActiveCellPhase = iota
	ActiveCellMutable
	ActiveCellFinalizing
)

// ActiveCellState is the sole future semantic source for an in-progress
// assistant/reasoning/tool cell. Display rows and ActiveBand are derived by
// Layout; they are never used as its source.
type ActiveCellState struct {
	CellID               scene.CellID
	Revision             uint64
	Kind                 scene.CellKind
	Phase                ActiveCellPhase
	Source               string
	HistoryCommitBlocked bool
	Stable               SourceRange
	Enqueued             SourceRange
	Acked                SourceRange
}

func (s ActiveCellState) Clone() ActiveCellState { return s }

// TranscriptOverlayState is the logical ownership record for the read-only
// alternate-screen transcript pager. The ScreenLease owns terminal transport;
// this state records which semantic view is entitled to consume that lease.
type TranscriptOverlayState struct {
	Active  bool
	LeaseID uint64
	Pager   TranscriptPagerState
}

// ResumePickerState records only alternate-screen ownership. Item matching,
// keyboard navigation and preview rendering are ephemeral to the picker; they
// must not become a second transcript or terminal projection model.
type ResumePickerState struct {
	Active  bool
	LeaseID uint64
}

// BacktrackPickerState intentionally holds only alternate-screen ownership.
// Navigation, search and the selected row remain local to the fullscreen list;
// an eventual mutation is committed only after lease release.
type BacktrackPickerState struct {
	Active  bool
	LeaseID uint64
}

// ModelPickerState intentionally holds only alternate-screen ownership.
// Navigation, search and the selected row remain local to the fullscreen list;
// the provider→model→reasoning mutation is committed only after lease release.
type ModelPickerState struct {
	Active  bool
	LeaseID uint64
}

// ThemePickerState intentionally holds only alternate-screen ownership.
// Navigation and the working theme snapshot remain local to the fullscreen
// list; the confirmed theme is applied only after lease release.
type ThemePickerState struct {
	Active  bool
	LeaseID uint64
}

// SkillPickerState intentionally holds only alternate-screen ownership.
// Navigation, search and the selected row remain local to the fullscreen list;
// the confirmed skill becomes a composer draft only after lease release.
type SkillPickerState struct {
	Active  bool
	LeaseID uint64
}

func (s TranscriptOverlayState) Clone() TranscriptOverlayState {
	s.Pager = s.Pager.Clone()
	return s
}

// ActiveCellFromTranscript derives the currently mutable semantic cell from a
// transcript snapshot. It deliberately leaves Stable/Enqueued/Acked at zero:
// Scene owns cell kind, revision and source. The pure ActiveBand projection
// consumes this semantic record when no legacy facade band is present; Phase 5
// will introduce the single streaming-range owner rather than guessing effect
// progress from display text.
func ActiveCellFromTranscript(transcript TranscriptState) (ActiveCellState, bool) {
	for _, cell := range transcript.Cells {
		if cell.Phase != scene.CellMutable {
			continue
		}
		// 跳过空的 reasoning 占位 cell（native-history ordering fence 的
		// 语义位置，见 encoder.markReasoningBarrier）：它只是迟到 reasoning
		// 的占位，不是流式主体；有实质内容的 mutable cell（assistant /
		// 工具链）才是 Active 的驻留源。
		if cell.Kind == scene.KindSupplement && strings.TrimSpace(cell.Source) == "" {
			continue
		}
		return ActiveCellState{
			CellID:               cell.ID,
			Revision:             cell.Revision,
			Kind:                 cell.Kind,
			Phase:                ActiveCellMutable,
			Source:               cell.Source,
			HistoryCommitBlocked: cell.HistoryCommitBlocked,
		}, true
	}
	return ActiveCellState{}, false
}

// BottomFocus describes which overlay owns the intended input cursor. The
// actual cursor movement remains a presenter concern and is not stored here.
type BottomFocus uint8

const (
	BottomFocusNone BottomFocus = iota
	BottomFocusPrompt
	BottomFocusPopup
)

// Clone returns a detached bottom-pane snapshot. BottomPaneState is also used
// by the legacy surface composer, so keeping its copy operation here avoids
// a second near-identical bottom state model during migration.
func (s BottomPaneState) Clone() BottomPaneState {
	s.StatusModel = cloneAppStateStatusModel(s.StatusModel)
	s.DynamicStatusModel = cloneAppStateStatusModel(s.DynamicStatusModel)
	s.PopupLines = append([]string(nil), s.PopupLines...)
	s.PopupStack = clonePopupLayers(s.PopupStack)
	s.PopupViewport = clonePopupViewportSpec(s.PopupViewport)
	s.ActiveBandLines = append([]string(nil), s.ActiveBandLines...)
	s.ActiveBandStyled = cloneRenderLines(s.ActiveBandStyled)
	return s
}

func cloneAppStateStatusModel(model *style.StatusLineModel) *style.StatusLineModel {
	if model == nil {
		return nil
	}
	clone := *model
	clone.Segments = append([]style.StatusSegment(nil), model.Segments...)
	return &clone
}
