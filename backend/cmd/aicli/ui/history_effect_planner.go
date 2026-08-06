package ui

import (
	"errors"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// planEligibleHistoryCommits selects finalized display ranges above the retained
// primary transcript viewport. It keeps source/display identity explicit: plain
// cells are split at stable source-line boundaries while structured Markdown
// rows use a renderer fragment identity. A still-mutable active cell contributes
// its own overflow prefix so early rows cross the physical writer before the
// final event arrives.
func planEligibleHistoryCommits(state AppState) []HistoryCommit {
	if state.Geometry.Width < 1 || state.Geometry.Height < 1 {
		return nil
	}
	frontierCells, frontierActive := canonicalHistoryCommitFrontier(state)
	var activeCommits []HistoryCommit
	if state.SemanticActiveCellProjection && frontierActive {
		activeCommits = planMutableActiveCellHistoryCommitsWithTheme(state.Active, state.Geometry, state.LayoutGeneration, state.Theme)
	}
	layout := LayoutAppState(state)
	width := layout.Geometry.Width
	if width < 1 {
		width = 80
	}
	// Commit eligibility is a physical display decision. Using semantic source
	// lines here would hand off a CJK/wrapped/tab-expanded cell while some of
	// its physical rows are still visible in the primary viewport.
	byID := transcriptCellsByID(state.Transcript)
	rows := layoutTranscriptScreenRows(layout.Transcript, byID, mutableTranscriptCellIDs(state.Transcript), width, state.Theme)
	if len(rows) == 0 {
		return activeCommits
	}
	// The primary frame now owns only the mutable/bottom inline viewport.
	// Finalized transcript rows all belong to native terminal history; retaining
	// a screen-sized transcript tail here would make those rows disappear as
	// soon as the viewport-only presenter stops repainting the old whole frame.
	firstVisible := len(rows)
	commits := make([]HistoryCommit, 0)
	for start := 0; start < len(rows); {
		cellID := rows[start].CellID
		end := start + 1
		for end < len(rows) && rows[end].CellID == cellID {
			end++
		}
		cell, found := byID[cellID]
		_, beforeFrontier := frontierCells[cellID]
		if found && beforeFrontier && cellIsFinalizedForHistory(cell) && cell.Source != "" {
			skipRows := activeAckedRenderedPrefixRows(state.HistoryEffects, cellID, rows[start:end], byID)
			if cellUsesStructuredPresentation(cell) {
				commits = append(commits, planMarkdownCellHistoryCommits(cell, rows[start:end], start, firstVisible, skipRows, state.LayoutGeneration, byID)...)
			} else if segments, mapped := planPlainCellHistoryCommits(cell, rows[start:end], start, firstVisible, skipRows, width, state.LayoutGeneration, byID); mapped {
				commits = append(commits, segments...)
			} else if skipRows == 0 && end <= firstVisible {
				commits = append(commits, wholeCellHistoryCommit(cell, rows[start:end], start, end, state.LayoutGeneration, byID))
			}
		}
		start = end
	}
	return append(commits, activeCommits...)
}

// canonicalHistoryCommitFrontier enforces the transcript's single physical
// ordering frontier. Only the contiguous finalized prefix may enter native
// history. The first mutable cell is a barrier; no later finalized or active
// cell may cross it. A mutable active cell may hand off overflow only when it
// is exactly that first barrier.
func canonicalHistoryCommitFrontier(state AppState) (map[scene.CellID]struct{}, bool) {
	eligible := make(map[scene.CellID]struct{})
	for _, cell := range state.Transcript.Cells {
		if !cellIsFinalizedForHistory(cell) {
			return eligible, state.Active.Phase == ActiveCellMutable &&
				state.Active.CellID == cell.ID && !state.Active.HistoryCommitBlocked
		}
		if cell.Source == "" {
			continue
		}
		eligible[cell.ID] = struct{}{}
	}
	return eligible, state.Active.Phase == ActiveCellMutable &&
		state.Active.CellID != 0 && !state.Active.HistoryCommitBlocked
}

// activeAckedRenderedPrefixRows proves how many leading finalized rows already
// crossed the terminal while the same cell was mutable. History planning uses
// the retained structured payload, not text hashes, and only accepts a
// contiguous source prefix whose lines still match the finalized projection.
func activeAckedRenderedPrefixRows(effects HistoryEffectQueueState, cellID scene.CellID, rows []AppScreenRow, byID map[scene.CellID]scene.TranscriptCell) int {
	frontier := 0
	delivered := make([]render.Line, 0)
	for _, entry := range effects.Entries() {
		commit := entry.Commit
		if entry.State != HistoryCommitAcked || commit.Origin != HistoryCommitActive ||
			commit.CellID != cellID || commit.SourceRange.Start > frontier ||
			commit.SourceRange.End <= frontier || len(commit.Lines) == 0 {
			continue
		}
		frontier = commit.SourceRange.End
		delivered = append(delivered, commit.Lines...)
	}
	if frontier == 0 || len(delivered) == 0 {
		return 0
	}
	matched := 0
	for _, row := range rows {
		if row.TranscriptGap {
			continue
		}
		if matched >= len(delivered) || !historyRenderLineEquivalent(delivered[matched], appTranscriptRenderLine(row, byID)) {
			break
		}
		matched++
	}
	if matched != len(delivered) {
		return 0
	}
	return matched
}

func historyRenderLineEquivalent(left, right render.Line) bool {
	if render.LinesEqual([]render.Line{left}, []render.Line{right}) {
		return true
	}
	// Empty source rows may be represented either as an empty structured line
	// or as an empty role span. They produce identical terminal bytes.
	return renderLineText(left) == "" && renderLineText(right) == ""
}

func renderLineText(line render.Line) string {
	var text string
	for _, span := range line.Spans {
		text += span.Text
	}
	return text
}

// planMutableActiveCellHistoryCommits hands off the stable prefix of a still
// mutable cell whose rendered body overflows the adaptive band budget.
// The active projection keeps only the viewport tail (ProjectActiveCellBand);
// every row above it must already have crossed the physical writer so an
// interrupted stream never loses earlier output. Each commit maps one physical
// row back to the exact half-open source range that produced it, mirroring
// planPlainCellHistoryCommits. Markdown is committed as one structured stable
// source fragment because its rendered rows are not byte-bijective.
func planMutableActiveCellHistoryCommits(active ActiveCellState, geometry GeometryState, generation uint64) []HistoryCommit {
	return planMutableActiveCellHistoryCommitsWithTheme(active, geometry, generation, style.ThemeContext{})
}

func planMutableActiveCellHistoryCommitsWithTheme(active ActiveCellState, geometry GeometryState, generation uint64, theme style.ThemeContext) []HistoryCommit {
	if active.Phase != ActiveCellMutable || active.CellID == 0 || active.HistoryCommitBlocked ||
		(active.Kind != scene.KindAssistant && active.Kind != scene.KindSupplement) || active.Source == "" ||
		geometry.Width < 1 || geometry.Height < 1 {
		return nil
	}
	if active.Kind == scene.KindAssistant && markdown.LooksLikeMarkdown(active.Source) {
		return planMutableMarkdownHistoryCommit(active, geometry, generation, theme)
	}
	start := active.Acked.End
	if start < 0 || start > len(active.Source) || !activeCellSourceBoundary(active.Source, start) {
		return nil
	}
	if start == len(active.Source) {
		return nil
	}
	width := geometry.Width
	rows := activeCellBandRows(active.Source[start:], width)
	maxRows := ActiveBandRows(geometry.Height)
	if len(rows) <= maxRows {
		return nil
	}
	firstVisible := len(rows) - maxRows

	role := appTranscriptRenderRole(active.Kind)
	lineRanges := sourceLineRanges(active.Source[start:])
	commits := make([]HistoryCommit, 0, firstVisible)
	displayRow := 0
	for _, line := range lineRanges {
		absolute := sourceLineRange{
			Source: SourceRange{Start: line.Source.Start + start, End: line.Source.End + start},
			Text:   line.Text,
		}
		if line.Text == "" {
			// Blank lines (including the trailing newline of a streamed body)
			// wrap to exactly one empty physical row. handle them explicitly:
			// wrapPlainAppScreenText deliberately declines empty input.
			if displayRow >= firstVisible {
				break
			}
			if rows[displayRow] != "" {
				return nil
			}
			commits = append(commits, HistoryCommit{
				Origin:           HistoryCommitActive,
				CellID:           active.CellID,
				Revision:         active.Revision,
				SourceRange:      absolute.Source,
				DisplayRange:     DisplayRange{Start: displayRow, End: displayRow + 1},
				LayoutGeneration: generation,
				Lines: []render.Line{{
					Spans: []render.Span{{Text: "", Style: render.Style{Role: string(role)}}},
				}},
			})
			displayRow++
			continue
		}
		wrapped, sourceRows, mapped := plainWrappedSourceRanges(absolute, width)
		if !mapped || len(wrapped) == 0 || len(sourceRows) != len(wrapped) {
			return nil
		}
		if displayRow >= firstVisible {
			break
		}
		commitRows := len(wrapped)
		if remaining := firstVisible - displayRow; commitRows > remaining {
			commitRows = remaining
		}
		for offset, text := range wrapped[:commitRows] {
			if rows[displayRow+offset] != text {
				return nil
			}
			sourceRange := sourceRows[offset]
			if sourceRange.End > active.Stable.End {
				return nil
			}
			commits = append(commits, HistoryCommit{
				Origin:           HistoryCommitActive,
				CellID:           active.CellID,
				Revision:         active.Revision,
				SourceRange:      sourceRange,
				DisplayRange:     DisplayRange{Start: displayRow + offset, End: displayRow + offset + 1},
				LayoutGeneration: generation,
				Lines: []render.Line{{
					Spans: []render.Span{{Text: text, Style: render.Style{Role: string(role)}}},
				}},
			})
		}
		displayRow += commitRows
		if commitRows < len(wrapped) {
			break
		}
	}
	if displayRow != firstVisible || len(commits) == 0 {
		return nil
	}
	return commits
}

func planMutableMarkdownHistoryCommit(active ActiveCellState, geometry GeometryState, generation uint64, theme style.ThemeContext) []HistoryCommit {
	start, stableEnd := active.Acked.End, active.Stable.End
	if start < 0 || stableEnd <= start || stableEnd > len(active.Source) ||
		!activeCellSourceBoundary(active.Source, start) ||
		!activeCellSourceBoundary(active.Source, stableEnd) {
		return nil
	}
	live, ok := activeMarkdownSuffixLines(active.Source, start, geometry.Width, theme)
	if !ok || len(live) <= ActiveBandRows(geometry.Height) {
		return nil
	}
	maxCommitRows := len(live) - ActiveBandRows(geometry.Height)
	commitEnd := active.Enqueued.End
	for _, line := range sourceLineRanges(active.Source[start:stableEnd]) {
		candidateEnd := start + line.Source.End
		if candidateEnd <= commitEnd || candidateEnd > stableEnd {
			continue
		}
		candidateLines, projected := activeMarkdownSuffixLines(active.Source[:candidateEnd], start, geometry.Width, theme)
		if !projected || len(candidateLines) == 0 || len(candidateLines) > maxCommitRows ||
			!render.LinesEqual(candidateLines, live[:len(candidateLines)]) {
			continue
		}
		commitEnd = candidateEnd
	}
	if commitEnd <= start {
		return nil
	}
	boundaries := make([]int, 0, 2)
	if active.Enqueued.End > start && active.Enqueued.End <= commitEnd {
		boundaries = append(boundaries, active.Enqueued.End)
	}
	if commitEnd > active.Enqueued.End {
		boundaries = append(boundaries, commitEnd)
	}
	commits := make([]HistoryCommit, 0, len(boundaries))
	sourceStart, displayStart := start, 0
	for _, sourceEnd := range boundaries {
		lines, projected := activeMarkdownSuffixLines(active.Source[:sourceEnd], sourceStart, geometry.Width, theme)
		if !projected || len(lines) == 0 || displayStart+len(lines) > len(live) ||
			!render.LinesEqual(lines, live[displayStart:displayStart+len(lines)]) {
			return nil
		}
		commits = append(commits, HistoryCommit{
			Origin:           HistoryCommitActive,
			CellID:           active.CellID,
			Revision:         active.Revision,
			SourceRange:      SourceRange{Start: sourceStart, End: sourceEnd},
			FragmentID:       uint64(sourceStart) + 1,
			DisplayRange:     DisplayRange{Start: displayStart, End: displayStart + len(lines)},
			LayoutGeneration: generation,
			Lines:            lines,
		})
		sourceStart = sourceEnd
		displayStart += len(lines)
	}
	return commits
}

// planMarkdownCellHistoryCommits hands off hidden rich-rendered rows one at a
// time. Markdown transformations are not source-byte bijective, so every row
// keeps the enclosing immutable source range and receives a stable renderer
// fragment ordinal. The terminal payload remains the renderer's structured
// line, never the raw Markdown source.
func planMarkdownCellHistoryCommits(cell scene.TranscriptCell, rows []AppScreenRow, displayStart, firstVisible, skipRows int, generation uint64, byID map[scene.CellID]scene.TranscriptCell) []HistoryCommit {
	commits := make([]HistoryCommit, 0, len(rows))
	fragmentID := uint64(0)
	for index, row := range rows {
		if row.TranscriptGap {
			continue
		}
		fragmentID++
		if int(fragmentID) <= skipRows {
			continue
		}
		end := index + 1
		if displayStart+end > firstVisible {
			continue
		}
		start := index
		if fragmentID == 1 {
			// A leading cell-boundary gap belongs to the first rendered row.
			start = 0
		}
		lines := make([]render.Line, 0, end-start)
		for _, displayRow := range rows[start:end] {
			lines = append(lines, appTranscriptRenderLine(displayRow, byID))
		}
		commits = append(commits, HistoryCommit{
			CellID:           cell.ID,
			Revision:         cell.Revision,
			SourceRange:      SourceRange{Start: 0, End: len(cell.Source)},
			FragmentID:       fragmentID,
			DisplayRange:     DisplayRange{Start: displayStart + start, End: displayStart + end},
			LayoutGeneration: generation,
			Lines:            lines,
		})
	}
	return commits
}

// wholeCellHistoryCommit is the conservative fallback for structured rendering
// and unusual source that cannot be bijectively related to plain source lines.
// It is only eligible when no row from the cell remains in the primary frame.
func wholeCellHistoryCommit(cell scene.TranscriptCell, rows []AppScreenRow, start, end int, generation uint64, byID map[scene.CellID]scene.TranscriptCell) HistoryCommit {
	lines := make([]render.Line, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, appTranscriptRenderLine(row, byID))
	}
	return HistoryCommit{
		CellID:           cell.ID,
		Revision:         cell.Revision,
		SourceRange:      SourceRange{Start: 0, End: len(cell.Source)},
		DisplayRange:     DisplayRange{Start: start, End: end},
		LayoutGeneration: generation,
		Lines:            lines,
	}
}

