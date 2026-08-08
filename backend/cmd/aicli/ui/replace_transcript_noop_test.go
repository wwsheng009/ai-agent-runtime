package ui

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func TestReplaceTranscriptSameSceneVersionIsAllocationFreeNoOp(t *testing.T) {
	snapshot := versionedSceneSnapshot(t, "stable history", 48)
	if snapshot.SceneID == 0 || snapshot.Revision == 0 || snapshot.ContentVersion == 0 {
		t.Fatalf("snapshot lacks a usable version fence: %#v", snapshot)
	}

	state := UIControllerState{AppState: AppState{
		Geometry:         GeometryState{Width: 80, Height: 8, Generation: 1},
		LayoutGeneration: 1,
	}}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 1)
	if len(state.Transcript.Cells) != len(snapshot.Cells) {
		t.Fatalf("initial replacement installed %d cells, want %d", len(state.Transcript.Cells), len(snapshot.Cells))
	}
	beforeToken := state.HistoryEffects.NextToken
	beforeEntries := state.HistoryEffects.Entries()
	beforeLedger := state.HistoryEffects.ledger

	nextRevision := uint64(2)
	allocs := testing.AllocsPerRun(100, func() {
		state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, nextRevision)
		nextRevision++
	})
	if allocs != 0 {
		t.Fatalf("same Scene version replacement allocated %.2f objects per reduce, want 0", allocs)
	}
	if state.Transcript.SceneID != snapshot.SceneID || state.Transcript.Revision != snapshot.Revision ||
		state.Transcript.ContentVersion != snapshot.ContentVersion || len(state.Transcript.Cells) != len(snapshot.Cells) {
		t.Fatalf("no-op replacement changed transcript identity: %+v", state.Transcript)
	}
	if state.HistoryEffects.NextToken != beforeToken || state.HistoryEffects.ledger != beforeLedger ||
		!reflect.DeepEqual(state.HistoryEffects.Entries(), beforeEntries) {
		t.Fatalf("no-op replacement replanned history: before=%#v after=%#v", beforeEntries, state.HistoryEffects.Entries())
	}
}

func TestReplaceTranscriptSameRevisionFromDifferentSceneIsInstalled(t *testing.T) {
	first := versionedSceneSnapshot(t, "old scene", 1)
	second := versionedSceneSnapshot(t, "rebuilt scene", 1)
	if first.SceneID == second.SceneID || first.Revision != second.Revision ||
		first.ContentVersion != second.ContentVersion {
		t.Fatalf("fixture version identities are not comparable: first=%#v second=%#v", first, second)
	}

	state := reduceUIControllerState(UIControllerState{}, ReplaceTranscriptAction{Snapshot: first}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: second}, 2)
	if state.Transcript.SceneID != second.SceneID || len(state.Transcript.Cells) != 1 ||
		state.Transcript.Cells[0].Source != "rebuilt scene" {
		t.Fatalf("rebuilt Scene was mistaken for a no-op: %+v", state.Transcript)
	}
}

func TestReplaceTranscriptActiveOnlySnapshotReusesFinalizedTranscript(t *testing.T) {
	s := scene.New()
	c := scene.NewController(s)
	if _, _, err := c.Submit(scene.SceneTransaction{Mutations: []scene.CellMutation{
		&scene.AppendCell{Cell: scene.TranscriptCell{
			ID: 1, Kind: scene.KindUser, Source: "finalized", Phase: scene.CellCommitted,
		}},
		&scene.AppendCell{Cell: scene.TranscriptCell{
			ID: 2, Kind: scene.KindAssistant, Source: "partial", Phase: scene.CellMutable,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	state := UIControllerState{AppState: AppState{SemanticActiveCellProjection: true}}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: s.Snapshot()}, 1)
	if state.Active.CellID != 2 || len(state.Transcript.Cells) != 2 {
		t.Fatalf("initial active fixture was not installed: %+v", state.AppState)
	}
	finalizedAddress := &state.Transcript.Cells[0]

	if _, _, err := c.Submit(scene.SceneTransaction{Mutations: []scene.CellMutation{
		&scene.UpdateCell{ID: 2, Revision: 1, Source: "partial response"},
	}}); err != nil {
		t.Fatal(err)
	}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: s.Snapshot()}, 2)
	if &state.Transcript.Cells[0] != finalizedAddress {
		t.Fatal("active-only replacement allocated a new finalized transcript slice")
	}
	if state.Transcript.Cells[0].Source != "finalized" || state.Transcript.Cells[1].Source != "partial response" {
		t.Fatalf("active-only replacement produced the wrong transcript: %+v", state.Transcript.Cells)
	}
}

func TestSceneSnapshotReusesUnchangedPointerSlice(t *testing.T) {
	s := scene.New()
	c := scene.NewController(s)
	if _, _, err := c.Submit(scene.SceneTransaction{Mutations: []scene.CellMutation{
		&scene.AppendCell{Cell: scene.TranscriptCell{ID: 1, Kind: scene.KindUser, Source: "one"}},
	}}); err != nil {
		t.Fatal(err)
	}
	first := s.Snapshot()
	second := s.Snapshot()
	if first != second {
		t.Fatal("unchanged Scene rebuilt its snapshot instead of reusing the immutable view")
	}
	if _, err := s.ApplyCellMutation(&scene.UpdateCell{ID: 1, Revision: 1, Source: "two"}); err != nil {
		t.Fatal(err)
	}
	third := s.Snapshot()
	if third == second || third.ContentVersion == second.ContentVersion || third.Cells[0].Source != "two" {
		t.Fatalf("mutated Scene reused a stale snapshot: before=%#v after=%#v", second, third)
	}
}

func versionedSceneSnapshot(t *testing.T, source string, count int) *scene.Snapshot {
	t.Helper()
	s := scene.New()
	mutations := make([]scene.CellMutation, 0, count)
	for index := 0; index < count; index++ {
		mutations = append(mutations, &scene.AppendCell{Cell: scene.TranscriptCell{
			ID: scene.CellID(index + 1), Kind: scene.KindRuntimeEvent,
			Source: source, Phase: scene.CellCommitted,
		}})
	}
	if _, _, err := scene.NewController(s).Submit(scene.SceneTransaction{Mutations: mutations}); err != nil {
		t.Fatalf("build versioned Scene: %v", err)
	}
	return s.Snapshot()
}
