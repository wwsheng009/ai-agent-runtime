package manager

import (
	"os"
	"path/filepath"
	"testing"
)

func isolateManagerHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestLoadConfigEffectiveFromAnchorsAtBaseAndRecordsLayerBase(t *testing.T) {
	isolateManagerHome(t)
	base := t.TempDir()
	target := filepath.Join(base, ".aicli", "mcp.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte(`
mcpServers:
  base-only:
    type: stdio
    command: base-only-cmd
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	mgr := NewManager()
	loader, ok := mgr.(LayeredConfigLoaderFrom)
	if !ok {
		t.Fatal("manager must implement LayeredConfigLoaderFrom")
	}
	if err := loader.LoadConfigEffectiveFrom(base, ""); err != nil {
		t.Fatalf("LoadConfigEffectiveFrom: %v", err)
	}

	statuses := mgr.ListMCPs()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 server, got %d", len(statuses))
	}
	if reporter, ok := mgr.(ConfigOriginReporter); ok && reporter != nil {
		origins := reporter.MCPConfigOrigins()
		if origin, found := origins["base-only"]; !found || origin.Source != "project" {
			t.Fatalf("origin should be project, got %#v", origin)
		}
	} else {
		t.Fatal("manager must implement ConfigOriginReporter")
	}

	concrete, ok := mgr.(*manager)
	if !ok {
		t.Fatalf("unexpected manager type %T", mgr)
	}
	concrete.mu.RLock()
	layerBase := concrete.layerBase
	concrete.mu.RUnlock()
	if filepath.Clean(layerBase) != filepath.Clean(base) {
		t.Fatalf("layerBase = %q, want %q", layerBase, base)
	}
}

func TestLoadConfigEffectiveFromRejectsMissingConfig(t *testing.T) {
	isolateManagerHome(t)
	base := t.TempDir()

	mgr := NewManager()
	loader := mgr.(LayeredConfigLoaderFrom)
	if err := loader.LoadConfigEffectiveFrom(base, ""); err == nil {
		t.Fatal("expected an error when the workspace chain has no config files")
	}
}
