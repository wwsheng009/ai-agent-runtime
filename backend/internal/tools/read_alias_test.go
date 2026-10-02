package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagerReadAliasResolvesAndExecutesView verifies the registered alias is
// reachable through the same lookup/execution path the agent loop uses
// (AgentAdapter.FindTool -> Manager.Execute) and that it runs the view reader.
func TestManagerReadAliasResolvesAndExecutesView(t *testing.T) {
	manager := NewDefaultManager(nil)

	found := false
	for _, descriptor := range manager.ListTools() {
		if descriptor.Name == "read" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected read alias in manager tool list")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "alias.txt")
	if err := os.WriteFile(target, []byte("content in alias\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	output, err := manager.Execute(context.Background(), "read", map[string]interface{}{
		"file_path": target,
	})
	if err != nil {
		t.Fatalf("read alias execute: %v", err)
	}
	if !strings.Contains(output, "1: content in alias") {
		t.Fatalf("read alias output missing file content: %q", output)
	}
}

// TestReadAliasPromotesViewArgumentAliases keeps the alias argument contract
// identical to view's: a model that sends the alias-friendly `path` argument
// must reach the same reader, and the policy must classify it as a runtime path.
func TestReadAliasPromotesViewArgumentAliases(t *testing.T) {
	manager := NewDefaultManager(nil)

	dir := t.TempDir()
	target := filepath.Join(dir, "aliased-arg.txt")
	if err := os.WriteFile(target, []byte("promoted argument\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	output, err := manager.Execute(context.Background(), "read", map[string]interface{}{
		"path": target,
	})
	if err != nil {
		t.Fatalf("read alias execute: %v", err)
	}
	if !strings.Contains(output, "1: promoted argument") {
		t.Fatalf("read alias output missing file content: %q", output)
	}
}
