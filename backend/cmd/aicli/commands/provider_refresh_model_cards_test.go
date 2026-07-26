package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestRunProviderRefreshModelCards_OverwritesStaleCardFields(t *testing.T) {
	dir := t.TempDir()
	setTestUserProfileDir(t, dir)

	cardsPath := filepath.Join(dir, "model_cards.yaml")
	if err := os.WriteFile(cardsPath, []byte(strings.TrimSpace(`
version: 1
cards:
  - id: test.gpt-5.4.codex
    title: GPT 5.4 Codex
    priority: 200
    provider_template: codex.responses
    match:
      model_ids:
        - gpt-5.4
      protocols:
        - codex
    capability:
      max_context_tokens: 1050000
      auto_compact_token_limit: 945000
      input_modalities:
        - text
        - image
      native_tools:
        image_generation: true
      reasoning_model: true
      default_reasoning_effort: medium
      reasoning_efforts:
        - low
        - medium
        - high
        - xhigh
`)+"\n"), 0o644); err != nil {
		t.Fatalf("write cards: %v", err)
	}

	cfg, path := writeProviderCommandConfig(t, `
providers:
  default_provider: openai_codex
  items:
    openai_codex:
      enabled: true
      protocol: codex
      base_url: https://api.openai.com/v1
      default_model: gpt-5.4
      supported_models:
        - gpt-5.4
        - local-only-model
      model_capabilities:
        gpt-5.4:
          max_context_tokens: 270000
          auto_compact_token_limit: 180000
          input_modalities:
            - text
          reasoning_model: true
          default_reasoning_effort: low
          reasoning_efforts:
            - low
            - medium
          max_tokens: 8192
          supports_remote_compact: true
        local-only-model:
          max_context_tokens: 12345
          input_modalities:
            - text
`)

	result, err := runProviderRefreshModelCardsCommand(cfg, providerRefreshModelCardsRequest{
		Names:                []string{"openai_codex"},
		ModelCardCatalogPath: cardsPath,
		NoUserCards:          true,
		Strict:               true,
	})
	if err != nil {
		t.Fatalf("runProviderRefreshModelCardsCommand: %v", err)
	}
	if result.UpdatedCount != 1 || result.ChangedModels < 1 {
		t.Fatalf("unexpected summary: %+v", result)
	}
	if len(result.Providers) != 1 || !result.Providers[0].Updated {
		t.Fatalf("unexpected providers: %+v", result.Providers)
	}

	provider := cfg.Providers.Items["openai_codex"]
	got := provider.ModelCapabilities["gpt-5.4"]
	if got.MaxContextTokens != 1050000 {
		t.Fatalf("expected max_context_tokens 1050000, got %d", got.MaxContextTokens)
	}
	if got.AutoCompactTokenLimit != 945000 {
		t.Fatalf("expected auto_compact_token_limit 945000, got %d", got.AutoCompactTokenLimit)
	}
	if strings.Join(got.InputModalities, ",") != "text,image" {
		t.Fatalf("unexpected input_modalities: %+v", got.InputModalities)
	}
	if !got.NativeTools.ImageGeneration {
		t.Fatal("expected image_generation=true")
	}
	if strings.Join(got.ReasoningEfforts, ",") != "low,medium,high,xhigh" {
		t.Fatalf("unexpected reasoning_efforts: %+v", got.ReasoningEfforts)
	}
	if got.DefaultReasoningEffort != "medium" {
		t.Fatalf("expected default_reasoning_effort medium, got %q", got.DefaultReasoningEffort)
	}
	// max_tokens is now card-managed for gpt-5.4 (builtin/catalog fill); local value should be overwritten.
	if got.MaxTokens != 128000 {
		t.Fatalf("expected card max_tokens 128000, got %d", got.MaxTokens)
	}
	// card-unmanaged local field should remain
	if !got.SupportsRemoteCompact {
		t.Fatalf("expected local supports_remote_compact preserved, got %+v", got)
	}
	// unmatched model should stay untouched
	local := provider.ModelCapabilities["local-only-model"]
	if local.MaxContextTokens != 12345 {
		t.Fatalf("unmatched model overwritten: %+v", local)
	}

	// persisted config should match memory
	reloaded, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	persisted := reloaded.Providers.Items["openai_codex"].ModelCapabilities["gpt-5.4"]
	if persisted.MaxContextTokens != 1050000 || strings.Join(persisted.ReasoningEfforts, ",") != "low,medium,high,xhigh" {
		t.Fatalf("persisted capability mismatch: %+v", persisted)
	}
}

