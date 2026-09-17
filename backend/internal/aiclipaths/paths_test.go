package aiclipaths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestExpandUserPathExpandsCurrentUserHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateHome(t, home)

	if got := ExpandUserPath("~"); got != filepath.Clean(home) {
		t.Fatalf("expected home %q, got %q", filepath.Clean(home), got)
	}

	got := ExpandUserPath("~/.aicli/logs/aicli.log")
	expected := filepath.Join(home, ".aicli", "logs", "aicli.log")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestExpandUserPathExpandsWindowsSeparatorOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows separator expansion is only meaningful on Windows")
	}

	home := filepath.Join(t.TempDir(), "home")
	isolateHome(t, home)

	got := ExpandUserPath("~\\.aicli\\logs\\aicli.log")
	expected := filepath.Join(home, ".aicli", "logs", "aicli.log")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestExpandUserPathLeavesNonCurrentUserTildePathsAlone(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateHome(t, home)

	got := ExpandUserPath("~other/.aicli/logs/aicli.log")
	expected := filepath.Clean("~other/.aicli/logs/aicli.log")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDefaultRuntimeConfigSearchPathsUseActiveProfileFilename(t *testing.T) {
	got := DefaultRuntimeConfigSearchPaths()
	want := []string{
		filepath.Join("configs", DefaultRuntimeConfigFileName),
		filepath.Join("backend", "configs", DefaultRuntimeConfigFileName),
	}
	if len(got) != len(want) {
		t.Fatalf("runtime config search paths = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("runtime config search paths = %v, want %v", got, want)
		}
	}
}

func TestResolveDefaultRuntimeConfigPathFromBaseSupportsBundleAndRepositoryLayouts(t *testing.T) {
	for _, relativePath := range []string{
		filepath.Join("configs", DefaultRuntimeConfigFileName),
		filepath.Join("backend", "configs", DefaultRuntimeConfigFileName),
	} {
		t.Run(relativePath, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, relativePath)
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatalf("create config directory: %v", err)
			}
			if err := os.WriteFile(configPath, []byte("version: v1\n"), 0o644); err != nil {
				t.Fatalf("write runtime config: %v", err)
			}
			startDir := filepath.Join(root, "work", "nested")
			if err := os.MkdirAll(startDir, 0o755); err != nil {
				t.Fatalf("create start directory: %v", err)
			}

			if got := resolveDefaultConfigPathFromBase(startDir, DefaultRuntimeConfigFileName, DefaultRuntimeConfigSearchPaths()); got != configPath {
				t.Fatalf("resolved runtime config = %q, want %q", got, configPath)
			}
		})
	}
}

func TestResolveRuntimeConfigBootstrapPathPreservesExplicitPath(t *testing.T) {
	home := t.TempDir()
	isolateHome(t, home)

	// Existing .aicli configs (user-level and project-level) must not shadow a
	// genuine explicit override: explicit intent still wins over convention.
	userConfig := filepath.Join(home, ".aicli", DefaultRuntimeConfigFileName)
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o755); err != nil {
		t.Fatalf("create user config dir: %v", err)
	}
	if err := os.WriteFile(userConfig, []byte("version: user\n"), 0o644); err != nil {
		t.Fatalf("write user runtime config: %v", err)
	}
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", DefaultRuntimeConfigFileName)
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o755); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	if err := os.WriteFile(projectConfig, []byte("version: project\n"), 0o644); err != nil {
		t.Fatalf("write project runtime config: %v", err)
	}
	t.Chdir(projectDir)

	explicit := filepath.Join("custom", "runtime.yaml")
	if got := ResolveRuntimeConfigBootstrapPath(explicit); got != explicit {
		t.Fatalf("explicit runtime config = %q, want %q", got, explicit)
	}
}

// A value copied from an older template (backend/configs/runtime.yaml) is a
// convention path, not an explicit override: it must not short-circuit the
// .aicli lookups, while the repository-layout compatibility path stays
// reachable when no .aicli config exists.
func TestResolveRuntimeConfigBootstrapPathTreatsLegacyLayoutValueAsConvention(t *testing.T) {
	home := t.TempDir()
	isolateHome(t, home)

	repoDir := t.TempDir()
	legacy := filepath.Join(repoDir, "backend", "configs", DefaultRuntimeConfigFileName)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatalf("create legacy config dir: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("version: legacy\n"), 0o644); err != nil {
		t.Fatalf("write legacy runtime config: %v", err)
	}
	t.Chdir(repoDir)

	legacyValue := filepath.FromSlash(filepath.Join("backend", "configs", DefaultRuntimeConfigFileName))
	if got := ResolveRuntimeConfigBootstrapPath(legacyValue); got != legacy {
		t.Fatalf("legacy convention value = %q, want repo-layout search hit %q", got, legacy)
	}
}

