package agentconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInitGlobalConfigLoadsProviderRetrySchedule(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
providers:
  max_retries: -1
  backoff:
    max_interval: 6m
    randomization: 0.1
    schedule: [30s, 1m, 2m, 3m, 5m]
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}
	if cfg.Providers.MaxRetries != -1 {
		t.Fatalf("expected unlimited transient retries, got %d", cfg.Providers.MaxRetries)
	}
	expected := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute}
	if len(cfg.Providers.Backoff.Schedule) != len(expected) {
		t.Fatalf("unexpected retry schedule: %v", cfg.Providers.Backoff.Schedule)
	}
	for index := range expected {
		if cfg.Providers.Backoff.Schedule[index] != expected[index] {
			t.Fatalf("retry schedule[%d]: expected %s, got %s", index, expected[index], cfg.Providers.Backoff.Schedule[index])
		}
	}
}

func TestInitGlobalConfigLoadsAndMergesProviderHeaders(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
providers:
  headers:
    X-Global: global-value
    X-Shared: global-value
  items:
    alpha:
      enabled: true
      protocol: openai
      headers:
        x-shared: provider-value
        X-Provider: provider-only
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}
	provider := cfg.Providers.Items["alpha"]
	merged := EffectiveProviderHeaders(cfg.Providers.Headers, provider.Headers)

	if got := merged["X-Global"]; got != "global-value" {
		t.Fatalf("global header = %q, want global-value", got)
	}
	if got := merged["X-Shared"]; got != "provider-value" {
		t.Fatalf("shared header = %q, want provider-value", got)
	}
	if got := merged["X-Provider"]; got != "provider-only" {
		t.Fatalf("provider header = %q, want provider-only", got)
	}

	merged["X-Global"] = "changed"
	if got := cfg.Providers.Headers["X-Global"]; got != "global-value" {
		t.Fatalf("merge mutated global headers: %q", got)
	}
	if got := provider.Headers["x-shared"]; got != "provider-value" {
		t.Fatalf("merge mutated provider headers: %q", got)
	}
}

func TestEffectiveProviderHeaders_UserAgentProviderOverridesGlobal(t *testing.T) {
	merged := EffectiveProviderHeaders(
		map[string]string{
			"User-Agent": "global-ua/1.0",
			"X-Trace":    "global-trace",
		},
		map[string]string{
			"user-agent": "provider-ua/2.0",
		},
	)

	if got := merged["User-Agent"]; got != "provider-ua/2.0" {
		t.Fatalf("User-Agent = %q, want provider-ua/2.0", got)
	}
	if got := merged["X-Trace"]; got != "global-trace" {
		t.Fatalf("X-Trace = %q, want global-trace", got)
	}
}

func TestInitGlobalConfigLoadsAICLIBalanceRefreshInterval(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
aicli:
  balance:
    refresh_interval: 45s
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}
	if got := EffectiveAICLIBalanceRefreshInterval(cfg); got != 45*time.Second {
		t.Fatalf("balance refresh interval = %s, want 45s", got)
	}
}

func TestEffectiveAICLIBalanceRefreshIntervalDefaultsToOneMinute(t *testing.T) {
	for _, cfg := range []*Config{
		nil,
		{},
		{AICLI: &AICLIConfig{}},
		{AICLI: &AICLIConfig{Balance: &AICLIBalanceConfig{}}},
	} {
		if got := EffectiveAICLIBalanceRefreshInterval(cfg); got != time.Minute {
			t.Fatalf("balance refresh interval = %s, want 1m", got)
		}
	}
}

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

func TestInitGlobalConfigLoadsIndependentTeamRouting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
aicli:
  subagents:
    routing:
      enabled: true
      levels:
        hard:
          provider: child-provider
          model: child-model
  teams:
    routing:
      enabled: true
      levels:
        hard:
          provider: team-provider
          model: team-model
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}
	teamRouting := EffectiveTeamRoutingConfig(cfg)
	if teamRouting == nil {
		t.Fatal("expected team routing config")
	}
	if got := teamRouting.Levels["hard"]; got.Provider != "team-provider" || got.Model != "team-model" {
		t.Fatalf("unexpected team hard route: %#v", got)
	}
}

func TestEffectiveTeamRoutingConfigFallsBackToSubagentRouting(t *testing.T) {
	subagentRouting := &AICLISubagentRoutingConfig{DefaultDifficulty: "hard"}
	cfg := &Config{AICLI: &AICLIConfig{
		Subagents: &AICLISubagentsConfig{Routing: subagentRouting},
	}}
	if got := EffectiveTeamRoutingConfig(cfg); got != subagentRouting {
		t.Fatalf("expected team routing to inherit subagent routing, got %#v", got)
	}
}

