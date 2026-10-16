package agentconfig

import "testing"

func TestEffectiveNativeWebSearchLegacyKeys(t *testing.T) {
	supports := true
	spec := ModelCapabilitySpec{
		SupportsSearchTool: &supports,
		WebSearchToolType:  "text_and_image",
	}
	got := spec.EffectiveNativeWebSearch()
	if !got.Supported || got.ToolType != "text_and_image" || got.Protocol != "" {
		t.Fatalf("unexpected effective native web search: %+v", got)
	}
}

func TestEffectiveNativeWebSearchNewKeyWithLegacySupport(t *testing.T) {
	supports := true
	spec := ModelCapabilitySpec{
		NativeTools: NativeToolCapabilities{
			WebSearch: &NativeWebSearchCapability{Protocol: "anthropic_server_tool"},
		},
		SupportsSearchTool: &supports,
		WebSearchToolType:  "text",
	}
	got := spec.EffectiveNativeWebSearch()
	if !got.Supported {
		t.Fatalf("legacy support declaration should survive a tool_type-only new block: %+v", got)
	}
	if got.ToolType != "text" {
		t.Fatalf("legacy tool type should fill an empty new-key field: %+v", got)
	}
	if got.Protocol != "anthropic_server_tool" {
		t.Fatalf("protocol not preserved: %+v", got)
	}
}

func TestEffectiveNativeWebSearchNewKeyPreferredAndTrimmed(t *testing.T) {
	supports := false
	spec := ModelCapabilitySpec{
		NativeTools: NativeToolCapabilities{
			WebSearch: &NativeWebSearchCapability{Supported: true, ToolType: "  text_and_image "},
		},
		SupportsSearchTool: &supports,
		WebSearchToolType:  "legacy-text",
	}
	got := spec.EffectiveNativeWebSearch()
	if !got.Supported || got.ToolType != "text_and_image" {
		t.Fatalf("new block should win and be trimmed: %+v", got)
	}
}

func TestEffectiveNativeWebSearchUndeclared(t *testing.T) {
	got := ModelCapabilitySpec{InputModalities: []string{"text"}}.EffectiveNativeWebSearch()
	if got.Supported || got.ToolType != "" || got.Protocol != "" {
		t.Fatalf("undeclared model must report zero capability: %+v", got)
	}
}
