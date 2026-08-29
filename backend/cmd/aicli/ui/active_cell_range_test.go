package ui

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func activeRangeFixture(source string, revision uint64, stable, enqueued, acked int) ActiveCellState {
	return ActiveCellState{
		CellID:   7,
		Revision: revision,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   source,
		Stable:   SourceRange{Start: 0, End: stable},
		Enqueued: SourceRange{Start: 0, End: enqueued},
		Acked:    SourceRange{Start: 0, End: acked},
	}
}

func TestActiveCellStateValidateStreamingRanges(t *testing.T) {
	valid := activeRangeFixture("abcdef", 3, 6, 4, 2)
	if err := valid.ValidateStreamingRanges(); err != nil {
		t.Fatalf("valid range rejected: %v", err)
	}

	cases := []ActiveCellState{
		activeRangeFixture("abcdef", 3, 3, 4, 2), // enqueued past stable
		activeRangeFixture("abcdef", 3, 6, 4, 5), // acked past enqueued
		activeRangeFixture("abcdef", 3, 7, 4, 2), // source bound
	}
	for _, active := range cases {
		if err := active.ValidateStreamingRanges(); !errors.Is(err, ErrInvalidActiveCellRanges) {
			t.Errorf("range %+v error = %v, want ErrInvalidActiveCellRanges", active, err)
		}
	}

	// Scene snapshots intentionally carry no effect cursor during migration.
	if err := (ActiveCellState{CellID: 7, Revision: 1, Phase: ActiveCellMutable, Source: "scene"}).ValidateStreamingRanges(); err != nil {
		t.Fatalf("all-zero migration range rejected: %v", err)
	}
}

func TestActiveCellRangeTransitionsPreserveQueuedTailUntilAck(t *testing.T) {
	active := activeRangeFixture("hello", 1, 0, 0, 0)
	var err error
	active, err = AdvanceActiveSource(active, 2, "hello world", len("hello world"))
	if err != nil {
		t.Fatalf("advance source: %v", err)
	}
	active, err = MarkActiveEnqueued(active, len("hello"))
	if err != nil {
		t.Fatalf("mark enqueued: %v", err)
	}
	if active.Acked.End != 0 || active.Enqueued.End != len("hello") {
		t.Fatalf("queued transition = %+v", active)
	}
	active, err = MarkActiveAcked(active, len("hello"))
	if err != nil {
		t.Fatalf("mark acked: %v", err)
	}
	if active.Acked.End != len("hello") || active.Enqueued.End != len("hello") {
		t.Fatalf("ack transition = %+v", active)
	}
	if _, err := MarkActiveAcked(active, len("hello")-1); !errors.Is(err, ErrInvalidActiveCellRanges) {
		t.Fatalf("backward ack error = %v, want range rejection", err)
	}
}

func TestAdvanceActiveSourceRejectsDeltaFinalRaceAndClearsCorrectionRanges(t *testing.T) {
	active := activeRangeFixture("partial", 4, len("partial"), len("part"), len("par"))
	if _, err := AdvanceActiveSource(active, 4, "final", len("final")); !errors.Is(err, ErrStaleActiveCellUpdate) {
		t.Fatalf("same revision error = %v, want stale update", err)
	}
	next, err := AdvanceActiveSource(active, 5, "final", len("final"))
	if err != nil {
		t.Fatalf("source correction: %v", err)
	}
	if next.Source != "final" || next.Stable.End != len("final") || next.Enqueued.End != 0 || next.Acked.End != 0 {
		t.Fatalf("correction retained old ranges: %+v", next)
	}
	if _, err := AdvanceActiveSource(next, 6, "终", 1); !errors.Is(err, ErrInvalidActiveCellRanges) {
		t.Fatalf("unsafe UTF-8 stable boundary error = %v, want invalid range", err)
	}
}

func TestUpdateActiveCellActionCoalescesWithoutLosingRangeFence(t *testing.T) {
	first := UpdateActiveCellAction{
		ExpectedCellID: 7, ExpectedRevision: 2,
		Active: activeRangeFixture("abcdef", 3, 6, 5, 4),
	}
	second := UpdateActiveCellAction{
		ExpectedCellID: 7, ExpectedRevision: 3,
		Active: activeRangeFixture("abcdefgh", 4, 8, 7, 4),
	}
	merged, ok := mergeActions(first, second).(UpdateActiveCellAction)
	if !ok {
		t.Fatalf("merged action = %T, want UpdateActiveCellAction", mergeActions(first, second))
	}
	if merged.Active.Revision != 4 || merged.Active.Source != "abcdefgh" {
		t.Fatalf("merged payload = %+v, want latest source/revision", merged.Active)
	}
	if merged.ExpectedCellID != 7 || merged.ExpectedRevision != 2 {
		t.Fatalf("merged fence = %d/%d, want original 7/2", merged.ExpectedCellID, merged.ExpectedRevision)
	}
}

