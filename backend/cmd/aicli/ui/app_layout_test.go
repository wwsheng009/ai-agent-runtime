package ui

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestLayoutAppStateDerivesTranscriptAndBottomWithoutAliases(t *testing.T) {
	state := AppState{
		Revision:         31,
		LayoutGeneration: 7,
		Geometry:         GeometryState{Width: 80, Height: 24, Generation: 7},
		Lease:            LeaseState{ID: 5, Active: true},
		Transcript: NewTranscriptState(&scene.Snapshot{
			Revision: 12,
			Cells: []*scene.TranscriptCell{
				{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "question", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
				{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "answer", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			},
		}),
		Active: ActiveCellState{CellID: 3, Revision: 4, Phase: ActiveCellMutable, Source: "live source"},
		Bottom: BottomPaneState{
			StatusModel:        &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
			PromptLine:         "> ",
			PromptInput:        "draft",
			PromptReservedRows: 1,
			PromptVisible:      true,
			Focus:              BottomFocusPrompt,
			PopupLines:         []string{"one choice"},
			ActiveBandLines:    []string{"legacy transient row"},
		},
	}

	first := LayoutAppState(state)
	second := LayoutAppState(state)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("pure layout is not deterministic\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.Revision != 31 || first.LayoutGeneration != 7 || first.Geometry != state.Geometry || first.Lease != state.Lease {
		t.Fatalf("layout metadata = %+v", first)
	}
	if len(first.Transcript) != 3 || first.Transcript[0].Text != "question" || first.Transcript[1].Gap != boundary.GapOne || first.Transcript[2].Text != "answer" {
		t.Fatalf("transcript layout = %+v", first.Transcript)
	}
	if first.Active.Source != "live source" || !first.Bottom.LegacyBandProjection || first.Bottom.StatusRows != 1 || first.Bottom.PromptRows < 1 || first.Bottom.ActiveBandRows < 1 || len(first.Bottom.VisiblePopupLines) != 1 {
		t.Fatalf("bottom layout = %+v", first.Bottom)
	}

	// Layout must detach all nested source slices and pointers from the caller.
	state.Transcript.Cells[0].Source = "mutated"
	state.Bottom.PopupLines[0] = "mutated"
	state.Bottom.ActiveBandLines[0] = "mutated"
	if first.Transcript[0].Text != "question" || first.Bottom.VisiblePopupLines[0] != "one choice" || first.Bottom.State.ActiveBandLines[0] != "legacy transient row" {
		t.Fatalf("layout retained caller-owned mutable memory: %+v", first)
	}
}

func TestLayoutAppScreenCombinesTranscriptTailAndBottomWithoutTerminal(t *testing.T) {
	state := AppState{
		Revision:         17,
		LayoutGeneration: 4,
		Geometry:         GeometryState{Width: 5, Height: 7, Generation: 4},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "abcdeF", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "甲乙xy", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		}}),
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		},
	}

	plan := LayoutAppScreen(state)
	if plan.Revision != 17 || plan.LayoutGeneration != 4 || plan.OutputBottomRow != 6 || len(plan.Rows) != 7 {
		t.Fatalf("screen metadata = %+v", plan)
	}
	want := []AppScreenRow{
		{Row: 1, Owner: renderengine.RowOwnerGap},
		{Row: 2, Owner: renderengine.RowOwnerTranscript, Text: "abcde", CellID: 1},
		{Row: 3, Owner: renderengine.RowOwnerTranscript, Text: "F", CellID: 1},
		{Row: 4, Owner: renderengine.RowOwnerGap, CellID: 2, TranscriptGap: true},
		{Row: 5, Owner: renderengine.RowOwnerTranscript, Text: "甲乙x", CellID: 2},
		{Row: 6, Owner: renderengine.RowOwnerTranscript, Text: "y", CellID: 2},
		{Row: 7, Owner: renderengine.RowOwnerStatus, Text: "Ready"},
	}
	if !reflect.DeepEqual(plan.Rows, want) {
		t.Fatalf("screen rows = %#v\nwant = %#v", plan.Rows, want)
	}
	if plan.ActiveProjectionPending || plan.LegacyBandProjection {
		t.Fatalf("committed-only plan unexpectedly reports active projection: %+v", plan)
	}
}

func TestLayoutAppScreenExcludesMutableTranscriptFromRetainedRows(t *testing.T) {
	state := AppState{
		Geometry: GeometryState{Width: 20, Height: 5},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "committed", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "mutable source", Phase: scene.CellMutable, Boundary: boundary.BoundaryNormal},
		}}),
		Active: ActiveCellState{CellID: 2, Revision: 3, Phase: ActiveCellMutable, Source: "mutable source"},
		Bottom: BottomPaneState{
			StatusModel:     &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
			ActiveBandLines: []string{"legacy active projection"},
		},
	}

	plan := LayoutAppScreen(state)
	if !plan.ActiveProjectionPending || !plan.LegacyBandProjection {
		t.Fatalf("active migration markers = %+v", plan)
	}
	var committed, mutable, band bool
	for _, row := range plan.Rows {
		if row.Owner == renderengine.RowOwnerBand {
			band = true
		}
		switch row.Text {
		case "committed":
			committed = row.Owner == renderengine.RowOwnerTranscript && row.CellID == 1
		case "mutable source":
			mutable = true
		}
	}
	if !committed || mutable || !band {
		t.Fatalf("screen layout mixed retained and mutable sources: %+v", plan.Rows)
	}
}
