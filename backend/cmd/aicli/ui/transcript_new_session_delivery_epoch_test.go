package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// /new rebuilds the Scene, so the fresh conversation starts from cell ID 1 /
// revision 1 again. The history ledger is keyed by that identity, and the
// presenter omits finalized transcript rows from the mutable viewport. If the
// reset does not open a new delivery epoch, old delivered records make the new
// session's confirmation and first prompt look like duplicates: they are never
// committed to native scrollback and the user sees no echo at all.
func TestFreshConversationReplacementOpensDeliveryEpochWithoutArmReplay(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "old session"),
	}, 2)
	oldToken := queuedHistoryTokenForCell(t, state, 1)
	state = deliverHistoryToken(state, oldToken, 3)

	// A real terminal resize advances the layout generation. Old delivered
	// records then satisfy cellRevisionFullyDelivered for every later plan unless
	// the session switch also advances the delivery epoch.
	state = reduceUIControllerState(state, Resize{Width: 72, Height: 11}, 5)

	// /new: clear the transcript and open the new session's delivery epoch. This
	// is intentionally not an armed scrollback replay (no session history load).
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:         &scene.Snapshot{},
		NewDeliveryEpoch: true,
	}, 6)
	// The new session's first cell reuses ID=1/Revision=1.
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(2, "fresh conversation"),
	}, 7)

	freshToken := queuedHistoryTokenForCell(t, state, 1)
	if freshToken == oldToken {
		t.Fatalf("fresh cell reused delivered token %d", oldToken)
	}
	entry := historyCommitEntry(t, state, freshToken)
	if entry.Commit.LayoutGeneration != state.Geometry.Generation {
		t.Fatalf("fresh commit generation = %d, want %d",
			entry.Commit.LayoutGeneration, state.Geometry.Generation)
	}
}

// Negative control: a plain transcript replacement (no epoch and no armed
// replay) must keep the append-only dedup proof. Otherwise the test above
// would pass even without opening the epoch.
func TestPlainTranscriptReplacementKeepsDeliveredIdentityDedup(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "old session"),
	}, 2)
	oldToken := queuedHistoryTokenForCell(t, state, 1)
	state = deliverHistoryToken(state, oldToken, 3)
	state = reduceUIControllerState(state, Resize{Width: 72, Height: 11}, 5)

	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: &scene.Snapshot{},
	}, 6)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(2, "fresh conversation"),
	}, 7)

	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == HistoryCommitQueued && entry.Commit.CellID == 1 {
			t.Fatalf("plain replacement queued a reused identity: %#v", entry)
		}
	}
}
