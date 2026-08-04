package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestProjectActiveCellBandUsesOnlyUnacknowledgedSemanticSuffix(t *testing.T) {
	const acknowledged = "already handed off\n"
	active := ActiveCellState{
		CellID:   17,
		Revision: 4,
		Kind:     scene.KindAssistant,
		Phase:    ActiveCellMutable,
		Source:   acknowledged + "still live\nfinal tail",
		Acked:    SourceRange{Start: 0, End: len(acknowledged)},
	}

	projection := ProjectActiveCellBand(active, GeometryState{Width: 80, Height: 24})
	if !projection.Valid() {
		t.Fatalf("projection = %+v, want valid source-backed tail", projection)
	}
	if projection.CellID != active.CellID || projection.Revision != active.Revision || projection.Kind != scene.KindAssistant {
		t.Fatalf("projection identity = %+v, want active identity", projection)
	}
	if projection.SourceRange != (SourceRange{Start: len(acknowledged), End: len(active.Source)}) {
		t.Fatalf("source range = %+v, want unacknowledged suffix", projection.SourceRange)
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(projection.Lines...))
	if strings.Contains(plain, "already handed off") || plain != "still live\nfinal tail" {
		t.Fatalf("projected plain tail = %q, want only unacknowledged source", plain)
	}
	for _, line := range projection.Lines {
		if len(line.Spans) != 1 || line.Spans[0].Style.Role != string(style.RoleAssistant) {
			t.Fatalf("line role = %#v, want assistant semantic role", line)
		}
	}
}

func TestProjectActiveCellBandUsesViewportTailAndRejectsUnsafeRangeBoundary(t *testing.T) {
	active := ActiveCellState{
		CellID:   19,
		Revision: 2,
		Kind:     scene.KindToolChain,
		Phase:    ActiveCellFinalizing,
		Source:   "one\ntwo\nthree\nfour\nfive\nsix\nseven",
	}
	projection := ProjectActiveCellBand(active, GeometryState{Width: 80, Height: 12})
	if got, want := len(projection.Lines), ActiveBandRows(12); got != want {
		t.Fatalf("line count = %d, want active-band budget %d", got, want)
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(projection.Lines...))
	if plain != "two\nthree\nfour\nfive\nsix\nseven" {
		t.Fatalf("viewport tail = %q, want latest bounded rows", plain)
	}
	if projection.Lines[0].Spans[0].Style.Role != string(style.RoleTool) {
		t.Fatalf("tool projection role = %#v", projection.Lines[0])
	}

	// SourceRange uses byte offsets. An offset inside a UTF-8 rune must not be
	// rounded because either direction would corrupt exact handoff ownership.
	unsafe := ActiveCellState{
		CellID: 20, Revision: 1, Kind: scene.KindAssistant, Phase: ActiveCellMutable,
		Source: "你好", Acked: SourceRange{Start: 0, End: 1},
	}
	if got := ProjectActiveCellBand(unsafe, GeometryState{Width: 80, Height: 24}); got.Valid() || len(got.Lines) != 0 {
		t.Fatalf("unsafe UTF-8 range produced projection: %+v", got)
	}
	nonPrefix := active
	nonPrefix.Acked = SourceRange{Start: 1, End: 4}
	if got := ProjectActiveCellBand(nonPrefix, GeometryState{Width: 80, Height: 24}); got.Valid() || len(got.Lines) != 0 {
		t.Fatalf("non-prefix acknowledged range produced projection: %+v", got)
	}
}

func TestLayoutAppStateUsesActiveProjectionOnlyWithoutLegacyBand(t *testing.T) {
	state := AppState{
		Geometry: GeometryState{Width: 40, Height: 16},
		Active: ActiveCellState{
			CellID: 31, Revision: 5, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: "semantic live tail",
		},
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
		},
	}

	layout := LayoutAppState(state)
	if !layout.ActiveBand.Valid() || layout.Bottom.LegacyBandProjection {
		t.Fatalf("active/legacy projection state = %+v / %+v", layout.ActiveBand, layout.Bottom)
	}
	if got := (render.PlainBackend{}).Render(render.LinesDoc(layout.Bottom.State.ActiveBandStyled...)); got != "semantic live tail" {
		t.Fatalf("derived active-band text = %q", got)
	}
	if len(state.Bottom.ActiveBandLines) != 0 || len(state.Bottom.ActiveBandStyled) != 0 {
		t.Fatalf("pure layout mutated caller bottom state: %+v", state.Bottom)
	}

	state.Bottom.ActiveBandLines = []string{"legacy active projection"}
	layout = LayoutAppState(state)
	if !layout.Bottom.LegacyBandProjection {
		t.Fatal("legacy facade input must remain selected during migration")
	}
	if got := strings.Join(layout.Bottom.State.ActiveBandLines, "\n"); got != "legacy active projection" {
		t.Fatalf("legacy active-band text = %q, want legacy facade projection", got)
	}
}
