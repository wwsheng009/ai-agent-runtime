package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFiles_LoadsOptionalPromptFiles(t *testing.T) {
	dir := t.TempDir()
	systemPath := filepath.Join(dir, "system.md")
	rolePath := filepath.Join(dir, "role.md")
	writePromptFile(t, systemPath, "You are system.")
	writePromptFile(t, rolePath, "You are role.")

	loaded, err := LoadFiles(Files{
		System: systemPath,
		Role:   rolePath,
	})
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if loaded.System != "You are system." {
		t.Fatalf("unexpected system prompt: %q", loaded.System)
	}
	if loaded.Role != "You are role." {
		t.Fatalf("unexpected role prompt: %q", loaded.Role)
	}
	if loaded.Tools != "" {
		t.Fatalf("expected empty tools prompt, got %q", loaded.Tools)
	}
	if !loaded.HasAny() {
		t.Fatal("expected HasAny to be true")
	}
}

func TestLoadFiles_ReturnsErrorForMissingExplicitFile(t *testing.T) {
	_, err := LoadFiles(Files{System: filepath.Join(t.TempDir(), "missing.md")})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCompose_OrdersSectionsAndSkipsEmptyValues(t *testing.T) {
	composed := Compose(&LoadedFiles{
		System: "System prompt",
		Tools:  "Tool instructions",
	})
	expected := "# System\nSystem prompt\n\n# Tools\nTool instructions"
	if composed != expected {
		t.Fatalf("unexpected composed prompt:\n%s", composed)
	}
}

func writePromptFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
