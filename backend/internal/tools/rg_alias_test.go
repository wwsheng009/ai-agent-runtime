package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagerRGAliasResolvesAndExecutesGrep verifies the registered alias is
// reachable through the same lookup/execution path the agent loop uses
// (AgentAdapter.FindTool -> Manager.Execute) and that it runs the grep engine.
func TestManagerRGAliasResolvesAndExecutesGrep(t *testing.T) {
	manager := NewDefaultManager(nil)

	found := false
	for _, descriptor := range manager.ListTools() {
		if descriptor.Name == "rg" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected rg alias in manager tool list")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "alias.txt")
	if err := os.WriteFile(target, []byte("needle in alias\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	output, err := manager.Execute(context.Background(), "rg", map[string]interface{}{
		"pattern": "needle",
		"path":    dir,
	})
	if err != nil {
		t.Fatalf("rg alias execute: %v", err)
	}
	if !strings.Contains(output, "needle in alias") {
		t.Fatalf("rg alias output missing match: %q", output)
	}
}
