package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func committedPagerCell(id scene.CellID, source string) scene.TranscriptCell {
	return scene.TranscriptCell{
		ID: id, Revision: uint64(id), Kind: scene.KindAssistant,
		Source: source, Phase: scene.CellCommitted,
	}
}

func pagerSnapshot(revision uint64, cells ...scene.TranscriptCell) TranscriptPagerSnapshot {
	return TranscriptPagerSnapshot{Transcript: TranscriptState{Revision: revision, Cells: cells}}
}

func TestTranscriptPagerModel_SeparatesMutableTailFromCommittedCells(t *testing.T) {
	snapshot := pagerSnapshot(4,
		committedPagerCell(1, "done"),
		scene.TranscriptCell{ID: 2, Revision: 7, Kind: scene.KindAssistant, Source: "old tail", Phase: scene.CellMutable},
	)
	snapshot.Active = ActiveCellState{CellID: 2, Revision: 8, Phase: ActiveCellMutable, Source: "latest tail"}
	model := NewTranscriptPagerModel(snapshot)
	if len(model.Cells) != 1 || model.Cells[0].ID != 1 {
		t.Fatalf("committed cells = %#v, want only cell 1", model.Cells)
	}
	if model.LiveTail == nil || model.LiveTail.CellID != 2 || model.LiveTail.Source != "latest tail" {
		t.Fatalf("live tail = %#v", model.LiveTail)
	}
	joined := transcriptPagerRowsText(model.Rows(40))
	if strings.Count(joined, "latest tail") != 1 || strings.Contains(joined, "old tail") {
		t.Fatalf("rows did not use one authoritative tail: %q", joined)
	}
}

func TestTranscriptPagerRowsUseSharedBoundaryPolicy(t *testing.T) {
	reasoning := committedPagerCell(1, "reasoning")
	reasoning.Kind = scene.KindReasoning
	reasoning.BoundaryGroupKey = "request-1"
	answer := committedPagerCell(2, "answer")
	answer.BoundaryGroupKey = "request-1"

	rows := NewTranscriptPagerModel(pagerSnapshot(1, reasoning, answer)).Rows(40)
	if got := transcriptPagerBoundaryRowCount(rows); got != 0 {
		t.Fatalf("same-request reasoning/assistant boundary rows = %d, want 0: %+v", got, rows)
	}

	answer.BoundaryGroupKey = "request-2"
	rows = NewTranscriptPagerModel(pagerSnapshot(2, reasoning, answer)).Rows(40)
	if got := transcriptPagerBoundaryRowCount(rows); got != 1 {
		t.Fatalf("different-request boundary rows = %d, want 1: %+v", got, rows)
	}
}

func TestTranscriptPagerLiveTailRetainsBoundaryMetadata(t *testing.T) {
	reasoning := committedPagerCell(1, "reasoning")
	reasoning.Kind = scene.KindReasoning
	reasoning.BoundaryGroupKey = "request-live"
	mutable := scene.TranscriptCell{
		ID: 2, Revision: 3, Kind: scene.KindAssistant, Source: "old partial",
		Phase: scene.CellMutable, BoundaryGroupKey: "request-live",
	}
	snapshot := pagerSnapshot(3, reasoning, mutable)
	snapshot.Active = ActiveCellState{
		CellID: 2, Revision: 4, Kind: scene.KindAssistant,
		Phase: ActiveCellMutable, Source: "latest partial",
	}
	model := NewTranscriptPagerModel(snapshot)
	if model.LiveTail == nil || model.LiveTail.BoundaryGroupKey != "request-live" {
		t.Fatalf("live tail lost request boundary metadata: %+v", model.LiveTail)
	}
	if got := transcriptPagerBoundaryRowCount(model.Rows(40)); got != 0 {
		t.Fatalf("same-request committed reasoning/live assistant boundary rows = %d, want 0", got)
	}
}

