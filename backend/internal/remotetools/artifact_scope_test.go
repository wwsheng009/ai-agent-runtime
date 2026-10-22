package remotetools

import (
	"context"
	"path/filepath"
	"testing"
)

func TestArtifactStorePutRefNamespacesTarget(t *testing.T) {
	store, err := NewArtifactStore(filepath.Join(t.TempDir(), "artifacts"), 0)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.PutRef("238", []byte("capture"))
	if err != nil {
		t.Fatal(err)
	}
	if ref.Scope != "remote" || ref.Target != "238" || ref.ID == "" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	if got := ref.String(); got == "" || got[:7] != "remote:" {
		t.Fatalf("ref string = %q", got)
	}
}

func TestRemoteArtifactReadRejectsNamespace(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		t.Fatal(err)
	}
	tool := newRemoteArtifactReadTool(cfg, workspace, nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"artifact_ref": "remote:other:art_0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("namespaced artifact ref must be rejected by target-internal read")
	}
	if got := result.Metadata["error_code"]; got != "ARTIFACT_SCOPE_DENIED" {
		t.Fatalf("error_code = %v", got)
	}
}

func TestConfigRejectsInvalidTargetName(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.TargetName = "Not Portable!"
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected invalid target name to fail closed")
	}
}
