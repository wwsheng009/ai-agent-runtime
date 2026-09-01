package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestTranscriptReplacementInvalidatesAckedHistory_MutableCellGrowth
// verifies that the acked prefix of a still-mutable (streaming) cell is not
// invalidated when its source grows append-only, even if the Presentation
// changes as a result of the growth. Before the fix at controller_state.go:617,
// reflect.DeepEqual on the full Presentation would return false for every
// streaming delta, triggering a scrollback reset + full visible replay loop.
func TestTranscriptReplacementInvalidatesAckedHistory_MutableCellGrowth(t *testing.T) {
	cellID := scene.CellID(7)

	// Old transcript: active cell with source "hello", acked range covers it.
	oldCell := scene.TranscriptCell{
		ID:       cellID,
		Kind:     scene.KindAssistant,
		Phase:    scene.CellMutable,
		Source:   "hello",
		Revision: 1,
		Presentation: scene.TranscriptPresentation{
			Kind: scene.PresentationPlain,
		},
	}
	previous := TranscriptState{
		SceneID:  1,
		Revision: 1,
		Cells:    []scene.TranscriptCell{oldCell},
	}

	// New transcript: same cell with source grown to "hello world" (append-only).
	// The acked prefix "hello" (bytes 0..5) is unchanged. Presentation is
	// different (Kind changed) because the longer source renders differently.
	newCell := scene.TranscriptCell{
		ID:       cellID,
		Kind:     scene.KindAssistant,
		Phase:    scene.CellMutable,
		Source:   "hello world",
		Revision: 2,
		Presentation: scene.TranscriptPresentation{
			Kind: scene.PresentationAssistantMarkdown, // different from old
		},
	}
	next := TranscriptState{
		SceneID:  1,
		Revision: 2,
		Cells:    []scene.TranscriptCell{newCell},
	}

	// Build a ledger with one acked commit covering the "hello" prefix.
	ledger := NewHistoryCommitLedger()
	ledger.byToken[1] = HistoryCommitEntry{
		Commit: HistoryCommit{
			Token:       1,
			CellID:      cellID,
			SourceRange: SourceRange{Start: 0, End: 5}, // "hello"
		},
		State: HistoryCommitAcked,
	}

	effects := HistoryEffectQueueState{ledger: ledger}

	if transcriptReplacementInvalidatesAckedHistory(previous, next, effects) {
		t.Fatal("mutable cell append-only growth must NOT invalidate acked history; " +
			"scrollback reset would cause visible replay loop")
	}
}

// TestTranscriptReplacementInvalidatesAckedHistory_CommittedCellPresentation
// verifies that a committed cell's Presentation change alone does NOT
// invalidate acked history. Presentation is a rendering projection of the
// semantic source; the acked prefix bytes are verified through each commit's
// SourceRange. A theme change that affects rendering is handled by
// SetThemeContextAction (rebasePending), not by the transcript replacement
// path. This is consistent with the mutable cell and prefix fixes.
func TestTranscriptReplacementInvalidatesAckedHistory_CommittedCellPresentation(t *testing.T) {
	cellID := scene.CellID(7)

	oldCell := scene.TranscriptCell{
		ID:       cellID,
		Kind:     scene.KindAssistant,
		Phase:    scene.CellCommitted,
		Source:   "hello",
		Revision: 1,
		Presentation: scene.TranscriptPresentation{
			Kind: scene.PresentationPlain,
		},
	}
	previous := TranscriptState{
		SceneID:  1,
		Revision: 1,
		Cells:    []scene.TranscriptCell{oldCell},
	}

	newCell := scene.TranscriptCell{
		ID:       cellID,
		Kind:     scene.KindAssistant,
		Phase:    scene.CellCommitted,
		Source:   "hello",
		Revision: 2,
		Presentation: scene.TranscriptPresentation{
			Kind: scene.PresentationDocument, // different from old
		},
	}
	next := TranscriptState{
		SceneID:  1,
		Revision: 2,
		Cells:    []scene.TranscriptCell{newCell},
	}

	ledger := NewHistoryCommitLedger()
	ledger.byToken[1] = HistoryCommitEntry{
		Commit: HistoryCommit{
			Token:       1,
			CellID:      cellID,
			SourceRange: SourceRange{Start: 0, End: 5},
		},
		State: HistoryCommitAcked,
	}

	effects := HistoryEffectQueueState{ledger: ledger}

	if transcriptReplacementInvalidatesAckedHistory(previous, next, effects) {
		t.Fatal("committed cell Presentation change must NOT invalidate acked history; " +
			"source bytes are unchanged, only the rendering projection changed")
	}
}

