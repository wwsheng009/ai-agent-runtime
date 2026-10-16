package config

import "testing"

func TestDefaultRuntimeConfigWebSearchDefaults(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	native := cfg.WebSearch.Native
	if native.Mode != "off" {
		t.Fatalf("native mode default = %q, want off", native.Mode)
	}
	if native.MaxUses != 5 || native.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("native defaults = %+v", native)
	}
	if native.APIVersion != "2023-06-01" || native.MaxTokens != 4096 {
		t.Fatalf("native protocol defaults = %+v", native)
	}
	if native.BaseURL != "" {
		t.Fatalf("BaseURL default should stay empty (provider/constant resolution applies): %q", native.BaseURL)
	}
	if cfg.WebSearch.Providers != "" {
		t.Fatalf("providers default should stay empty: %q", cfg.WebSearch.Providers)
	}
}