func TestResolveMCPConfigPathPrefersProjectThenUserAICLIDirs(t *testing.T) {
	home := t.TempDir()
	isolateHome(t, home)

	userConfig := filepath.Join(home, ".aicli", DefaultMCPConfigFileName)
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o755); err != nil {
		t.Fatalf("create user mcp dir: %v", err)
	}
	if err := os.WriteFile(userConfig, []byte("mcp_servers: {}\n"), 0o644); err != nil {
		t.Fatalf("write user mcp config: %v", err)
	}

	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", DefaultMCPConfigFileName)
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o755); err != nil {
		t.Fatalf("create project mcp dir: %v", err)
	}
	if err := os.WriteFile(projectConfig, []byte("mcp_servers: {}\n"), 0o644); err != nil {
		t.Fatalf("write project mcp config: %v", err)
	}
	t.Chdir(projectDir)

	// The template default value (configs/mcp.yaml) is a convention path and
	// must fall through to the .aicli lookups instead of being returned as-is.
	if got := ResolveMCPConfigPath(DefaultMCPConfigRelativePath); got != projectConfig {
		t.Fatalf("project-level mcp config = %q, want %q", got, projectConfig)
	}

	// Outside the project directory the user-level file wins.
	nestedDir := filepath.Join(projectDir, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("create nested dir: %v", err)
	}
	t.Chdir(nestedDir)
	if got := ResolveMCPConfigPath(DefaultMCPConfigRelativePath); got != userConfig {
		t.Fatalf("user-level mcp config = %q, want %q", got, userConfig)
	}

	// An explicit, non-convention override still wins.
	override := filepath.Join(t.TempDir(), "selected-mcp.yaml")
	if got := ResolveMCPConfigPath(override); got != override {
		t.Fatalf("explicit mcp config = %q, want %q", got, override)
	}

	// Empty stays empty so callers keep "MCP not configured" semantics.
	if got := ResolveMCPConfigPath(""); got != "" {
		t.Fatalf("empty mcp config = %q, want empty", got)
	}
}

func TestResolveMCPConfigPathExpandsTildeAndFallsBackToPortableDefault(t *testing.T) {
	home := t.TempDir()
	isolateHome(t, home)
	emptyDir := t.TempDir()
	t.Chdir(emptyDir)

	// No .aicli candidate anywhere: the portable default is returned unchanged
	// (never a bare "mcp.yaml" that only looks in the process working directory).
	if got := ResolveMCPConfigPath(DefaultMCPConfigRelativePath); got != DefaultMCPConfigRelativePath {
		t.Fatalf("portable fallback = %q, want %q", got, DefaultMCPConfigRelativePath)
	}

	// Tilde values must expand to the user home instead of being passed through
	// literally to the MCP loader.
	tildeConfig := filepath.Join(home, ".aicli", DefaultMCPConfigFileName)
	if err := os.MkdirAll(filepath.Dir(tildeConfig), 0o755); err != nil {
		t.Fatalf("create tilde mcp dir: %v", err)
	}
	if err := os.WriteFile(tildeConfig, []byte("mcp_servers: {}\n"), 0o644); err != nil {
		t.Fatalf("write tilde mcp config: %v", err)
	}
	if got := ResolveMCPConfigPath("~/.aicli/" + DefaultMCPConfigFileName); got != tildeConfig {
		t.Fatalf("tilde mcp config = %q, want %q", got, tildeConfig)
	}
}

