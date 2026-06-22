package agentconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestInitGlobalConfigLoadsSubagentRouting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
aicli:
  subagents:
    routing:
      enabled: true
      compatibility_mode: strict
      default_difficulty: normal
      unsupported_reasoning_policy: downgrade
      allow_explicit_model_override: true
      allow_explicit_reasoning_override: true
      allowed_provider_overrides:
        - strong
      allowed_model_overrides:
        - strong-model
      levels:
        hard:
          provider: strong
          model: strong-model
          reasoning_effort: high
          max_tokens: 12000
      roles:
        verifier:
          hard:
            provider: verify
            model: verify-model
            thinking_effort: medium
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}
	if cfg.AICLI == nil || cfg.AICLI.Subagents == nil || cfg.AICLI.Subagents.Routing == nil {
		t.Fatal("expected subagent routing config")
	}
	routing := cfg.AICLI.Subagents.Routing
	if routing.Enabled == nil || !*routing.Enabled {
		t.Fatalf("expected routing enabled, got %#v", routing.Enabled)
	}
	if routing.CompatibilityMode != "strict" {
		t.Fatalf("unexpected compatibility mode: %q", routing.CompatibilityMode)
	}
	if routing.UnsupportedReasoningPolicy != "downgrade" {
		t.Fatalf("unexpected unsupported reasoning policy: %q", routing.UnsupportedReasoningPolicy)
	}
	if routing.Levels["hard"].Provider != "strong" || routing.Levels["hard"].Model != "strong-model" {
		t.Fatalf("unexpected hard route: %#v", routing.Levels["hard"])
	}
	if len(routing.AllowedProviderOverrides) != 1 || routing.AllowedProviderOverrides[0] != "strong" {
		t.Fatalf("unexpected allowed provider overrides: %#v", routing.AllowedProviderOverrides)
	}
	if len(routing.AllowedModelOverrides) != 1 || routing.AllowedModelOverrides[0] != "strong-model" {
		t.Fatalf("unexpected allowed model overrides: %#v", routing.AllowedModelOverrides)
	}
	if !routing.AllowExplicitReasoningOverride {
		t.Fatal("expected explicit reasoning override to be allowed")
	}
	if routing.Roles["verifier"]["hard"].ThinkingEffort != "medium" {
		t.Fatalf("unexpected verifier route: %#v", routing.Roles["verifier"]["hard"])
	}
}

func TestInitGlobalConfigRejectsInvalidEnabledSubagentRoutingCompatibilityMode(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
aicli:
  subagents:
    routing:
      enabled: true
      compatibility_mode: loose
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	_, err := InitGlobalConfig(configPath)
	if err == nil {
		t.Fatal("expected invalid routing compatibility mode error")
	}
	if !strings.Contains(err.Error(), "compatibility_mode") {
		t.Fatalf("expected compatibility_mode error, got %v", err)
	}
}

func TestInitGlobalConfigRejectsInvalidEnabledSubagentRoutingDefaultDifficulty(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
aicli:
  subagents:
    routing:
      enabled: true
      default_difficulty: impossible
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	_, err := InitGlobalConfig(configPath)
	if err == nil {
		t.Fatal("expected invalid routing default difficulty error")
	}
	if !strings.Contains(err.Error(), "default_difficulty") {
		t.Fatalf("expected default_difficulty error, got %v", err)
	}
}

func TestInitGlobalConfigRejectsInvalidEnabledSubagentRoutingReasoningPolicy(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
aicli:
  subagents:
    routing:
      enabled: true
      unsupported_reasoning_policy: explode
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	_, err := InitGlobalConfig(configPath)
	if err == nil {
		t.Fatal("expected invalid unsupported reasoning policy error")
	}
	if !strings.Contains(err.Error(), "unsupported_reasoning_policy") {
		t.Fatalf("expected unsupported_reasoning_policy error, got %v", err)
	}
}

func TestInitGlobalConfigAllowsInvalidDisabledSubagentRoutingDraft(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
aicli:
  subagents:
    routing:
      enabled: false
      default_difficulty: impossible
      levels:
        super-hard:
          max_tokens: -1
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}
	if cfg.AICLI == nil || cfg.AICLI.Subagents == nil || cfg.AICLI.Subagents.Routing == nil {
		t.Fatal("expected disabled routing draft config to load")
	}
}

