package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func TestNewTranscriptStateFromSnapshotReusesUnchangedPrefix(t *testing.T) {
	previous := NewTranscriptState(&scene.Snapshot{SceneID: 7, Revision: 1, Cells: []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Revision: 1, Kind: scene.KindUser, Source: "prompt", Phase: scene.CellCommitted},
		{ID: 2, Sequence: 2, Revision: 1, Kind: scene.KindAssistant, Source: "reply", Phase: scene.CellCommitted},
	}})
	snapshot := &scene.Snapshot{SceneID: 7, Revision: 9, ContentVersion: 3, Cells: []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Revision: 1, Kind: scene.KindUser, Source: "prompt", Phase: scene.CellCommitted},
		{ID: 2, Sequence: 2, Revision: 2, Kind: scene.KindAssistant, Source: "reply more", Phase: scene.CellCommitted},
		{ID: 3, Sequence: 3, Revision: 1, Kind: scene.KindReasoning, Source: "new tail", Phase: scene.CellMutable},
		// nil entries must be skipped without breaking index alignment.
		nil,
	}}

	next := newTranscriptStateFromSnapshot(previous, snapshot)
	if next.SceneID != 7 || next.Revision != 9 || next.ContentVersion != 3 {
		t.Fatalf("scene fence = %d/%d/%d, want 7/9/3", next.SceneID, next.Revision, next.ContentVersion)
	}
	if len(next.Cells) != 3 {
		t.Fatalf("cells = %d, want 3 (nil skipped)", len(next.Cells))
	}
	if next.Cells[0].Source != "prompt" || next.Cells[0].Revision != 1 {
		t.Fatalf("unchanged prefix cell was not reused: %+v", next.Cells[0])
	}
	if next.Cells[1].Source != "reply more" || next.Cells[1].Revision != 2 {
		t.Fatalf("changed cell was not detached from the snapshot: %+v", next.Cells[1])
	}
	if next.Cells[2].ID != 3 || next.Cells[2].Source != "new tail" {
		t.Fatalf("appended tail cell missing: %+v", next.Cells[2])
	}
}

func TestNewTranscriptStateFromSnapshotDetachesChangedPresentation(t *testing.T) {
	previous := NewTranscriptState(&scene.Snapshot{SceneID: 7, Revision: 1, Cells: []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Revision: 1, Kind: scene.KindAssistant, Source: "a", Phase: scene.CellCommitted},
	}})
	snapshot := &scene.Snapshot{SceneID: 7, Revision: 2, Cells: []*scene.TranscriptCell{
		{
			ID: 1, Sequence: 1, Revision: 2, Kind: scene.KindAssistant, Source: "a", Phase: scene.CellCommitted,
			Presentation: scene.TranscriptPresentation{
				Kind:     scene.PresentationDocument,
				Document: render.Document{Blocks: []render.Block{{Lines: []render.Line{{Spans: []render.Span{{Text: "detached"}}}}}}},
			},
		},
	}}
	next := newTranscriptStateFromSnapshot(previous, snapshot)
	if len(next.Cells) != 1 || len(next.Cells[0].Presentation.Document.Blocks) != 1 {
		t.Fatalf("changed presentation was not installed: %+v", next.Cells)
	}
	// Mutating the snapshot after the detach must not leak into actor-owned state.
	snapshot.Cells[0].Presentation.Document.Blocks[0].Lines[0].Spans[0].Text = "mutated"
	if got := next.Cells[0].Presentation.Document.Blocks[0].Lines[0].Spans[0].Text; got != "detached" {
		t.Fatalf("actor-owned presentation aliased the snapshot: %q", got)
	}
}

func TestNewTranscriptStateFromSnapshotDifferentSceneRebuildsCells(t *testing.T) {
	previous := NewTranscriptState(&scene.Snapshot{SceneID: 7, Revision: 1, Cells: []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Revision: 1, Kind: scene.KindUser, Source: "prompt", Phase: scene.CellCommitted},
	}})
	snapshot := &scene.Snapshot{SceneID: 8, Revision: 1, Cells: []*scene.TranscriptCell{
		{ID: 9, Sequence: 1, Revision: 1, Kind: scene.KindUser, Source: "replayed", Phase: scene.CellCommitted},
	}}
	next := newTranscriptStateFromSnapshot(previous, snapshot)
	if next.SceneID != 8 || len(next.Cells) != 1 || next.Cells[0].ID != 9 {
		t.Fatalf("scene rebuild did not detach the new scene: %+v", next)
	}
}

func TestTranscriptCellUnchangedForActiveOnlyChecksContentAndMetadata(t *testing.T) {
	left := scene.TranscriptCell{ID: 1, Sequence: 1, Revision: 3, Kind: scene.KindAssistant, Source: "body", Phase: scene.CellCommitted}
	if !transcriptCellUnchangedForActiveOnly(left, left) {
		t.Fatal("identical cell reported as changed")
	}

	changedSource := left
	changedSource.Source = "corrected body"
	if transcriptCellUnchangedForActiveOnly(left, changedSource) {
		t.Fatal("source correction without a revision bump must invalidate active-only")
	}

	changedMetadata := left
	changedMetadata.BoundaryGroupKey = "other-group"
	if transcriptCellUnchangedForActiveOnly(left, changedMetadata) {
		t.Fatal("boundary-group change must invalidate active-only")
	}

	changedRevision := left
	changedRevision.Revision++
	if transcriptCellUnchangedForActiveOnly(left, changedRevision) {
		t.Fatal("revision bump must invalidate active-only")
	}
}