// sourceLineRange is a non-overlapping source range for one logical source
// line. Newlines belong to the preceding line so ranges cover every byte once.
// A final empty source line has a zero-width range. The finalized plain planner
// maps that display row back to the terminating newline byte and gives it a
// fragment identity, because HistoryCommit deliberately rejects empty ranges.
type sourceLineRange struct {
	Source SourceRange
	Text   string
}

func sourceLineRanges(source string) []sourceLineRange {
	if source == "" {
		return nil
	}
	ranges := make([]sourceLineRange, 0, 1)
	start := 0
	for index := 0; index < len(source); index++ {
		if source[index] != '\n' {
			continue
		}
		ranges = append(ranges, sourceLineRange{
			Source: SourceRange{Start: start, End: index + 1},
			Text:   source[start:index],
		})
		start = index + 1
	}
	ranges = append(ranges, sourceLineRange{
		Source: SourceRange{Start: start, End: len(source)},
		Text:   source[start:],
	})
	return ranges
}

// planPlainCellHistoryCommits maps every finalized plain display row back to a
// stable source fragment. A source line can wrap across the primary boundary;
// committing it only as one unit leaves its hidden prefix nowhere to browse.
// Per-row fragments make the handoff continuous without duplicating already
// delivered source when later rows of the same long line become eligible.
//
// Markdown is handled by planMarkdownCellHistoryCommits because its renderer
// can add/remove physical rows and therefore has no source-byte fragment map.
func planPlainCellHistoryCommits(cell scene.TranscriptCell, rows []AppScreenRow, displayStart, firstVisible, skipRows, width int, generation uint64, byID map[scene.CellID]scene.TranscriptCell) ([]HistoryCommit, bool) {
	lineRanges := sourceLineRanges(cell.Source)
	if len(lineRanges) == 0 {
		return nil, false
	}

	row := 0
	leadingGaps := 0
	for leadingGaps < len(rows) && rows[leadingGaps].TranscriptGap {
		leadingGaps++
	}
	row = leadingGaps
	commits := make([]HistoryCommit, 0, len(rows))
	contentRow := 0
	for index, sourceLine := range lineRanges {
		var (
			wrapped     []string
			sourceRows  []SourceRange
			fragmentIDs []uint64
			mapped      bool
		)
		if sourceLine.Text == "" {
			// The fast plain wrapper deliberately declines empty input, but an
			// internal blank line is still one physical row owned by its newline.
			// A trailing empty line has no bytes of its own, so retain the final
			// newline as its source and distinguish the second presentation row
			// with a stable non-zero fragment identity.
			var sourceRange SourceRange
			var fragmentID uint64
			sourceRange, fragmentID, mapped = plainBlankSourceIdentity(cell.Source, sourceLine)
			if mapped {
				wrapped = []string{""}
				sourceRows = []SourceRange{sourceRange}
				fragmentIDs = []uint64{fragmentID}
			}
		} else {
			wrapped, sourceRows, mapped = plainWrappedSourceRanges(sourceLine, width)
			fragmentIDs = make([]uint64, len(sourceRows))
		}
		if !mapped || len(wrapped) == 0 || row+len(wrapped) > len(rows) {
			return nil, false
		}
		for offset, text := range wrapped {
			if rows[row+offset].TranscriptGap || rows[row+offset].Text != text || len(rows[row+offset].RenderLine.Spans) != 0 {
				return nil, false
			}
		}

		for offset, sourceRange := range sourceRows {
			contentRow++
			if contentRow <= skipRows {
				continue
			}
			start := row + offset
			if index == 0 && offset == 0 {
				// A cell boundary gap is a display artifact belonging to the
				// first source row, not an independent zero-width effect.
				start = 0
			}
			end := row + offset + 1
			if displayStart+end > firstVisible {
				continue
			}
			lines := make([]render.Line, 0, end-start)
			for _, displayRow := range rows[start:end] {
				lines = append(lines, appTranscriptRenderLine(displayRow, byID))
			}
			commits = append(commits, HistoryCommit{
				CellID:           cell.ID,
				Revision:         cell.Revision,
				SourceRange:      sourceRange,
				FragmentID:       fragmentIDs[offset],
				DisplayRange:     DisplayRange{Start: displayStart + start, End: displayStart + end},
				LayoutGeneration: generation,
				Lines:            lines,
			})
		}
		row += len(wrapped)
	}
	if row != len(rows) {
		return nil, false
	}
	return commits, true
}

