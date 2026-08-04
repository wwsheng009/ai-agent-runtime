package ui

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// AppScreenRow is one row in the pure, plain-text screen-layout shadow. It is
// deliberately not a terminal cell row: it has no ANSI, front-buffer state,
// cursor movement, or write result. CellID and TranscriptGap preserve the
// semantic identity needed by later Compose and HistoryCommit stages.
type AppScreenRow struct {
	Row           int
	Owner         renderengine.RowOwner
	Text          string
	CellID        scene.CellID
	TranscriptGap bool
}

// AppScreenLayout combines the committed transcript tail and BottomPane row
// plan into one viewport-sized plain-text layout. It is a Phase 2 shadow
// artifact only: Presenter/TerminalSession must still create physical frames,
// ANSI diffs, and cursor writes in later phases.
//
// A mutable Active cell is deliberately excluded from Transcript rows. During
// the migration it is still displayed by the explicitly marked legacy band
// projection, so treating it as retained transcript here would duplicate a
// semantic range in a future full-screen Compose.
type AppScreenLayout struct {
	Revision                uint64
	LayoutGeneration        uint64
	Geometry                GeometryState
	Lease                   LeaseState
	Rows                    []AppScreenRow
	OutputBottomRow         int
	CursorFocus             BottomFocus
	Active                  ActiveCellState
	LegacyBandProjection    bool
	ActiveProjectionPending bool
}

// LayoutAppScreen derives a complete plain screen-row layout from one
// immutable AppState snapshot. It must never inspect FixedBottomSurface,
// ScreenModel, terminal state, or effect/projection progress.
func LayoutAppScreen(state AppState) AppScreenLayout {
	state = state.Clone()
	layout := LayoutAppState(state)
	height := layout.Geometry.Height
	result := AppScreenLayout{
		Revision:                layout.Revision,
		LayoutGeneration:        layout.LayoutGeneration,
		Geometry:                layout.Geometry,
		Lease:                   layout.Lease,
		CursorFocus:             layout.Bottom.CursorFocus,
		Active:                  layout.Active.Clone(),
		LegacyBandProjection:    layout.Bottom.LegacyBandProjection,
		ActiveProjectionPending: layout.Active.Phase != ActiveCellInactive,
	}
	if height < 1 {
		return result
	}

	width := layout.Geometry.Width
	if width < 1 {
		width = 80
	}
	result.Rows = makeAppScreenRows(height)
	result.OutputBottomRow = height
	for _, bottom := range layout.Bottom.RowPlan.Rows {
		if bottom.Row < 1 || bottom.Row > height {
			continue
		}
		result.Rows[bottom.Row-1] = AppScreenRow{
			Row:   bottom.Row,
			Owner: bottom.Owner,
			Text:  bottom.Text,
		}
	}
	if layout.Bottom.RowPlan.OutputBottomRow > 0 {
		result.OutputBottomRow = layout.Bottom.RowPlan.OutputBottomRow
	}
	if result.OutputBottomRow < 0 {
		result.OutputBottomRow = 0
	}
	if result.OutputBottomRow > height {
		result.OutputBottomRow = height
	}

	mutable := mutableTranscriptCellIDs(state.Transcript)
	transcript := layoutTranscriptScreenRows(layout.Transcript, mutable, width)
	if len(transcript) > result.OutputBottomRow {
		transcript = transcript[len(transcript)-result.OutputBottomRow:]
	}
	startRow := result.OutputBottomRow - len(transcript) + 1
	for index, row := range transcript {
		row.Row = startRow + index
		if row.Row < 1 || row.Row > result.OutputBottomRow {
			continue
		}
		result.Rows[row.Row-1] = row
	}
	return result
}

func makeAppScreenRows(height int) []AppScreenRow {
	rows := make([]AppScreenRow, height)
	for index := range rows {
		rows[index] = AppScreenRow{Row: index + 1, Owner: renderengine.RowOwnerGap}
	}
	return rows
}

func mutableTranscriptCellIDs(transcript TranscriptState) map[scene.CellID]struct{} {
	var ids map[scene.CellID]struct{}
	for _, cell := range transcript.Cells {
		if cell.Phase != scene.CellMutable {
			continue
		}
		if ids == nil {
			ids = make(map[scene.CellID]struct{})
		}
		ids[cell.ID] = struct{}{}
	}
	return ids
}

func layoutTranscriptScreenRows(rows []scene.LayoutRow, mutable map[scene.CellID]struct{}, width int) []AppScreenRow {
	if len(rows) == 0 {
		return nil
	}
	result := make([]AppScreenRow, 0, len(rows))
	for _, row := range rows {
		if _, excluded := mutable[row.CellID]; excluded {
			continue
		}
		if row.Gap > 0 {
			for count := 0; count < int(row.Gap); count++ {
				result = append(result, AppScreenRow{
					Owner:         renderengine.RowOwnerGap,
					CellID:        row.CellID,
					TranscriptGap: true,
				})
			}
			continue
		}
		for _, line := range wrapAppScreenText(row.Text, width) {
			result = append(result, AppScreenRow{
				Owner:  renderengine.RowOwnerTranscript,
				Text:   line,
				CellID: row.CellID,
			})
		}
	}
	return result
}

// wrapAppScreenText only expands a semantic source line to visible-width rows.
// It preserves internal and trailing spaces, unlike the bottom overlay's
// terminal-snapshot parity plan where trailing blank cells are intentionally
// omitted. Combining and directional-isolate runes have zero display width and
// stay attached to the current physical row.
func wrapAppScreenText(text string, width int) []string {
	if width < 1 || text == "" {
		return []string{text}
	}
	rows := make([]string, 0, 1)
	var line strings.Builder
	used := 0
	for _, r := range text {
		runeWidth := DisplayWidth(string(r))
		if runeWidth > 0 && used > 0 && used+runeWidth > width {
			rows = append(rows, line.String())
			line.Reset()
			used = 0
		}
		line.WriteRune(r)
		if runeWidth > 0 {
			used += runeWidth
		}
		if runeWidth > width {
			rows = append(rows, line.String())
			line.Reset()
			used = 0
		}
	}
	return append(rows, line.String())
}
