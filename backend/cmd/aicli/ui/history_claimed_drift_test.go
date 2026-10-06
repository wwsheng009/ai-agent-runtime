package ui

import (
	"errors"
	"testing"
)

// TestClaimedPresentationDriftCountsAndConvergesAfterDeferred pins G4/C1: a
// claimed token whose presentation drifted is never rebased mid-write (payload
// unchanged, cursor held); the drift is counted for diagnostics; after the
// executor's generation gate releases the cursor (Deferred), the next rebase
// converges the payload.
func TestClaimedPresentationDriftCountsAndConvergesAfterDeferred(t *testing.T) {
	queue := HistoryEffectQueueState{}
	commit := testHistoryCommit(0, 61, 3)
	if err := queue.enqueue(commit); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := queue.markInFlight(1, 3); err != nil {
		t.Fatalf("markInFlight: %v", err)
	}

	drifted := testHistoryCommit(0, 61, 4)
	drifted.DisplayRange = DisplayRange{Start: 8, End: 13}
	if !queue.claimedPresentationDrifted(drifted) {
		t.Fatal("claimed token with new generation/display range must report drift")
	}
	if queue.claimedPresentationDrifted(commit) {
		t.Fatal("identical candidate must not report drift")
	}
	if !queue.noteClaimedPresentationDrift(drifted) || queue.ClaimedPresentationDrift != 1 {
		t.Fatalf("drift counter = %d, want 1", queue.ClaimedPresentationDrift)
	}

	// Mid-write: rebase refuses and the payload/cursor stay untouched.
	if err := queue.rebasePending(drifted); !errors.Is(err, ErrCommitNotPending) {
		t.Fatalf("rebase while claimed = %v, want ErrCommitNotPending", err)
	}
	entry, ok := queue.ledger.Entry(1)
	if !ok || entry.Commit.LayoutGeneration != 3 || queue.WriteCursor != 1 {
		t.Fatalf("claimed payload/cursor changed: gen=%d cursor=%d", entry.Commit.LayoutGeneration, queue.WriteCursor)
	}

	// Executor generation gate releases the cursor (Deferred); the next rebase
	// converges the payload to the new presentation.
	if err := queue.deferInFlight(1, 3); err != nil {
		t.Fatalf("deferInFlight: %v", err)
	}
	if err := queue.rebasePending(drifted); err != nil {
		t.Fatalf("rebase after defer: %v", err)
	}
	entry, ok = queue.ledger.Entry(1)
	if !ok || entry.Commit.LayoutGeneration != 4 || queue.WriteCursor != 0 {
		t.Fatalf("post-defer convergence: gen=%d cursor=%d, want 4/0", entry.Commit.LayoutGeneration, queue.WriteCursor)
	}
}