func TestInitGlobalConfigRejectsIncompleteRouteWhenSubagentRoutingMustNotInherit(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
aicli:
  subagents:
    routing:
      enabled: true
      inherit_parent_when_missing: false
      levels:
        hard:
          provider: strong
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	_, err := InitGlobalConfig(configPath)
	if err == nil {
		t.Fatal("expected incomplete subagent route error")
	}
	if !strings.Contains(err.Error(), "provider and model") {
		t.Fatalf("expected provider/model completeness error, got %v", err)
	}
}

func TestJoinBaseURLAndPathDedupesOverlappingPathSegments(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		requestPath string
		want        string
	}{
		{
			name:        "openai version prefix",
			baseURL:     "https://api.example.com/v1",
			requestPath: "/v1/models",
			want:        "https://api.example.com/v1/models",
		},
		{
			name:        "nested version prefix",
			baseURL:     "https://api.example.com/api/v1",
			requestPath: "/v1/chat/completions",
			want:        "https://api.example.com/api/v1/chat/completions",
		},
		{
			name:        "chatgpt codex prefix",
			baseURL:     "https://chatgpt.com/backend-api/codex",
			requestPath: "/backend-api/codex/models",
			want:        "https://chatgpt.com/backend-api/codex/models",
		},
		{
			name:        "plain host keeps request path",
			baseURL:     "https://api.example.com",
			requestPath: "/v1/models",
			want:        "https://api.example.com/v1/models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := JoinBaseURLAndPath(tt.baseURL, tt.requestPath); got != tt.want {
				t.Fatalf("JoinBaseURLAndPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildUpstreamURLWithPathDedupesVersionPrefixInBaseURL(t *testing.T) {
	got := BuildUpstreamURLWithPath(Provider{
		BaseURL: "https://api.example.com/v1",
	}, "/v1/responses", "", "gpt-5")
	want := "https://api.example.com/v1/responses"
	if got != want {
		t.Fatalf("BuildUpstreamURLWithPath() = %q, want %q", got, want)
	}
}

func TestBuildUpstreamURLWithRelativeForwardURLDedupesBasePath(t *testing.T) {
	got := BuildUpstreamURLWithPath(Provider{
		BaseURL:    "https://chatgpt.com/backend-api/codex/responses",
		ForwardURL: "/responses",
	}, "/responses", "", "gpt-5")
	want := "https://chatgpt.com/backend-api/codex/responses"
	if got != want {
		t.Fatalf("BuildUpstreamURLWithPath() = %q, want %q", got, want)
	}
}

func TestInitGlobalConfigMimoAnthropicCapabilities(t *testing.T) {
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

	provider, ok := cfg.Providers.Items["mimo_anthropic"]
	if !ok {
		t.Fatal("mimo_anthropic provider not found")
	}

	if got := provider.GetMaxTokensLimit(); got != 131072 {
		t.Fatalf("mimo_anthropic max tokens = %d, want 131072", got)
	}
	if got := provider.DefaultModel; got != "mimo-v2.5-pro" {
		t.Fatalf("mimo_anthropic default model = %q, want mimo-v2.5-pro", got)
	}

	if len(provider.SupportedModels) != 2 {
		t.Fatalf("mimo_anthropic supported models = %d, want 2", len(provider.SupportedModels))
	}
	for _, want := range []string{"mimo-v2.5-pro", "mimo-v2-pro"} {
		found := false
		for _, got := range provider.SupportedModels {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("mimo_anthropic supported models missing %q: %#v", want, provider.SupportedModels)
		}
	}

	for _, model := range []string{"mimo-v2.5-pro", "mimo-v2-pro"} {
		capability, ok := provider.ModelCapabilities[model]
		if !ok {
			t.Fatalf("model capability %q not found", model)
		}
		if got := capability.MaxContextTokens; got != 1000000 {
			t.Fatalf("%s max context tokens = %d, want 1000000", model, got)
		}
		if got := capability.MaxTokens; got != 131072 {
			t.Fatalf("%s max tokens = %d, want 131072", model, got)
		}
		if !capability.ReasoningModel {
			t.Fatalf("%s reasoning_model = false, want true", model)
		}
	}

	if got := provider.RequestsPerMinute; got != 100 {
		t.Fatalf("mimo_anthropic requests_per_minute = %d, want 100", got)
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
