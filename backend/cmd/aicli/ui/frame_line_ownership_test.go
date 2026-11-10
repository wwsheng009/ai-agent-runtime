package ui

import (
	"runtime"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func structuredFrameLineState() AppState {
	document := render.Document{Blocks: []render.Block{{
		Kind: render.BlockParagraph,
		Lines: []render.Line{{
			Spans: []render.Span{{Text: "structured row", Style: render.Style{Role: "assistant"}}},
		}},
	}}}
	return AppState{
		Revision: 7,
		Geometry: GeometryState{Width: 40, Height: 10, Generation: 3},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{
				ID:       1,
				Sequence: 1,
				Kind:     scene.KindAssistant,
				Source:   "structured row",
				Phase:    scene.CellCommitted,
				Boundary: boundary.BoundaryNormal,
				Presentation: scene.TranscriptPresentation{
					Kind:     scene.PresentationDocument,
					Document: document,
				},
			},
		}}),
		Bottom: BottomPaneState{
			PromptLine:         "> ",
			PromptReservedRows: 1,
			PromptVisible:      true,
		},
	}
}

func findStructuredTranscriptRow(t *testing.T, rows []AppScreenRow) int {
	t.Helper()
	for index := range rows {
		if rows[index].Owner == renderengine.RowOwnerTranscript && len(rows[index].RenderLine.Spans) > 0 {
			return index
		}
	}
	t.Fatal("fixture did not produce a structured transcript row")
	return -1
}

func TestComposeAppRenderFrameDetachesStructuredTranscriptLines(t *testing.T) {
	state := structuredFrameLineState()
	frame := ComposeAppRenderFrame(state)
	index := findStructuredTranscriptRow(t, func() []AppScreenRow {
		rows := make([]AppScreenRow, len(frame.Rows))
		for i := range frame.Rows {
			rows[i] = frame.Rows[i].Screen
		}
		return rows
	}())
	original := frame.Rows[index].Line.Spans[0].Text
	frame.Rows[index].Line.Spans[0].Text = "mutated"

	second := ComposeAppRenderFrame(state)
	if got := second.Rows[index].Line.Spans[0].Text; got != original {
		t.Fatalf("layout cache was mutated through app render frame: got %q, want %q", got, original)
	}
}

func TestComposeTerminalFramePlanDoesNotAliasRenderRowsToScreenRows(t *testing.T) {
	state := structuredFrameLineState()
	plan := ComposeTerminalFramePlan(state)
	index := findStructuredTranscriptRow(t, plan.Rows)
	original := plan.Rows[index].RenderLine.Spans[0].Text
	plan.RenderRows[index].Spans[0].Text = "mutated"

	if got := plan.Rows[index].RenderLine.Spans[0].Text; got != original {
		t.Fatalf("RenderRows aliases Screen.RenderLine: got %q, want %q", got, original)
	}
	second := ComposeTerminalFramePlan(state)
	if got := second.RenderRows[index].Spans[0].Text; got != original {
		t.Fatalf("layout cache was mutated through terminal frame plan: got %q, want %q", got, original)
	}
}

func composeTerminalFramePlanWithExtraLineClone(state AppState) TerminalFramePlan {
	frame := ComposeAppRenderFrame(state)
	rows := make([]AppScreenRow, len(frame.Rows))
	renderRows := make([]render.Line, len(frame.Rows))
	for index, row := range frame.Rows {
		rows[index] = row.Screen
		renderRows[index] = cloneAppRenderLine(row.Line)
	}
	return TerminalFramePlan{
		LayoutGeneration: frame.LayoutGeneration,
		TerminalEpoch:    state.HistoryEffects.TerminalEpoch,
		Theme:            cloneThemeContext(state.Theme),
		Geometry:         frame.Geometry,
		Lease:            frame.Lease,
		OutputBottomRow:  frame.OutputBottomRow,
		Rows:             rows,
		RenderRows:       renderRows,
		Cursor:           cloneTerminalCursor(frame.Cursor),
	}
}

func composeTerminalFramePlanWithAllLineClones(state AppState) TerminalFramePlan {
	frame := ComposeAppRenderFrame(state)
	rows := make([]AppScreenRow, len(frame.Rows))
	renderRows := make([]render.Line, len(frame.Rows))
	for index, row := range frame.Rows {
		rows[index] = row.Screen
		// Approximate the pre-optimization pipeline: ComposeAppRenderFrame
		// cloned every line, then ComposeTerminalFramePlan cloned it again.
		cloned := cloneAppRenderLine(row.Line)
		frame.Rows[index].Line = cloned
		renderRows[index] = cloneAppRenderLine(cloned)
	}
	return TerminalFramePlan{
		LayoutGeneration: frame.LayoutGeneration,
		TerminalEpoch:    state.HistoryEffects.TerminalEpoch,
		Theme:            cloneThemeContext(state.Theme),
		Geometry:         frame.Geometry,
		Lease:            frame.Lease,
		OutputBottomRow:  frame.OutputBottomRow,
		Rows:             rows,
		RenderRows:       renderRows,
		Cursor:           cloneTerminalCursor(frame.Cursor),
	}
}

func TestComposeTerminalFramePlanAvoidsSecondLineClone(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过分配量对照")
	}
	state := composeFixtureState()
	_ = ComposeTerminalFramePlan(state)
	_ = composeTerminalFramePlanWithExtraLineClone(state)
	_ = composeTerminalFramePlanWithAllLineClones(state)

	const iterations = 300
	measure := func(compose func(AppState) TerminalFramePlan) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < iterations; i++ {
			_ = compose(state)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	withClone := measure(composeTerminalFramePlanWithExtraLineClone)
	withAllClones := measure(composeTerminalFramePlanWithAllLineClones)
	withoutClone := measure(ComposeTerminalFramePlan)
	t.Logf("frame plan alloc (%d iterations): all_clones=%dB second_clone_only=%dB new=%dB reduction_all=%.1f%% reduction_second=%.1f%%",
		iterations, withAllClones, withClone, withoutClone,
		(1-float64(withoutClone)/float64(withAllClones))*100,
		(1-float64(withoutClone)/float64(withClone))*100)
	if withoutClone >= withClone || withoutClone >= withAllClones {
		t.Fatalf("removing line clones did not reduce allocations: all=%d second=%d new=%d",
			withAllClones, withClone, withoutClone)
	}
}
