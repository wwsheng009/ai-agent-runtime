package subagentbatch

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestSQLiteBatchStoreIsDurable pins the I9 capability probe (§6.13): only an
// on-disk store may answer true, because the runtime parks turns there.
func TestSQLiteBatchStoreIsDurable(t *testing.T) {
	cases := []struct {
		name string
		cfg  *StoreConfig
		want bool
	}{
		{name: "nil config is in-memory", cfg: nil, want: false},
		{name: "empty config is in-memory", cfg: &StoreConfig{}, want: false},
		{name: "explicit memory dsn", cfg: &StoreConfig{DSN: "file:batches?mode=memory&cache=shared"}, want: false},
		{name: "memory path", cfg: &StoreConfig{Path: ":memory:"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewSQLiteBatchStore(tc.cfg)
			if err != nil {
				t.Fatalf("NewSQLiteBatchStore: %v", err)
			}
			defer store.Close()
			if got := store.IsDurable(); got != tc.want {
				t.Fatalf("IsDurable() = %v, want %v", got, tc.want)
			}
		})
	}

	fileStore, err := NewSQLiteBatchStore(&StoreConfig{Path: filepath.Join(t.TempDir(), "batches.db")})
	if err != nil {
		t.Fatalf("NewSQLiteBatchStore(file): %v", err)
	}
	defer fileStore.Close()
	if !fileStore.IsDurable() {
		t.Fatal("a file-backed store must report durable")
	}
}

// TestSQLiteBatchStoreTurnSuspensionRoundTrip covers the §6.12 parked-state
// ledger: write, read back, idempotent re-park (one row), clear, and survival
// across a store reopen (the restart precondition behind AC-C0-1c).
func TestSQLiteBatchStoreTurnSuspensionRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "batches.db")
	store, err := NewSQLiteBatchStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewSQLiteBatchStore: %v", err)
	}

	parkedAt := time.Now().UTC().Truncate(time.Millisecond)
	window := parkedAt.Add(30 * time.Second)
	record := &TurnSuspension{
		TurnID:              "turn-1",
		SessionID:           "sess-1",
		RootScopeID:         "sess-1",
		ObligationIDs:       []string{"batch-1", "task-1", "task-2"},
		ParkedAt:            parkedAt,
		DecisionWindowUntil: window,
		ResumeQueue:         []string{"batch-1"},
	}
	if err := store.ParkTurnSuspension(ctx, record); err != nil {
		t.Fatalf("ParkTurnSuspension: %v", err)
	}

	got, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-1")
	if err != nil || !ok {
		t.Fatalf("GetTurnSuspension = (%v, %v), want a record", ok, err)
	}
	if len(got.ObligationIDs) != 3 || got.ObligationIDs[0] != "batch-1" {
		t.Fatalf("obligation ids = %v, want [batch-1 task-1 task-2]", got.ObligationIDs)
	}
	if len(got.ResumeQueue) != 1 || got.ResumeQueue[0] != "batch-1" {
		t.Fatalf("resume queue = %v, want [batch-1]", got.ResumeQueue)
	}
	if got.RootScopeID != "sess-1" {
		t.Fatalf("root scope = %q, want sess-1", got.RootScopeID)
	}
	if !got.ParkedAt.Equal(parkedAt) {
		t.Fatalf("parked at = %v, want %v", got.ParkedAt, parkedAt)
	}
	if !got.DecisionWindowUntil.Equal(window) {
		t.Fatalf("decision window = %v, want %v", got.DecisionWindowUntil, window)
	}

	// Re-parking the same turn (retry after a crash) overwrites, never duplicates.
	if err := store.ParkTurnSuspension(ctx, record); err != nil {
		t.Fatalf("ParkTurnSuspension(re-park): %v", err)
	}
	sqliteStore, okCast := store.(*sqliteBatchStore)
	if !okCast {
		t.Fatalf("store type = %T, want *sqliteBatchStore", store)
	}
	var rows int
	if err := sqliteStore.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM turn_suspensions WHERE session_id = ? AND turn_id = ?`,
		"sess-1", "turn-1").Scan(&rows); err != nil {
		t.Fatalf("count turn_suspensions: %v", err)
	}
	if rows != 1 {
		t.Fatalf("turn_suspensions rows = %d, want 1 (idempotent park)", rows)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := NewSQLiteBatchStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	reread, ok, err := reopened.GetTurnSuspension(ctx, "sess-1", "turn-1")
	if err != nil || !ok {
		t.Fatalf("GetTurnSuspension after reopen = (%v, %v), want a record", ok, err)
	}
	if len(reread.ObligationIDs) != 3 || reread.ObligationIDs[2] != "task-2" {
		t.Fatalf("reopened obligation ids = %v, want the parked set", reread.ObligationIDs)
	}

	if err := reopened.ClearTurnSuspension(ctx, "sess-1", "turn-1"); err != nil {
		t.Fatalf("ClearTurnSuspension: %v", err)
	}
	if _, ok, err := reopened.GetTurnSuspension(ctx, "sess-1", "turn-1"); err != nil || ok {
		t.Fatalf("GetTurnSuspension after clear = (%v, %v), want no record", ok, err)
	}
	// Clearing an absent record is not an error.
	if err := reopened.ClearTurnSuspension(ctx, "sess-1", "turn-1"); err != nil {
		t.Fatalf("ClearTurnSuspension(absent): %v", err)
	}
}

// TestSQLiteBatchStoreTurnSuspensionRejectsIncompleteRecord guards the primary
// key: a record without session/turn cannot be resumed and must be rejected
// instead of silently parked under a blank key.
func TestSQLiteBatchStoreTurnSuspensionRejectsIncompleteRecord(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteBatchStore(nil)
	if err != nil {
		t.Fatalf("NewSQLiteBatchStore: %v", err)
	}
	defer store.Close()

	if err := store.ParkTurnSuspension(ctx, nil); err == nil {
		t.Fatal("nil record must be rejected")
	}
	if err := store.ParkTurnSuspension(ctx, &TurnSuspension{SessionID: "sess-1"}); err == nil {
		t.Fatal("a record without turn_id must be rejected")
	}
	if _, ok, err := store.GetTurnSuspension(ctx, "", "turn-1"); err != nil || ok {
		t.Fatalf("blank session must read as absent, got (%v, %v)", ok, err)
	}
}
