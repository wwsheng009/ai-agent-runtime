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

func TestMergeCapabilityPreferCardOverwritesCardManagedFields(t *testing.T) {
	existing := agentconfig.ModelCapabilitySpec{
		InputModalities:        []string{"text"},
		ReasoningEfforts:       []string{"low"},
		DefaultReasoningEffort: "low",
		MaxContextTokens:       270000,
		AutoCompactTokenLimit:  180000,
		MaxTokens:              8192,
	}
	card := agentconfig.ModelCapabilitySpec{
		InputModalities:        []string{"text", "image"},
		ReasoningEfforts:       []string{"low", "medium", "high"},
		DefaultReasoningEffort: "medium",
		MaxContextTokens:       1050000,
		AutoCompactTokenLimit:  945000,
		NativeTools:            agentconfig.NativeToolCapabilities{ImageGeneration: true},
		ReasoningModel:         true,
	}

	got := MergeCapabilityPreferCard(card, existing)
	if got.MaxContextTokens != 1050000 || got.AutoCompactTokenLimit != 945000 {
		t.Fatalf("card context fields not authoritative: %+v", got)
	}
	if strings.Join(got.InputModalities, ",") != "text,image" {
		t.Fatalf("card input_modalities not authoritative: %+v", got)
	}
	if strings.Join(got.ReasoningEfforts, ",") != "low,medium,high" || got.DefaultReasoningEffort != "medium" {
		t.Fatalf("card reasoning fields not authoritative: %+v", got)
	}
	if !got.NativeTools.ImageGeneration || !got.ReasoningModel {
		t.Fatalf("card flags not applied: %+v", got)
	}
	if got.MaxTokens != 8192 {
		t.Fatalf("local-only max_tokens should be preserved: %+v", got)
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

	type expectedCodexCard struct {
		cardID                string
		maxContextTokens      int
		autoCompactTokenLimit int
	}
	cases := map[string]expectedCodexCard{
		"codex-auto-review":            {cardID: "openai.codex-auto-review.codex", maxContextTokens: 400000, autoCompactTokenLimit: 360000},
		"gpt-5.2":                      {cardID: "openai.gpt-5.2-codex", maxContextTokens: 400000, autoCompactTokenLimit: 360000},
		"gpt-5.2-openai-compact":       {cardID: "openai.gpt-5.2-codex", maxContextTokens: 400000, autoCompactTokenLimit: 360000},
		"gpt-5.3-codex":                {cardID: "openai.gpt-5.3-codex", maxContextTokens: 400000, autoCompactTokenLimit: 360000},
		"gpt-5.3-codex-openai-compact": {cardID: "openai.gpt-5.3-codex", maxContextTokens: 400000, autoCompactTokenLimit: 360000},
		"gpt-5.4-nano":                 {cardID: "openai.gpt-5.4-nano.codex", maxContextTokens: 400000, autoCompactTokenLimit: 360000},
		"gpt-5.4-openai-compact":       {cardID: "openai.gpt-5.4.codex", maxContextTokens: 1050000, autoCompactTokenLimit: 945000},
		"gpt-5.5":                      {cardID: "openai.gpt-5.5.codex", maxContextTokens: 1050000, autoCompactTokenLimit: 945000},
		"gpt-5.5-openai-compact":       {cardID: "openai.gpt-5.5.codex", maxContextTokens: 1050000, autoCompactTokenLimit: 945000},
		"gpt-5.6-sol":                  {cardID: "openai.gpt-5.6-sol.codex", maxContextTokens: 1050000, autoCompactTokenLimit: 945000},
		"gpt-5.6-terra":                {cardID: "openai.gpt-5.6-terra.codex", maxContextTokens: 1050000, autoCompactTokenLimit: 945000},
		"gpt-5.6-luna":                 {cardID: "openai.gpt-5.6-luna.codex", maxContextTokens: 1050000, autoCompactTokenLimit: 945000},
	}
	for modelID, expected := range cases {
		template, applied, ok := catalog.RecommendedProviderTemplate(Context{
			RuntimeProtocol:  "openai",
			LoginProtocol:    "openai",
			ProviderTemplate: "openai.chat",
		}, modelID)
		if !ok || template.ID != "codex.responses" || len(applied) == 0 || applied[0].CardID != expected.cardID {
			t.Fatalf("expected %q to recommend codex.responses via %s, got template=%+v applied=%+v ok=%v", modelID, expected.cardID, template, applied, ok)
		}

		spec, resolved := catalog.Resolve(Context{RuntimeProtocol: "codex"}, modelID)
		if len(resolved) == 0 || resolved[0].CardID != expected.cardID ||
			spec.MaxContextTokens != expected.maxContextTokens ||
			spec.AutoCompactTokenLimit != expected.autoCompactTokenLimit {
			t.Fatalf("expected codex capability for %q via %s, got spec=%+v applied=%+v", modelID, expected.cardID, spec, resolved)
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

	cases := []struct {
		modelID            string
		wantCardID         string
		wantContext        int
		wantMaxTokens      int
		wantReasoning      string
		wantDefaultEffort  string
	}{
		{"claude-fable-5", "anthropic.claude-fable-5", 1000000, 128000, "low,medium,high,xhigh,max", "high"},
		{"claude-mythos-5", "anthropic.claude-mythos-5", 1000000, 128000, "low,medium,high,xhigh,max", "high"},
		{"claude-mythos-preview", "anthropic.claude-mythos-5", 1000000, 128000, "low,medium,high,xhigh,max", "high"},
		{"claude-opus-5", "anthropic.claude-opus-5", 1000000, 128000, "low,medium,high,xhigh,max", "high"},
		{"claude-sonnet-5", "anthropic.claude-sonnet-5", 1000000, 128000, "low,medium,high,xhigh,max", "high"},
		{"claude-opus-4-8", "anthropic.claude-opus-4-8", 1000000, 128000, "low,medium,high,xhigh,max", "high"},
		{"claude-opus-4-7", "anthropic.claude-opus-4-7", 1000000, 128000, "low,medium,high,xhigh,max", "high"},
		{"claude-sonnet-4-6", "anthropic.claude-sonnet-4-6", 1000000, 128000, "low,medium,high,max", "high"},
		{"claude-haiku-4-5", "anthropic.claude-haiku-4-5", 200000, 64000, "", ""},
	}
	for _, tc := range cases {
		spec, applied := catalog.Resolve(Context{RuntimeProtocol: "anthropic"}, tc.modelID)
		if len(applied) == 0 {
			t.Fatalf("expected card for %q", tc.modelID)
		}
		if applied[0].CardID != tc.wantCardID {
			t.Fatalf("%q: expected card %q, got %q via %+v", tc.modelID, tc.wantCardID, applied[0].CardID, applied)
		}
		if spec.MaxContextTokens != tc.wantContext || spec.MaxTokens != tc.wantMaxTokens {
			t.Fatalf("%q: unexpected token limits: %+v", tc.modelID, spec)
		}
		if strings.Join(spec.ReasoningEfforts, ",") != tc.wantReasoning {
			t.Fatalf("%q: unexpected reasoning efforts: %+v", tc.modelID, spec.ReasoningEfforts)
		}
		if strings.TrimSpace(spec.DefaultReasoningEffort) != tc.wantDefaultEffort {
			t.Fatalf("%q: unexpected default effort %q", tc.modelID, spec.DefaultReasoningEffort)
		}
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

func TestRecommendedProviderTemplatesReturnsMultiProtocolMatches(t *testing.T) {
	catalog, _, err := LoadSources([]Source{BuiltinSource()}, true)
	if err != nil {
		t.Fatalf("LoadSources builtin: %v", err)
	}
	matches := catalog.RecommendedProviderTemplates(Context{
		RuntimeProtocol: "openai",
		LoginProtocol:   "openai",
	}, "grok-4.5")
	if len(matches) != 3 {
		t.Fatalf("expected 3 multi-protocol recommendations, got %+v", matches)
	}
	ids := make([]string, 0, len(matches))
	cardIDs := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.Template.ID)
		if len(match.Applied) == 0 {
			t.Fatalf("expected applied card for template %s", match.Template.ID)
		}
		cardIDs = append(cardIDs, match.Applied[0].CardID)
	}
	// Equal priority/score => lexicographic card id order.
	if strings.Join(ids, ",") != "anthropic.messages,codex.responses,openai.chat" {
		t.Fatalf("unexpected templates: %v cards=%v", ids, cardIDs)
	}
	if strings.Join(cardIDs, ",") != "xai.grok-4.5.anthropic,xai.grok-4.5.codex,xai.grok-4.5.openai" {
		t.Fatalf("unexpected applied cards: %v", cardIDs)
	}
	expectedCards := map[string]string{
		"openai.chat":        "xai.grok-4.5.openai",
		"anthropic.messages": "xai.grok-4.5.anthropic",
		"codex.responses":    "xai.grok-4.5.codex",
	}
	for _, match := range matches {
		expectedCard, ok := expectedCards[match.Template.ID]
		if !ok {
			t.Fatalf("unexpected template id %q", match.Template.ID)
		}
		if match.Applied[0].CardID != expectedCard {
			t.Fatalf("expected card %s for %s, got %+v", expectedCard, match.Template.ID, match.Applied)
		}
	}

	// Top recommendation should remain stable for callers of the singular API.
	template, applied, ok := catalog.RecommendedProviderTemplate(Context{
		RuntimeProtocol: "openai",
		LoginProtocol:   "openai",
	}, "grok-4.5")
	if !ok || len(applied) == 0 {
		t.Fatalf("expected singular recommendation, got template=%+v applied=%+v ok=%v", template, applied, ok)
	}
	if template.ID != matches[0].Template.ID || applied[0].CardID != matches[0].Applied[0].CardID {
		t.Fatalf("singular recommendation should match first multi recommendation, got template=%+v applied=%+v", template, applied)
	}
}

func TestRecommendedProviderTemplatesDedupesSameTemplate(t *testing.T) {
	catalog, _, err := LoadSources([]Source{BuiltinSource()}, true)
	if err != nil {
		t.Fatalf("LoadSources builtin: %v", err)
	}
	matches := catalog.RecommendedProviderTemplates(Context{
		RuntimeProtocol: "anthropic",
		LoginProtocol:   "anthropic",
	}, "claude-sonnet-4-6")
	if len(matches) != 1 {
		t.Fatalf("expected single anthropic template after dedupe, got %+v", matches)
	}
	if matches[0].Template.ID != "anthropic.messages" || matches[0].Applied[0].CardID != "anthropic.claude-sonnet-4-6" {
		t.Fatalf("expected exact anthropic card to win, got %+v", matches)
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