func plainBlankSourceIdentity(source string, line sourceLineRange) (SourceRange, uint64, bool) {
	if line.Text != "" || !line.Source.Valid() || line.Source.End > len(source) {
		return SourceRange{}, 0, false
	}
	if line.Source.End > line.Source.Start {
		return line.Source, 0, true
	}
	end := line.Source.End
	if end == 0 || end != len(source) || source[end-1] != '\n' {
		return SourceRange{}, 0, false
	}
	return SourceRange{Start: end - 1, End: end}, uint64(end) + 1, true
}

// plainWrappedSourceRanges returns the same rows as wrapAppScreenText together
// with the exact half-open source range responsible for each row. It accepts
// only the source-preserving wrapper path. Control-sequence/tab cases continue
// to use the conservative whole-cell fallback because their terminal state is
// not bijective with source bytes.
func plainWrappedSourceRanges(sourceLine sourceLineRange, width int) ([]string, []SourceRange, bool) {
	wrapped, ok := wrapPlainAppScreenText(sourceLine.Text, width)
	if !ok || len(wrapped) == 0 {
		return nil, nil, false
	}
	if width < 1 {
		width = 80
	}
	ranges := make([]SourceRange, 0, len(wrapped))
	start := 0
	used := 0
	for offset, value := range sourceLine.Text {
		glyphWidth := render.Width(string(value))
		if glyphWidth == 0 {
			if offset == start && used == 0 {
				return nil, nil, false
			}
			continue
		}
		if glyphWidth > width {
			return nil, nil, false
		}
		if used > 0 && used+glyphWidth > width {
			ranges = append(ranges, SourceRange{Start: sourceLine.Source.Start + start, End: sourceLine.Source.Start + offset})
			start = offset
			used = 0
		}
		used += glyphWidth
	}
	ranges = append(ranges, SourceRange{Start: sourceLine.Source.Start + start, End: sourceLine.Source.End})
	if len(ranges) != len(wrapped) {
		return nil, nil, false
	}
	for _, sourceRange := range ranges {
		if sourceRange.End <= sourceRange.Start {
			return nil, nil, false
		}
	}
	return wrapped, ranges, true
}