func TestRunProviderRefreshModelCards_DryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	setTestUserProfileDir(t, dir)

	cardsPath := filepath.Join(dir, "model_cards.yaml")
	if err := os.WriteFile(cardsPath, []byte(strings.TrimSpace(`
version: 1
cards:
  - id: test.gpt-5.4.codex
    priority: 100
    match:
      model_ids: [gpt-5.4]
      protocols: [codex]
    capability:
      max_context_tokens: 1050000
      reasoning_efforts: [low, medium, high]
`)+"\n"), 0o644); err != nil {
		t.Fatalf("write cards: %v", err)
	}

	cfg, path := writeProviderCommandConfig(t, `
providers:
  items:
    openai_codex:
      enabled: true
      protocol: codex
      default_model: gpt-5.4
      supported_models: [gpt-5.4]
      model_capabilities:
        gpt-5.4:
          max_context_tokens: 270000
          reasoning_efforts: [low]
`)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config before: %v", err)
	}

	result, err := runProviderRefreshModelCardsCommand(cfg, providerRefreshModelCardsRequest{
		Names:                []string{"openai_codex"},
		DryRun:               true,
		ModelCardCatalogPath: cardsPath,
		NoUserCards:          true,
		Strict:               true,
	})
	if err != nil {
		t.Fatalf("runProviderRefreshModelCardsCommand: %v", err)
	}
	if !result.DryRun || result.UpdatedCount != 1 || result.ChangedModels != 1 {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("dry-run mutated config file")
	}
	// memory should also remain unchanged for dry-run
	if cfg.Providers.Items["openai_codex"].ModelCapabilities["gpt-5.4"].MaxContextTokens != 270000 {
		t.Fatalf("dry-run mutated in-memory config: %+v", cfg.Providers.Items["openai_codex"].ModelCapabilities["gpt-5.4"])
	}
}

func TestRunProviderRefreshModelCards_ProtocolFilterAndSkipFallback(t *testing.T) {
	dir := t.TempDir()
	setTestUserProfileDir(t, dir)

	cardsPath := filepath.Join(dir, "model_cards.yaml")
	if err := os.WriteFile(cardsPath, []byte(strings.TrimSpace(`
version: 1
cards:
  - id: test.gpt-5.4.codex
    priority: 100
    match:
      model_ids: [gpt-5.4]
      protocols: [codex]
    capability:
      max_context_tokens: 1050000
  - id: fallback.openai
    fallback: true
    priority: 1
    match:
      protocols: [openai]
    capability:
      max_context_tokens: 128000
`)+"\n"), 0o644); err != nil {
		t.Fatalf("write cards: %v", err)
	}

	cfg, _ := writeProviderCommandConfig(t, `
providers:
  items:
    openai_codex:
      enabled: true
      protocol: codex
      default_model: gpt-5.4
      supported_models: [gpt-5.4]
      model_capabilities:
        gpt-5.4:
          max_context_tokens: 270000
    openai_chat:
      enabled: true
      protocol: openai
      default_model: gpt-4.1
      supported_models: [gpt-4.1]
      model_capabilities:
        gpt-4.1:
          max_context_tokens: 100000
`)

	result, err := runProviderRefreshModelCardsCommand(cfg, providerRefreshModelCardsRequest{
		Protocol:             "codex",
		ModelCardCatalogPath: cardsPath,
		NoUserCards:          true,
		Strict:               true,
	})
	if err != nil {
		t.Fatalf("runProviderRefreshModelCardsCommand: %v", err)
	}
	if len(result.Providers) != 1 || result.Providers[0].Name != "openai_codex" {
		t.Fatalf("protocol filter failed: %+v", result.Providers)
	}
	if cfg.Providers.Items["openai_codex"].ModelCapabilities["gpt-5.4"].MaxContextTokens != 1050000 {
		t.Fatalf("codex provider not refreshed")
	}
	if cfg.Providers.Items["openai_chat"].ModelCapabilities["gpt-4.1"].MaxContextTokens != 100000 {
		t.Fatalf("openai provider should remain untouched by protocol filter")
	}

	// fallback-only should skip without overwrite
	result2, err := runProviderRefreshModelCardsCommand(cfg, providerRefreshModelCardsRequest{
		Names:                []string{"openai_chat"},
		ModelCardCatalogPath: cardsPath,
		NoUserCards:          true,
		Strict:               true,
	})
	if err != nil {
		t.Fatalf("fallback refresh: %v", err)
	}
	if result2.UpdatedCount != 0 {
		t.Fatalf("expected fallback-only skip, got %+v", result2)
	}
	if len(result2.Providers) != 1 || result2.Providers[0].SkippedModels == 0 {
		t.Fatalf("expected skipped models for fallback-only: %+v", result2.Providers)
	}
	if got := result2.Providers[0].Skipped[0].Reason; got != "fallback_only" && got != "no_matching_card" {
		// resolve may return fallback cards; reason should reflect fallback-only
		t.Fatalf("unexpected skip reason: %q (%+v)", got, result2.Providers[0].Skipped)
	}
	if cfg.Providers.Items["openai_chat"].ModelCapabilities["gpt-4.1"].MaxContextTokens != 100000 {
		t.Fatalf("fallback card overwrote existing capability")
	}
}

