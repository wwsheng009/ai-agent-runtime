package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestComposeAppRenderFramePreservesTextFrameAndStructuredSources(t *testing.T) {
	state := AppState{
		Revision:         55,
		LayoutGeneration: 4,
		Geometry:         GeometryState{Width: 24, Height: 14, Generation: 4},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{
			{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "question", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "answer", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
			{ID: 3, Sequence: 3, Kind: scene.KindToolChain, Source: "tool result", Phase: scene.CellCommitted, Boundary: boundary.BoundaryNormal},
		}}),
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"},
			ActiveBandStyled: []render.Line{{Spans: []render.Span{
				{Text: "live activity", Style: render.Style{Role: string(style.RoleProgress)}},
			}}},
		},
	}

	frame := ComposeAppRenderFrame(state)
	plain := ComposeAppTextLayout(state)
	if frame.Revision != state.Revision || frame.LayoutGeneration != state.LayoutGeneration || frame.Geometry != state.Geometry {
		t.Fatalf("frame metadata = %+v", frame)
	}
	if len(frame.Rows) != len(plain.Rows) {
		t.Fatalf("render rows = %d, text rows = %d", len(frame.Rows), len(plain.Rows))
	}

	roles := make(map[scene.CellID]string)
	var band render.Line
	for index, row := range frame.Rows {
		if !reflect.DeepEqual(row.Screen, plain.Rows[index]) {
			t.Fatalf("row %d screen identity diverged\nrender=%+v\ntext  =%+v", index+1, row.Screen, plain.Rows[index])
		}
		if got := (render.PlainBackend{}).Render(render.LinesDoc(row.Line)); got != row.Screen.Text {
			// 用户 prompt 消息的渲染行带 "> " 前缀（区分用户信息与 LLM 信息）。
			prefixed := userMessagePrefix + row.Screen.Text
			if !(row.Screen.UserMessage && got == prefixed) {
				t.Fatalf("row %d structured plain text = %q, want %q", index+1, got, row.Screen.Text)
			}
		}
		if row.Screen.Owner == renderengine.RowOwnerTranscript && !row.Screen.TranscriptGap && len(row.Line.Spans) > 0 {
			roles[row.Screen.CellID] = row.Line.Spans[0].Style.Role
		}
		if row.Screen.Owner == renderengine.RowOwnerBand {
			band = row.Line
		}
	}
	if got, want := roles[1], string(style.RoleUser); got != want {
		t.Fatalf("user row role = %q, want %q", got, want)
	}
	if got, want := roles[2], string(style.RoleAssistant); got != want {
		t.Fatalf("assistant row role = %q, want %q", got, want)
	}
	if got, want := roles[3], string(style.RoleTool); got != want {
		t.Fatalf("tool row role = %q, want %q", got, want)
	}
	if !render.LinesEqual([]render.Line{band}, state.Bottom.ActiveBandStyled) {
		t.Fatalf("active band structured line = %#v, want %#v", band, state.Bottom.ActiveBandStyled[0])
	}
	if frame.Cursor != nil {
		t.Fatalf("cursor = %+v, want nil without focus", frame.Cursor)
	}
}

func TestComposeAppRenderFrameDetachesStructuredLines(t *testing.T) {
	state := composeFixtureState()
	state.Bottom.ActiveBandStyled = []render.Line{{Spans: []render.Span{{
		Text: "live", Style: render.Style{Role: string(style.RoleProgress)},
	}}}}
	state.Bottom.ActiveBandLines = nil

	frame := ComposeAppRenderFrame(state)
	for index := range frame.Rows {
		if frame.Rows[index].Screen.Owner == renderengine.RowOwnerBand && len(frame.Rows[index].Line.Spans) > 0 {
			frame.Rows[index].Line.Spans[0].Text = "mutated"
		}
	}
	second := ComposeAppRenderFrame(state)
	for _, row := range second.Rows {
		if row.Screen.Owner == renderengine.RowOwnerBand && len(row.Line.Spans) > 0 && row.Line.Spans[0].Text != "live" {
			t.Fatalf("render frame retained caller/output alias: %+v", row)
		}
	}
}

