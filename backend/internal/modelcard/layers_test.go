package modelcard

import (
	"strings"
	"testing"
)

func TestLoadSourcesMergesSameIDCardAcrossLayers(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{
		{
			Name: "builtin.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
provider_templates:
  - id: openai.chat
    protocol: openai
    api_path: /v1/chat/completions
cards:
  - id: layer.gpt
    priority: 100
    provider_template: openai.chat
    match:
      model_ids:
        - gpt-layer
      protocols:
        - openai
    capability:
      input_modalities:
        - text
        - image
      max_context_tokens: 100
      native_tools:
        image_generation: true
      reasoning_efforts:
        - low
        - medium
`) + "\n"),
		},
		{
			Name: "workspace.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: layer.gpt
    capability:
      max_context_tokens: 200
      native_tools:
        images_generations_api: true
      reasoning_efforts:
        - low
        - medium
        - high
`) + "\n"),
		},
	}, false)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if len(catalog.Cards) != 1 {
		t.Fatalf("same id cards should merge into one entry, got %+v", catalog.Cards)
	}

	spec, applied := catalog.Resolve(Context{RuntimeProtocol: "openai"}, "gpt-layer")
	if len(applied) != 1 || applied[0].CardID != "layer.gpt" {
		t.Fatalf("unexpected applied cards: %+v", applied)
	}
	// 高层覆盖标量与数组。
	if spec.MaxContextTokens != 200 {
		t.Fatalf("expected workspace max_context_tokens 200, got %d", spec.MaxContextTokens)
	}
	if strings.Join(spec.ReasoningEfforts, ",") != "low,medium,high" {
		t.Fatalf("expected array replaced by workspace, got %+v", spec.ReasoningEfforts)
	}
	// 低层独有字段保留（对象递归合并）。
	if strings.Join(spec.InputModalities, ",") != "text,image" {
		t.Fatalf("expected base input_modalities preserved, got %+v", spec.InputModalities)
	}
	if !spec.NativeTools.ImageGeneration || !spec.NativeTools.ImagesGenerationsAPI {
		t.Fatalf("expected native_tools deep merge, got %+v", spec.NativeTools)
	}
	// match / provider_template 等未覆盖字段保留。
	if card := catalog.Cards[0]; card.ProviderTemplate != "openai.chat" || card.Priority != 100 {
		t.Fatalf("expected base card metadata preserved, got %+v", card)
	}
	if len(catalog.Cards[0].Match.Protocols) != 1 || catalog.Cards[0].Match.Protocols[0] != "openai" {
		t.Fatalf("expected base match preserved, got %+v", catalog.Cards[0].Match)
	}
}