func TestTranscriptPagerLiveToolTailKeepsChainDensity(t *testing.T) {
	completed := committedPagerCell(1, "completed tool output")
	completed.Kind = scene.KindToolChain
	completed.ChainKey = "tool-chain-1"
	mutable := scene.TranscriptCell{
		ID: 2, Revision: 2, Kind: scene.KindToolChain, Source: "running tool",
		Phase: scene.CellMutable, ChainKey: "tool-chain-1",
	}
	snapshot := pagerSnapshot(2, completed, mutable)
	snapshot.Active = ActiveCellState{
		CellID: 2, Revision: 3, Kind: scene.KindToolChain,
		Phase: ActiveCellMutable, Source: "running tool now",
	}
	model := NewTranscriptPagerModel(snapshot)
	if model.LiveTail == nil || model.LiveTail.ChainKey != "tool-chain-1" {
		t.Fatalf("live tool tail lost chain metadata: %+v", model.LiveTail)
	}
	if got := transcriptPagerBoundaryRowCount(model.Rows(40)); got != 0 {
		t.Fatalf("same-chain committed/live tool boundary rows = %d, want 0", got)
	}
}

func TestTranscriptPagerState_AppendFollowsBottom(t *testing.T) {
	model := NewTranscriptPagerModel(pagerSnapshot(1,
		committedPagerCell(1, "one"), committedPagerCell(2, "two"),
	))
	state := NewTranscriptPagerState()
	state.Reconcile(model, 40, 2)
	before := state.Anchor.Offset
	model = NewTranscriptPagerModel(pagerSnapshot(2,
		committedPagerCell(1, "one"), committedPagerCell(2, "two"), committedPagerCell(3, "three"),
	))
	state.Reconcile(model, 40, 2)
	if !state.FollowBottom || state.Anchor.Offset <= before {
		t.Fatalf("append must move a bottom-following pager: before=%d after=%d state=%#v", before, state.Anchor.Offset, state)
	}
}

func TestTranscriptPagerState_AppendPreservesScrolledAnchor(t *testing.T) {
	model := NewTranscriptPagerModel(pagerSnapshot(1,
		committedPagerCell(1, "one"), committedPagerCell(2, "two"),
		committedPagerCell(3, "three"), committedPagerCell(4, "four"),
	))
	state := NewTranscriptPagerState()
	state.Reconcile(model, 40, 3)
	state.Scroll(model, 40, 3, -4)
	anchor := state.Anchor
	if state.FollowBottom || anchor.CellID == 0 {
		t.Fatalf("expected a user-owned anchor after scroll: %#v", state)
	}
	model = NewTranscriptPagerModel(pagerSnapshot(2,
		committedPagerCell(1, "one"), committedPagerCell(2, "two"),
		committedPagerCell(3, "three"), committedPagerCell(4, "four"), committedPagerCell(5, "five"),
	))
	state.Reconcile(model, 40, 3)
	if state.Anchor.CellID != anchor.CellID || state.Anchor.Row != anchor.Row {
		t.Fatalf("append changed inspected anchor: before=%#v after=%#v", anchor, state.Anchor)
	}
}

func TestTranscriptPagerState_ReplacesRemovedAnchorSafely(t *testing.T) {
	model := NewTranscriptPagerModel(pagerSnapshot(1,
		committedPagerCell(1, "one"), committedPagerCell(2, "two"), committedPagerCell(3, "three"),
	))
	state := NewTranscriptPagerState()
	state.Reconcile(model, 40, 2)
	state.Scroll(model, 40, 2, -4)
	if state.Anchor.CellID == 0 {
		t.Fatal("expected initial anchor")
	}
	removed := state.Anchor.CellID
	model = NewTranscriptPagerModel(pagerSnapshot(2, committedPagerCell(1, "one"), committedPagerCell(3, "three")))
	state.Reconcile(model, 40, 2)
	if state.Anchor.CellID == removed {
		t.Fatalf("removed anchor %d was retained", removed)
	}
	if state.Anchor.Offset < 0 || state.Anchor.Offset > transcriptPagerMaxOffset(len(model.Rows(40)), 2) {
		t.Fatalf("fallback offset out of bounds: %#v", state.Anchor)
	}
}

