package commands

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestLoadCachedRuntimeConfig_ReusesSamePath(t *testing.T) {
	resetChatRuntimeConfigCacheForTest()
	t.Cleanup(resetChatRuntimeConfigCacheForTest)

	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	content := []byte("agent:\n  defaultModel: cached-model\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	first, loadedPath, err := loadCachedRuntimeConfig(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if first == nil {
		t.Fatal("expected config")
	}
	if first.Agent.DefaultModel != "cached-model" {
		t.Fatalf("unexpected model %q", first.Agent.DefaultModel)
	}
	if loadedPath == "" {
		t.Fatal("expected loaded path")
	}

	// Mutate the returned clone; cache baseline must stay intact.
	first.Agent.DefaultModel = "mutated"
	second, _, err := loadCachedRuntimeConfig(path)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if second == nil {
		t.Fatal("expected second config")
	}
	if second.Agent.DefaultModel != "cached-model" {
		t.Fatalf("cache baseline mutated: got %q", second.Agent.DefaultModel)
	}

	// Content change should invalidate cache.
	if err := os.WriteFile(path, []byte("agent:\n  defaultModel: refreshed-model\n"), 0o644); err != nil {
		t.Fatalf("rewrite runtime config: %v", err)
	}
	third, _, err := loadCachedRuntimeConfig(path)
	if err != nil {
		t.Fatalf("third load: %v", err)
	}
	if third == nil || third.Agent.DefaultModel != "refreshed-model" {
		t.Fatalf("expected refreshed model, got %#v", third)
	}
}

func TestLoadCachedRuntimeConfig_MissingFileIsSoftMiss(t *testing.T) {
	resetChatRuntimeConfigCacheForTest()
	t.Cleanup(resetChatRuntimeConfigCacheForTest)

	missing := filepath.Join(t.TempDir(), "missing-runtime.yaml")
	cfg, loadedPath, err := loadCachedRuntimeConfig(missing)
	if err != nil {
		t.Fatalf("expected soft miss without error, got %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for missing file, got %#v", cfg)
	}
	if loadedPath != missing {
		t.Fatalf("expected loaded path %q, got %q", missing, loadedPath)
	}
}

