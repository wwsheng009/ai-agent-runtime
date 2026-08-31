//go:build win7compat

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// win7compat build: the default config filename is config.win7.yaml, but
// standard-layout agent configs (config.yaml) must still be discoverable so
// services started without an explicit --config can find the real config.
func TestWin7DefaultSearchFallsBackToStandardConfigName(t *testing.T) {
	names := runtimeServerConfigSearchNames()
	if names[0] != "config.win7.yaml" {
		t.Fatalf("expected win7 profile name to lead, got %v", names)
	}
	if len(names) != 2 || names[1] != "config.yaml" {
		t.Fatalf("expected standard config.yaml fallback second, got %v", names)
	}

	root := t.TempDir()
	isolateRuntimeServerHome(t, root)

	// Only the standard-layout config exists: it must be found.
	homeConfigDir := filepath.Join(root, "home", ".aicli")
	if err := os.MkdirAll(homeConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeConfigDir, "config.yaml"), []byte("auth:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveRuntimeServerConfigPath("")
	if got == "" {
		t.Fatal("expected standard-layout config.yaml to be resolved by default search")
	}
	if got != filepath.Join(homeConfigDir, "config.yaml") {
		t.Fatalf("unexpected resolved path: %q", got)
	}

	// When the profile-specific file also exists it must win.
	if err := os.WriteFile(filepath.Join(homeConfigDir, "config.win7.yaml"), []byte("auth:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = resolveRuntimeServerConfigPath("")
	if got == "" {
		t.Fatal("expected profile-specific config to be resolved")
	}
	if got != filepath.Join(homeConfigDir, "config.win7.yaml") {
		t.Fatalf("expected profile-specific config to win, got %q", got)
	}
}