func TestComposeAppRenderFrameNormalizesStructuredBandTrailingSpaces(t *testing.T) {
	state := AppState{
		LayoutGeneration: 1,
		Geometry:         GeometryState{Width: 40, Height: 12, Generation: 1},
		Bottom: BottomPaneState{
			ActiveBandStyled: []render.Line{
				{Spans: []render.Span{{Text: "first", Style: render.Style{Role: string(style.RoleAssistant)}}}},
				{Spans: []render.Span{{Text: "  ", Style: render.Style{Role: string(style.RoleAssistant)}}}},
				{Spans: []render.Span{
					{Text: "last", Style: render.Style{Role: string(style.RoleAssistant)}},
					{Text: "   ", Style: render.Style{Role: string(style.RoleTextMuted)}},
				}},
			},
			StatusModel: &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
		},
	}

	frame := ComposeAppRenderFrame(state)
	bandRows := 0
	for _, row := range frame.Rows {
		if row.Screen.Owner != renderengine.RowOwnerBand {
			continue
		}
		bandRows++
		plain := (render.PlainBackend{}).Render(render.LinesDoc(row.Line))
		if plain != row.Screen.Text {
			t.Fatalf("band row %d structured text = %q, want %q", row.Screen.Row, plain, row.Screen.Text)
		}
	}
	if bandRows != 3 {
		t.Fatalf("band rows = %d, want 3", bandRows)
	}
	plan := ComposeTerminalFramePlan(state)
	if _, err := terminalFrameCells(plan.Rows, plan.RenderRows, 40, 12, plan.Theme); err != nil {
		t.Fatalf("normalized structured frame was rejected: %v", err)
	}
}

func TestAppTranscriptRenderRoleCoversCellKinds(t *testing.T) {
	cases := []struct {
		kind scene.CellKind
		want style.Role
	}{
		{scene.KindUser, style.RoleUser},
		{scene.KindAssistant, style.RoleAssistant},
		{scene.KindToolChain, style.RoleTool},
		{scene.KindSupplement, style.RoleReasoning},
		{scene.KindReasoning, style.RoleReasoning},
		{scene.KindCommand, style.RoleCommand},
		{scene.KindSystem, style.RoleSystem},
		{scene.KindRuntimeEvent, style.RoleSystem},
		{scene.KindDiagnostic, style.RoleSystem},
	}
	for _, tc := range cases {
		if got := appTranscriptRenderRole(tc.kind); got != tc.want {
			t.Errorf("kind %s role = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestComposeAppRenderFrameUsesSourceBackedActiveBandFallback(t *testing.T) {
	state := AppState{
		Geometry: GeometryState{Width: 40, Height: 16},
		Active: ActiveCellState{
			CellID: 71, Revision: 6, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: "semantic live tail",
		},
		Bottom: BottomPaneState{
			StatusModel: &style.StatusLineModel{State: style.RunStreaming, StateText: "Working"},
		},
	}

	frame := ComposeAppRenderFrame(state)
	var bandRows int
	for _, row := range frame.Rows {
		if row.Screen.Owner != renderengine.RowOwnerBand {
			if row.Screen.Owner == renderengine.RowOwnerTranscript && row.Screen.Text == "semantic live tail" {
				t.Fatal("active source was also placed in retained transcript rows")
			}
			continue
		}
		bandRows++
		if row.Screen.Text != "semantic live tail" || len(row.Line.Spans) != 1 || row.Line.Spans[0].Style.Role != string(style.RoleAssistant) {
			t.Fatalf("active band row = %+v / %#v", row.Screen, row.Line)
		}
	}
	if bandRows != 1 {
		t.Fatalf("source-backed active band rows = %d, want 1", bandRows)
	}
}

func TestComposeAppRenderFrameRendersCommittedAssistantMarkdown(t *testing.T) {
	state := AppState{
		LayoutGeneration: 1,
		Geometry:         GeometryState{Width: 40, Height: 12, Generation: 1},
		Transcript: NewTranscriptState(&scene.Snapshot{Cells: []*scene.TranscriptCell{{
			ID: 1, Sequence: 1, Kind: scene.KindAssistant,
			Source: "# Rendered heading\n\n- **finished**\n- `code`",
			Phase:  scene.CellCommitted, Boundary: boundary.BoundaryNormal,
		}}}),
		Bottom: BottomPaneState{StatusModel: &style.StatusLineModel{State: style.RunReady, StateText: "Ready"}},
	}

	frame := ComposeAppRenderFrame(state)
	var visible []string
	var structured bool
	for _, row := range frame.Rows {
		if row.Screen.Owner != renderengine.RowOwnerTranscript || row.Screen.TranscriptGap {
			continue
		}
		visible = append(visible, row.Screen.Text)
		if len(row.Line.Spans) > 0 && row.Line.Spans[0].Style.Role != string(style.RoleAssistant) {
			structured = true
		}
	}
	rendered := strings.Join(visible, "\n")
	for _, want := range []string{"Rendered heading", "finished", "code"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("markdown transcript missing %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "# Rendered heading") || strings.Contains(rendered, "**finished**") {
		t.Fatalf("raw markdown leaked into committed transcript: %q", rendered)
	}
	if !structured {
		t.Fatalf("markdown transcript did not retain structured render spans: %#v", frame.Rows)
	}
}