func TestValidateConfigRejectsInvalidEnabledTeamRouting(t *testing.T) {
	enabled := true
	err := ValidateConfig(&Config{AICLI: &AICLIConfig{
		Teams: &AICLITeamsConfig{Routing: &AICLISubagentRoutingConfig{
			Enabled:           &enabled,
			DefaultDifficulty: "impossible",
		}},
	}})
	if err == nil {
		t.Fatal("expected invalid team routing error")
	}
	if !strings.Contains(err.Error(), "aicli.teams.routing.default_difficulty") {
		t.Fatalf("unexpected validation error: %v", err)
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

// P3 mention 注入配置（plan §4.7 / §5 P3）：默认 auto、预算默认 4/32768/65536，
// 0/负数回退默认；auto 仅交互式回合生效（headless/JSON 不注入）。
func TestSkillsRuntimeMentionInjectionDefaults(t *testing.T) {
	cfg := &SkillsRuntimeConfig{}
	if got := cfg.MentionInjectionMode(); got != SkillMentionInjectionAuto {
		t.Fatalf("default mode = %q, want auto", got)
	}
	if !cfg.MentionInjectionEnabled(true) {
		t.Fatal("default auto must enable interactive mention injection")
	}
	if cfg.MentionInjectionEnabled(false) {
		t.Fatal("default auto must not enable headless mention injection")
	}
	if got := cfg.MentionMultiLimitValue(); got != DefaultSkillMentionMultiLimit {
		t.Fatalf("default multi limit = %d, want %d", got, DefaultSkillMentionMultiLimit)
	}
	if got := cfg.MentionInjectMaxCharsValue(); got != DefaultSkillMentionInjectMaxChars {
		t.Fatalf("default per-skill chars = %d, want %d", got, DefaultSkillMentionInjectMaxChars)
	}
	if got := cfg.MentionInjectTotalCharsValue(); got != DefaultSkillMentionInjectTotalChars {
		t.Fatalf("default total chars = %d, want %d", got, DefaultSkillMentionInjectTotalChars)
	}
	if cfg.CatalogResident {
		t.Fatal("catalog_resident must default to false (P1 gray release)")
	}
}

func TestSkillsRuntimeMentionInjectionModeParsing(t *testing.T) {
	cases := []struct {
		raw                 string
		wantMode            string
		wantEnabledInteract bool
		wantEnabledHeadless bool
	}{
		{raw: "", wantMode: SkillMentionInjectionAuto, wantEnabledInteract: true},
		{raw: "off", wantMode: SkillMentionInjectionOff},
		{raw: " OFF ", wantMode: SkillMentionInjectionOff},
		{raw: "unknown", wantMode: SkillMentionInjectionAuto, wantEnabledInteract: true},
		{raw: "auto", wantMode: SkillMentionInjectionAuto, wantEnabledInteract: true},
		{raw: "Auto", wantMode: SkillMentionInjectionAuto, wantEnabledInteract: true},
		{raw: "on", wantMode: SkillMentionInjectionOn, wantEnabledInteract: true, wantEnabledHeadless: true},
		{raw: "ON", wantMode: SkillMentionInjectionOn, wantEnabledInteract: true, wantEnabledHeadless: true},
	}
	for _, tc := range cases {
		cfg := &SkillsRuntimeConfig{MentionInjection: tc.raw}
		if got := cfg.MentionInjectionMode(); got != tc.wantMode {
			t.Fatalf("mode(%q) = %q, want %q", tc.raw, got, tc.wantMode)
		}
		if got := cfg.MentionInjectionEnabled(true); got != tc.wantEnabledInteract {
			t.Fatalf("enabled(interactive) for %q = %v, want %v", tc.raw, got, tc.wantEnabledInteract)
		}
		if got := cfg.MentionInjectionEnabled(false); got != tc.wantEnabledHeadless {
			t.Fatalf("enabled(headless) for %q = %v, want %v", tc.raw, got, tc.wantEnabledHeadless)
		}
	}
	var nilCfg *SkillsRuntimeConfig
	if got := nilCfg.MentionInjectionMode(); got != SkillMentionInjectionAuto {
		t.Fatalf("nil mode = %q, want auto", got)
	}
	if !nilCfg.MentionInjectionEnabled(true) {
		t.Fatal("nil config must default to auto for interactive turns")
	}
	if nilCfg.MentionInjectionEnabled(false) {
		t.Fatal("nil config must not enable mention injection for headless turns")
	}
}

func TestSkillsRuntimeMentionInjectionBudgetClamping(t *testing.T) {
	zero := &SkillsRuntimeConfig{MentionMultiLimit: 0, MentionInjectMaxChars: -1, MentionInjectTotalChars: 0}
	if got := zero.MentionMultiLimitValue(); got != DefaultSkillMentionMultiLimit {
		t.Fatalf("zero multi limit = %d, want default", got)
	}
	if got := zero.MentionInjectMaxCharsValue(); got != DefaultSkillMentionInjectMaxChars {
		t.Fatalf("negative per-skill chars = %d, want default", got)
	}
	if got := zero.MentionInjectTotalCharsValue(); got != DefaultSkillMentionInjectTotalChars {
		t.Fatalf("zero total chars = %d, want default", got)
	}

	explicit := &SkillsRuntimeConfig{MentionMultiLimit: 2, MentionInjectMaxChars: 1024, MentionInjectTotalChars: 4096}
	if got := explicit.MentionMultiLimitValue(); got != 2 {
		t.Fatalf("explicit multi limit = %d, want 2", got)
	}
	if got := explicit.MentionInjectMaxCharsValue(); got != 1024 {
		t.Fatalf("explicit per-skill chars = %d, want 1024", got)
	}
	if got := explicit.MentionInjectTotalCharsValue(); got != 4096 {
		t.Fatalf("explicit total chars = %d, want 4096", got)
	}
}

// P3 函数面收敛（plan §5 P3 / §8 Q5）：文本类 skill 函数默认隐藏，
// nil/true 开启（隐藏），显式 false 回退暴露。
func TestSkillsRuntimeMentionHideTextSkillFunctionsEnabled(t *testing.T) {
	if !(*SkillsRuntimeConfig)(nil).MentionHideTextSkillFunctionsEnabled() {
		t.Fatal("nil config must default to hiding text skill functions")
	}
	if !(&SkillsRuntimeConfig{}).MentionHideTextSkillFunctionsEnabled() {
		t.Fatal("nil field must default to hiding text skill functions")
	}
	enabled := true
	falseValue := false
	if !(&SkillsRuntimeConfig{MentionHideTextSkillFunctions: &enabled}).MentionHideTextSkillFunctionsEnabled() {
		t.Fatal("explicit true must keep text skill functions hidden")
	}
	if (&SkillsRuntimeConfig{MentionHideTextSkillFunctions: &falseValue}).MentionHideTextSkillFunctionsEnabled() {
		t.Fatal("explicit false must re-expose text skill functions")
	}
}

// P1 常驻 catalog（plan §4.5/§4.7）：默认 false，显式 true 生效，nil 安全。
func TestSkillsRuntimeCatalogResidentEnabled(t *testing.T) {
	if (*SkillsRuntimeConfig)(nil).CatalogResidentEnabled() {
		t.Fatal("nil config must not enable resident catalog")
	}
	if (&SkillsRuntimeConfig{}).CatalogResidentEnabled() {
		t.Fatal("default catalog_resident must be off")
	}
	if !(&SkillsRuntimeConfig{CatalogResident: true}).CatalogResidentEnabled() {
		t.Fatal("explicit catalog_resident=true must enable resident catalog")
	}
}

func TestInitGlobalConfigLoadsMentionInjection(t *testing.T) {
	path := writeSkillsEnabledConfig(t, `skills_runtime:
  mention_injection: auto
  mention_multi_limit: 2
  mention_inject_max_chars: 1024
  mention_inject_total_chars: 4096
  mention_hide_text_skill_functions: false
  catalog_resident: true
`)
	cfg, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if cfg.SkillsRuntime == nil {
		t.Fatal("skills_runtime section must be present")
	}
	runtimeCfg := cfg.SkillsRuntime
	if got := runtimeCfg.MentionInjectionMode(); got != SkillMentionInjectionAuto {
		t.Fatalf("mention_injection = %q, want auto", got)
	}
	if !runtimeCfg.MentionInjectionEnabled(true) || runtimeCfg.MentionInjectionEnabled(false) {
		t.Fatal("auto must enable interactive turns only")
	}
	if got := runtimeCfg.MentionMultiLimitValue(); got != 2 {
		t.Fatalf("mention_multi_limit = %d, want 2", got)
	}
	if got := runtimeCfg.MentionInjectMaxCharsValue(); got != 1024 {
		t.Fatalf("mention_inject_max_chars = %d, want 1024", got)
	}
	if got := runtimeCfg.MentionInjectTotalCharsValue(); got != 4096 {
		t.Fatalf("mention_inject_total_chars = %d, want 4096", got)
	}
	if runtimeCfg.MentionHideTextSkillFunctionsEnabled() {
		t.Fatal("mention_hide_text_skill_functions = false must re-expose text skill functions")
	}
	if !runtimeCfg.CatalogResident {
		t.Fatal("catalog_resident = false, want true")
	}
}