func cellIsFinalizedForHistory(cell scene.TranscriptCell) bool {
	switch cell.Phase {
	case scene.CellCommitted, scene.CellPartiallyHandedOff, scene.CellHandedOff:
		return true
	default:
		return false
	}
}

// syncHistoryEffectsForTranscript is invoked only by semantic transcript
// transitions. Geometry changes rebase existing pending payloads separately;
// they never mint a new token merely because the viewport resized.
func syncHistoryEffectsForTranscript(state *UIControllerState) {
	if state == nil {
		return
	}
	candidates := planEligibleHistoryCommits(state.AppState)
	valid := make(map[historyCommitSourceKey]HistoryCommit, len(candidates))
	for _, candidate := range candidates {
		valid[historyCommitSourceIdentity(candidate)] = candidate
	}
	if ledger := state.HistoryEffects.ledger; ledger != nil {
		for _, entry := range ledger.byToken {
			candidate, exists := valid[historyCommitSourceIdentity(entry.Commit)]
			switch entry.State {
			case HistoryCommitPending:
				if !exists {
					_ = state.HistoryEffects.invalidate(entry.Commit.Token)
					continue
				}
				// A semantic snapshot can retain the same cell source while
				// changing a preceding boundary/gap. The token remains the
				// same unstarted effect, but its display payload must be rebased
				// before a presenter can write the old physical rows.
				if !historyCommitPresentationEqual(entry.Commit, candidate) {
					if err := ledger.RebasePending(entry.Commit.Token, candidate); err != nil {
						state.HistoryEffects.ProjectionUnknown = true
					}
				}
			case HistoryCommitInFlight:
				// Once a terminal transaction was claimed, a changed display
				// payload may already be partially written. Never let its old
				// acknowledgement prove delivery for the new semantic layout.
				if !exists || !historyCommitPresentationEqual(entry.Commit, candidate) {
					_ = state.HistoryEffects.invalidate(entry.Commit.Token)
				}
			}
		}
	}
	for _, candidate := range candidates {
		if state.HistoryEffects.hasTerminalRecordForSource(candidate) {
			continue
		}
		if err := state.HistoryEffects.enqueue(candidate); err != nil &&
			!errors.Is(err, ErrDuplicateCommitRange) {
			state.HistoryEffects.ProjectionUnknown = true
		}
	}
	advanceActiveCellEnqueuedFromEffects(state)
}

