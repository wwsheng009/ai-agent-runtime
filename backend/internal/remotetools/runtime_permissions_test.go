package remotetools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// TestRemotePermissionsMapsDelete locks the operation mapping: before OpDelete
// existed, apply_patch move/delete fell through to "unsupported path operation"
// instead of the sandbox's delete decision.
func TestRemotePermissionsMapsDelete(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed outside: %v", err)
	}
	cfg := DefaultConfig()
	cfg.Workspace = root
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	sandbox, err := NewSandbox(cfg, workspace)
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	permissions := remotePermissions{sandbox: sandbox}
	err = permissions.CheckPath(toolcore.OpDelete, outside)
	if err == nil {
		t.Fatal("expected outside path to be denied")
	}
	if strings.Contains(err.Error(), "unsupported path operation") {
		t.Fatalf("OpDelete was not mapped to the sandbox: %v", err)
	}
}
