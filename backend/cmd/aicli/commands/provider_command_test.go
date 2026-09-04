package commands

import (
	"bytes"
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

func TestProviderCommand_RemoveAllowsInteractiveNoArgs(t *testing.T) {
	cmd := newProviderRemoveCommand(func() *config.Config { return nil })
	if err := cmd.Args(cmd, nil); err != nil {
		t.Fatalf("expected provider remove without args to enter interactive mode, got %v", err)
	}
}

func TestProviderCommand_InteractiveRemoveSelectionSupportsBulkActions(t *testing.T) {
	providers := []config.ProviderSummary{
		{Name: "alpha", Enabled: true, Protocol: "openai"},
		{Name: "beta", Enabled: false, Protocol: "anthropic", Default: true},
		{Name: "gamma", Enabled: true, Protocol: "gemini"},
	}
	input := strings.NewReader("all\n2\ninvert\ndone\n")
	var output bytes.Buffer

	selected, err := promptProviderRemoveSelection(input, &output, providers)
	if err != nil {
		t.Fatalf("promptProviderRemoveSelection: %v\noutput:\n%s", err, output.String())
	}
	if strings.Join(selected, ",") != "beta" {
		t.Fatalf("expected beta to remain selected after all/toggle/invert, got %v\noutput:\n%s", selected, output.String())
	}
	for _, want := range []string{"all 全选", "clear 清空", "invert 反选", "done 继续", "beta"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("expected interactive menu to contain %q, got:\n%s", want, output.String())
		}
	}
}

func TestProviderCommand_InteractiveRemoveSelectionSupportsNamesRangesAndClear(t *testing.T) {
	providers := []config.ProviderSummary{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	}
	input := strings.NewReader("alpha 3\nclear\n1-2\ndone\n")
	var output bytes.Buffer

	selected, err := promptProviderRemoveSelection(input, &output, providers)
	if err != nil {
		t.Fatalf("promptProviderRemoveSelection: %v\noutput:\n%s", err, output.String())
	}
	if strings.Join(selected, ",") != "alpha,beta" {
		t.Fatalf("expected alpha,beta from name/clear/range flow, got %v\noutput:\n%s", selected, output.String())
	}
}

