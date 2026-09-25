package planstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newRetentionStore builds a store with one record carrying three snapshot
// rounds, so pruning has something to remove.
func newRetentionStore(t *testing.T) (*Store, Record) {
	t.Helper()
	store := NewStore(t.TempDir())
	rec, err := store.Record(RecordOptions{
		ID:        "proj/plan",
		SessionID: "session-1",
		PlanPath:  "docs/plan.md",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	for i, decision := range []string{"request_changes", "request_changes", "approve"} {
		rec, err = store.Snapshot(SnapshotOptions{
			ID:       rec.ID,
			Decision: decision,
			Source:   "user",
			Content:  []byte("round"),
		})
		if err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
	return store, rec
}

func TestStorePruneVersionsKeepsNewestRounds(t *testing.T) {
	store, rec := newRetentionStore(t)
	if rec.Version != 3 {
		t.Fatalf("expected 3 rounds, got %d", rec.Version)
	}

	result, err := store.PruneVersions(PruneOptions{ID: rec.ID, Keep: 2})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.RemovedRounds != 1 || result.RemovedFiles != 1 || result.KeptRounds != 2 {
		t.Fatalf("unexpected prune result: %+v", result)
	}
	if result.Record.Version != 3 {
		t.Fatalf("version counter must stay monotonic, got %d", result.Record.Version)
	}
	if len(result.Record.Rounds) != 2 {
		t.Fatalf("expected 2 rounds after prune, got %d", len(result.Record.Rounds))
	}

	// The pruned version is unreadable, the kept ones still resolve.
	if _, err := store.ReadVersion(rec.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected version 1 to be gone, got %v", err)
	}
	for _, version := range []int{2, 3} {
		if _, err := store.ReadVersion(rec.ID, version); err != nil {
			t.Fatalf("version %d must survive: %v", version, err)
		}
	}
	if _, err := store.ReadLatest(rec.ID); err != nil {
		t.Fatalf("ReadLatest must keep working: %v", err)
	}
}

func TestStorePruneVersionsNoopBelowLimit(t *testing.T) {
	store, rec := newRetentionStore(t)

	result, err := store.PruneVersions(PruneOptions{ID: rec.ID, Keep: 5})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.RemovedRounds != 0 || result.KeptRounds != 3 {
		t.Fatalf("expected a no-op prune, got %+v", result)
	}

	// Keep <= 0 disables pruning entirely.
	result, err = store.PruneVersions(PruneOptions{ID: rec.ID})
	if err != nil {
		t.Fatalf("prune with keep=0: %v", err)
	}
	if result.RemovedRounds != 0 || result.KeptRounds != 3 {
		t.Fatalf("keep=0 must retain every round, got %+v", result)
	}

	if _, err := store.PruneVersions(PruneOptions{ID: "missing/plan", Keep: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an unknown record, got %v", err)
	}
}

func TestStorePruneVersionsRemovesSnapshotFiles(t *testing.T) {
	store, rec := newRetentionStore(t)
	rel := rec.Rounds[0].Snapshot
	abs := filepath.Join(store.Root(), filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("snapshot %s missing before prune: %v", rel, err)
	}

	if _, err := store.PruneVersions(PruneOptions{ID: rec.ID, Keep: 1}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(abs); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned snapshot file still present: %v", err)
	}
}

func TestStoreDeleteRemovesRecordAndSnapshots(t *testing.T) {
	store, rec := newRetentionStore(t)
	var paths []string
	for _, round := range rec.Rounds {
		paths = append(paths, filepath.Join(store.Root(), filepath.FromSlash(round.Snapshot)))
	}

	if err := store.Delete(rec.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := store.Get(rec.ID); err != nil || ok {
		t.Fatalf("record must be gone, ok=%v err=%v", ok, err)
	}
	records, err := store.List()
	if err != nil || len(records) != 0 {
		t.Fatalf("index must be empty, got %d records (err=%v)", len(records), err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("snapshot %s still present after delete: %v", path, err)
		}
	}
	// Deleting a missing record is idempotent, not an error.
	if err := store.Delete(rec.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	// A sibling record in the same project must survive.
	if _, err := store.Record(RecordOptions{ID: "proj/other", PlanPath: "docs/other.md"}); err != nil {
		t.Fatalf("record sibling: %v", err)
	}
	if _, ok, err := store.Get("proj/other"); err != nil || !ok {
		t.Fatalf("sibling record must survive a delete, ok=%v err=%v", ok, err)
	}
}
