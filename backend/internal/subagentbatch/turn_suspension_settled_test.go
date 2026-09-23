package subagentbatch

import (
	"context"
	"path/filepath"
	"testing"
)

// TestTurnObligationsSettled pins the EC-E1 settle predicate behind the §6.12
// parked-turn record: the record may only be cleared once every obligation batch
// reached a terminal state, and an unresolvable batch id is never treated as
// evidence — the safe direction is keeping the turn parked, because clearing on
// a guess would drop obligations the parent still owes.
func TestTurnObligationsSettled(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteBatchStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "batches.db")})
	if err != nil {
		t.Fatalf("NewSQLiteBatchStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	seedSettledBatch(t, store, "batch-done", BatchCompleted)
	seedSettledBatch(t, store, "batch-running", BatchRunning)

	cases := []struct {
		name   string
		record *TurnSuspension
		want   bool
	}{
		{
			name:   "terminal batch settles the turn",
			record: &TurnSuspension{TurnID: "t1", SessionID: "s1", ResumeQueue: []string{"batch-done"}},
			want:   true,
		},
		{
			name:   "running batch keeps the turn parked",
			record: &TurnSuspension{TurnID: "t1", SessionID: "s1", ResumeQueue: []string{"batch-running"}},
			want:   false,
		},
		{
			name:   "one running batch is enough to keep the turn parked",
			record: &TurnSuspension{TurnID: "t1", SessionID: "s1", ResumeQueue: []string{"batch-done", "batch-running"}},
			want:   false,
		},
		{
			name:   "unknown batch id is not evidence",
			record: &TurnSuspension{TurnID: "t1", SessionID: "s1", ResumeQueue: []string{"batch-missing"}},
			want:   false,
		},
		{
			name:   "a resolved terminal batch settles even when another id is unknown",
			record: &TurnSuspension{TurnID: "t1", SessionID: "s1", ResumeQueue: []string{"batch-done", "batch-missing"}},
			want:   true,
		},
		{
			name:   "obligation ids are the fallback when no queue was recorded",
			record: &TurnSuspension{TurnID: "t1", SessionID: "s1", ObligationIDs: []string{"batch-done", "task-1"}},
			want:   true,
		},
		{
			name:   "record without obligations is not settled",
			record: &TurnSuspension{TurnID: "t1", SessionID: "s1"},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TurnObligationsSettled(ctx, store, tc.record)
			if err != nil {
				t.Fatalf("TurnObligationsSettled: %v", err)
			}
			if got != tc.want {
				t.Fatalf("TurnObligationsSettled = %v, want %v", got, tc.want)
			}
		})
	}

	if settled, err := TurnObligationsSettled(ctx, store, nil); err != nil || settled {
		t.Fatalf("nil record = (%v, %v), want (false, nil)", settled, err)
	}
	if settled, err := TurnObligationsSettled(ctx, nil, cases[0].record); err != nil || settled {
		t.Fatalf("nil store = (%v, %v), want (false, nil)", settled, err)
	}
}

// seedSettledBatch creates one batch row with the wanted status. CreateBatch
// honours a caller-set status, so terminal fixtures need no transition dance.
func seedSettledBatch(t *testing.T, store BatchStore, batchID string, status BatchStatus) {
	t.Helper()
	if _, err := store.CreateBatch(context.Background(), &SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     "s1",
		ParentSessionID: "s1",
		Status:          status,
	}, nil); err != nil {
		t.Fatalf("CreateBatch(%s): %v", batchID, err)
	}
}
