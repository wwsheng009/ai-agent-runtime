package ui

import (
	"errors"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// planEligibleHistoryCommits selects only complete finalized cells that lie
// above the retained primary transcript viewport. Keeping whole cells is a
// conservative migration rule: it avoids inventing partial source ranges from
// legacy line caches while still giving every effect stable semantic identity.
func planEligibleHistoryCommits(state AppState) []HistoryCommit {
	if state.Geometry.Width < 1 || state.Geometry.Height < 1 {
		return nil
	}
	layout := LayoutAppState(state)
	visibleRows := layout.Bottom.RowPlan.OutputBottomRow
	if visibleRows < 1 {
		return nil
	}
	width := layout.Geometry.Width
	if width < 1 {
		width = 80
	}
	// Commit eligibility is a physical display decision. Using semantic source
	// lines here would hand off a CJK/wrapped/tab-expanded cell while some of
	// its physical rows are still visible in the primary viewport.
	rows := layoutTranscriptScreenRows(layout.Transcript, mutableTranscriptCellIDs(state.Transcript), width)
	if len(rows) <= visibleRows {
		return nil
	}
	firstVisible := len(rows) - visibleRows
	byID := make(map[scene.CellID]scene.TranscriptCell, len(state.Transcript.Cells))
	for _, cell := range state.Transcript.Cells {
		byID[cell.ID] = cell
	}
	commits := make([]HistoryCommit, 0)
	for start := 0; start < len(rows); {
		cellID := rows[start].CellID
		end := start + 1
		for end < len(rows) && rows[end].CellID == cellID {
			end++
		}
		cell, found := byID[cellID]
		if found && cellIsFinalizedForHistory(cell) && cell.Source != "" && end <= firstVisible {
			lines := make([]render.Line, 0, end-start)
			for _, row := range rows[start:end] {
				// History handoff and the primary viewport must retain the
				// same semantic cell presentation. row.Text remains the
				// physical-layout parity field; appTranscriptRenderLine carries
				// the role-tagged render IR without deriving anything from a
				// legacy screen/history cache.
				lines = append(lines, appTranscriptRenderLine(row, byID))
			}
			commits = append(commits, HistoryCommit{
				CellID:           cell.ID,
				Revision:         cell.Revision,
				SourceRange:      SourceRange{Start: 0, End: len(cell.Source)},
				DisplayRange:     DisplayRange{Start: start, End: end},
				LayoutGeneration: state.LayoutGeneration,
				Lines:            lines,
			})
		}
		start = end
	}
	return commits
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
}

// historyCommitPresentationEqual compares every non-token field that can
// affect terminal bytes. Token is reducer-owned delivery identity and is
// intentionally omitted so a pending effect can retain its identity while its
// current-layout display payload is safely rebased before any write begins.
func historyCommitPresentationEqual(current, candidate HistoryCommit) bool {
	return current.CellID == candidate.CellID &&
		current.Revision == candidate.Revision &&
		current.SourceRange == candidate.SourceRange &&
		current.DisplayRange == candidate.DisplayRange &&
		current.LayoutGeneration == candidate.LayoutGeneration &&
		render.LinesEqual(current.Lines, candidate.Lines)
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
	}
}
