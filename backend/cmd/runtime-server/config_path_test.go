package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRuntimeServerConfigPathFallsBackToBackendLayoutFromRepoRoot(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "backend", "configs", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("server:\n  port: 8101\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir legacy configs dir: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(originalWD); chdirErr != nil {
			t.Fatalf("restore wd: %v", chdirErr)
		}
	})

	resolved := resolveRuntimeServerConfigPath("./configs/config.yaml")
	expected, err := filepath.Abs(configPath)
	if err != nil {
		t.Fatalf("abs expected: %v", err)
	}
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestResolveRuntimeServerConfigPathPrefersExistingLegacyPath(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "configs", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("server:\n  port: 8101\n"), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	backendPath := filepath.Join(root, "backend", "configs", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(backendPath), 0o755); err != nil {
		t.Fatalf("mkdir backend dir: %v", err)
	}
	if err := os.WriteFile(backendPath, []byte("server:\n  port: 8102\n"), 0o644); err != nil {
		t.Fatalf("write backend config: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(originalWD); chdirErr != nil {
			t.Fatalf("restore wd: %v", chdirErr)
		}
	})

	resolved := resolveRuntimeServerConfigPath("./configs/config.yaml")
	expected, err := filepath.Abs(legacyPath)
	if err != nil {
		t.Fatalf("abs expected: %v", err)
	}
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestResolveRuntimeServerConfigPathReturnsAbsolutePathFromBackendDir(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "backend", "configs", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("server:\n  port: 8101\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	backendDir := filepath.Join(root, "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatalf("mkdir backend dir: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(backendDir); err != nil {
		t.Fatalf("chdir backend: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(originalWD); chdirErr != nil {
			t.Fatalf("restore wd: %v", chdirErr)
		}
	})

	resolved := resolveRuntimeServerConfigPath("./configs/config.yaml")
	expected, err := filepath.Abs(filepath.Join(backendDir, "configs", "config.yaml"))
	if err != nil {
		t.Fatalf("abs expected: %v", err)
	}
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}
