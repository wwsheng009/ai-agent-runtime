package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRuntimeServerConfigPathUsesConfigsConfigFromCurrentDir(t *testing.T) {
	root := t.TempDir()
	isolateRuntimeServerHome(t, root)
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
	isolateRuntimeServerHome(t, root)
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
	isolateRuntimeServerHome(t, root)
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

func TestDefaultRuntimeServerDotEnvSearchPathsFollowConfigDirectories(t *testing.T) {
	root := t.TempDir()
	isolateRuntimeServerHome(t, root)

	paths := defaultRuntimeServerDotEnvSearchPaths()
	expected := []string{
		filepath.Join(root, "home", ".aicli", ".env"),
		filepath.Join(".aicli", ".env"),
		".env",
		filepath.Join("configs", ".env"),
	}
	if len(paths) != len(expected) {
		t.Fatalf("unexpected .env path count: got %d %v, want %d %v", len(paths), paths, len(expected), expected)
	}
	for i := range expected {
		if paths[i] != expected[i] {
			t.Fatalf("unexpected .env path at %d: got %q, want %q\nall paths: %v", i, paths[i], expected[i], paths)
		}
	}
}

func isolateRuntimeServerHome(t *testing.T, root string) {
	t.Helper()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
}
