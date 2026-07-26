package commands

import (
	"os"
	"path/filepath"
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
