package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

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