func TestReducerUpdateActiveCellRejectsStaleAndPreservesQueuedRange(t *testing.T) {
	state := UIControllerState{AppState: AppState{
		Active: activeRangeFixture("abcdef", 3, 6, 5, 4),
	}}
	stale := UpdateActiveCellAction{
		ExpectedCellID: 7, ExpectedRevision: 2,
		Active: activeRangeFixture("abcdefgh", 4, 8, 7, 4),
	}
	state = reduceUIControllerState(state, stale, 1)
	if state.Active.Source != "abcdef" || state.Active.Revision != 3 || state.Active.Enqueued.End != 5 {
		t.Fatalf("stale update mutated active range: %+v", state.Active)
	}

	valid := stale
	valid.ExpectedRevision = 3
	state = reduceUIControllerState(state, valid, 2)
	if state.Active.Source != "abcdefgh" || state.Active.Revision != 4 || state.Active.Enqueued.End != 7 || state.Active.Acked.End != 4 {
		t.Fatalf("valid update lost range ledger: %+v", state.Active)
	}
}

func TestReducerUpdateActiveCellClearsRangesOnSourceCorrection(t *testing.T) {
	state := UIControllerState{AppState: AppState{
		Active: activeRangeFixture("abcdef", 3, 6, 5, 4),
	}}
	correction := UpdateActiveCellAction{
		ExpectedCellID: 7, ExpectedRevision: 3,
		Active: activeRangeFixture("rewritten", 4, 0, 0, 0),
	}
	state = reduceUIControllerState(state, correction, 1)
	if state.Active.Source != "rewritten" || state.Active.Revision != 4 {
		t.Fatalf("correction was rejected: %+v", state.Active)
	}
	if state.Active.StreamingRangesKnown() {
		t.Fatalf("source correction retained old effect cursors: %+v", state.Active)
	}
}

func TestReplaceTranscriptDoesNotRollbackNewerShadowActiveCell(t *testing.T) {
	state := UIControllerState{AppState: AppState{
		Active: activeRangeFixture("newer source", 5, 11, 8, 6),
	}}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 4,
		Cells: []*scene.TranscriptCell{{
			ID: 7, Revision: 4, Kind: scene.KindAssistant, Source: "older source", Phase: scene.CellMutable,
		}},
	}}, 1)
	if state.Active.Source != "newer source" || state.Active.Revision != 5 || state.Active.Enqueued.End != 8 || state.Active.Acked.End != 6 {
		t.Fatalf("stale Scene snapshot rolled back active state: %+v", state.Active)
	}
}

func TestReplaceTranscriptClearsLedgerOnSceneSourceCorrection(t *testing.T) {
	state := UIControllerState{AppState: AppState{
		Active: activeRangeFixture("abcdef", 3, 6, 5, 4),
	}}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 4,
		Cells: []*scene.TranscriptCell{{
			ID: 7, Revision: 4, Kind: scene.KindAssistant, Source: "rewritten", Phase: scene.CellMutable,
		}},
	}}, 1)
	if state.Active.Source != "rewritten" || state.Active.StreamingRangesKnown() {
		t.Fatalf("source correction retained old ledger: %+v", state.Active)
	}
}

func TestReplaceTranscriptPreservesStreamingLedgerForSameMutableSource(t *testing.T) {
	current := activeRangeFixture("abc", 2, 3, 2, 1)
	state := UIControllerState{AppState: AppState{Active: current}}
	snapshot := &scene.Snapshot{Revision: 3, Cells: []*scene.TranscriptCell{{
		ID: 7, Kind: scene.KindAssistant, Source: "abc", Revision: 2, Phase: scene.CellMutable,
	}}}

	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 4)
	if state.Active != current {
		t.Fatalf("same mutable Scene source dropped streaming ledger: got %+v want %+v", state.Active, current)
	}
}

