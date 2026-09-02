package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func TestEnsureStarterConfigFileCreatesMinimalConfig(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	home := filepath.Join(tempDir, "home")
	previousHome := userHomeDir
	userHomeDir = func() (string, error) {
		return home, nil
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
		userHomeDir = previousHome
	})

	path, created, err := EnsureStarterConfigFile("")
	if err != nil {
		t.Fatalf("EnsureStarterConfigFile failed: %v", err)
	}
	if !created {
		t.Fatalf("expected starter config to be created")
	}
	expectedPath := filepath.Join(home, starterConfigRelativePath)
	if path != expectedPath {
		t.Fatalf("starter config path = %q, want %q", path, expectedPath)
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
	if !strings.Contains(content, "headers: {}") {
		t.Fatalf("starter config missing global provider headers section: %s", content)
	}
	if !strings.Contains(content, "config_file: "+aiclipaths.DefaultRuntimeConfigRelativePath) {
		t.Fatalf("starter config missing build-profile runtime config path: %s", content)
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
	if cfg.SkillsRuntime == nil || cfg.SkillsRuntime.ConfigFile != aiclipaths.DefaultRuntimeConfigRelativePath {
		t.Fatalf("expected build-profile runtime config default, got %+v", cfg.SkillsRuntime)
	}
}

func TestEnsureStarterConfigFileDoesNotOverwriteExistingFile(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "custom.yaml")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	original := []byte("providers:\n  default_provider: custom\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	gotPath, created, err := EnsureStarterConfigFile(path)
	if err != nil {
		t.Fatalf("EnsureStarterConfigFile failed: %v", err)
	}
	if created {
		t.Fatalf("expected existing config to be preserved")
	}
	if gotPath != path {
		t.Fatalf("starter config path = %q, want %q", gotPath, path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(raw) != string(original) {
		t.Fatalf("existing config was modified:\n%s", string(raw))
	}
}

func TestEnsureStarterConfigFileFallsBackToLocalWhenHomeUnavailable(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	previousHome := userHomeDir
	userHomeDir = func() (string, error) {
		return "", os.ErrNotExist
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
		userHomeDir = previousHome
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
	expected := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	if path != expected {
		t.Fatalf("unexpected global config path: %q, want %q", path, expected)
	}
}

func TestDefaultConfigSearchPathsUseBuildProfileNames(t *testing.T) {
	home := t.TempDir()
	previous := userHomeDir
	userHomeDir = func() (string, error) {
		return home, nil
	}
	t.Cleanup(func() {
		userHomeDir = previous
	})

	paths := DefaultConfigSearchPaths()
	expected := []string{
		filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName),
	}
	if aiclipaths.DefaultConfigFileName != aiclipaths.StandardConfigFileName {
		expected = append(expected, filepath.Join(home, ".aicli", aiclipaths.StandardConfigFileName))
	}
	expected = append(expected,
		filepath.Join(".aicli", aiclipaths.DefaultConfigFileName),
		aiclipaths.DefaultCLIConfigFileName,
		filepath.Join("configs", aiclipaths.DefaultConfigFileName),
	)
	if aiclipaths.DefaultConfigFileName != aiclipaths.StandardConfigFileName {
		expected = append(expected,
			filepath.Join(".aicli", aiclipaths.StandardConfigFileName),
			filepath.Join("configs", aiclipaths.StandardConfigFileName),
		)
	}
	if strings.Join(paths, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("unexpected %s config search paths:\n got: %v\nwant: %v", aiclipaths.BuildProfile, paths, expected)
	}
}

// TestResolveConfigPathFallsBackToStandardConfigName verifies that a non-main
// build profile (for example win7compat) still discovers the standard-layout
// config.yaml when no profile-specific file exists, so reads and writes stay
// on the same file as the standard runtime-server / web UI.
func TestResolveConfigPathFallsBackToStandardConfigName(t *testing.T) {
	home := t.TempDir()
	previous := userHomeDir
	userHomeDir = func() (string, error) {
		return home, nil
	}
	t.Cleanup(func() {
		userHomeDir = previous
	})

	standardPath := filepath.Join(home, ".aicli", aiclipaths.StandardConfigFileName)
	if err := os.MkdirAll(filepath.Dir(standardPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standardPath, []byte("auth:\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ResolveConfigPath(DefaultConfigSearchPaths())
	if got != standardPath {
		t.Fatalf("expected standard config %q to be discovered, got %q", standardPath, got)
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

func TestExplicitConfigPathFromArgsUsesLastFlagBeforeSeparator(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "long flag with separate value",
			args: []string{"serve", "--config", filepath.Join("profiles", "first.yaml")},
			want: filepath.Join("profiles", "first.yaml"),
		},
		{
			name: "short flag with equals",
			args: []string{"-c=" + filepath.Join("profiles", "short.yaml"), "chat"},
			want: filepath.Join("profiles", "short.yaml"),
		},
		{
			name: "attached short flag",
			args: []string{"-c" + filepath.Join("profiles", "attached.yaml"), "chat"},
			want: filepath.Join("profiles", "attached.yaml"),
		},
		{
			name: "last repeated flag wins",
			args: []string{
				"--config=first.yaml",
				"chat",
				"-c",
				filepath.Join("profiles", "last.yaml"),
			},
			want: filepath.Join("profiles", "last.yaml"),
		},
		{
			name: "flags after separator are ignored",
			args: []string{"chat", "--", "--config", "ignored.yaml"},
			want: "",
		},
		{
			name: "unrelated args",
			args: []string{"chat", "--message", "hello"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExplicitConfigPathFromArgs(tt.args); got != tt.want {
				t.Fatalf("ExplicitConfigPathFromArgs(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestStartupDotEnvSearchPathsPrefersExplicitConfigDirectory(t *testing.T) {
	paths := StartupDotEnvSearchPaths(
		[]string{"chat", "--config", filepath.Join("profiles", "custom.yaml")},
		[]string{
			filepath.Join("configs", "config.yaml"),
			filepath.Join("profiles", "fallback.yaml"),
			"config.yaml",
		},
	)
	expected := []string{
		filepath.Join("profiles", ".env"),
		filepath.Join("configs", ".env"),
		".env",
	}
	if strings.Join(paths, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("unexpected startup .env paths:\n got: %v\nwant: %v", paths, expected)
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