func TestLoadSourcesMergesSameIDProviderTemplateAcrossLayers(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{
		{
			Name: "builtin.yaml",
			Data: []byte(strings.TrimSpace(`
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
`) + "\n"),
		},
		{
			Name: "workspace.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
provider_templates:
  - id: OPENAI.CHAT
    api_path: /custom/chat
cards: []
`) + "\n"),
		},
	}, false)
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
	if template.APIPath != "/custom/chat" {
		t.Fatalf("expected workspace api_path, got %+v", template)
	}
	if template.ForwardURL != "/v1/chat/completions" || template.Protocol != "openai" || template.MaxTokensLimit != 10000 {
		t.Fatalf("expected base template fields preserved, got %+v", template)
	}
	if strings.Join(template.SupportTypes, ",") != "openai" {
		t.Fatalf("expected base support_types preserved, got %+v", template.SupportTypes)
	}
}

func TestLoadSourcesLayerRankBreaksPriorityScoreTies(t *testing.T) {
	catalog, _, err := LoadSources([]Source{
		{
			Name: "builtin.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: builtin.tie
    match:
      model_ids:
        - tie-model
    capability:
      input_modalities:
        - text
      max_context_tokens: 100
      auto_compact_token_limit: 80
`) + "\n"),
		},
		{
			Name: "workspace.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: workspace.tie
    match:
      model_ids:
        - tie-model
    capability:
      max_context_tokens: 200
`) + "\n"),
		},
	}, false)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}

	// 同 priority（0）同匹配分：高层先补齐，低层只补高层没写的字段。
	spec, applied := catalog.Resolve(Context{RuntimeProtocol: "openai"}, "tie-model")
	if len(applied) != 2 {
		t.Fatalf("expected both cards applied, got %+v", applied)
	}
	if applied[0].CardID != "workspace.tie" || applied[1].CardID != "builtin.tie" {
		t.Fatalf("expected workspace card first, got %+v", applied)
	}
	if spec.MaxContextTokens != 200 {
		t.Fatalf("expected workspace max_context_tokens, got %d", spec.MaxContextTokens)
	}
	if strings.Join(spec.InputModalities, ",") != "text" || spec.AutoCompactTokenLimit != 80 {
		t.Fatalf("expected builtin card to fill missing fields, got %+v", spec)
	}
}

func TestLoadSourcesPriorityStillOutranksLayerRank(t *testing.T) {
	catalog, _, err := LoadSources([]Source{
		{
			Name: "builtin.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: builtin.specific
    priority: 100
    match:
      model_ids:
        - specific-model
    capability:
      max_context_tokens: 100
      input_modalities:
        - text
`) + "\n"),
		},
		{
			Name: "workspace.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: workspace.pattern
    priority: 10
    match:
      model_patterns:
        - "specific-*"
    capability:
      max_context_tokens: 200
`) + "\n"),
		},
	}, false)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	// 匹配精度（priority/score）优先于层序：高层通配卡不能顶掉低层精确卡。
	spec, applied := catalog.Resolve(Context{RuntimeProtocol: "openai"}, "specific-model")
	if len(applied) == 0 || applied[0].CardID != "builtin.specific" {
		t.Fatalf("expected priority to outrank layer rank, got %+v", applied)
	}
	if spec.MaxContextTokens != 100 {
		t.Fatalf("expected builtin exact card to win max_context_tokens, got %d", spec.MaxContextTokens)
	}
}

func TestLoadSourcesDuplicateCardIDWithinLayerRejectsLayer(t *testing.T) {
	base := Source{
		Name: "builtin.yaml",
		Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: base.card
    match:
      model_ids:
        - base-model
    capability:
      input_modalities:
        - text
`) + "\n"),
	}
	duplicate := Source{
		Name: "workspace.yaml",
		Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: dup.card
    match:
      model_ids:
        - dup-model
    capability:
      input_modalities:
        - text
  - id: DUP.CARD
    match:
      model_ids:
        - dup-model-2
    capability:
      input_modalities:
        - text
`) + "\n"),
	}

	catalog, warnings, err := LoadSources([]Source{base, duplicate}, false)
	if err != nil {
		t.Fatalf("non-strict LoadSources returned error: %v", err)
	}
	if len(warnings) != 1 || warnings[0].Code != "validate_failed" || !strings.Contains(warnings[0].Message, "duplicate card id") {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if len(catalog.Cards) != 1 || catalog.Cards[0].ID != "base.card" {
		t.Fatalf("bad layer should be rolled back, got %+v", catalog.Cards)
	}

	if _, _, err := LoadSources([]Source{base, duplicate}, true); err == nil || !strings.Contains(err.Error(), "duplicate card id") {
		t.Fatalf("expected strict duplicate error, got %v", err)
	}
}

func TestLoadSourcesMalformedLayerKeepsEarlierLayers(t *testing.T) {
	// 已有契约：非 strict 下坏层只记 warning，低层结果保留。
	catalog, warnings, err := LoadSources([]Source{
		{
			Name: "builtin.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: base.card
    match:
      model_ids:
        - base-model
    capability:
      input_modalities:
        - text
`) + "\n"),
		},
		{Name: "workspace.yaml", Data: []byte("version: [")},
	}, false)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(warnings) != 1 || warnings[0].Code != "parse_failed" {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	spec, applied := catalog.Resolve(Context{}, "base-model")
	if len(applied) != 1 || applied[0].CardID != "base.card" {
		t.Fatalf("expected earlier layer preserved, got applied=%+v spec=%+v", applied, spec)
	}
}

func TestLayerRankTracksHighestContributor(t *testing.T) {
	catalog, _, err := LoadSources([]Source{
		{
			Name: "builtin.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: shared.card
    match:
      model_ids:
        - shared-model
    capability:
      max_context_tokens: 100
`) + "\n"),
		},
		{
			Name: "user.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: shared.card
    capability:
      max_context_tokens: 200
`) + "\n"),
		},
		{
			Name: "workspace.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: shared.card
    capability:
      auto_compact_token_limit: 180
`) + "\n"),
		},
	}, false)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if _, applied := catalog.Resolve(Context{}, "shared-model"); len(applied) == 0 {
		t.Fatal("expected merged card to resolve")
	}
	if catalog.Cards[0].layerRank != 2 {
		t.Fatalf("expected layerRank 2 (workspace contributor), got %d", catalog.Cards[0].layerRank)
	}
	// 合并后的卡片携带最高贡献层序号；若另有同分卡片，工作区层优先。
	spec, _ := catalog.Resolve(Context{}, "shared-model")
	if spec.MaxContextTokens != 200 || spec.AutoCompactTokenLimit != 180 {
		t.Fatalf("unexpected merged capability: %+v", spec)
	}
}

func TestLayerRankFallbackOrdering(t *testing.T) {
	// fallback 卡片同样遵循层序兜底。
	catalog, _, err := LoadSources([]Source{
		{
			Name: "builtin.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: fallback.builtin
    fallback: true
    match:
      protocols:
        - openai
    capability:
      max_context_tokens: 100
`) + "\n"),
		},
		{
			Name: "workspace.yaml",
			Data: []byte(strings.TrimSpace(`
version: 1
cards:
  - id: fallback.workspace
    fallback: true
    match:
      protocols:
        - openai
    capability:
      max_context_tokens: 200
`) + "\n"),
		},
	}, false)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	spec, applied := catalog.Resolve(Context{RuntimeProtocol: "openai"}, "unknown-model")
	if len(applied) == 0 || applied[0].CardID != "fallback.workspace" {
		t.Fatalf("expected workspace fallback first, got %+v", applied)
	}
	if spec.MaxContextTokens != 200 {
		t.Fatalf("expected workspace fallback max_context_tokens, got %d", spec.MaxContextTokens)
	}
}
