package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateNodeIDStable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)

	first := LoadOrCreateNodeID()
	if first == "" {
		t.Fatal("LoadOrCreateNodeID returned empty id")
	}
	if second := LoadOrCreateNodeID(); second != first {
		t.Fatalf("id not stable: %q vs %q", first, second)
	}

	data, err := os.ReadFile(filepath.Join(home, ".aicli", NodeIDFileName))
	if err != nil {
		t.Fatalf("identity file missing: %v", err)
	}
	if got := string(data[:len(data)-1]); got != first {
		t.Fatalf("file content %q != %q", got, first)
	}
}

func TestLoadOrCreateNodeIDWithoutHome(t *testing.T) {
	t.Setenv("AICLI_HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOME", "")
	// Degrades to a per-process id (non-empty, never panics).
	if LoadOrCreateNodeID() == "" {
		t.Fatal("expected a fallback id, got empty")
	}
}

func TestLoadOrCreateNodeIDAdoptsExistingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AICLI_HOME", home)
	dir := filepath.Join(home, ".aicli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, NodeIDFileName), []byte("node-abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadOrCreateNodeID(); got != "node-abc123" {
		t.Fatalf("existing id not adopted: %q", got)
	}
}