// TestLoadCachedRuntimeConfig_MergesUserAndProjectLayers pins the merge
// semantics for the .aicli runtime.yaml layers: the project layer overrides only
// the keys it writes, so the user layer's remaining settings survive.
func TestLoadCachedRuntimeConfig_MergesUserAndProjectLayers(t *testing.T) {
	resetChatRuntimeConfigCacheForTest()
	t.Cleanup(resetChatRuntimeConfigCacheForTest)

	home := isolateInitHome(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	workspace := t.TempDir()
	t.Chdir(workspace)

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeTestFile(t, userConfig, "agent:\n  maxSteps: 7\n  defaultModel: user-model\n")
	projectConfig := filepath.Join(workspace, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeTestFile(t, projectConfig, "agent:\n  defaultModel: project-model\n")

	merged, loadedPath, err := loadCachedRuntimeConfig(projectConfig)
	if err != nil {
		t.Fatalf("merged load: %v", err)
	}
	if merged == nil {
		t.Fatal("expected merged config")
	}
	if merged.Agent.MaxMaxSteps != 7 {
		t.Fatalf("user-layer key lost in merge: maxSteps = %d, want 7", merged.Agent.MaxMaxSteps)
	}
	if merged.Agent.DefaultModel != "project-model" {
		t.Fatalf("project layer must override the user layer: model = %q", merged.Agent.DefaultModel)
	}
	if filepath.Clean(loadedPath) != filepath.Clean(projectConfig) {
		t.Fatalf("effective source = %q, want the highest present layer %q", loadedPath, projectConfig)
	}

	// Both layer paths name the same merged stack: entering through the user
	// layer must still see the project override.
	viaUser, userPath, err := loadCachedRuntimeConfig(userConfig)
	if err != nil {
		t.Fatalf("user-layer entry point: %v", err)
	}
	if viaUser == nil || viaUser.Agent.DefaultModel != "project-model" {
		t.Fatalf("user-layer entry point must load the merged stack, got %#v", viaUser)
	}
	if filepath.Clean(userPath) != filepath.Clean(projectConfig) {
		t.Fatalf("user-layer entry point source = %q, want %q", userPath, projectConfig)
	}

	// Editing a layer must invalidate the merged cache.
	writeTestFile(t, userConfig, "agent:\n  maxSteps: 11\n  defaultModel: user-model\n")
	reloaded, _, err := loadCachedRuntimeConfig(projectConfig)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Agent.MaxMaxSteps != 11 {
		t.Fatalf("merged cache not invalidated: maxSteps = %d, want 11", reloaded.Agent.MaxMaxSteps)
	}
}

// TestLoadCachedRuntimeConfig_ExplicitPathIsNotMerged: an explicit (non-layer)
// path is a deliberate single-file selection, so .aicli layers must not leak in.
func TestLoadCachedRuntimeConfig_ExplicitPathIsNotMerged(t *testing.T) {
	resetChatRuntimeConfigCacheForTest()
	t.Cleanup(resetChatRuntimeConfigCacheForTest)

	home := isolateInitHome(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())

	writeTestFile(t, filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName), "agent:\n  maxSteps: 7\n")

	explicit := filepath.Join(t.TempDir(), "explicit-runtime.yaml")
	writeTestFile(t, explicit, "agent:\n  defaultModel: explicit-model\n")

	cfg, loadedPath, err := loadCachedRuntimeConfig(explicit)
	if err != nil {
		t.Fatalf("explicit load: %v", err)
	}
	if cfg == nil || cfg.Agent.DefaultModel != "explicit-model" {
		t.Fatalf("explicit config = %#v, want defaultModel=explicit-model", cfg)
	}
	if want := runtimecfg.DefaultRuntimeConfig().Agent.MaxMaxSteps; cfg.Agent.MaxMaxSteps != want {
		t.Fatalf("explicit path must not merge .aicli layers: maxSteps = %d, want %d", cfg.Agent.MaxMaxSteps, want)
	}
	if filepath.Clean(loadedPath) != filepath.Clean(explicit) {
		t.Fatalf("loaded path = %q, want %q", loadedPath, explicit)
	}
}

func TestFormatRuntimeConfigLoadFallback(t *testing.T) {
	t.Parallel()

	missingPath := filepath.Join("backend", "configs", "runtime.yaml")
	gotMissing := formatRuntimeConfigLoadFallback(missingPath, nil)
	if !strings.Contains(gotMissing, "未找到配置文件") || !strings.Contains(gotMissing, missingPath) {
		t.Fatalf("missing-file fallback = %q", gotMissing)
	}
	if strings.Contains(gotMissing, "<nil>") {
		t.Fatalf("fallback must not render bare nil error: %q", gotMissing)
	}

	loadErr := errors.New("yaml: parse error")
	gotErr := formatRuntimeConfigLoadFallback(missingPath, loadErr)
	if !strings.Contains(gotErr, missingPath) || !strings.Contains(gotErr, loadErr.Error()) {
		t.Fatalf("error fallback = %q", gotErr)
	}

	if got := formatRuntimeConfigLoadFallback("", nil); got != "配置为空" {
		t.Fatalf("empty fallback = %q", got)
	}
}

func TestLoadRuntimeToolConfig_MissingFileFallsBackWithoutNilWarning(t *testing.T) {
	resetChatRuntimeConfigCacheForTest()
	t.Cleanup(resetChatRuntimeConfigCacheForTest)

	missing := filepath.Join(t.TempDir(), "does-not-exist-runtime.yaml")
	session := &ChatSession{RuntimeConfigPath: missing}

	var cfgLoaded bool
	stderr := captureStderr(t, func() {
		cfg := loadRuntimeToolConfig(nil, session)
		cfgLoaded = cfg != nil
	})
	if !cfgLoaded {
		t.Fatal("expected default runtime config")
	}
	if !strings.Contains(stderr, "加载 runtime tools 配置失败") {
		t.Fatalf("expected runtime tools warning, got %q", stderr)
	}
	if !strings.Contains(stderr, "未找到配置文件") || !strings.Contains(stderr, missing) {
		t.Fatalf("expected missing-path reason, got %q", stderr)
	}
	if strings.Contains(stderr, "<nil>") {
		t.Fatalf("warning must not include bare <nil>: %q", stderr)
	}
}

func TestLoadLocalChatRuntimeConfig_MissingFileFallsBackWithoutNilWarning(t *testing.T) {
	resetChatRuntimeConfigCacheForTest()
	t.Cleanup(resetChatRuntimeConfigCacheForTest)

	missing := filepath.Join(t.TempDir(), "does-not-exist-actor-runtime.yaml")
	session := &ChatSession{RuntimeConfigPath: missing}

	var (
		cfgLoaded bool
		loadErr   error
	)
	stderr := captureStderr(t, func() {
		cfg, err := loadLocalChatRuntimeConfig(nil, session)
		cfgLoaded = cfg != nil
		loadErr = err
	})
	if loadErr != nil {
		t.Fatalf("unexpected error: %v", loadErr)
	}
	if !cfgLoaded {
		t.Fatal("expected default runtime config")
	}
	if !strings.Contains(stderr, "加载 actor runtime 配置失败") {
		t.Fatalf("expected actor runtime warning, got %q", stderr)
	}
	if !strings.Contains(stderr, "未找到配置文件") || !strings.Contains(stderr, missing) {
		t.Fatalf("expected missing-path reason, got %q", stderr)
	}
	if strings.Contains(stderr, "<nil>") {
		t.Fatalf("warning must not include bare <nil>: %q", stderr)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = original
		_ = w.Close()
		_ = r.Close()
	}()

	fn()
	os.Stderr = original
	_ = w.Close()
	output, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read captured stderr: %v", readErr)
	}
	return string(output)
}
