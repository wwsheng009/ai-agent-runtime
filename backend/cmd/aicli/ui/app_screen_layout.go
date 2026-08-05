package ui

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
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
	ActiveBand              ActiveBandProjection
	LegacyBandProjection    bool
	ActiveProjectionPending bool
	// bottom is retained so Compose can derive cursor intent without running
	// the full bottom-pane layout a second time.
	bottom BottomPaneLayout
}

// LayoutAppScreen derives a complete plain screen-row layout from one
// immutable AppState snapshot. It must never inspect FixedBottomSurface,
// ScreenModel, terminal state, or effect/projection progress.
func LayoutAppScreen(state AppState) AppScreenLayout {
	layout := LayoutAppState(state)
	height := layout.Geometry.Height
	result := AppScreenLayout{
		Revision:                layout.Revision,
		LayoutGeneration:        layout.LayoutGeneration,
		Geometry:                layout.Geometry,
		Lease:                   layout.Lease,
		CursorFocus:             layout.Bottom.CursorFocus,
		Active:                  layout.Active.Clone(),
		ActiveBand:              layout.ActiveBand.Clone(),
		LegacyBandProjection:    layout.Bottom.LegacyBandProjection,
		ActiveProjectionPending: layout.Active.Phase != ActiveCellInactive,
		bottom:                  layout.Bottom,
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
	// Zero is a valid output boundary when the entire one-row terminal is
	// occupied by the fixed status reserve.
	result.OutputBottomRow = layout.Bottom.RowPlan.OutputBottomRow
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
					// A semantic boundary is an empty transcript row, not
					// unowned bottom-pane headroom. Keeping its physical owner
					// as Transcript matches the existing owned viewport while
					// TranscriptGap retains the semantic distinction needed by
					// later HistoryCommit handling.
					Owner:         renderengine.RowOwnerTranscript,
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

// wrapAppScreenText expands one semantic source line with the same pure VT
// model used by the legacy owned viewport. This avoids a second, subtly
// incompatible width algorithm for deferred wrap, wide runes, leading
// combining marks, tab stops, and SGR/control-sequence parsing. It remains a
// pure in-memory operation: no live terminal, surface, or projection cache is
// read. Trailing blank cells are omitted because AppScreenRow.Text represents
// visible glyphs rather than a terminal-cell buffer.
func wrapAppScreenText(text string, width int) []string {
	if width < 1 {
		width = 80
	}
	if rows, ok := wrapPlainAppScreenText(text, width); ok {
		return rows
	}
	return wrapVTAppScreenText(text, width)
}

// wrapPlainAppScreenText handles the overwhelmingly common transcript case
// without constructing a width-by-height VT cell matrix. It deliberately
// declines control sequences, tabs, and runes wider than the viewport because
// those cases depend on the full VT state machine below.
func wrapPlainAppScreenText(text string, width int) ([]string, bool) {
	if text == "" {
		return nil, false
	}
	if isPlainASCII(text) {
		rows := make([]string, 0, (len(text)+width-1)/width)
		for start := 0; start < len(text); start += width {
			end := start + width
			if end > len(text) {
				end = len(text)
			}
			rows = append(rows, text[start:end])
		}
		return rows, true
	}

	rows := make([]string, 0, 1)
	line := strings.Builder{}
	used := 0
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return nil, false
		}
		runeWidth := render.Width(string(r))
		if runeWidth > width {
			return nil, false
		}
		if runeWidth == 0 {
			// VT drops a leading combining mark because there is no cell to
			// attach it to. Otherwise the mark remains part of the prior cell.
			if line.Len() > 0 {
				line.WriteRune(r)
			}
			continue
		}
		if used > 0 && used+runeWidth > width {
			rows = append(rows, line.String())
			line = strings.Builder{}
			used = 0
		}
		line.WriteRune(r)
		used += runeWidth
	}
	if line.Len() > 0 || len(rows) == 0 {
		rows = append(rows, line.String())
	}
	return rows, true
}

func isPlainASCII(text string) bool {
	for index := 0; index < len(text); index++ {
		if text[index] < 0x20 || text[index] > 0x7e {
			return false
		}
	}
	return true
}

func wrapVTAppScreenText(text string, width int) []string {
	// This is only a scratch-screen capacity estimate. VT remains the sole
	// physical-row expansion rule. Size by display columns rather than source
	// rune count so a long ordinary line does not allocate width*runeCount
	// cells.
	screen := vt.NewScreen(width, appScreenScratchHeight(text, width))
	screen.Feed(text)
	screen.Feed("\r\n")
	end := screen.CursorRow() - 1
	if end < 1 {
		return nil
	}
	return screen.Lines(1, end)
}

func appScreenScratchHeight(text string, width int) int {
	if width < 1 {
		width = 80
	}
	displayWidth := DisplayWidth(text)
	if displayWidth < 1 {
		displayWidth = 1
	}
	return (displayWidth+width-1)/width + 2
}

// appScreenCellRowText converts a terminal-cell row into the plain screen
// shadow. Unlike cellRowPlainText, blank cells before or between glyphs are
// materialized as spaces because they carry visible column position (for
// example a source indentation, tab stop, or cursor-relative overwrite).
// Trailing blanks remain omitted because the row is not a fixed-width cell
// buffer.
func appScreenCellRowText(cells []vt.Cell) string {
	var text strings.Builder
	for _, cell := range cells {
		if cell.Cont {
			continue
		}
		if cell.Text == "" {
			text.WriteByte(' ')
			continue
		}
		text.WriteString(cell.Text)
	}
	return strings.TrimRight(text.String(), " ")
}
