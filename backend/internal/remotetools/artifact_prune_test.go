package remotetools

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestArtifactStorePrunesExpiredFiles pins the 72h retention contract: the
// prune sweep removes only artifacts older than the window, and a subsequent
// read reports NOT_FOUND with the expiry hint.
func TestArtifactStorePrunesExpiredFiles(t *testing.T) {
	root := t.TempDir()
	store, err := NewArtifactStore(root, 1024)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	freshID, err := store.Put([]byte("fresh payload"))
	if err != nil {
		t.Fatalf("put fresh: %v", err)
	}
	oldID, err := store.Put([]byte("old payload"))
	if err != nil {
		t.Fatalf("put old: %v", err)
	}
	past := time.Now().Add(-artifactRetention - time.Hour)
	if err := os.Chtimes(store.path(oldID), past, past); err != nil {
		t.Fatalf("age artifact: %v", err)
	}
	// Force the periodic gate open without waiting six hours.
	store.lastPrune = time.Time{}
	if err := store.pruneIfDue(); err != nil {
		t.Fatalf("pruneIfDue: %v", err)
	}
	if _, err := os.Stat(store.path(oldID)); !os.IsNotExist(err) {
		t.Fatalf("expired artifact must be removed, stat err=%v", err)
	}
	if _, err := os.Stat(store.path(freshID)); err != nil {
		t.Fatalf("fresh artifact must survive: %v", err)
	}
	if _, _, _, err := store.Read(oldID, 0, 64); err == nil {
		t.Fatal("reading a pruned artifact must fail")
	} else if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected the 72h expiry hint, got %v", err)
	}
}
