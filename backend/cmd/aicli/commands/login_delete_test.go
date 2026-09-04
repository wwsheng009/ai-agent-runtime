package commands

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"gopkg.in/yaml.v3"
)

// loginDeleteTestConfig writes a real config file (through the production
// persistence path) and returns an in-memory Config mirroring it.
func loginDeleteTestConfig(t *testing.T, path string, defaultProvider string) *config.Config {
	t.Helper()
	enabled := true
	protocol := "openai"
	baseURL := "https://example.com"
	for _, name := range []string{"alpha", "beta"} {
		apiKeyRef := "ref-" + name
		_, err := config.UpdateProviderConfig(path, config.ProviderConfigUpdate{
			Name:      name,
			Enabled:   &enabled,
			Protocol:  &protocol,
			BaseURL:   &baseURL,
			APIKeyRef: &apiKeyRef,
		})
		if err != nil {
			t.Fatalf("seed provider %s: %v", name, err)
		}
	}
	if defaultProvider != "" {
		if _, err := config.UpdateProviderConfig(path, config.ProviderConfigUpdate{
			Name:               defaultProvider,
			SetDefaultProvider: true,
		}); err != nil {
			t.Fatalf("set default provider: %v", err)
		}
	}
	return &config.Config{
		ConfigFilePath: path,
		Providers: config.ProvidersConfig{
			DefaultProvider: defaultProvider,
			Items: map[string]config.Provider{
				"alpha": {APIKeyRef: "ref-alpha"},
				"beta":  {APIKeyRef: "ref-beta"},
			},
		},
	}
}

// readProviderNamesInConfig returns the provider names present in providers.items.
func readProviderNamesInConfig(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	root := document.Content[0]
	var providers, items *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case "providers":
			providers = root.Content[i+1]
		}
	}
	if providers == nil {
		return nil
	}
	for i := 0; i+1 < len(providers.Content); i += 2 {
		if providers.Content[i].Value == "items" {
			items = providers.Content[i+1]
		}
	}
	if items == nil {
		return nil
	}
	names := make([]string, 0, len(items.Content)/2)
	for i := 0; i+1 < len(items.Content); i += 2 {
		names = append(names, items.Content[i].Value)
	}
	return names
}

func TestCLILoginPrompterConfirmDeleteProviderPersists(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := loginDeleteTestConfig(t, cfgPath, "alpha")
	p := &cliLoginPrompter{
		reader:        bufio.NewReader(strings.NewReader("y\n")),
		cfg:           cfg,
		authStorePath: filepath.Join(dir, "auth.json"),
	}
	deleted, err := p.confirmAndDeleteProvider("alpha")
	if err != nil {
		t.Fatalf("confirmAndDeleteProvider: %v", err)
	}
	if !deleted {
		t.Fatal("expected provider to be deleted")
	}
	// The deletion must be persisted to the config file immediately.
	names := readProviderNamesInConfig(t, cfgPath)
	if len(names) != 1 || names[0] != "beta" {
		t.Fatalf("expected only beta left in config, got %v", names)
	}
	// The in-memory config mirrors the persisted file.
	if _, exists := cfg.Providers.Items["alpha"]; exists {
		t.Fatal("expected alpha removed from in-memory items")
	}
	if cfg.Providers.DefaultProvider != "" {
		t.Fatalf("expected cleared default provider, got %q", cfg.Providers.DefaultProvider)
	}
}

func TestCLILoginPrompterConfirmDeleteProviderDecline(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := loginDeleteTestConfig(t, cfgPath, "alpha")
	p := &cliLoginPrompter{
		reader:        bufio.NewReader(strings.NewReader("n\n")),
		cfg:           cfg,
		authStorePath: filepath.Join(dir, "auth.json"),
	}
	deleted, err := p.confirmAndDeleteProvider("alpha")
	if err != nil {
		t.Fatalf("confirmAndDeleteProvider: %v", err)
	}
	if deleted {
		t.Fatal("expected deletion declined")
	}
	names := readProviderNamesInConfig(t, cfgPath)
	if len(names) != 2 {
		t.Fatalf("expected both providers to remain, got %v", names)
	}
	if _, exists := cfg.Providers.Items["alpha"]; !exists {
		t.Fatal("expected alpha still in memory")
	}
	if cfg.Providers.DefaultProvider != "alpha" {
		t.Fatalf("expected default provider untouched, got %q", cfg.Providers.DefaultProvider)
	}
}

func TestCLILoginPrompterConfirmDeleteProviderPrunesAuth(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	authPath := filepath.Join(dir, "auth.json")
	cfg := loginDeleteTestConfig(t, cfgPath, "")
	for _, ref := range []string{"ref-alpha", "ref-beta"} {
		if err := config.SaveProviderAuthToPath(authPath, ref, config.ProviderAuthRecord{
			KeyType: "api_key",
			APIKey:  "sk-" + ref,
		}); err != nil {
			t.Fatalf("seed auth %s: %v", ref, err)
		}
	}
	p := &cliLoginPrompter{
		reader:        bufio.NewReader(strings.NewReader("y\n")),
		cfg:           cfg,
		authStorePath: authPath,
	}
	deleted, err := p.confirmAndDeleteProvider("alpha")
	if err != nil {
		t.Fatalf("confirmAndDeleteProvider: %v", err)
	}
	if !deleted {
		t.Fatal("expected provider to be deleted")
	}
	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth store: %v", err)
	}
	if strings.Contains(string(raw), "ref-alpha") {
		t.Fatalf("expected ref-alpha pruned from auth store, got %s", raw)
	}
	if !strings.Contains(string(raw), "ref-beta") {
		t.Fatalf("expected ref-beta to survive (shared by beta), got %s", raw)
	}
}

func TestCLILoginPrompterConfirmDeleteProviderMissing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := loginDeleteTestConfig(t, cfgPath, "alpha")
	p := &cliLoginPrompter{
		reader:        bufio.NewReader(strings.NewReader("y\n")),
		cfg:           cfg,
		authStorePath: filepath.Join(dir, "auth.json"),
	}
	deleted, err := p.confirmAndDeleteProvider("ghost")
	if err != nil {
		t.Fatalf("confirmAndDeleteProvider: %v", err)
	}
	if deleted {
		t.Fatal("expected missing provider to report no deletion")
	}
	names := readProviderNamesInConfig(t, cfgPath)
	if len(names) != 2 {
		t.Fatalf("expected both providers to remain, got %v", names)
	}
}