// TestTranscriptReplacementInvalidatesAckedHistory_PrefixCorrection
// verifies that a genuine acked-prefix correction on a mutable cell still
// triggers reconciliation. The source-range byte comparison (controller_state.go:620-626)
// must catch this — the fix only skips Presentation comparison, not source bytes.
func TestTranscriptReplacementInvalidatesAckedHistory_PrefixCorrection(t *testing.T) {
	cellID := scene.CellID(7)

	oldCell := scene.TranscriptCell{
		ID:       cellID,
		Kind:     scene.KindAssistant,
		Phase:    scene.CellMutable,
		Source:   "hello world",
		Revision: 1,
		Presentation: scene.TranscriptPresentation{
			Kind: scene.PresentationPlain,
		},
	}
	previous := TranscriptState{
		SceneID:  1,
		Revision: 1,
		Cells:    []scene.TranscriptCell{oldCell},
	}

	// New transcript: the acked prefix "hello" (bytes 0..5) changed to "HELLO".
	newCell := scene.TranscriptCell{
		ID:       cellID,
		Kind:     scene.KindAssistant,
		Phase:    scene.CellMutable,
		Source:   "HELLO world",
		Revision: 2,
		Presentation: scene.TranscriptPresentation{
			Kind: scene.PresentationPlain,
		},
	}
	next := TranscriptState{
		SceneID:  1,
		Revision: 2,
		Cells:    []scene.TranscriptCell{newCell},
	}

	ledger := NewHistoryCommitLedger()
	ledger.byToken[1] = HistoryCommitEntry{
		Commit: HistoryCommit{
			Token:       1,
			CellID:      cellID,
			SourceRange: SourceRange{Start: 0, End: 5}, // "hello" vs "HELLO"
		},
		State: HistoryCommitAcked,
	}

	effects := HistoryEffectQueueState{ledger: ledger}

	if !transcriptReplacementInvalidatesAckedHistory(previous, next, effects) {
		t.Fatal("acked prefix bytes changed; must invalidate history")
	}
}
// TestTranscriptReplacementInvalidatesAckedHistory_PrefixPresentationChange
// covers the resume scenario where a transcript contains multiple finalized
// cells followed by a mutable active cell. If the runtime re-renders finalized
// cells' Presentation (e.g. theme fingerprint or Document reconstruction after
// resume) while their source bytes are unchanged, the prefix comparison must
// NOT invalidate already-acknowledged native scrollback. Before the fix at
// transcriptSemanticPrefixEqual, reflect.DeepEqual on Presentation returned
// false for every re-render, triggering a scrollback reset + full visible
// replay of the whole transcript on each streaming delta.
func TestTranscriptReplacementInvalidatesAckedHistory_PrefixPresentationChange(t *testing.T) {
	prefixCellID := scene.CellID(3)
	activeCellID := scene.CellID(7)

	prefixPresentation := scene.TranscriptPresentation{Kind: scene.PresentationPlain}
	changedPrefixPresentation := scene.TranscriptPresentation{Kind: scene.PresentationDocument} // re-render after resume

	// Old transcript: finalized prefix cell + mutable active cell.
	previous := TranscriptState{
		SceneID:  1,
		Revision: 10,
		Cells: []scene.TranscriptCell{
			{
				ID:           prefixCellID,
				Kind:         scene.KindUser,
				Phase:        scene.CellCommitted,
				Source:       "prefix",
				Revision:     1,
				Presentation: prefixPresentation,
			},
			{
				ID:           activeCellID,
				Kind:         scene.KindAssistant,
				Phase:        scene.CellMutable,
				Source:       "streaming",
				Revision:     5,
				Presentation: scene.TranscriptPresentation{Kind: scene.PresentationPlain},
			},
		},
	}

	// New transcript: same sources, same cells, but the finalized prefix cell's
	// Presentation differs (rendering projection only — source unchanged).
	next := TranscriptState{
		SceneID:  1,
		Revision: 11,
		Cells: []scene.TranscriptCell{
			{
				ID:           prefixCellID,
				Kind:         scene.KindUser,
				Phase:        scene.CellCommitted,
				Source:       "prefix",
				Revision:     1,
				Presentation: changedPrefixPresentation, // different Presentation, same Source
			},
			{
				ID:           activeCellID,
				Kind:         scene.KindAssistant,
				Phase:        scene.CellMutable,
				Source:       "streaming",
				Revision:     6,
				Presentation: scene.TranscriptPresentation{Kind: scene.PresentationPlain},
			},
		},
	}

	// Acked commit covering the finalized prefix cell's source.
	ledger := NewHistoryCommitLedger()
	ledger.byToken[1] = HistoryCommitEntry{
		Commit: HistoryCommit{
			Token:       1,
			CellID:      prefixCellID,
			SourceRange: SourceRange{Start: 0, End: 6}, // "prefix"
		},
		State: HistoryCommitAcked,
	}

	effects := HistoryEffectQueueState{ledger: ledger}

	if transcriptReplacementInvalidatesAckedHistory(previous, next, effects) {
		t.Fatal("finalized prefix Presentation re-render with unchanged source must NOT " +
			"invalidate acked history; scrollback reset would replay the whole transcript")
	}
}

