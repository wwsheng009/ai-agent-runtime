package modelcard

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func loadCatalogForWebSearchTest(t *testing.T, data string) *Catalog {
	t.Helper()
	catalog, warnings, err := LoadSources([]Source{{Name: "test.yaml", Data: []byte(data)}}, true)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	return catalog
}

func containsCapabilityField(fields []string, target string) bool {
	for _, field := range fields {
		if field == target {
			return true
		}
	}
	return false
}

func TestResolveLegacySearchKeysAreNormalized(t *testing.T) {
	catalog := loadCatalogForWebSearchTest(t, `
version: 1
cards:
  - id: legacy.search
    priority: 100
    match:
      model_ids:
        - legacy-model
    capability:
      supports_search_tool: true
      web_search_tool_type: text_and_image
`)
	spec, applied := catalog.Resolve(Context{}, "legacy-model")
	if len(applied) != 1 {
		t.Fatalf("expected one applied card, got %+v", applied)
	}
	got := spec.EffectiveNativeWebSearch()
	if !got.Supported || got.ToolType != "text_and_image" {
		t.Fatalf("legacy keys were not normalized: %+v", got)
	}
	if !containsCapabilityField(CapabilityFieldNames(spec), "native_tools.web_search") {
		t.Fatalf("canonical field name missing: %+v", CapabilityFieldNames(spec))
	}
}

func TestResolveNewStyleNativeWebSearch(t *testing.T) {
	catalog := loadCatalogForWebSearchTest(t, `
version: 1
cards:
  - id: native.search
    priority: 100
    match:
      model_ids:
        - native-model
    capability:
      native_tools:
        web_search:
          supported: true
          tool_type: text_and_image
          protocol: anthropic_server_tool
`)
	spec, _ := catalog.Resolve(Context{}, "native-model")
	got := spec.EffectiveNativeWebSearch()
	if !got.Supported || got.ToolType != "text_and_image" || got.Protocol != "anthropic_server_tool" {
		t.Fatalf("new-style declaration not honored: %+v", got)
	}
}

func TestLayeredMergeKeepsLegacySupportWithNewToolType(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{
		{Name: "low.yaml", Data: []byte(`
version: 1
cards:
  - id: layered.search
    priority: 100
    match:
      model_ids:
        - layered-model
    capability:
      supports_search_tool: true
`)},
		{Name: "high.yaml", Data: []byte(`
version: 1
cards:
  - id: layered.search
    capability:
      native_tools:
        web_search:
          tool_type: text_and_image
          protocol: anthropic_server_tool
`)},
	}, true)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	spec, applied := catalog.Resolve(Context{}, "layered-model")
	if len(applied) != 1 {
		t.Fatalf("expected one applied card, got %+v", applied)
	}
	got := spec.EffectiveNativeWebSearch()
	if !got.Supported || got.ToolType != "text_and_image" || got.Protocol != "anthropic_server_tool" {
		t.Fatalf("layered declaration lost support or fields: %+v", got)
	}
}

func TestCapabilityWithOnlyLegacySearchDeclarationIsNotEmpty(t *testing.T) {
	supports := true
	if capabilityIsEmpty(agentconfig.ModelCapabilitySpec{SupportsSearchTool: &supports}) {
		t.Fatalf("legacy search-only capability must not be considered empty")
	}
}

func TestBuiltinCatalogExposesCodexNativeSearch(t *testing.T) {
	catalog, warnings, err := LoadSources([]Source{BuiltinSource()}, true)
	if err != nil {
		t.Fatalf("LoadSources(builtin): %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected builtin warnings: %+v", warnings)
	}
	spec, applied := catalog.Resolve(Context{RuntimeProtocol: "codex"}, "gpt-5.6-sol")
	if len(applied) == 0 {
		t.Fatalf("expected a codex card for gpt-5.6-sol")
	}
	got := spec.EffectiveNativeWebSearch()
	if !got.Supported || got.ToolType != "text_and_image" {
		t.Fatalf("builtin gpt-5.6-sol should expose native web search: %+v (applied=%+v)", got, applied)
	}
}

func TestMergeCapabilityCarriesNativeWebSearch(t *testing.T) {
	remote := agentconfig.ModelCapabilitySpec{
		NativeTools: agentconfig.NativeToolCapabilities{
			WebSearch: &agentconfig.NativeWebSearchCapability{Supported: true, ToolType: "text"},
		},
	}
	got := MergeCapability(agentconfig.ModelCapabilitySpec{}, remote, agentconfig.ModelCapabilitySpec{}, agentconfig.ModelCapabilitySpec{})
	if got.NativeTools.WebSearch == nil || !got.EffectiveNativeWebSearch().Supported {
		t.Fatalf("native web search declaration was not merged: %+v", got)
	}
	clone := CloneCapabilitySpec(got)
	clone.NativeTools.WebSearch.Supported = false
	if !got.NativeTools.WebSearch.Supported {
		t.Fatalf("CloneCapabilitySpec must deep-copy the web search pointer")
	}
}

func TestCapabilitySpecsEqualTreatsEncodingStylesEqually(t *testing.T) {
	supports := true
	legacy := agentconfig.ModelCapabilitySpec{
		SupportsSearchTool: &supports,
		WebSearchToolType:  "text_and_image",
	}
	modern := agentconfig.ModelCapabilitySpec{
		NativeTools: agentconfig.NativeToolCapabilities{
			WebSearch: &agentconfig.NativeWebSearchCapability{Supported: true, ToolType: "text_and_image"},
		},
	}
	if !CapabilitySpecsEqual(legacy, modern) {
		t.Fatalf("legacy and new encodings of the same capability must compare equal")
	}
	changed := agentconfig.ModelCapabilitySpec{
		SupportsSearchTool: &supports,
		WebSearchToolType:  "text",
	}
	if CapabilitySpecsEqual(legacy, changed) {
		t.Fatalf("tool type change must be detected")
	}
}
