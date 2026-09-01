package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestResumeStreamingDeltasDoNotLoopScrollbackReset is a controller-level
// regression test for the visible replay loop reported on `aicli resume`.
//
// Resume rebuilds the Scene from the persisted runtime-events log while the
// actor retains its own ActiveCellState (acked prefix, streaming ranges). Each
// streaming delta then arrives as ReplaceTranscriptAction. If ANY comparison
// in the transcript-replacement path (activeOnly or full) treats the append-only
// growth as invalidating already-acknowledged native scrollback, the reducer
// sets ReconciliationRequired on every delta, the terminal executor resets the
// scrollback and replays the ENTIRE transcript each time — the visible
// "continuously replaying history" loop.
//
// This test drives the real reduceUIControllerState path (not the helper
// function in isolation) across many streaming deltas and asserts the
// ReconciliationRequired flag stays clear and no scrollback reset is requested.
func TestResumeStreamingDeltasDoNotLoopScrollbackReset(t *testing.T) {
	const cellID = scene.CellID(7)

	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 24, Generation: 1}, 1)
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, 2)

	// Initial Scene: a finalized prefix cell + a mutable active cell. This
	// mirrors a resumed session where "hello" (bytes 0..5) already reached
	// native scrollback in a previous run.
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		SceneID: 1, Revision: 1, ContentVersion: 1,
		Cells: []*scene.TranscriptCell{
			{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1},
			{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: "hello", Revision: 1},
		},
	}}, 3)

	// Mount the active cell with acked prefix "hello" (bytes 0..5) and seed the
	// ledger with the corresponding acked commit, exactly as a previous
	// session's scrollback handoff would have left it.
	state = reduceUIControllerState(state, SetActiveCellAction{Active: ActiveCellState{
		CellID: cellID, Revision: 1, Kind: scene.KindAssistant,
		Phase: ActiveCellMutable, Source: "hello",
		Stable:   SourceRange{Start: 0, End: 5},
		Enqueued: SourceRange{Start: 0, End: 5},
		Acked:    SourceRange{Start: 0, End: 5},
	}}, 4)
	state.HistoryEffects.ledger.byToken[1] = HistoryCommitEntry{
		Commit: HistoryCommit{
			Token: 1, CellID: cellID, SourceRange: SourceRange{Start: 0, End: 5},
			LayoutGeneration: state.LayoutGeneration,
		},
		State: HistoryCommitAcked,
	}
	if state.Active.Acked.End != 5 {
		t.Fatalf("after SetActiveCellAction: active=%+v", state.Active)
	}
	if state.HistoryEffects.ReconciliationRequired {
		t.Fatal("initial state already set ReconciliationRequired")
	}

	// Simulate N streaming deltas. Each grows the active cell source
	// append-only; the acked prefix bytes stay identical. Presentation changes
	// because the longer source renders differently.
	stream := "hello"
	for index := 1; index <= 10; index++ {
		stream += " chunk-" + strings.Repeat("x", index)
		snapshot := &scene.Snapshot{
			SceneID: 1, Revision: uint64(index + 1), ContentVersion: uint64(index + 1),
			Cells: []*scene.TranscriptCell{
				{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1},
				{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: stream, Revision: uint64(index + 1),
					Presentation: scene.TranscriptPresentation{Kind: scene.PresentationAssistantMarkdown}},
			},
		}
		state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, uint64(4+index))
		if state.HistoryEffects.ReconciliationRequired {
			t.Fatalf("delta %d set ReconciliationRequired: streaming append-only growth "+
				"was treated as invalidating acked scrollback; this would reset+replay the whole "+
				"transcript on every delta (visible replay loop). Stream=%q", index, stream)
		}
		if state.HistoryEffects.ProjectionUnknown {
			t.Fatalf("delta %d set ProjectionUnknown", index)
		}
	}
}

// TestResumeStreamingFullPathPrefixPresentationChange exercises the FULL
// transcript-replacement path (not activeOnly, because SceneID changes) and
// verifies that Presentation re-derivation of finalized prefix cells after
// resume does not invalidate acked history. This covers the scenario where
// the event-log replay rebuilds the Scene under a new process-local SceneID
// while the actor still holds the old transcript.
func TestResumeStreamingFullPathPrefixPresentationChange(t *testing.T) {
	const cellID = scene.CellID(7)

	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 24, Generation: 1}, 1)
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, 2)

	// SceneID 1 initial scene.
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		SceneID: 1, Revision: 1, ContentVersion: 1,
		Cells: []*scene.TranscriptCell{
			{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1},
			{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: "hello", Revision: 1},
		},
	}}, 3)
	state = reduceUIControllerState(state, SetActiveCellAction{Active: ActiveCellState{
		CellID: cellID, Revision: 1, Kind: scene.KindAssistant,
		Phase: ActiveCellMutable, Source: "hello",
		Stable: SourceRange{Start: 0, End: 5}, Enqueued: SourceRange{Start: 0, End: 5},
		Acked: SourceRange{Start: 0, End: 5},
	}}, 4)
	state.HistoryEffects.ledger.byToken[1] = HistoryCommitEntry{
		Commit: HistoryCommit{
			Token: 1, CellID: cellID, SourceRange: SourceRange{Start: 0, End: 5},
			LayoutGeneration: state.LayoutGeneration,
		},
		State: HistoryCommitAcked,
	}
	if state.HistoryEffects.ReconciliationRequired {
		t.Fatal("initial state already set ReconciliationRequired")
	}

	// Scene rebuilt under a NEW SceneID (simulates event-log re-render after
	// resume). Prefix cell Presentation changes; source bytes unchanged.
	stream := "hello"
	for index := 1; index <= 15; index++ {
		stream += "-part"
		state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
			SceneID: 2, Revision: uint64(index + 1), ContentVersion: uint64(index + 1),
			Cells: []*scene.TranscriptCell{
				{ID: 3, Kind: scene.KindUser, Phase: scene.CellCommitted, Source: "prefix", Revision: 1,
					Presentation: scene.TranscriptPresentation{Kind: scene.PresentationDocument}},
				{ID: cellID, Kind: scene.KindAssistant, Phase: scene.CellMutable, Source: stream, Revision: uint64(index + 1),
					Presentation: scene.TranscriptPresentation{Kind: scene.PresentationAssistantMarkdown}},
			},
		}}, uint64(4+index))
		if state.HistoryEffects.ReconciliationRequired {
			t.Fatalf("delta %d (new SceneID) set ReconciliationRequired on streaming append-only growth", index)
		}
	}
}