func TestTranscriptPagerState_ReflowKeepsCellAnchor(t *testing.T) {
	model := NewTranscriptPagerModel(pagerSnapshot(1,
		committedPagerCell(1, "a long source that wraps over several visual rows"),
		committedPagerCell(2, "second"),
	))
	state := NewTranscriptPagerState()
	state.Reconcile(model, 12, 2)
	state.Scroll(model, 12, 2, -1)
	anchor := state.Anchor
	state.Reconcile(model, 28, 2)
	if state.Anchor.CellID != anchor.CellID {
		t.Fatalf("resize lost cell anchor: before=%#v after=%#v", anchor, state.Anchor)
	}
	if state.Anchor.LayoutGeneration <= anchor.LayoutGeneration {
		t.Fatalf("resize did not advance layout generation: before=%#v after=%#v", anchor, state.Anchor)
	}
}

func TestTranscriptPagerModel_FinalizationReplacesTailExactlyOnce(t *testing.T) {
	before := pagerSnapshot(1, scene.TranscriptCell{ID: 8, Revision: 2, Kind: scene.KindAssistant, Source: "partial", Phase: scene.CellMutable})
	before.Active = ActiveCellState{CellID: 8, Revision: 2, Phase: ActiveCellMutable, Source: "partial"}
	after := pagerSnapshot(2, committedPagerCell(8, "final answer"))
	beforeModel := NewTranscriptPagerModel(before)
	afterModel := NewTranscriptPagerModel(after)
	if beforeModel.LiveTail == nil || afterModel.LiveTail != nil || len(afterModel.Cells) != 1 {
		t.Fatalf("unexpected finalization models: before=%#v after=%#v", beforeModel, afterModel)
	}
	if got := strings.Count(transcriptPagerRowsText(afterModel.Rows(40)), "final answer"); got != 1 {
		t.Fatalf("finalized content rendered %d times", got)
	}
}

func TestApplyTranscriptPagerKey_CtrlTClosesAndUpScrolls(t *testing.T) {
	model := NewTranscriptPagerModel(pagerSnapshot(1,
		committedPagerCell(1, "one"), committedPagerCell(2, "two"), committedPagerCell(3, "three"),
	))
	state := NewTranscriptPagerState()
	state.Reconcile(model, 40, 2)
	before := state.Anchor.Offset
	if applyTranscriptPagerKey(&state, model, 40, 5, editorKey{kind: editorKeyUp}) {
		t.Fatal("up must not close pager")
	}
	if state.Anchor.Offset >= before {
		t.Fatalf("up did not scroll: before=%d after=%d", before, state.Anchor.Offset)
	}
	if !applyTranscriptPagerKey(&state, model, 40, 5, editorKey{kind: editorKeyTranspose}) {
		t.Fatal("Ctrl+T must close pager")
	}
}

func TestRunTranscriptPagerLoop_RefreshesCommittedSnapshotBeforeClose(t *testing.T) {
	snapshotCalls := 0
	keyCalls := 0
	frames := make([]string, 0, 2)
	err := runTranscriptPagerLoop(context.Background(), transcriptPagerLoopHooks{
		refreshSize: func() (int, int) { return 40, 8 },
		snapshot: func() TranscriptPagerSnapshot {
			snapshotCalls++
			if snapshotCalls == 1 {
				return pagerSnapshot(1, committedPagerCell(1, "first"))
			}
			return pagerSnapshot(2, committedPagerCell(1, "first"), committedPagerCell(2, "second"))
		},
		writeFrame: func(frame string) error {
			frames = append(frames, frame)
			return nil
		},
		readKey: func(context.Context) (editorKey, bool, error) {
			keyCalls++
			if keyCalls == 1 {
				return editorKey{}, false, nil
			}
			return editorKey{kind: editorKeyTranspose}, true, nil
		},
	})
	if err != nil {
		t.Fatalf("runTranscriptPagerLoop: %v", err)
	}
	if len(frames) != 2 || !strings.Contains(frames[1], "second") {
		t.Fatalf("pager did not render replacement snapshot: %#v", frames)
	}
}