func TestProviderCommand_ConfirmInteractiveRemoveRequiresYes(t *testing.T) {
	confirmed, err := confirmProviderRemoveSelection(strings.NewReader("no\n"), ioDiscard{}, []string{"alpha"})
	if err != nil {
		t.Fatalf("confirmProviderRemoveSelection: %v", err)
	}
	if confirmed {
		t.Fatal("expected non-yes confirmation to cancel")
	}
	confirmed, err = confirmProviderRemoveSelection(strings.NewReader("yes\n"), ioDiscard{}, []string{"alpha"})
	if err != nil {
		t.Fatalf("confirmProviderRemoveSelection yes: %v", err)
	}
	if !confirmed {
		t.Fatal("expected yes confirmation to proceed")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	return content
}

func TestProviderCommand_ProxySetAndRemove(t *testing.T) {
	cfg, path := writeProviderCommandConfig(t, `
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
`)

	httpURL := "http://127.0.0.1:7890"
	result, err := runProviderProxySetCommand(cfg, "alpha", config.ProviderProxyUpdate{HTTP: &httpURL})
	if err != nil {
		t.Fatalf("runProviderProxySetCommand: %v", err)
	}
	if result.Proxy == nil || result.Proxy.HTTP != httpURL || !result.Proxy.Enabled {
		t.Fatalf("unexpected proxy result: %+v", result)
	}
	raw := string(mustReadFile(t, path))
	if !strings.Contains(raw, "proxy:") || !strings.Contains(raw, httpURL) {
		t.Fatalf("proxy not written to config:\n%s", raw)
	}

	// Partial update must preserve the http value.
	httpsURL := "http://127.0.0.1:7891"
	result, err = runProviderProxySetCommand(cfg, "alpha", config.ProviderProxyUpdate{HTTPS: &httpsURL})
	if err != nil {
		t.Fatalf("runProviderProxySetCommand (https): %v", err)
	}
	if result.Proxy.HTTP != httpURL || result.Proxy.HTTPS != httpsURL || !result.Proxy.Enabled {
		t.Fatalf("merge failed: %+v", result.Proxy)
	}

	// Disable keeps the proxy configuration.
	disabled := false
	result, err = runProviderProxySetCommand(cfg, "alpha", config.ProviderProxyUpdate{Enabled: &disabled})
	if err != nil {
		t.Fatalf("runProviderProxySetCommand (disable): %v", err)
	}
	if result.Proxy.Enabled {
		t.Fatalf("expected disabled proxy: %+v", result.Proxy)
	}

	removed, err := runProviderProxyRemoveCommand(cfg, "alpha")
	if err != nil {
		t.Fatalf("runProviderProxyRemoveCommand: %v", err)
	}
	if !removed.Removed {
		t.Fatalf("unexpected remove result: %+v", removed)
	}
	if strings.Contains(string(mustReadFile(t, path)), "proxy:") {
		t.Fatal("expected proxy node to be removed from config")
	}

	// Invalid proxy URLs are rejected.
	bad := "ftp://proxy.example.com:21"
	if _, err := runProviderProxySetCommand(cfg, "alpha", config.ProviderProxyUpdate{HTTP: &bad}); err == nil {
		t.Fatal("expected error for unsupported proxy scheme")
	}
}

func TestProviderCommand_ProxyShowDisplaysProxy(t *testing.T) {
	cfg, _ := writeProviderCommandConfig(t, `
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
      proxy:
        http: http://127.0.0.1:7890
        https: http://user:secret@proxy.example.com:8080
        enabled: true
    beta:
      enabled: true
      protocol: anthropic
`)

	show, _, err := runProviderShowCommand(cfg, "alpha", false)
	if err != nil {
		t.Fatalf("runProviderShowCommand: %v", err)
	}
	if show.Proxy == nil || show.Proxy.HTTP != "http://127.0.0.1:7890" || !show.Proxy.Enabled {
		t.Fatalf("unexpected proxy in show result: %+v", show.Proxy)
	}
	if text := formatProviderProxyDisplay(show.Proxy); !strings.Contains(text, "enabled") ||
		!strings.Contains(text, "http://127.0.0.1:7890") ||
		strings.Contains(text, "secret") {
		t.Fatalf("unexpected proxy display text %q", text)
	}

	plain, _, err := runProviderShowCommand(cfg, "beta", false)
	if err != nil {
		t.Fatalf("runProviderShowCommand beta: %v", err)
	}
	if plain.Proxy != nil {
		t.Fatalf("expected no proxy for beta: %+v", plain.Proxy)
	}
}
func TestProviderCommand_GlobalProxySetAndRemove(t *testing.T) {
	cfg, path := writeProviderCommandConfig(t, `
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
`)

	httpURL := "http://127.0.0.1:7890"
	result, err := runProviderProxyGlobalSetCommand(cfg, config.GlobalProxyUpdate{HTTP: &httpURL})
	if err != nil {
		t.Fatalf("runProviderProxyGlobalSetCommand: %v", err)
	}
	if result.Removed || result.Proxy == nil || result.Proxy.HTTP != httpURL || !result.Proxy.Enabled {
		t.Fatalf("unexpected global proxy result: %+v", result)
	}
	raw := string(mustReadFile(t, path))
	if !strings.Contains(raw, "proxy:") || !strings.Contains(raw, httpURL) {
		t.Fatalf("global proxy not written to config:\n%s", raw)
	}

	// Partial update preserves the http value.
	httpsURL := "http://127.0.0.1:7891"
	result, err = runProviderProxyGlobalSetCommand(cfg, config.GlobalProxyUpdate{HTTPS: &httpsURL})
	if err != nil {
		t.Fatalf("runProviderProxyGlobalSetCommand (https): %v", err)
	}
	if result.Proxy.HTTP != httpURL || result.Proxy.HTTPS != httpsURL || !result.Proxy.Enabled {
		t.Fatalf("global merge failed: %+v", result.Proxy)
	}

	// Disable keeps the configuration.
	disabled := false
	result, err = runProviderProxyGlobalSetCommand(cfg, config.GlobalProxyUpdate{Enabled: &disabled})
	if err != nil {
		t.Fatalf("runProviderProxyGlobalSetCommand (disable): %v", err)
	}
	if result.Proxy.Enabled {
		t.Fatalf("expected disabled global proxy: %+v", result.Proxy)
	}

	removed, err := runProviderProxyGlobalRemoveCommand(cfg)
	if err != nil {
		t.Fatalf("runProviderProxyGlobalRemoveCommand: %v", err)
	}
	if !removed.Removed {
		t.Fatalf("unexpected remove result: %+v", removed)
	}
	if strings.Contains(string(mustReadFile(t, path)), "proxy:") {
		t.Fatal("expected global proxy node to be removed from config")
	}

	// Removing again reports an error.
	if _, err := runProviderProxyGlobalRemoveCommand(cfg); err == nil {
		t.Fatal("expected error when removing a missing global proxy")
	}

	// Invalid proxy URLs are rejected.
	bad := "ftp://proxy.example.com:21"
	if _, err := runProviderProxyGlobalSetCommand(cfg, config.GlobalProxyUpdate{HTTP: &bad}); err == nil {
		t.Fatal("expected error for unsupported proxy scheme")
	}
}
