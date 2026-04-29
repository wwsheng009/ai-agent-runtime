package agentconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInitGlobalConfigProviderMaxTokenAlias(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
providers:
  items:
    alias_only:
      enabled: true
      default_model: gpt-4
      max_token: 16384
    legacy_only:
      enabled: true
      default_model: gpt-4
      max_tokens_limit: 8000
    both:
      enabled: true
      default_model: gpt-4
      max_tokens_limit: 6000
      max_token: 12000
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}

	aliasOnly := cfg.Providers.Items["alias_only"]
	legacyOnly := cfg.Providers.Items["legacy_only"]
	both := cfg.Providers.Items["both"]

	if got := aliasOnly.GetMaxTokensLimit(); got != 16384 {
		t.Fatalf("alias_only max tokens = %d, want 16384", got)
	}
	if got := legacyOnly.GetMaxTokensLimit(); got != 8000 {
		t.Fatalf("legacy_only max tokens = %d, want 8000", got)
	}
	if got := both.GetMaxTokensLimit(); got != 12000 {
		t.Fatalf("both max tokens = %d, want 12000", got)
	}
}

func TestInitGlobalConfigIncludesOpenAIImageProviderForImageGenerations(t *testing.T) {
	t.Setenv("CODEX_04_API_KEYS", "shared-codex-key")
	t.Setenv("CODEX_04_BASE_URL", "https://shared-codex.example.com")

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	configPath := filepath.Join(repoRoot, "configs", "config.yaml")

	previous := globalConfig
	t.Cleanup(func() {
		globalConfig = previous
	})

	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}

	selection, err := SelectImagesGenerationsProvider(cfg, ImagesGenerationsHint{Model: "gpt-image-2"})
	if err != nil {
		t.Fatalf("SelectImagesGenerationsProvider failed: %v", err)
	}
	if selection.ProviderName != "OPENAI_IMAGE" {
		t.Fatalf("expected OPENAI_IMAGE provider, got %+v", selection)
	}
	if selection.Model != "gpt-image-2" {
		t.Fatalf("expected gpt-image-2 model, got %+v", selection)
	}
	if selection.Provider.BaseURL != "https://shared-codex.example.com" {
		t.Fatalf("expected shared CODEX_04 base URL, got %q", selection.Provider.BaseURL)
	}
	if got := selection.Provider.GetAPIKey(); got != "shared-codex-key" {
		t.Fatalf("expected shared CODEX_04 API key, got %q", got)
	}
}
