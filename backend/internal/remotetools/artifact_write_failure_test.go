package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// TestShellReportsArtifactWriteFailure pins the fail-closed archive contract:
// when the capture exceeds the model window but the artifact store cannot
// persist it, the call must fail with ARTIFACT_WRITE_FAILED instead of
// returning a folded body whose omitted middle is unrecoverable.
func TestShellReportsArtifactWriteFailure(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.Workspace = root
	cfg.TargetName = "dev"
	cfg.Profile = ProfileOps
	cfg.Shell.AllowedCommands = []string{"echo"}
	cfg.Shell.DenyShellMetachars = true
	cfg.Limits.CommandTimeout = Duration{Duration: 10 * time.Second}
	cfg.Limits.MaxOutputBytes = 4096
	cfg.Limits.ModelVisibleCap = 512
	stateDir := t.TempDir()
	cfg.Artifacts.Root = filepath.Join(stateDir, "artifacts")
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	sandbox, err := NewSandbox(cfg, workspace)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	artifacts, err := NewArtifactStore(cfg.Artifacts.Root, 0)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	// Break the store after construction: the directory the tool captured at
	// build time is gone, so the archive write must fail at call time.
	if err := os.RemoveAll(cfg.Artifacts.Root); err != nil {
		t.Fatalf("break artifact root: %v", err)
	}
	tool := newRemoteShellTool(cfg, workspace, sandbox, artifacts)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo " + strings.Repeat("x", 2000),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatalf("archive failure must not report success: %+v", result)
	}
	if result.Metadata["error_code"] != toolcore.ErrArtifactWriteFailed {
		t.Fatalf("error_code = %v, want %s", result.Metadata["error_code"], toolcore.ErrArtifactWriteFailed)
	}
	if result.Error == nil {
		t.Fatal("archive failure must carry an error")
	}
}