// TestTranscriptReplacementInvalidatesAckedHistory_PrefixSourceChange verifies
// that a genuine source-byte change in the finalized prefix still invalidates
// acked history. The prefix comparison must keep catching real semantic
// insertions/corrections — the fix only drops the rendering-projection
// comparison, not the source identity check.
func TestTranscriptReplacementInvalidatesAckedHistory_PrefixSourceChange(t *testing.T) {
	prefixCellID := scene.CellID(3)
	activeCellID := scene.CellID(7)

	previous := TranscriptState{
		SceneID:  1,
		Revision: 10,
		Cells: []scene.TranscriptCell{
			{
				ID:           prefixCellID,
				Kind:         scene.KindUser,
				Phase:        scene.CellCommitted,
				Source:       "prefix",
				Revision:     1,
				Presentation: scene.TranscriptPresentation{Kind: scene.PresentationPlain},
			},
			{
				ID:           activeCellID,
				Kind:         scene.KindAssistant,
				Phase:        scene.CellMutable,
				Source:       "streaming",
				Revision:     5,
				Presentation: scene.TranscriptPresentation{Kind: scene.PresentationPlain},
			},
		},
	}

	// The finalized prefix cell's SOURCE changed within the acked range (0..6).
	next := TranscriptState{
		SceneID:  1,
		Revision: 11,
		Cells: []scene.TranscriptCell{
			{
				ID:           prefixCellID,
				Kind:         scene.KindUser,
				Phase:        scene.CellCommitted,
				Source:       "prefIX", // ^ changed at byte 4
				Revision:     2,
				Presentation: scene.TranscriptPresentation{Kind: scene.PresentationPlain},
			},
			{
				ID:           activeCellID,
				Kind:         scene.KindAssistant,
				Phase:        scene.CellMutable,
				Source:       "streaming",
				Revision:     6,
				Presentation: scene.TranscriptPresentation{Kind: scene.PresentationPlain},
			},
		},
	}

	ledger := NewHistoryCommitLedger()
	ledger.byToken[1] = HistoryCommitEntry{
		Commit: HistoryCommit{
			Token:       1,
			CellID:      prefixCellID,
			SourceRange: SourceRange{Start: 0, End: 6}, // "prefix" vs "prefix-..."
		},
		State: HistoryCommitAcked,
	}

	effects := HistoryEffectQueueState{ledger: ledger}

	if !transcriptReplacementInvalidatesAckedHistory(previous, next, effects) {
		t.Fatal("finalized prefix source change must invalidate acked history")
	}
}
