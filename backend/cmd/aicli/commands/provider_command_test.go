package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func writeProviderCommandConfig(t *testing.T, raw string) (*config.Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(raw)+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	return cfg, path
}

func TestProviderCommand_ListAndShow(t *testing.T) {
	cfg, _ := writeProviderCommandConfig(t, `
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
      api_key_ref: alpha-ref
      default_model: gpt-5
      supported_models:
        - gpt-5
        - gpt-5-mini
    beta:
      enabled: false
      protocol: anthropic
      base_url: https://beta.example.com
provider_groups:
  - name: default
    providers:
      - name: alpha
        weight: 1
`)

	list, _, err := runProviderListCommand(cfg, "openai", true, false)
	if err != nil {
		t.Fatalf("runProviderListCommand: %v", err)
	}
	if list.Total != 1 || list.Providers[0].Name != "alpha" || !list.Providers[0].Default {
		t.Fatalf("unexpected list result: %+v", list)
	}

	show, _, err := runProviderShowCommand(cfg, "alpha", true)
	if err != nil {
		t.Fatalf("runProviderShowCommand: %v", err)
	}
	if show.Name != "alpha" || show.APIKeyRef != "alpha-ref" || show.HasAPIKey || strings.Join(show.Groups, ",") != "default" {
		t.Fatalf("unexpected show result: %+v", show)
	}
	if strings.Join(show.SupportedModels, ",") != "gpt-5,gpt-5-mini" {
		t.Fatalf("unexpected shown models: %+v", show.SupportedModels)
	}
}

func TestProviderCommand_EnableDisableAndSetDefault(t *testing.T) {
	cfg, path := writeProviderCommandConfig(t, `
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
    beta:
      enabled: false
      protocol: anthropic
`)

	enabled, err := runProviderEnableCommand(cfg, []string{"beta"}, true)
	if err != nil {
		t.Fatalf("runProviderEnableCommand: %v", err)
	}
	if strings.Join(enabled.Updated, ",") != "beta" || !enabled.Enabled {
		t.Fatalf("unexpected enable result: %+v", enabled)
	}

	defaultResult, _, err := runProviderSetDefaultCommand(cfg, "beta")
	if err != nil {
		t.Fatalf("runProviderSetDefaultCommand: %v", err)
	}
	if defaultResult.DefaultProvider != "beta" || defaultResult.PreviousDefault != "alpha" {
		t.Fatalf("unexpected default result: %+v", defaultResult)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{"default_provider: beta", "enabled: true"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
}

func TestProviderCommand_RemoveUsesConfigPath(t *testing.T) {
	cfg, path := writeProviderCommandConfig(t, `
providers:
  default_provider: beta
  items:
    alpha:
      enabled: true
      protocol: openai
    beta:
      enabled: true
      protocol: anthropic
`)

	result, err := runProviderRemoveCommand(cfg, config.ProviderDeleteRequest{Names: []string{"alpha"}})
	if err != nil {
		t.Fatalf("runProviderRemoveCommand: %v", err)
	}
	if strings.Join(result.Deleted, ",") != "alpha" {
		t.Fatalf("unexpected remove result: %+v", result)
	}
	if strings.Contains(string(mustReadFile(t, path)), "\n    alpha:") {
		t.Fatal("expected alpha provider to be removed")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	return content
}
