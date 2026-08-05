package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func TestStreamingActiveFinalizeTransfersResidentTailExactlyOnce(t *testing.T) {
	const width, height = 100, 24
	markers := make([]string, 40)
	lines := make([]string, len(markers))
	for index := range markers {
		markers[index] = fmt.Sprintf("STREAM-FINAL-%02d terminal history validation", index+1)
		lines[index] = markers[index]
	}

	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: width, Height: height, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		ShowPromptAction{Line: "> "},
	)

	var source string
	for index, line := range lines {
		if source != "" {
			source += "\n"
		}
		source += line
		cell := &scene.TranscriptCell{
			ID: 91, Revision: uint64(index + 1), Kind: scene.KindAssistant,
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
		t.Fatalf("streaming active frontier=%+v, want an acknowledged prefix and resident suffix", streaming.Active)
	}
	finalCell := &scene.TranscriptCell{
		ID: 91, Revision: 41, Kind: scene.KindAssistant,
		Source: source, Phase: scene.CellCommitted,
	}
	h.post(t, FinalizeActiveCellAction{
		Snapshot:             &scene.Snapshot{Revision: 41, Cells: []*scene.TranscriptCell{finalCell}},
		ExpectedActiveCellID: 91, ExpectedActiveRevision: streaming.Active.Revision,
		ExpectedSceneRevision: 41,
		ExpectedActiveKind:    scene.KindAssistant, ExpectedActiveKindKnown: true,
	})

	pendingTail := false
	for _, entry := range h.controller.State().HistoryEffects.Entries() {
		if entry.State == HistoryCommitPending && entry.Commit.Origin == HistoryCommitTranscript &&
			strings.Contains(renderLineText(entry.Commit.Lines[0]), markers[len(markers)-1]) {
			pendingTail = true
		}
	}
	if !pendingTail {
		t.Fatalf("finalization did not plan the resident active tail: active=%+v effects=%+v", streaming.Active, h.controller.State().HistoryEffects.Entries())
	}

	h.flush()
	assertPhysicalMarkersExactlyOnce(t, h.physical.String(), width, height, markers)
}