func TestReplaceTranscriptPreservesLedgerWhenSceneSourceAppends(t *testing.T) {
	current := activeRangeFixture("abc", 2, 3, 2, 1)
	state := UIControllerState{AppState: AppState{Active: current}}
	snapshot := &scene.Snapshot{Revision: 4, Cells: []*scene.TranscriptCell{{
		ID: 7, Kind: scene.KindAssistant, Source: "abcd", Revision: 3, Phase: scene.CellMutable,
	}}}

	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 5)
	if state.Active.Source != "abcd" || state.Active.Revision != 3 {
		t.Fatalf("advanced Scene source was not installed: %+v", state.Active)
	}
	if state.Active.Stable != current.Stable ||
		state.Active.Enqueued != current.Enqueued ||
		state.Active.Acked != current.Acked {
		t.Fatalf("append-only Scene source lost physical ledger: got %+v want %+v", state.Active, current)
	}
}

func TestReplaceTranscriptDropsActiveWhenSceneHasNoMutableCell(t *testing.T) {
	state := UIControllerState{AppState: AppState{
		Active: activeRangeFixture("abc", 2, 3, 2, 1),
	}}
	snapshot := &scene.Snapshot{Revision: 5, Cells: []*scene.TranscriptCell{{
		ID: 7, Kind: scene.KindAssistant, Source: "abc", Revision: 3, Phase: scene.CellCommitted,
	}}}

	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 6)
	if state.Active != (ActiveCellState{}) {
		t.Fatalf("finalized Scene unexpectedly retained active cell: %+v", state.Active)
	}
}

func TestUIControllerCoalescesQueuedActiveUpdatesByCellID(t *testing.T) {
	c := NewUIController(UIControllerConfig{MailboxSize: 8}, nil, nil)
	initial := activeRangeFixture("a", 1, 0, 0, 0)
	if !c.Post(SetActiveCellAction{Active: initial}) {
		t.Fatal("post initial active cell")
	}
	if !c.Post(UpdateActiveCellAction{
		ExpectedCellID: 7, ExpectedRevision: 1,
		Active: activeRangeFixture("ab", 2, 2, 1, 0),
	}) {
		t.Fatal("post first active update")
	}
	if !c.Post(UpdateActiveCellAction{
		ExpectedCellID: 7, ExpectedRevision: 2,
		Active: activeRangeFixture("abc", 3, 3, 2, 1),
	}) {
		t.Fatal("post second active update")
	}
	go c.Run()
	c.WaitIdle()

	active := c.AppState().Active
	if active.Source != "abc" || active.Revision != 3 || active.Stable.End != 3 || active.Enqueued.End != 2 || active.Acked.End != 1 {
		t.Fatalf("coalesced active state = %+v", active)
	}
	c.Close()
	c.WaitIdle()
}

func TestFinalizeActiveCellRequiresMatchingFinalSceneCell(t *testing.T) {
	state := UIControllerState{AppState: AppState{
		Transcript: TranscriptState{Revision: 1, Cells: []scene.TranscriptCell{{
			ID: 7, Revision: 1, Kind: scene.KindAssistant, Source: "partial", Phase: scene.CellMutable,
		}}},
		Active: activeRangeFixture("partial", 1, 0, 0, 0),
	}}

	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		ExpectedActiveCellID: 7, ExpectedActiveRevision: 1,
		Snapshot: &scene.Snapshot{Revision: 2, Cells: []*scene.TranscriptCell{{
			ID: 7, Revision: 2, Kind: scene.KindAssistant, Source: "final", Phase: scene.CellMutable,
		}}},
	}, 2)
	if state.Active.Phase != ActiveCellMutable || state.Transcript.Cells[0].Source != "partial" {
		t.Fatalf("mutable snapshot incorrectly finalized: %+v", state.AppState)
	}

	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		ExpectedActiveCellID: 7, ExpectedActiveRevision: 1,
		Snapshot: &scene.Snapshot{Revision: 3, Cells: []*scene.TranscriptCell{{
			ID: 7, Revision: 2, Kind: scene.KindAssistant, Source: "final", Phase: scene.CellCommitted,
		}}},
	}, 3)
	if state.Active != (ActiveCellState{}) || state.Transcript.Cells[0].Phase != scene.CellCommitted {
		t.Fatalf("matching final snapshot was not committed: %+v", state.AppState)
	}
}

