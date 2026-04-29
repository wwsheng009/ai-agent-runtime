package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRuntimeServerConfigPathUsesConfigsConfigFromCurrentDir(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "configs", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("server:\n  port: 8101\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
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

	resolved := resolveRuntimeServerConfigPath("")
	expected, err := filepath.Abs(configPath)
	if err != nil {
		t.Fatalf("abs expected: %v", err)
	}
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestResolveRuntimeServerConfigPathPrefersProjectConfigYAMLOverConfigsDir(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("server:\n  port: 8101\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	legacyPath := filepath.Join(root, "configs", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("server:\n  port: 8102\n"), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
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

	resolved := resolveRuntimeServerConfigPath("")
	expected, err := filepath.Abs(configPath)
	if err != nil {
		t.Fatalf("abs expected: %v", err)
	}
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestResolveRuntimeServerConfigPathDoesNotRemapExplicitConfigPath(t *testing.T) {
	root := t.TempDir()
	backendConfigPath := filepath.Join(root, "backend", "configs", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(backendConfigPath), 0o755); err != nil {
		t.Fatalf("mkdir backend config dir: %v", err)
	}
	if err := os.WriteFile(backendConfigPath, []byte("server:\n  port: 8101\n"), 0o644); err != nil {
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

	resolved := resolveRuntimeServerConfigPath(filepath.Join(".", "configs", "config.yaml"))
	expected, err := filepath.Abs(filepath.Join(root, "configs", "config.yaml"))
	if err != nil {
		t.Fatalf("abs expected: %v", err)
	}
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}