func TestRunTranscriptPagerLoop_UsesActorPagerIntentAndLeaseFence(t *testing.T) {
	snapshot := pagerSnapshot(1,
		committedPagerCell(1, "one"), committedPagerCell(2, "two"), committedPagerCell(3, "three"),
	)
	model := NewTranscriptPagerModel(snapshot)
	actorState := NewTranscriptPagerState()
	actorState.Reconcile(model, 40, 5)
	frames := make([]string, 0, 2)
	var posted []UIAction
	keyCalls := 0
	err := runTranscriptPagerLoop(context.Background(), transcriptPagerLoopHooks{
		refreshSize: func() (int, int) { return 40, 8 },
		snapshot:    func() TranscriptPagerSnapshot { return snapshot },
		viewState:   func() (TranscriptPagerState, bool) { return actorState, true },
		leaseID:     17,
		postAction: func(action UIAction) bool {
			posted = append(posted, action)
			scroll, ok := action.(TranscriptPagerScroll)
			if !ok {
				t.Fatalf("pager action = %T, want TranscriptPagerScroll", action)
			}
			if scroll.LeaseID != 17 || scroll.Delta != -1 {
				t.Fatalf("pager action = %#v, want lease=17 delta=-1", scroll)
			}
			actorState.Scroll(model, 40, 5, scroll.Delta)
			return true
		},
		writeFrame: func(frame string) error {
			frames = append(frames, frame)
			return nil
		},
		readKey: func(context.Context) (editorKey, bool, error) {
			keyCalls++
			if keyCalls == 1 {
				return editorKey{kind: editorKeyUp}, true, nil
			}
			return editorKey{kind: editorKeyTranspose}, true, nil
		},
	})
	if err != nil {
		t.Fatalf("runTranscriptPagerLoop: %v", err)
	}
	if len(posted) != 1 || actorState.FollowBottom {
		t.Fatalf("actor-owned pager state was not updated: posted=%#v state=%#v", posted, actorState)
	}
	if len(frames) != 2 {
		t.Fatalf("frame count = %d, want 2", len(frames))
	}
}