func TestRunProviderRefreshModelCards_Idempotent(t *testing.T) {
	dir := t.TempDir()
	setTestUserProfileDir(t, dir)

	cardsPath := filepath.Join(dir, "model_cards.yaml")
	if err := os.WriteFile(cardsPath, []byte(strings.TrimSpace(`
version: 1
cards:
  - id: test.gpt-5.4.codex
    priority: 100
    match:
      model_ids: [gpt-5.4]
      protocols: [codex]
    capability:
      max_context_tokens: 1050000
      reasoning_efforts: [low, medium, high]
`)+"\n"), 0o644); err != nil {
		t.Fatalf("write cards: %v", err)
	}

	cfg, _ := writeProviderCommandConfig(t, `
providers:
  items:
    openai_codex:
      enabled: true
      protocol: codex
      default_model: gpt-5.4
      supported_models: [gpt-5.4]
      model_capabilities:
        gpt-5.4:
          max_context_tokens: 270000
          reasoning_efforts: [low]
`)

	first, err := runProviderRefreshModelCardsCommand(cfg, providerRefreshModelCardsRequest{
		Names:                []string{"openai_codex"},
		ModelCardCatalogPath: cardsPath,
		NoUserCards:          true,
		Strict:               true,
	})
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if first.UpdatedCount != 1 {
		t.Fatalf("expected first update, got %+v", first)
	}

	second, err := runProviderRefreshModelCardsCommand(cfg, providerRefreshModelCardsRequest{
		Names:                []string{"openai_codex"},
		ModelCardCatalogPath: cardsPath,
		NoUserCards:          true,
		Strict:               true,
	})
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if second.UpdatedCount != 0 || second.ChangedModels != 0 {
		t.Fatalf("expected idempotent no-op, got %+v", second)
	}
	if second.Providers[0].Reason != "already_up_to_date" {
		t.Fatalf("unexpected reason: %+v", second.Providers[0])
	}
}

func TestCollectProviderModelsForCapabilityRefresh(t *testing.T) {
	models := collectProviderModelsForCapabilityRefresh(config.Provider{
		DefaultModel:    "gpt-5.4",
		SupportedModels: []string{"gpt-5.4", "*", "gpt-5.5", "gpt-5.4"},
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			"local-model": {},
			"gpt-5.5":     {},
		},
	})
	if strings.Join(models, ",") != "gpt-5.4,gpt-5.5,local-model" {
		t.Fatalf("unexpected models: %+v", models)
	}
}
