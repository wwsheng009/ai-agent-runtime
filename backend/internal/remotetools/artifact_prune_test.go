package remotetools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
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

	// The shared artifact_handler must preserve ARTIFACT_EXPIRED end to end
	// instead of collapsing the missing file into INVALID_ARGUMENT.
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.TargetName = "dev"
	cfg.Artifacts.Root = root
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	tool := newRemoteArtifactReadTool(cfg, workspace, store)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"artifact_id": oldID})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("expired artifact read must fail")
	}
	if result.Metadata["error_code"] != toolcore.ErrArtifactExpired {
		t.Fatalf("error_code = %v, want %s", result.Metadata["error_code"], toolcore.ErrArtifactExpired)
	}
}