func TestRunTranscriptPagerLoop_RedrawsDelayedActorPagerState(t *testing.T) {
	snapshot := pagerSnapshot(1,
		committedPagerCell(1, "one"), committedPagerCell(2, "two"), committedPagerCell(3, "three"),
	)
	model := NewTranscriptPagerModel(snapshot)
	actorState := NewTranscriptPagerState()
	actorState.Reconcile(model, 40, 5)
	frames := make([]string, 0, 3)
	postCalls := 0
	keyCalls := 0
	viewReads := 0
	err := runTranscriptPagerLoop(context.Background(), transcriptPagerLoopHooks{
		refreshSize: func() (int, int) { return 40, 8 },
		view: func() TranscriptPagerView {
			viewReads++
			return TranscriptPagerView{
				Snapshot:   snapshot,
				Pager:      actorState,
				PagerKnown: true,
			}
		},
		leaseID: 17,
		postAction: func(action UIAction) bool {
			postCalls++
			if _, ok := action.(TranscriptPagerScroll); !ok {
				t.Fatalf("pager action = %T, want TranscriptPagerScroll", action)
			}
			// Posting only enqueues work. Keep the actor state unchanged here to
			// model a reducer that runs after this input iteration.
			return true
		},
		writeFrame: func(frame string) error {
			frames = append(frames, frame)
			return nil
		},
		readKey: func(context.Context) (editorKey, bool, error) {
			keyCalls++
			switch keyCalls {
			case 1:
				return editorKey{kind: editorKeyUp}, true, nil
			case 2:
				actorState.Scroll(model, 40, 5, -1)
				return editorKey{}, false, nil
			default:
				return editorKey{kind: editorKeyTranspose}, true, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("runTranscriptPagerLoop: %v", err)
	}
	if postCalls != 1 || viewReads < 3 || actorState.FollowBottom {
		t.Fatalf("delayed actor state was not applied: posts=%d reads=%d state=%#v", postCalls, viewReads, actorState)
	}
	if len(frames) != 3 {
		t.Fatalf("frame count = %d, want redraw after delayed actor state", len(frames))
	}
	if frames[1] == frames[2] {
		t.Fatalf("actor pager anchor changed without a replacement frame: %#v", frames)
	}
}

func TestRunTranscriptPagerLoop_ActorViewWithoutPosterRemainsReadOnly(t *testing.T) {
	snapshot := pagerSnapshot(1,
		committedPagerCell(1, "one"), committedPagerCell(2, "two"), committedPagerCell(3, "three"),
	)
	model := NewTranscriptPagerModel(snapshot)
	actorState := NewTranscriptPagerState()
	actorState.Reconcile(model, 40, 5)
	before := actorState
	frames := 0
	keyCalls := 0
	err := runTranscriptPagerLoop(context.Background(), transcriptPagerLoopHooks{
		refreshSize: func() (int, int) { return 40, 8 },
		view: func() TranscriptPagerView {
			return TranscriptPagerView{Snapshot: snapshot, Pager: actorState, PagerKnown: true}
		},
		writeFrame: func(string) error {
			frames++
			return nil
		},
		readKey: func(context.Context) (editorKey, bool, error) {
			keyCalls++
			if keyCalls == 1 {
				return editorKey{kind: editorKeyUp}, true, nil
			}
			return editorKey{kind: editorKeyTranspose}, true, nil
		},
	})
	if err != nil {
		t.Fatalf("runTranscriptPagerLoop: %v", err)
	}
	if actorState != before || frames != 1 {
		t.Fatalf("read-only actor pager acquired local scroll state: before=%#v after=%#v frames=%d", before, actorState, frames)
	}
}

func TestTranscriptOverlayReducer_SynchronizesLeaseAndTranscript(t *testing.T) {
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 40, Height: 12}, 1)
	state = reduceUIControllerState(state, LeaseAcquired{LeaseID: 9}, 2)
	state = reduceUIControllerState(state, OpenTranscriptOverlay{LeaseID: 9}, 3)
	if !state.TranscriptOverlay.Active || state.TranscriptOverlay.LeaseID != 9 {
		t.Fatalf("overlay was not opened: %#v", state.TranscriptOverlay)
	}
	cell := committedPagerCell(1, "committed")
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 4, Cells: []*scene.TranscriptCell{&cell},
	}}, 4)
	if state.TranscriptOverlay.Pager.Anchor.CellID != 1 {
		t.Fatalf("pager did not receive transcript replacement: %#v", state.TranscriptOverlay.Pager)
	}
	state = reduceUIControllerState(state, LeaseReleased{LeaseID: 9}, 5)
	if state.TranscriptOverlay.Active || state.Lease.Active {
		t.Fatalf("lease release did not clear overlay: lease=%#v overlay=%#v", state.Lease, state.TranscriptOverlay)
	}
}

func TestResumePickerReducer_BindsAndClearsLeaseOwnership(t *testing.T) {
	state := UIControllerState{}
	state = reduceUIControllerState(state, LeaseAcquired{LeaseID: 9}, 1)
	state = reduceUIControllerState(state, OpenResumePicker{LeaseID: 8}, 2)
	if state.ResumePicker.Active {
		t.Fatalf("stale picker open acquired ownership: %#v", state.ResumePicker)
	}
	state = reduceUIControllerState(state, OpenResumePicker{LeaseID: 9}, 3)
	if !state.ResumePicker.Active || state.ResumePicker.LeaseID != 9 {
		t.Fatalf("picker was not bound to active lease: %#v", state.ResumePicker)
	}
	state = reduceUIControllerState(state, CloseResumePicker{LeaseID: 8}, 4)
	if !state.ResumePicker.Active {
		t.Fatalf("stale picker close cleared active state: %#v", state.ResumePicker)
	}
	state = reduceUIControllerState(state, LeaseReleased{LeaseID: 9}, 5)
	if state.ResumePicker.Active || state.Lease.Active {
		t.Fatalf("lease release did not clear picker state: lease=%#v picker=%#v", state.Lease, state.ResumePicker)
	}
}

