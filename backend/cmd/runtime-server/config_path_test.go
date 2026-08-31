package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func TestResolveRuntimeServerConfigPathUsesConfigsConfigFromCurrentDir(t *testing.T) {
	root := t.TempDir()
	isolateRuntimeServerHome(t, root)
	configPath := filepath.Join(root, "configs", runtimeServerDefaultConfigName)
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
	configPath := filepath.Join(root, runtimeServerDefaultConfigName)
	if err := os.WriteFile(configPath, []byte("server:\n  port: 8101\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	legacyPath := filepath.Join(root, "configs", runtimeServerDefaultConfigName)
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

func TestDefaultRuntimeServerConfigSearchPathsFollowSearchNames(t *testing.T) {
	root := t.TempDir()
	isolateRuntimeServerHome(t, root)

	expected := runtimeServerConfigSearchPathsForNames(runtimeServerConfigSearchNames())
	paths := defaultRuntimeServerConfigSearchPaths()
	if len(paths) != len(expected) {
		t.Fatalf("unexpected config path count: got %d %v, want %d %v", len(paths), paths, len(expected), expected)
	}
	for i := range expected {
		if paths[i] != expected[i] {
			t.Fatalf("unexpected config path at %d: got %q, want %q\nall paths: %v", i, paths[i], expected[i], paths)
		}
	}
}

func TestDefaultRuntimeServerDotEnvSearchPathsFollowConfigDirectories(t *testing.T) {
	root := t.TempDir()
	isolateRuntimeServerHome(t, root)

	paths := defaultRuntimeServerDotEnvSearchPaths()
	expected := runtimeServerConfigPathsForDotEnv(runtimeServerConfigSearchNames())
	if len(paths) != len(expected) {
		t.Fatalf("unexpected .env path count: got %d %v, want %d %v", len(paths), paths, len(expected), expected)
	}
	for i := range expected {
		if paths[i] != expected[i] {
			t.Fatalf("unexpected .env path at %d: got %q, want %q\nall paths: %v", i, paths[i], expected[i], paths)
		}
	}
}

func TestRuntimeServerConfigSearchNamesAlwaysFallBackToStandardName(t *testing.T) {
	names := runtimeServerConfigSearchNames()
	if len(names) == 0 || names[0] != runtimeServerDefaultConfigName {
		t.Fatalf("expected build profile name %q to lead, got %v", runtimeServerDefaultConfigName, names)
	}
	foundStandard := false
	for _, name := range names {
		if name == aiclipaths.StandardConfigFileName {
			foundStandard = true
		}
	}
	if !foundStandard {
		t.Fatalf("expected standard config file name %q in search names, got %v", aiclipaths.StandardConfigFileName, names)
	}
}

func runtimeServerConfigSearchPathsForNames(names []string) []string {
	paths := make([]string, 0, 4*len(names))
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		for _, name := range names {
			paths = append(paths, filepath.Join(home, ".aicli", name))
		}
	}
	for _, name := range names {
		paths = append(paths,
			filepath.Join(".aicli", name),
			name,
			filepath.Join("configs", name),
		)
	}
	return paths
}

func runtimeServerConfigPathsForDotEnv(names []string) []string {
	paths := make([]string, 0, 4*len(names))
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".aicli", ".env"))
	}
	paths = append(paths,
		filepath.Join(".aicli", ".env"),
		".env",
		filepath.Join("configs", ".env"),
	)
	return paths
}

func isolateRuntimeServerHome(t *testing.T, root string) {
	t.Helper()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
}
