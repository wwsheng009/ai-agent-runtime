//go:build win7compat

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// win7compat build shares the standard-layout user config file (config.yaml)
// with the main build, so runtime-server and the Web UI read and write the same
// file. The win7-specific runtime config (runtime.win7.yaml) and session
// database remain separate only for SQLite driver isolation (Go 1.20 driver vs
// the main build's newer driver), not for the user-facing bootstrap config.
func TestWin7DefaultSearchUsesStandardConfigName(t *testing.T) {
	names := runtimeServerConfigSearchNames()
	if len(names) != 1 || names[0] != aiclipaths.StandardConfigFileName {
		t.Fatalf("expected win7 build to search standard config name only, got %v", names)
	}

	root := t.TempDir()
	isolateRuntimeServerHome(t, root)

	// The standard-layout config must be resolved by default search.
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

	// A legacy profile-specific file must NOT win over the standard config,
	// because the win7 build and the standard build share config.yaml.
	if err := os.WriteFile(filepath.Join(homeConfigDir, "config.win7.yaml"), []byte("auth:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = resolveRuntimeServerConfigPath("")
	if got != filepath.Join(homeConfigDir, "config.yaml") {
		t.Fatalf("expected standard config.yaml to stay authoritative, got %q", got)
	}
}