func TestBacktrackPickerReducer_BindsAndClearsLeaseOwnership(t *testing.T) {
	state := UIControllerState{}
	state = reduceUIControllerState(state, LeaseAcquired{LeaseID: 12}, 1)
	state = reduceUIControllerState(state, OpenBacktrackPicker{LeaseID: 11}, 2)
	if state.BacktrackPicker.Active {
		t.Fatalf("stale backtrack picker open acquired ownership: %#v", state.BacktrackPicker)
	}
	state = reduceUIControllerState(state, OpenBacktrackPicker{LeaseID: 12}, 3)
	if !state.BacktrackPicker.Active || state.BacktrackPicker.LeaseID != 12 {
		t.Fatalf("backtrack picker was not bound to active lease: %#v", state.BacktrackPicker)
	}
	state = reduceUIControllerState(state, CloseBacktrackPicker{LeaseID: 11}, 4)
	if !state.BacktrackPicker.Active {
		t.Fatalf("stale backtrack picker close cleared active state: %#v", state.BacktrackPicker)
	}
	state = reduceUIControllerState(state, LeaseReleased{LeaseID: 12}, 5)
	if state.BacktrackPicker.Active {
		t.Fatalf("lease release did not clear backtrack picker: %#v", state.BacktrackPicker)
	}
}

func TestTranscriptOverlayReducer_RejectsStaleLeasePagerIntent(t *testing.T) {
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 40, Height: 12}, 1)
	state = reduceUIControllerState(state, LeaseAcquired{LeaseID: 9}, 2)
	state = reduceUIControllerState(state, OpenTranscriptOverlay{LeaseID: 9}, 3)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 4,
		Cells: []*scene.TranscriptCell{
			ptrPagerCell(committedPagerCell(1, "one")),
			ptrPagerCell(committedPagerCell(2, "two")),
			ptrPagerCell(committedPagerCell(3, "three")),
			ptrPagerCell(committedPagerCell(4, "four")),
			ptrPagerCell(committedPagerCell(5, "five")),
		},
	}}, 4)
	before := state.TranscriptOverlay.Pager
	state = reduceUIControllerState(state, TranscriptPagerScroll{LeaseID: 8, Delta: -1}, 5)
	if state.TranscriptOverlay.Pager != before {
		t.Fatalf("stale lease changed pager: before=%#v after=%#v", before, state.TranscriptOverlay.Pager)
	}
	state = reduceUIControllerState(state, TranscriptPagerScroll{LeaseID: 9, Delta: -1}, 6)
	if state.TranscriptOverlay.Pager.FollowBottom {
		t.Fatalf("current lease scroll did not update pager: %#v", state.TranscriptOverlay.Pager)
	}
}

func TestFinalizeActiveCellAction_RejectsStaleActiveVersion(t *testing.T) {
	state := UIControllerState{AppState: AppState{
		Transcript: TranscriptState{Revision: 1, Cells: []scene.TranscriptCell{{
			ID: 4, Revision: 2, Kind: scene.KindAssistant, Source: "partial", Phase: scene.CellMutable,
		}}},
		Active: ActiveCellState{CellID: 4, Revision: 3, Phase: ActiveCellMutable, Source: "newer"},
	}}
	final := committedPagerCell(4, "final")
	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		Snapshot:             &scene.Snapshot{Revision: 2, Cells: []*scene.TranscriptCell{&final}},
		ExpectedActiveCellID: 4, ExpectedActiveRevision: 2,
	}, 1)
	if state.Active.Revision != 3 || state.Transcript.Revision != 1 {
		t.Fatalf("stale finalization modified state: %#v", state.AppState)
	}
}

func transcriptPagerRowsText(rows []TranscriptPagerRow) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.Text)
	}
	return strings.Join(parts, "\n")
}

func transcriptPagerBoundaryRowCount(rows []TranscriptPagerRow) int {
	count := 0
	for _, row := range rows {
		if row.CellID == 0 && row.Text == "" {
			count++
		}
	}
	return count
}

func ptrPagerCell(cell scene.TranscriptCell) *scene.TranscriptCell {
	return &cell
}