// historyCommitPresentationEqual compares every non-token field that can
// affect terminal bytes. Token is reducer-owned delivery identity and is
// intentionally omitted so a pending effect can retain its identity while its
// current-layout display payload is safely rebased before any write begins.
func historyCommitPresentationEqual(current, candidate HistoryCommit) bool {
	sameRevision := current.Revision == candidate.Revision ||
		(current.Origin == HistoryCommitActive && candidate.Origin == HistoryCommitActive)
	return current.Origin == candidate.Origin &&
		current.CellID == candidate.CellID && sameRevision &&
		current.SourceRange == candidate.SourceRange &&
		current.FragmentID == candidate.FragmentID &&
		current.DisplayRange == candidate.DisplayRange &&
		current.LayoutGeneration == candidate.LayoutGeneration &&
		render.LinesEqual(current.Lines, candidate.Lines)
}

func advanceActiveCellEnqueuedFromEffects(state *UIControllerState) {
	if state == nil || state.Active.Phase != ActiveCellMutable || state.Active.CellID == 0 ||
		state.HistoryEffects.ledger == nil {
		return
	}
	frontier := state.Active.Enqueued.End
	for {
		advanced := false
		for _, entry := range state.HistoryEffects.Entries() {
			commit := entry.Commit
			if commit.Origin != HistoryCommitActive || commit.CellID != state.Active.CellID ||
				commit.SourceRange.Start > frontier || commit.SourceRange.End <= frontier ||
				commit.SourceRange.End > state.Active.Stable.End {
				continue
			}
			switch entry.State {
			case HistoryCommitPending, HistoryCommitInFlight, HistoryCommitAcked, HistoryCommitStateFailed:
				frontier = commit.SourceRange.End
				advanced = true
			}
		}
		if !advanced {
			break
		}
	}
	if frontier <= state.Active.Enqueued.End {
		return
	}
	if next, err := MarkActiveEnqueued(state.Active, frontier); err == nil {
		state.Active = next
	}
}

