package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// AppRenderRow pairs the stable screen-row identity with the structured render
// line that a future primary presenter will encode. Text remains available for
// text parity and cursor calculations, but Line is the presentation IR: no
// raw ANSI, terminal cache, or legacy surface state is retained here.
type AppRenderRow struct {
	Screen AppScreenRow
	Line   render.Line
}

// AppRenderFrame is the source-backed, viewport-sized structured frame
// derived from one AppState snapshot. It is intentionally not a terminal
// transaction: terminal color negotiation, cursor movement, front/back cache,
// history delivery, and byte writes remain TerminalSession responsibilities.
//
// This closes the plain-only frame gap in the migration without installing a
// second presenter beside FixedBottomSurface. The current TerminalSession
// continues to consume TerminalFramePlan until the production cutover can
// replace the whole legacy primary path atomically.
type AppRenderFrame struct {
	Revision         uint64
	LayoutGeneration uint64
	Geometry         GeometryState
	Lease            LeaseState
	OutputBottomRow  int
	Rows             []AppRenderRow
	Cursor           *AppCursor
}

// ComposeAppRenderFrame preserves the existing AppScreenLayout row allocation
// and AppTextLayout cursor contract while retaining structured style metadata
// for every row that currently has a semantic style source. It is pure: it
// reads only the supplied AppState and process-independent rendering helpers;
// it never reads FixedBottomSurface/historyWindow/ScreenModel or emits bytes.
func ComposeAppRenderFrame(state AppState) AppRenderFrame {
	screen := LayoutAppScreen(state)
	text := composeAppTextLayoutFromScreen(screen)
	transcript := make(map[scene.CellID]scene.TranscriptCell, len(state.Transcript.Cells))
	for _, cell := range state.Transcript.Cells {
		transcript[cell.ID] = cell
	}
	bottomLines := appBottomRenderLines(screen.bottom, screen.Geometry.Width)

	frame := AppRenderFrame{
		Revision:         screen.Revision,
		LayoutGeneration: screen.LayoutGeneration,
		Geometry:         screen.Geometry,
		Lease:            screen.Lease,
		OutputBottomRow:  screen.OutputBottomRow,
		Rows:             make([]AppRenderRow, len(text.Rows)),
		Cursor:           cloneAppRenderCursor(text.Cursor),
	}
	for index, row := range text.Rows {
		line := appPlainRenderLine(row.Text)
		switch row.Owner {
		case renderengine.RowOwnerTranscript:
			line = appTranscriptRenderLine(row, transcript)
		case renderengine.RowOwnerBand, renderengine.RowOwnerStatus:
			if styled, ok := bottomLines[row.Row]; ok {
				line = styled
			}
		}
		frame.Rows[index] = AppRenderRow{
			Screen: row,
			Line:   cloneAppRenderLine(line),
		}
	}
	return frame
}

func appTranscriptRenderLine(row AppScreenRow, cells map[scene.CellID]scene.TranscriptCell) render.Line {
	if row.TranscriptGap || row.Text == "" {
		return render.Line{}
	}
	cell, ok := cells[row.CellID]
	if !ok {
		return appPlainRenderLine(row.Text)
	}
	return render.Line{Spans: []render.Span{{
		Text:  row.Text,
		Style: render.Style{Role: string(appTranscriptRenderRole(cell.Kind))},
	}}}
}

func appTranscriptRenderRole(kind scene.CellKind) style.Role {
	switch kind {
	case scene.KindUser:
		return style.RoleUser
	case scene.KindAssistant:
		return style.RoleAssistant
	case scene.KindToolChain:
		return style.RoleTool
	case scene.KindSupplement:
		return style.RoleReasoning
	case scene.KindCommand:
		return style.RoleCommand
	case scene.KindSystem, scene.KindRuntimeEvent, scene.KindDiagnostic:
		return style.RoleSystem
	default:
		return style.RoleTextSecondary
	}
}

// appBottomRenderLines retains the structured sources already available in
// BottomPaneState. Rows without a structured source deliberately stay plain;
// the eventual presenter must not invent style by inspecting legacy ANSI.
func appBottomRenderLines(bottom BottomPaneLayout, width int) map[int]render.Line {
	plan := bottom.RowPlan
	if len(plan.Rows) == 0 {
		return nil
	}
	if width < 1 {
		width = 80
	}
	lines := make(map[int]render.Line, len(plan.Rows))
	band := cloneRenderLines(bottom.State.ActiveBandStyled)
	bandRows := 0
	for _, row := range plan.Rows {
		if row.Owner == renderengine.RowOwnerBand {
			bandRows++
		}
	}
	if len(band) > bandRows {
		band = band[len(band)-bandRows:]
	}
	bandIndex := 0
	for _, row := range plan.Rows {
		switch {
		case row.Owner == renderengine.RowOwnerBand:
			if bandIndex < len(band) {
				lines[row.Row] = cloneAppRenderLine(band[bandIndex])
			}
			bandIndex++
		case row.Owner == renderengine.RowOwnerStatus && row.Row == plan.StatusRow:
			lines[row.Row] = appStatusRenderLine(bottom.State.StatusModel, width, row.Text)
		case row.Owner == renderengine.RowOwnerStatus:
			lines[row.Row] = appStatusRenderLine(bottom.State.DynamicStatusModel, width, row.Text)
		}
	}
	return lines
}

func appStatusRenderLine(model *style.StatusLineModel, width int, fallback string) render.Line {
	if model == nil {
		return appPlainRenderLine(fallback)
	}
	document := style.StatusLineDocument(*model, width)
	for _, block := range document.Blocks {
		if len(block.Lines) > 0 {
			return cloneAppRenderLine(block.Lines[0])
		}
	}
	return appPlainRenderLine(fallback)
}

func appPlainRenderLine(text string) render.Line {
	if text == "" {
		return render.Line{}
	}
	return render.Line{Spans: []render.Span{{Text: text}}}
}

func cloneAppRenderLine(line render.Line) render.Line {
	clone := line
	clone.Spans = append([]render.Span(nil), line.Spans...)
	return clone
}

func cloneAppRenderCursor(cursor *AppCursor) *AppCursor {
	if cursor == nil {
		return nil
	}
	clone := *cursor
	return &clone
}
