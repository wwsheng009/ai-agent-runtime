package modelcard

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestLoadSourcesAndResolveExactModelCard(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{{
		Name: "test.yaml",
		Data: []byte(`
version: 1
provider_templates:
  - id: openai.chat
    protocol: openai
    api_path: /v1/chat/completions
cards:
  - id: test.gpt
    priority: 100
    provider_template: openai.chat
    match:
      model_ids:
        - gpt-test
      protocols:
        - openai
    capability:
      input_modalities:
        - text
        - image
      max_context_tokens: 123
`),
	}}, true)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	spec, applied := catalog.Resolve(Context{RuntimeProtocol: "openai"}, "gpt-test")
	if len(applied) != 1 || applied[0].CardID != "test.gpt" {
		t.Fatalf("unexpected applied cards: %+v", applied)
	}
	if spec.MaxContextTokens != 123 || strings.Join(spec.InputModalities, ",") != "text,image" {
		t.Fatalf("unexpected capability: %+v", spec)
	}
	template, ok := catalog.ProviderTemplate("openai.chat")
	if !ok || template.APIPath != "/v1/chat/completions" {
		t.Fatalf("expected provider template, got %+v ok=%v", template, ok)
	}
}

func TestResolveSkipsProtocolMismatch(t *testing.T) {
	catalog, _, err := LoadSources([]Source{{
		Name: "test.yaml",
		Data: []byte(`
version: 1
cards:
  - id: test.gpt
    match:
      model_ids:
        - gpt-test
      protocols:
        - codex
    capability:
      max_context_tokens: 123
`),
	}}, true)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	spec, applied := catalog.Resolve(Context{RuntimeProtocol: "openai"}, "gpt-test")
	if len(applied) != 0 || !capabilityIsEmpty(spec) {
		t.Fatalf("expected no match, got spec=%+v applied=%+v", spec, applied)
	}
}

func TestMergeCapabilityPreservesExistingFields(t *testing.T) {
	existing := agentconfig.ModelCapabilitySpec{
		InputModalities:       []string{"text"},
		ReasoningEfforts:      []string{"low"},
		MaxContextTokens:      100,
		AutoCompactTokenLimit: 80,
	}
	remote := agentconfig.ModelCapabilitySpec{
		InputModalities:       []string{"text", "image"},
		ReasoningEfforts:      []string{"high"},
		MaxContextTokens:      200,
		AutoCompactTokenLimit: 160,
		NativeTools:           agentconfig.NativeToolCapabilities{ImageGeneration: true},
	}
	card := agentconfig.ModelCapabilitySpec{
		MaxTokens:             4096,
		SupportsRemoteCompact: true,
	}

	got := MergeCapability(existing, remote, card, agentconfig.ModelCapabilitySpec{})
	if strings.Join(got.InputModalities, ",") != "text" {
		t.Fatalf("input_modalities overwritten: %+v", got)
	}
	if strings.Join(got.ReasoningEfforts, ",") != "low" || got.MaxContextTokens != 100 || got.AutoCompactTokenLimit != 80 {
		t.Fatalf("existing fields overwritten: %+v", got)
	}
	if !got.NativeTools.ImageGeneration || got.MaxTokens != 4096 || !got.SupportsRemoteCompact {
		t.Fatalf("missing fill fields: %+v", got)
	}
}

func TestLoadSourcesNonStrictReturnsWarnings(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{{Name: "broken.yaml", Data: []byte("version: [")}}, false)
	if err != nil {
		t.Fatalf("non-strict LoadSources returned error: %v", err)
	}
	if catalog == nil {
		t.Fatal("expected catalog")
	}
	if len(warnings) != 1 || warnings[0].Code != "parse_failed" {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
}

func TestLoadSourcesStrictFails(t *testing.T) {
	_, _, err := LoadSources([]Source{{Name: "broken.yaml", Data: []byte("version: [")}}, true)
	if err == nil {
		t.Fatal("expected strict parse error")
	}
}

