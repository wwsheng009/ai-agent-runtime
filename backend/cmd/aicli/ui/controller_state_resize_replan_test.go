package ui

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// committedTranscriptSnapshot builds the smallest loaded-history Scene: two
// finalized cells that are eligible for native-scrollback delivery.
func committedTranscriptSnapshot(t *testing.T) *scene.Snapshot {
	t.Helper()
	finalizedAt := time.Now()
	return &scene.Snapshot{Revision: 7, Cells: []*scene.TranscriptCell{
		{
			ID: 1, Revision: 7, Kind: scene.KindUser, Source: "查看 docs",
			Phase: scene.CellCommitted, FinalizedAt: &finalizedAt,
		},
		{
			ID: 2, Revision: 7, Kind: scene.KindAssistant, Source: "目录里有 README。",
			Phase: scene.CellCommitted, FinalizedAt: &finalizedAt,
		},
	}}
}

// TestResizeReplansTranscriptInstalledBeforeGeometry pins the blank-body
// recovery path for session loads: /resume, /load and startup restore install
// their replacement Scene from the persisted transcript, and on a slow first
// frame that snapshot can reach the reducer before any applied resize. The
// planner is geometry-gated, so that pass legitimately mints no candidate;
// the first geometry that arrives afterwards must re-derive the plan from the
// installed Scene. Rebasing pending payloads alone can never recreate a plan
// that was never made, which is why the transcript body used to stay blank no
// matter how often the user resized the terminal.
func TestResizeReplansTranscriptInstalledBeforeGeometry(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{},
		ReplaceTranscriptAction{Snapshot: committedTranscriptSnapshot(t), ArmScrollbackReplay: true}, 1)
	if got := len(state.HistoryEffects.Entries()); got != 0 {
		t.Fatalf("precondition: geometry-free load planned %d entries, want 0", got)
	}
	if !state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("precondition: session load did not arm the one-shot replay authorization")
	}

	state = reduceUIControllerState(state, Resize{Width: 100, Height: 30, Generation: 1}, 2)
	entries := state.HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatalf("geometry arrival did not re-plan the installed transcript (cells=%d)",
			len(state.Transcript.Cells))
	}
	for _, entry := range entries {
		if entry.State != HistoryCommitPending {
			t.Fatalf("token %d planned in state %v, want pending", entry.Commit.Token, entry.State)
		}
		if entry.Commit.LayoutGeneration != state.LayoutGeneration {
			t.Fatalf("token %d carries generation %d, want %d",
				entry.Commit.Token, entry.Commit.LayoutGeneration, state.LayoutGeneration)
		}
	}

	// An ordinary resize must still never mint a token by itself: the plan is
	// derived from cell ranges, not from viewport size.
	tokens := make(map[uint64]struct{}, len(entries))
	for _, entry := range entries {
		tokens[entry.Commit.Token] = struct{}{}
	}
	state = reduceUIControllerState(state, Resize{Width: 120, Height: 40, Generation: 2}, 3)
	resized := state.HistoryEffects.Entries()
	if len(resized) != len(entries) {
		t.Fatalf("resize minted %d extra history commits (got %d, want %d)",
			len(resized)-len(entries), len(resized), len(entries))
	}
	for _, entry := range resized {
		if _, ok := tokens[entry.Commit.Token]; !ok {
			t.Fatalf("resize minted a new token %d for an already planned range", entry.Commit.Token)
		}
	}
}