func rebasePendingHistoryEffects(state *UIControllerState) {
	if state == nil {
		return
	}
	candidates := planEligibleHistoryCommits(state.AppState)
	valid := make(map[historyCommitSourceKey]struct{}, len(candidates))
	for _, candidate := range candidates {
		valid[historyCommitSourceIdentity(candidate)] = struct{}{}
	}
	if ledger := state.HistoryEffects.ledger; ledger != nil {
		for _, entry := range ledger.byToken {
			switch entry.State {
			case HistoryCommitPending:
				if _, exists := valid[historyCommitSourceIdentity(entry.Commit)]; !exists {
					_ = state.HistoryEffects.invalidate(entry.Commit.Token)
				}
			case HistoryCommitInFlight:
				// A terminal transaction is bound to the viewport generation it
				// started with. A resize can race the write after Begin; preserve
				// its token as invalidated and force projection recovery rather than
				// accepting a stale acknowledgement or repainting around it.
				_ = state.HistoryEffects.invalidate(entry.Commit.Token)
			}
		}
	}
	for _, candidate := range candidates {
		if err := state.HistoryEffects.rebasePending(candidate); err != nil &&
			!errors.Is(err, ErrCommitNotPending) {
			state.HistoryEffects.ProjectionUnknown = true
		}
		if candidate.Origin == HistoryCommitActive &&
			!state.HistoryEffects.hasTerminalRecordForSource(candidate) {
			if err := state.HistoryEffects.enqueue(candidate); err != nil &&
				!errors.Is(err, ErrDuplicateCommitRange) {
				state.HistoryEffects.ProjectionUnknown = true
			}
		}
	}
	advanceActiveCellEnqueuedFromEffects(state)
}
