package memorystore

import (
	"context"
	"strings"
	"testing"
)

// TestFTS5CapabilityProbe checks the same SQLite driver used by memory v2
// against a temporary database only. Search remains LIKE-backed until the
// memory-specific FTS index and query path are implemented.
func TestFTS5CapabilityProbe(t *testing.T) {
	store, err := New(Config{Root: t.TempDir(), Mode: "on"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db, err := store.ensureV2()
	if err != nil {
		t.Fatalf("open temporary memory database: %v", err)
	}
	_, err = db.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE memory_fts_capability_probe USING fts5(content)`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such module: fts5") {
			t.Logf("fts_unavailable: %v", err)
			return
		}
		t.Fatalf("FTS5 capability probe failed unexpectedly: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DROP TABLE memory_fts_capability_probe`); err != nil {
		t.Fatalf("drop temporary FTS5 capability probe: %v", err)
	}
	t.Log("fts_available")
}