func TestFinalizeActiveCellAcceptsEqualSceneRevisionForShadowFence(t *testing.T) {
	active := activeRangeFixture("partial", 3, 7, 0, 0)
	active.CellID = 9
	state := UIControllerState{AppState: AppState{
		Transcript: TranscriptState{Revision: 2, Cells: []scene.TranscriptCell{{
			ID: 9, Revision: 3, Kind: scene.KindAssistant, Source: "partial", Phase: scene.CellMutable,
		}}},
		Active: active,
	}}

	// A coalesced shadow source update may consume revision 3 while the Scene
	// finalization transitions the same semantic cell from mutable to committed
	// at revision 3. The active fence still protects newer (revision > 3) state;
	// requiring Scene revision 4 here would incorrectly leave the shadow cell
	// mounted until a separate transcript replacement action arrives.
	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		ExpectedActiveCellID: 9, ExpectedActiveRevision: 3,
		Snapshot: &scene.Snapshot{Revision: 3, Cells: []*scene.TranscriptCell{{
			ID: 9, Revision: 3, Kind: scene.KindAssistant, Source: "final", Phase: scene.CellCommitted,
		}}},
	}, 3)
	if state.Active != (ActiveCellState{}) || len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Source != "final" {
		t.Fatalf("equal-revision finalization was rejected: %+v", state.AppState)
	}
}

func TestFinalizeActiveCellCorrectionRequiresScrollbackReconciliation(t *testing.T) {
	active := activeRangeFixture("old-prefix\nresident-tail", 3, len("old-prefix\nresident-tail"), len("old-prefix"), len("old-prefix"))
	state := UIControllerState{AppState: AppState{
		Transcript: TranscriptState{Revision: 2, Cells: []scene.TranscriptCell{{
			ID: 7, Revision: 3, Kind: scene.KindAssistant, Source: active.Source, Phase: scene.CellMutable,
		}}},
		Active: active,
	}}

	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		ExpectedActiveCellID: 7, ExpectedActiveRevision: 3,
		Snapshot: &scene.Snapshot{Revision: 4, Cells: []*scene.TranscriptCell{{
			ID: 7, Revision: 4, Kind: scene.KindAssistant, Source: "correct-prefix\nresident-tail", Phase: scene.CellCommitted,
		}}},
	}, 4)

	if !state.HistoryEffects.ProjectionUnknown || !state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("authoritative correction reused acknowledged bytes: %#v", state.HistoryEffects)
	}
	if state.Active != (ActiveCellState{}) || state.Transcript.Cells[0].Source != "correct-prefix\nresident-tail" {
		t.Fatalf("authoritative final was not committed: %+v", state.AppState)
	}
}

func TestFinalizeActiveCellRejectsMismatchedSemanticKind(t *testing.T) {
	active := activeRangeFixture("partial", 3, 7, 0, 0)
	active.CellID = 12
	state := UIControllerState{AppState: AppState{
		Transcript: TranscriptState{Revision: 2, Cells: []scene.TranscriptCell{{
			ID: 12, Revision: 3, Kind: scene.KindAssistant, Source: "partial", Phase: scene.CellMutable,
		}}},
		Active: active,
	}}

	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		ExpectedActiveCellID: 12, ExpectedActiveRevision: 3, ExpectedActiveKind: scene.KindAssistant, ExpectedActiveKindKnown: true,
		Snapshot: &scene.Snapshot{Revision: 3, Cells: []*scene.TranscriptCell{{
			ID: 12, Revision: 3, Kind: scene.KindToolChain, Source: "wrong cell", Phase: scene.CellCommitted,
		}}},
	}, 3)
	if state.Active != active || state.Transcript.Cells[0].Phase != scene.CellMutable {
		t.Fatalf("mismatched final kind modified state: %+v", state.AppState)
	}
}

func TestClearActiveCellActionFencesDelayedShadowCompletion(t *testing.T) {
	active := activeRangeFixture("newer", 4, 5, 0, 0)
	state := UIControllerState{AppState: AppState{Active: active}}

	state = reduceUIControllerState(state, ClearActiveCellAction{
		ExpectedCellID: 8, ExpectedKind: scene.KindAssistant, ExpectedKindKnown: true,
	}, 1)
	if state.Active != active {
		t.Fatalf("different-cell clear erased newer active state: %+v", state.Active)
	}

	state = reduceUIControllerState(state, ClearActiveCellAction{
		ExpectedCellID: active.CellID, ExpectedKind: scene.KindToolChain, ExpectedKindKnown: true,
	}, 2)
	if state.Active != active {
		t.Fatalf("different-kind clear erased newer active state: %+v", state.Active)
	}

	// The clear owns this semantic cell, not an exact source revision. A
	// coalesced same-cell update may have advanced revision before completion
	// reaches the reducer and must not leave an orphaned active projection.
	state = reduceUIControllerState(state, ClearActiveCellAction{
		ExpectedCellID: active.CellID, ExpectedKind: active.Kind, ExpectedKindKnown: true,
	}, 3)
	if state.Active != (ActiveCellState{}) {
		t.Fatalf("matching shadow clear did not remove active state: %+v", state.Active)
	}
}
