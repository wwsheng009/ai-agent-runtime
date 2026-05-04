package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureStarterConfigFileCreatesMinimalConfig(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	path, created, err := EnsureStarterConfigFile("")
	if err != nil {
		t.Fatalf("EnsureStarterConfigFile failed: %v", err)
	}
	if !created {
		t.Fatalf("expected starter config to be created")
	}
	if path != filepath.Clean(starterConfigRelativePath) {
		t.Fatalf("starter config path = %q, want %q", path, filepath.Clean(starterConfigRelativePath))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "providers:") {
		t.Fatalf("starter config missing providers section: %s", content)
	}
	if !strings.Contains(content, "aicli:") {
		t.Fatalf("starter config missing aicli section: %s", content)
	}

	cfg, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}
	if cfg.ConfigFilePath == "" {
		t.Fatalf("expected config file path to be recorded")
	}
	if cfg.AICLI == nil || cfg.AICLI.Chat == nil || cfg.AICLI.Chat.Stream == nil || !*cfg.AICLI.Chat.Stream {
		t.Fatalf("expected aicli.chat.stream to default to true")
	}
	if len(cfg.Providers.Items) != 0 {
		t.Fatalf("expected no providers in starter config, got %d", len(cfg.Providers.Items))
	}
}

func TestEnsureStarterConfigFileDoesNotOverwriteExistingFile(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	if err := os.MkdirAll(filepath.Dir(starterConfigRelativePath), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	original := []byte("providers:\n  default_provider: custom\n")
	if err := os.WriteFile(starterConfigRelativePath, original, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	path, created, err := EnsureStarterConfigFile("")
	if err != nil {
		t.Fatalf("EnsureStarterConfigFile failed: %v", err)
	}
	if created {
		t.Fatalf("expected existing config to be preserved")
	}
	if path != filepath.Clean(starterConfigRelativePath) {
		t.Fatalf("starter config path = %q, want %q", path, filepath.Clean(starterConfigRelativePath))
	}

	raw, err := os.ReadFile(starterConfigRelativePath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(raw) != string(original) {
		t.Fatalf("existing config was modified:\n%s", string(raw))
	}
}

func TestResolveGlobalConfigPathUsesHomeDirectory(t *testing.T) {
	home := t.TempDir()
	previous := userHomeDir
	userHomeDir = func() (string, error) {
		return home, nil
	}
	t.Cleanup(func() {
		userHomeDir = previous
	})

	path, err := ResolveGlobalConfigPath()
	if err != nil {
		t.Fatalf("ResolveGlobalConfigPath failed: %v", err)
	}
	expected := filepath.Join(home, ".aicli", "config.yaml")
	if path != expected {
		t.Fatalf("unexpected global config path: %q, want %q", path, expected)
	}
}

func TestNormalizeConfigPathExpandsTildeWithForwardSlashes(t *testing.T) {
	home := t.TempDir()
	previous := userHomeDir
	userHomeDir = func() (string, error) {
		return home, nil
	}
	t.Cleanup(func() {
		userHomeDir = previous
	})

	path := normalizeConfigPath("~/.aicli/config.yaml")
	expected := filepath.Join(home, ".aicli", "config.yaml")
	if path != expected {
		t.Fatalf("unexpected normalized path: %q, want %q", path, expected)
	}
}

func TestDefaultDotEnvSearchPathsDeriveFromConfigSearchPaths(t *testing.T) {
	home := t.TempDir()
	previous := userHomeDir
	userHomeDir = func() (string, error) {
		return home, nil
	}
	t.Cleanup(func() {
		userHomeDir = previous
	})

	paths := DefaultDotEnvSearchPaths()
	expected := []string{
		filepath.Join(home, ".aicli", ".env"),
		filepath.Join(".aicli", ".env"),
		".env",
		filepath.Join("configs", ".env"),
	}
	if strings.Join(paths, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("unexpected .env search paths:\n got: %v\nwant: %v", paths, expected)
	}
}

func TestDotEnvSearchPathsForConfigPathsDeduplicatesDirectories(t *testing.T) {
	paths := DotEnvSearchPathsForConfigPaths([]string{
		"aicli.yaml",
		"config.yaml",
		filepath.Join("configs", "config.yaml"),
		filepath.Join("configs", "override.yaml"),
	})
	expected := []string{
		".env",
		filepath.Join("configs", ".env"),
	}
	if strings.Join(paths, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("unexpected deduplicated .env paths:\n got: %v\nwant: %v", paths, expected)
	}
}

func TestResolveDotEnvPathSkipsDirectories(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	envDir := filepath.Join(".aicli", ".env")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(".env", []byte("AICLI_TEST_ENV=1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	path := ResolveDotEnvPath([]string{envDir, ".env"})
	if path != ".env" {
		t.Fatalf("unexpected .env path: %q, want .env", path)
	}
}