func TestResolveRuntimeConfigBootstrapPathPrefersUserHomeConfig(t *testing.T) {
	home := t.TempDir()
	isolateHome(t, home)

	// Write a user-level runtime config in ~/.aicli/runtime.yaml
	userConfig := filepath.Join(home, ".aicli", DefaultRuntimeConfigFileName)
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o755); err != nil {
		t.Fatalf("create user config dir: %v", err)
	}
	if err := os.WriteFile(userConfig, []byte("version: v1\n"), 0o644); err != nil {
		t.Fatalf("write user runtime config: %v", err)
	}

	// Also create a repo-level config that should NOT be picked up
	repoDir := t.TempDir()
	repoConfig := filepath.Join(repoDir, "backend", "configs", DefaultRuntimeConfigFileName)
	if err := os.MkdirAll(filepath.Dir(repoConfig), 0o755); err != nil {
		t.Fatalf("create repo config dir: %v", err)
	}
	if err := os.WriteFile(repoConfig, []byte("version: v1\n"), 0o644); err != nil {
		t.Fatalf("write repo runtime config: %v", err)
	}

	// Change to the repo directory so CWD-based search would find repoConfig
	t.Chdir(repoDir)

	// Case 1: empty configPath → should prefer user home
	got := ResolveRuntimeConfigBootstrapPath("")
	if got != userConfig {
		t.Fatalf("empty configPath: expected user home config %q, got %q", userConfig, got)
	}

	// Case 2: env-var default path (backend/configs/runtime.yaml) is an
	// explicit non-default path, but user-level config still takes priority
	// over source-tree paths. This is the core fix: the env-var default
	// backend/configs/runtime.yaml should NOT shadow ~/.aicli/runtime.yaml.
	envDefault := filepath.FromSlash(filepath.Join("backend", "configs", DefaultRuntimeConfigFileName))
	got = ResolveRuntimeConfigBootstrapPath(envDefault)
	if got != userConfig {
		t.Fatalf("env-var default configPath: expected user home config %q, got %q", userConfig, got)
	}

	// Case 3: portable default (configs/runtime.yaml) → should prefer user home
	portableDefault := filepath.FromSlash(DefaultRuntimeConfigRelativePath)
	got = ResolveRuntimeConfigBootstrapPath(portableDefault)
	if got != userConfig {
		t.Fatalf("portable default configPath: expected user home config %q, got %q", userConfig, got)
	}
}

func TestResolveRuntimeConfigBootstrapPathPrefersProjectLevelOverUserHome(t *testing.T) {
	home := t.TempDir()
	isolateHome(t, home)

	// Write a user-level runtime config in ~/.aicli/runtime.yaml
	userConfig := filepath.Join(home, ".aicli", DefaultRuntimeConfigFileName)
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o755); err != nil {
		t.Fatalf("create user config dir: %v", err)
	}
	if err := os.WriteFile(userConfig, []byte("version: v1\n"), 0o644); err != nil {
		t.Fatalf("write user runtime config: %v", err)
	}

	// Create a project-level config in ./.aicli/runtime.yaml
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", DefaultRuntimeConfigFileName)
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o755); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	if err := os.WriteFile(projectConfig, []byte("version: v2\n"), 0o644); err != nil {
		t.Fatalf("write project runtime config: %v", err)
	}

	// Change to the project directory so CWD-based search would find projectConfig
	t.Chdir(projectDir)

	// Project-level config should take priority over user-level config
	got := ResolveRuntimeConfigBootstrapPath("")
	if got != projectConfig {
		t.Fatalf("expected project config %q, got %q", projectConfig, got)
	}
}

func TestDatePartitionUsesLocalYMD(t *testing.T) {
	stamp := time.Date(2026, 7, 25, 15, 4, 5, 0, time.Local)
	year, month, day := DatePartition(stamp)
	if year != "2026" || month != "07" || day != "25" {
		t.Fatalf("unexpected partition: %s/%s/%s", year, month, day)
	}
}

func TestJoinDatePartitionNestsUnderYMD(t *testing.T) {
	stamp := time.Date(2026, 7, 25, 15, 4, 5, 0, time.Local)
	got := JoinDatePartition(filepath.Join("root", "chat-logs"), stamp, "session-id", "debug.log")
	want := filepath.Join("root", "chat-logs", "2026", "07", "25", "session-id", "debug.log")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseTimestampedSessionIDTime(t *testing.T) {
	chatID := "20260725_150405.123_ab12cd34"
	got, ok := ParseTimestampedSessionIDTime(chatID)
	if !ok {
		t.Fatal("expected chat log session id to parse")
	}
	if got.Format("20060102_150405.000") != "20260725_150405.123" {
		t.Fatalf("unexpected chat id time: %v", got)
	}

	fileID := "session_20260725150405_abcdEF12"
	got, ok = ParseTimestampedSessionIDTime(fileID)
	if !ok {
		t.Fatal("expected file session id to parse")
	}
	if got.Format("20060102150405") != "20260725150405" {
		t.Fatalf("unexpected file id time: %v", got)
	}
}

func isolateHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
}