func TestBuiltinSourceLoads(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{BuiltinSource()}, true)
	if err != nil {
		t.Fatalf("LoadSources builtin: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	spec, applied := catalog.Resolve(Context{RuntimeProtocol: "codex"}, "gpt-5.4")
	if len(applied) == 0 {
		t.Fatalf("expected builtin gpt-5.4 card")
	}
	if spec.MaxContextTokens == 0 || !spec.NativeTools.ImageGeneration {
		t.Fatalf("unexpected builtin capability: %+v", spec)
	}
	template, ok := catalog.ProviderTemplateForProtocol("codex")
	if !ok || template.ID != "codex.responses" || template.APIPath != "/v1/responses" {
		t.Fatalf("unexpected builtin provider template: %+v ok=%v", template, ok)
	}
	template, ok = catalog.ProviderTemplateForProtocol("openai_image")
	if !ok || template.ID != "openai.images" || template.APIPath != "/v1/images/generations" {
		t.Fatalf("unexpected builtin image provider template: %+v ok=%v", template, ok)
	}
}

func TestBuiltinSourceRecommendsCodexTemplateForCodexCompatibleAliases(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{BuiltinSource()}, true)
	if err != nil {
		t.Fatalf("LoadSources builtin: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}

	cases := map[string]string{
		"codex-auto-review":            "openai.codex-auto-review.codex",
		"gpt-5.2":                      "openai.gpt-5.2-codex",
		"gpt-5.2-openai-compact":       "openai.gpt-5.2-codex",
		"gpt-5.3-codex":                "openai.gpt-5.3-codex",
		"gpt-5.3-codex-openai-compact": "openai.gpt-5.3-codex",
		"gpt-5.4-nano":                 "openai.gpt-5.4-nano.codex",
		"gpt-5.4-openai-compact":       "openai.gpt-5.4.codex",
		"gpt-5.5":                      "openai.gpt-5.5.codex",
		"gpt-5.5-openai-compact":       "openai.gpt-5.5.codex",
	}
	for modelID, expectedCardID := range cases {
		template, applied, ok := catalog.RecommendedProviderTemplate(Context{
			RuntimeProtocol:  "openai",
			LoginProtocol:    "openai",
			ProviderTemplate: "openai.chat",
		}, modelID)
		if !ok || template.ID != "codex.responses" || len(applied) == 0 || applied[0].CardID != expectedCardID {
			t.Fatalf("expected %q to recommend codex.responses via %s, got template=%+v applied=%+v ok=%v", modelID, expectedCardID, template, applied, ok)
		}

		spec, resolved := catalog.Resolve(Context{RuntimeProtocol: "codex"}, modelID)
		if len(resolved) == 0 || resolved[0].CardID != expectedCardID || spec.MaxContextTokens != 270000 || spec.AutoCompactTokenLimit != 200000 {
			t.Fatalf("expected codex capability for %q via %s, got spec=%+v applied=%+v", modelID, expectedCardID, spec, resolved)
		}
	}
}

func TestResolveUsesProtocolFallbackWhenNoModelCardMatches(t *testing.T) {
	catalog, _, err := LoadSources([]Source{BuiltinSource()}, true)
	if err != nil {
		t.Fatalf("LoadSources builtin: %v", err)
	}
	spec, applied := catalog.Resolve(Context{
		RuntimeProtocol:  "openai",
		ProviderTemplate: "openai.chat",
	}, "unknown-chat-model")
	if len(applied) != 1 || applied[0].CardID != "fallback.openai.chat" {
		t.Fatalf("expected openai chat fallback, got spec=%+v applied=%+v", spec, applied)
	}
	if strings.Join(spec.InputModalities, ",") != "text" {
		t.Fatalf("unexpected fallback capability: %+v", spec)
	}
	template, _, ok := catalog.RecommendedProviderTemplate(Context{
		RuntimeProtocol:  "openai_image",
		ProviderTemplate: "openai.images",
	}, "unknown-image-model")
	if !ok || template.ID != "openai.images" {
		t.Fatalf("expected image fallback template, got %+v ok=%v", template, ok)
	}
	imageSpec, imageApplied := catalog.Resolve(Context{
		RuntimeProtocol:  "openai_image",
		ProviderTemplate: "openai.images",
	}, "unknown-image-model")
	if len(imageApplied) != 1 || imageApplied[0].CardID != "fallback.openai.images" || !imageSpec.NativeTools.ImagesGenerationsAPI {
		t.Fatalf("expected image fallback capability, got spec=%+v applied=%+v", imageSpec, imageApplied)
	}
}

func TestResolveFallbackRequiresProviderTemplateContext(t *testing.T) {
	catalog, _, err := LoadSources([]Source{BuiltinSource()}, true)
	if err != nil {
		t.Fatalf("LoadSources builtin: %v", err)
	}
	spec, applied := catalog.Resolve(Context{RuntimeProtocol: "openai"}, "unknown-openai-model")
	if len(applied) != 0 || !capabilityIsEmpty(spec) {
		t.Fatalf("expected no endpoint fallback without provider template, got spec=%+v applied=%+v", spec, applied)
	}
	spec, applied = catalog.Resolve(Context{
		RuntimeProtocol:  "openai",
		ProviderTemplate: "openai.chat",
	}, "unknown-openai-model")
	if len(applied) != 1 || applied[0].CardID != "fallback.openai.chat" {
		t.Fatalf("expected openai chat fallback with provider template, got spec=%+v applied=%+v", spec, applied)
	}
}

func TestBuiltinSourceResolvesAnthropicLatestModels(t *testing.T) {
	catalog, _, err := LoadSources([]Source{BuiltinSource()}, true)
	if err != nil {
		t.Fatalf("LoadSources builtin: %v", err)
	}
	opus, applied := catalog.Resolve(Context{RuntimeProtocol: "anthropic"}, "claude-opus-4-7")
	if len(applied) == 0 {
		t.Fatal("expected claude-opus-4-7 card")
	}
	if opus.MaxContextTokens != 1000000 || opus.MaxTokens != 128000 || strings.Join(opus.ReasoningEfforts, ",") != "low,medium,high,xhigh,max" {
		t.Fatalf("unexpected opus capability: %+v", opus)
	}
	haiku, applied := catalog.Resolve(Context{RuntimeProtocol: "anthropic"}, "claude-haiku-4-5")
	if len(applied) == 0 {
		t.Fatal("expected claude-haiku-4-5 alias card")
	}
	if haiku.MaxContextTokens != 200000 || haiku.MaxTokens != 64000 || len(haiku.ReasoningEfforts) != 0 {
		t.Fatalf("unexpected haiku capability: %+v", haiku)
	}
}

func TestModelIDsFuzzyMatchCommonProviderAliases(t *testing.T) {
	catalog, _, err := LoadSources([]Source{{
		Name: "test.yaml",
		Data: []byte(`
version: 1
cards:
  - id: anthropic.test
    match:
      model_ids:
        - claude-sonnet-4-6
    capability:
      max_context_tokens: 1000000
`),
	}}, true)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	cases := []string{
		"anthropic.claude-sonnet-4-6",
		"anthropic.claude-sonnet-4-6-v1:0",
		"models/claude-sonnet-4-6",
	}
	for _, modelID := range cases {
		spec, applied := catalog.Resolve(Context{}, modelID)
		if len(applied) != 1 || applied[0].CardID != "anthropic.test" {
			t.Fatalf("expected fuzzy match for %q, got spec=%+v applied=%+v", modelID, spec, applied)
		}
		if spec.MaxContextTokens != 1000000 {
			t.Fatalf("unexpected capability for %q: %+v", modelID, spec)
		}
	}
}

func TestModelIDsDoNotFuzzyMatchVersionDots(t *testing.T) {
	catalog, _, err := LoadSources([]Source{{
		Name: "test.yaml",
		Data: []byte(`
version: 1
cards:
  - id: mimo.test
    match:
      model_ids:
        - mimo-v2.5-pro
    capability:
      max_context_tokens: 1000000
`),
	}}, true)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	spec, applied := catalog.Resolve(Context{}, "gemini-2.5-pro")
	if len(applied) != 0 || !capabilityIsEmpty(spec) {
		t.Fatalf("version-like dot suffix should not fuzzy match, got spec=%+v applied=%+v", spec, applied)
	}
}

func TestLoadSourcesProviderTemplateLaterWins(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{
		{
			Name: "builtin-like.yaml",
			Data: []byte(`
version: 1
provider_templates:
  - id: openai.chat
    protocol: openai
    api_path: /v1/chat/completions
    forward_url: /v1/chat/completions
    support_types:
      - openai
    max_tokens_limit: 10000
cards: []
`),
		},
		{
			Name: "user.yaml",
			Data: []byte(`
version: 1
provider_templates:
  - id: OPENAI.CHAT
    protocol: openai
    api_path: /custom/chat
    forward_url: /custom/chat
    support_types:
      - custom-openai
    max_tokens_limit: 20000
cards: []
`),
		},
	}, true)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	template, ok := catalog.ProviderTemplate("openai.chat")
	if !ok {
		t.Fatal("expected openai.chat provider template")
	}
	if template.APIPath != "/custom/chat" || template.ForwardURL != "/custom/chat" || template.MaxTokensLimit != 20000 {
		t.Fatalf("expected later provider template to win, got %+v", template)
	}
	if strings.Join(template.SupportTypes, ",") != "custom-openai" {
		t.Fatalf("unexpected support types: %+v", template.SupportTypes)
	}
}

func TestRecommendedProviderTemplateIgnoresCurrentLoginProtocol(t *testing.T) {
	catalog, _, err := LoadSources([]Source{BuiltinSource()}, true)
	if err != nil {
		t.Fatalf("LoadSources builtin: %v", err)
	}
	template, applied, ok := catalog.RecommendedProviderTemplate(Context{
		RuntimeProtocol: "openai",
		LoginProtocol:   "openai",
	}, "claude-sonnet-4-6")
	if !ok {
		t.Fatal("expected recommended provider template")
	}
	if template.ID != "anthropic.messages" || template.Protocol != "anthropic" {
		t.Fatalf("unexpected template: %+v", template)
	}
	if len(applied) == 0 || applied[0].CardID != "anthropic.claude-sonnet-4-6" {
		t.Fatalf("unexpected applied cards: %+v", applied)
	}
}

func TestLoadSourcesAllowsLaterCardToReferenceEarlierProviderTemplate(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{
		{
			Name: "templates.yaml",
			Data: []byte(`
version: 1
provider_templates:
  - id: anthropic.messages
    protocol: anthropic
    api_path: /v1/messages
cards: []
`),
		},
		{
			Name: "cards.yaml",
			Data: []byte(`
version: 1
cards:
  - id: custom.claude
    provider_template: anthropic.messages
    match:
      model_ids:
        - claude-test
    capability:
      input_modalities:
        - text
`),
		},
	}, true)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	template, _, ok := catalog.RecommendedProviderTemplate(Context{RuntimeProtocol: "openai"}, "claude-test")
	if !ok || template.ID != "anthropic.messages" {
		t.Fatalf("unexpected recommendation: %+v ok=%v", template, ok)
	}
}
