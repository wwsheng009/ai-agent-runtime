package ui

import "testing"

// Settled finalized cells must not be laid out again. The prior planner skipped
// their *payload materialization* inside mint, but still ran the full layout
// pass over every cell first; on resumed sessions that kept re-rendering the
// entire delivered history (production pprof: renderLayoutJob + diff/highlight
// dominated allocations for a 1,999-cell session).
func TestSettledFinalizedCellSkipsLayout(t *testing.T) {
	state := settledLayoutFixture(t)

	if commits, _ := planEligibleHistoryCommitsWithin(state.AppState); len(commits) == 0 {
		t.Fatal("fixture produced no plan")
	}
	if acked := ackQueuedLedgerEntries(t, state); acked == 0 {
		t.Fatal("fixture queued nothing to acknowledge")
	}

	// The first settled observation records the content fingerprint and is
	// intentionally laid out once. Only the next plan can prove it is a repeat.
	if commits, _ := planEligibleHistoryCommitsWithin(state.AppState); len(commits) != 0 {
		t.Fatalf("fingerprint-prime plan rebuilt %d commits; want 0", len(commits))
	}

	swapSharedCellRowsCache(t)
	if commits, _ := planEligibleHistoryCommitsWithin(state.AppState); len(commits) != 0 {
		t.Fatalf("settled re-plan rebuilt %d commits; want 0", len(commits))
	}
	_, misses, _, _, _ := sharedCellRows.stats()
	if misses != 0 {
		t.Fatalf("settled re-plan performed %d layout misses; want 0", misses)
	}
}

// A queued/in-flight revision is not settled: its remaining fragments still
// need layout for both reconciliation and eventual delivery.
func TestQueuedFinalizedCellStillLaysOut(t *testing.T) {
	state := settledLayoutFixture(t)

	if commits, _ := planEligibleHistoryCommitsWithin(state.AppState); len(commits) == 0 {
		t.Fatal("fixture produced no plan")
	}

	swapSharedCellRowsCache(t)
	if commits, _ := planEligibleHistoryCommitsWithin(state.AppState); len(commits) == 0 {
		t.Fatal("queued re-plan returned no commits")
	}
	_, misses, _, _, _ := sharedCellRows.stats()
	if misses == 0 {
		t.Fatal("queued re-plan did not lay out the cell")
	}
}

// A new delivery epoch means the loaded history may be appended again. Settled
// bookkeeping from the previous epoch must not suppress the layout pass.
func TestNewDeliveryEpochStillLaysOutSettledCell(t *testing.T) {
	state := settledLayoutFixture(t)

	if commits, _ := planEligibleHistoryCommitsWithin(state.AppState); len(commits) == 0 {
		t.Fatal("fixture produced no plan")
	}
	if acked := ackQueuedLedgerEntries(t, state); acked == 0 {
		t.Fatal("fixture queued nothing to acknowledge")
	}
	state.HistoryEffects.openDeliveryEpoch()

	swapSharedCellRowsCache(t)
	if commits, _ := planEligibleHistoryCommitsWithin(state.AppState); len(commits) == 0 {
		t.Fatal("new epoch re-plan returned no commits")
	}
	_, misses, _, _, _ := sharedCellRows.stats()
	if misses == 0 {
		t.Fatal("new epoch re-plan did not lay out the cell")
	}
}

// A replacement transcript can reuse the same cellID+revision while changing
// the source (backtrack applies exactly this shape). The settled fingerprint
// must invalidate, otherwise the new content disappears from the terminal.
func TestReusedCellRevisionWithChangedSourceStillLaysOut(t *testing.T) {
	state := settledLayoutFixture(t)

	if commits, _ := planEligibleHistoryCommitsWithin(state.AppState); len(commits) == 0 {
		t.Fatal("fixture produced no plan")
	}
	if acked := ackQueuedLedgerEntries(t, state); acked == 0 {
		t.Fatal("fixture queued nothing to acknowledge")
	}
	// Prime the settled fingerprint for the original content.
	_, _ = planEligibleHistoryCommitsWithin(state.AppState)

	state.Transcript.Cells[0].Source = "replacement session"

	swapSharedCellRowsCache(t)
	_, _ = planEligibleHistoryCommitsWithin(state.AppState)
	_, misses, _, _, _ := sharedCellRows.stats()
	if misses == 0 {
		t.Fatal("reused revision with changed source did not lay out")
	}
}

// Bulk guard: a fully delivered multi-cell transcript must produce no layout
// work at all on the next re-plan.
func TestSettledFinalizedCellsSkipBulkLayout(t *testing.T) {
	state := historyEffectTestState(t, 1)
	if acked := ackQueuedLedgerEntries(t, state); acked == 0 {
		t.Fatal("fixture queued nothing to acknowledge")
	}

	// Prime fingerprints for every cell; this first pass may still lay out.
	_, _ = planEligibleHistoryCommitsWithin(state.AppState)

	swapSharedCellRowsCache(t)
	if commits, _ := planEligibleHistoryCommitsWithin(state.AppState); len(commits) != 0 {
		t.Fatalf("settled 20-cell re-plan rebuilt %d commits; want 0", len(commits))
	}
	_, misses, _, _, _ := sharedCellRows.stats()
	if misses != 0 {
		t.Fatalf("settled 20-cell re-plan performed %d layout misses; want 0", misses)
	}
}

func settledLayoutFixture(t *testing.T) UIControllerState {
	t.Helper()
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	return reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "loaded session"),
	}, 2)
}

func ackQueuedLedgerEntries(t *testing.T, state UIControllerState) int {
	t.Helper()
	ledger := state.HistoryEffects.ledger
	acked := 0
	for _, token := range ledger.orderedTokens() {
		entry, ok := ledger.byToken[token]
		if !ok || entry.State != HistoryCommitQueued {
			continue
		}
		if err := ledger.Ack(token, 1, entry.Commit.LayoutGeneration); err != nil {
			t.Fatalf("ack %d: %v", token, err)
		}
		acked++
	}
	return acked
}

func swapSharedCellRowsCache(t *testing.T) {
	t.Helper()
	old := sharedCellRows
	sharedCellRows = &cellRowsCache{
		lru: newCellLayoutLRU[cellLayoutKey, []AppScreenRow](cellRowsCacheMax, cellRowsCacheMaxBytes),
	}
	t.Cleanup(func() { sharedCellRows = old })
}
