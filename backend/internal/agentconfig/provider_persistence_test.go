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
		APIPath:            stringPtr("/v1/chat/completions"),
		ForwardURL:         stringPtr("/v1/chat/completions"),
		APIKey:             stringPtr("sk-test"),
		APIKeyRef:          stringPtr("alpha-key"),
		SupportedModels:    &models,
		DefaultModel:       stringPtr("gpt-4.1-mini"),
		AuthMode:           stringPtr("api_key"),
		ModelsPath:         stringPtr("/v1/models"),
		ModelsVerifiedAt:   stringPtr("2026-05-02T00:00:00Z"),
		SupportTypes:       &[]string{"openai", "openai"},
		MaxTokensLimit:     intPtr(10000),
	})
	if err != nil {
		t.Fatalf("UpdateProviderConfig: %v", err)
	}
	if updated == nil || updated.Protocol != "openai" || updated.DefaultModel != "gpt-4.1-mini" || updated.APIPath != "/v1/chat/completions" || updated.ForwardURL != "/v1/chat/completions" || updated.MaxTokensLimit != 10000 {
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
		"api_path: /v1/chat/completions",
		"forward_url: /v1/chat/completions",
		"api_key: sk-test",
		"api_key_ref: alpha-key",
		"supported_models:",
		"support_types:",
		"max_tokens_limit: 10000",
		"- gpt-4.1-mini",
		"- gpt-5",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in updated file:\n%s", expected, text)
		}
	}
}

func TestUpdateProviderConfig_WritesModelCapabilities(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := "providers:\n  items: {}\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	capabilities := map[string]ModelCapabilitySpec{
		"*": {
			InputModalities: []string{"text"},
			ReasoningModel:  true,
			ReasoningEfforts: []string{
				"low",
				"medium",
				"high",
				"xhigh",
				"none",
			},
			ReasoningEffortBudgets: map[string]int{
				"none": 0,
				"low":  0,
			},
		},
		"gpt-5.4": {
			MaxContextTokens: 270000,
		},
	}
	updated, err := UpdateProviderConfig(path, ProviderConfigUpdate{
		Name:              "codex",
		ModelCapabilities: &capabilities,
	})
	if err != nil {
		t.Fatalf("UpdateProviderConfig: %v", err)
	}
	if len(updated.ModelCapabilities) != 2 || !updated.ModelCapabilities["*"].ReasoningModel {
		t.Fatalf("unexpected model capabilities: %+v", updated.ModelCapabilities)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"model_capabilities:",
		"reasoning_model: true",
		"reasoning_efforts:",
		"reasoning_effort_budgets:",
		"none: 0",
		"- xhigh",
		"- none",
		"max_context_tokens: 270000",
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
		Name:      "codex",
		APIKey:    stringPtr(""),
		APIKeyRef: stringPtr(""),
		AuthMode:  stringPtr("oauth"),
		AuthRef:   stringPtr("codex_default"),
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

func intPtr(v int) *int {
	return &v
}
