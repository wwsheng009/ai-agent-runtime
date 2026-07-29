package commands

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
