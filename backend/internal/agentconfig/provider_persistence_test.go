package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateProviderConfig_CreatesProviderAndPreservesSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
server:
  name: test
custom_section:
  keep: true
`)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	models := []string{"gpt-4.1-mini", "gpt-4.1-mini", "gpt-5"}
	updated, err := UpdateProviderConfig(path, ProviderConfigUpdate{
		Name:               "alpha",
		SetDefaultProvider: true,
		Enabled:            boolPtr(true),
		Protocol:           stringPtr("openai"),
		BaseURL:            stringPtr("https://api.example.com"),
		APIKey:             stringPtr("sk-test"),
		SupportedModels:    &models,
		DefaultModel:       stringPtr("gpt-4.1-mini"),
		AuthMode:           stringPtr("api_key"),
		ModelsPath:         stringPtr("/v1/models"),
		ModelsVerifiedAt:   stringPtr("2026-05-02T00:00:00Z"),
	})
	if err != nil {
		t.Fatalf("UpdateProviderConfig: %v", err)
	}
	if updated == nil || updated.Protocol != "openai" || updated.DefaultModel != "gpt-4.1-mini" {
		t.Fatalf("unexpected provider: %+v", updated)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"server:",
		"custom_section:",
		"default_provider: alpha",
		"alpha:",
		"protocol: openai",
		"api_key: sk-test",
		"supported_models:",
		"- gpt-4.1-mini",
		"- gpt-5",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in updated file:\n%s", expected, text)
		}
	}
}

func TestUpdateProviderConfig_EditsOnlyProvidedFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://old.example.com
      api_key: ${OPENAI_API_KEY}
      extra_field: keep-me
      supported_models:
        - old-model
      default_model: old-model
`)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	models := []string{"new-model"}
	updated, err := UpdateProviderConfig(path, ProviderConfigUpdate{
		Name:            "alpha",
		BaseURL:         stringPtr("https://new.example.com"),
		SupportedModels: &models,
		DefaultModel:    stringPtr("new-model"),
	})
	if err != nil {
		t.Fatalf("UpdateProviderConfig: %v", err)
	}
	if updated.BaseURL != "https://new.example.com" || updated.APIKey != "${OPENAI_API_KEY}" {
		t.Fatalf("unexpected provider after partial edit: %+v", updated)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"base_url: https://new.example.com",
		"api_key: ${OPENAI_API_KEY}",
		"extra_field: keep-me",
		"- new-model",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in updated file:\n%s", expected, text)
		}
	}
}

func TestUpdateProviderConfig_OAuthRemovesAPIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  items:
    codex:
      enabled: true
      protocol: codex
      base_url: https://chatgpt.com/backend-api/codex
      api_key: old-secret
`)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	updated, err := UpdateProviderConfig(path, ProviderConfigUpdate{
		Name:     "codex",
		APIKey:   stringPtr(""),
		AuthMode: stringPtr("oauth"),
		AuthRef:  stringPtr("codex_default"),
	})
	if err != nil {
		t.Fatalf("UpdateProviderConfig: %v", err)
	}
	if updated.APIKey != "" || updated.AuthMode != "oauth" || updated.AuthRef != "codex_default" {
		t.Fatalf("unexpected oauth provider: %+v", updated)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(content), "old-secret") || strings.Contains(string(content), "api_key:") {
		t.Fatalf("expected api_key to be removed:\n%s", string(content))
	}
}

func boolPtr(v bool) *bool {
	return &v
}
