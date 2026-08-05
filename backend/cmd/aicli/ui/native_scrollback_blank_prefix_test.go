package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func TestStreamingFinalizePlansTailAfterBlankSourceRows(t *testing.T) {
	const width, height = 100, 24
	lines := []string{
		"Terminal scrollback keeps completed rows in the host buffer.",
		"",
		"BLANK-PREFIX-REASONING-SENTINEL",
		"",
	}
	markers := make([]string, 40)
	for index := range markers {
		markers[index] = fmt.Sprintf("BLANK-PREFIX-FINAL-%02d terminal history validation", index+1)
		lines = append(lines, markers[index])
	}

	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: width, Height: height, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		ShowPromptAction{Line: "> "},
	)

	var source string
	for index, line := range lines {
		if index > 0 {
			source += "\n"
		}
		source += line
		cell := &scene.TranscriptCell{
			ID: 92, Revision: uint64(index + 1), Kind: scene.KindAssistant,
			Source: source, Phase: scene.CellMutable,
		}
		if index == 0 {
			h.post(t, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
				Revision: uint64(index + 1), Cells: []*scene.TranscriptCell{cell},
			}})
		} else {
			current := h.controller.State().Active
			next := current
			next.Revision++
			next.Source = source
			h.post(t,
				UpdateActiveCellAction{ExpectedCellID: current.CellID, ExpectedRevision: current.Revision, Active: next},
				ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
					Revision: uint64(index + 1), Cells: []*scene.TranscriptCell{cell},
				}},
			)
		}
		h.flush()
	}

	streaming := h.controller.State()
	if streaming.Active.Acked.End == 0 || streaming.Active.Acked.End >= len(source) {
		t.Fatalf("fixture did not retain a resident suffix: %+v", streaming.Active)
	}
	finalCell := &scene.TranscriptCell{
		ID: 92, Revision: uint64(len(lines) + 1), Kind: scene.KindAssistant,
		Source: source, Phase: scene.CellCommitted,
	}
	h.post(t, FinalizeActiveCellAction{
		Snapshot:                &scene.Snapshot{Revision: uint64(len(lines) + 1), Cells: []*scene.TranscriptCell{finalCell}},
		ExpectedActiveCellID:    92,
		ExpectedActiveRevision:  streaming.Active.Revision,
		ExpectedSceneRevision:   uint64(len(lines) + 1),
		ExpectedActiveKind:      scene.KindAssistant,
		ExpectedActiveKindKnown: true,
	})

	pendingTail := false
	for _, entry := range h.controller.State().HistoryEffects.Entries() {
		if entry.State == HistoryCommitPending && entry.Commit.Origin == HistoryCommitTranscript &&
			strings.Contains(renderLineText(entry.Commit.Lines[0]), markers[len(markers)-1]) {
			pendingTail = true
		}
	}
	if !pendingTail {
		finalState := h.controller.State()
		t.Fatalf("blank source rows prevented final-tail planning: active=%+v candidates=%+v rows=%+v effects=%+v",
			streaming.Active, planEligibleHistoryCommits(finalState.AppState),
			LayoutAppState(finalState.AppState).Transcript, finalState.HistoryEffects.Entries())
	}
}
