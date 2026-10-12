package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// userHomeDirForTemplatesTest resolves the isolated test home configured by
// isolatePresetTestEnv (defined in preset_test.go).
func userHomeDirForTemplatesTest(t *testing.T) string {
	t.Helper()
	home, err := userHomeDir()
	if err != nil {
		t.Fatalf("resolve isolated home: %v", err)
	}
	return home
}

func writeTemplatesUserPresets(t *testing.T, presetYAML string) {
	t.Helper()
	home := userHomeDirForTemplatesTest(t)
	if err := os.MkdirAll(filepath.Join(home, ".aicli"), 0o755); err != nil {
		t.Fatalf("mkdir .aicli: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aicli", UserPresetsFileName), []byte(presetYAML), 0o644); err != nil {
		t.Fatalf("write presets: %v", err)
	}
}

func findTemplateByName(templates []ProviderLoginTemplate, name string) *ProviderLoginTemplate {
	for i := range templates {
		if templates[i].Name == name {
			return &templates[i]
		}
	}
	return nil
}

// TestLoadPresetProviderTemplates_BuiltinGatewayIsProjectable asserts the
// binary's built-in opencode-gateway preset surfaces as a login template with
// its base URL prefilled — this is exactly what a fresh install sees.
func TestLoadPresetProviderTemplates_BuiltinGatewayIsProjectable(t *testing.T) {
	isolatePresetTestEnv(t)
	templates := LoadPresetProviderTemplates("")
	tmpl := findTemplateByName(templates, "opencode.ai")
	if tmpl == nil {
		t.Fatalf("expected builtin opencode.ai template, got %+v", templates)
	}
	if tmpl.Protocol != "openai" {
		t.Fatalf("unexpected protocol: %+v", tmpl)
	}
	if tmpl.BaseURL != "https://opencode.ai/zen/go" {
		t.Fatalf("unexpected base URL: %+v", tmpl)
	}
	if tmpl.PresetName != "opencode-gateway" {
		t.Fatalf("expected preset name opencode-gateway, got %q", tmpl.PresetName)
	}
}

// TestLoadPresetProviderTemplates_UserPresetsExtend writes a user presets
// file declaring additional providers (one with base_url, one without, one
// disabled preset) and asserts the projection adds only the usable entry
// while the builtin template stays present.
func TestLoadPresetProviderTemplates_UserPresetsExtend(t *testing.T) {
	isolatePresetTestEnv(t)
	presetYAML := `
presets:
  - name: gateway-a
    enabled: true
    config:
      providers:
        items:
          extra.example:
            enabled: true
            protocol: openai
            base_url: https://extra.example/v1
            models_path: /v1/models
          no-url:
            protocol: openai
  - name: gateway-disabled
    enabled: false
    config:
      providers:
        items:
          disabled.example:
            enabled: true
            protocol: openai
            base_url: https://disabled.example
`
	writeTemplatesUserPresets(t, presetYAML)

	templates := LoadPresetProviderTemplates("some-host")
	extra := findTemplateByName(templates, "extra.example")
	if extra == nil {
		t.Fatalf("expected extra.example template, got %+v", templates)
	}
	if extra.BaseURL != "https://extra.example/v1" || extra.ModelsPath != "/v1/models" {
		t.Fatalf("unexpected prefill: %+v", extra)
	}
	if extra.PresetName != "gateway-a" {
		t.Fatalf("expected preset name gateway-a, got %q", extra.PresetName)
	}
	if findTemplateByName(templates, "no-url") != nil {
		t.Fatal("provider without base_url must not be projected")
	}
	if findTemplateByName(templates, "disabled.example") != nil {
		t.Fatal("disabled preset must not be projected")
	}
	if findTemplateByName(templates, "opencode.ai") == nil {
		t.Fatal("builtin opencode.ai template must remain projectable")
	}
}

// TestLoadPresetProviderTemplates_DeduplicatesByName keeps the first
// occurrence when two enabled presets declare the same provider name.
func TestLoadPresetProviderTemplates_DeduplicatesByName(t *testing.T) {
	isolatePresetTestEnv(t)
	presetYAML := `
presets:
  - name: first
    enabled: true
    config:
      providers:
        items:
          shared:
            protocol: openai
            base_url: https://first.example
  - name: second
    enabled: true
    config:
      providers:
        items:
          shared:
            protocol: anthropic
            base_url: https://second.example
`
	writeTemplatesUserPresets(t, presetYAML)

	templates := LoadPresetProviderTemplates("")
	shared := findTemplateByName(templates, "shared")
	if shared == nil {
		t.Fatalf("expected shared template, got %+v", templates)
	}
	if shared.BaseURL != "https://first.example" || shared.PresetName != "first" {
		t.Fatalf("first preset must win: %+v", shared)
	}
	if second := findTemplateByName(templates, "shared"); second == nil || second.PresetName == "second" {
		t.Fatalf("duplicate entries must be collapsed: %+v", templates)
	}
}
