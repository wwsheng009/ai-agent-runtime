package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"gopkg.in/yaml.v3"
)

// chatModelDeleteTestConfig seeds a provider node (through the production
// persistence path) with the given supported models and default, and mirrors
// it in an in-memory Config.
func chatModelDeleteTestConfig(t *testing.T, path, providerName, defaultModel string, models []string) *config.Config {
	t.Helper()
	enabled := true
	protocol := "openai"
	baseURL := "https://example.com"
	apiKeyRef := "ref-" + providerName
	defaultPtr := &defaultModel
	if defaultModel == "" {
		defaultPtr = nil
	}
	_, err := config.UpdateProviderConfig(path, config.ProviderConfigUpdate{
		Name:            providerName,
		Enabled:         &enabled,
		Protocol:        &protocol,
		BaseURL:         &baseURL,
		APIKeyRef:       &apiKeyRef,
		SupportedModels: &models,
		DefaultModel:    defaultPtr,
	})
	if err != nil {
		t.Fatalf("seed provider %s: %v", providerName, err)
	}
	cfg := &config.Config{
		ConfigFilePath: path,
		Providers: config.ProvidersConfig{
			Items: map[string]config.Provider{
				providerName: {
					APIKeyRef:       apiKeyRef,
					Enabled:         enabled,
					DefaultModel:    defaultModel,
					SupportedModels: append([]string(nil), models...),
				},
			},
		},
	}
	if defaultModel != "" {
		cfg.Providers.DefaultProvider = providerName
	}
	return cfg
}

// readProviderModelsInConfig returns the supported_models list of one provider
// as persisted in the YAML file.
func readProviderModelsInConfig(t *testing.T, path, providerName string) []string {
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
		if root.Content[i].Value == "providers" {
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
	for i := 0; i+1 < len(items.Content); i += 2 {
		if items.Content[i].Value != providerName {
			continue
		}
		node := items.Content[i+1]
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value == "supported_models" {
				seq := node.Content[j+1]
				models := make([]string, 0, len(seq.Content))
				for _, m := range seq.Content {
					models = append(models, m.Value)
				}
				return models
			}
		}
	}
	return nil
}

func TestChatModelRemovalGuard(t *testing.T) {
	provider := config.Provider{
		DefaultModel:    "m-a",
		SupportedModels: []string{"m-a", "m-b"},
	}
	cases := []struct {
		name         string
		current      string
		target       string
		wantErr      string
	}{
		{name: "in use", current: "m-b", target: "m-b", wantErr: "正在使用"},
		{name: "not managed", current: "m-a", target: "m-c", wantErr: "受管模型列表"},
		{name: "managed ok", current: "m-a", target: "m-b", wantErr: ""},
		{name: "empty target", current: "m-a", target: "  ", wantErr: "无效的模型名"},
		{name: "case insensitive", current: "m-a", target: "M-B", wantErr: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := chatModelRemovalGuard(provider, tc.current, tc.target)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected allowed deletion, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestPersistChatModelRemovalWritesAndSyncs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := chatModelDeleteTestConfig(t, cfgPath, "alpha", "m-a", []string{"m-a", "m-b", "m-c"})

	if err := persistChatModelRemoval(cfg, "alpha", "m-b"); err != nil {
		t.Fatalf("persistChatModelRemoval: %v", err)
	}
	// Persisted file no longer contains m-b.
	got := readProviderModelsInConfig(t, cfgPath, "alpha")
	if len(got) != 2 || got[0] != "m-a" || got[1] != "m-c" {
		t.Fatalf("expected supported_models [m-a m-c], got %v", got)
	}
	// In-memory copy mirrors the file.
	provider := cfg.Providers.Items["alpha"]
	if len(provider.SupportedModels) != 2 || provider.SupportedModels[0] != "m-a" || provider.SupportedModels[1] != "m-c" {
		t.Fatalf("expected in-memory supported_models [m-a m-c], got %v", provider.SupportedModels)
	}
}

func TestPersistChatModelRemovalClearsDefaultModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := chatModelDeleteTestConfig(t, cfgPath, "alpha", "m-a", []string{"m-a", "m-b"})

	if err := persistChatModelRemoval(cfg, "alpha", "m-a"); err != nil {
		t.Fatalf("persistChatModelRemoval: %v", err)
	}
	got := readProviderModelsInConfig(t, cfgPath, "alpha")
	if len(got) != 1 || got[0] != "m-b" {
		t.Fatalf("expected supported_models [m-b], got %v", got)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), "default_model: m-a") {
		t.Fatalf("expected dangling default_model cleared, got %s", raw)
	}
	if cfg.Providers.Items["alpha"].DefaultModel != "" {
		t.Fatalf("expected in-memory default cleared, got %q", cfg.Providers.Items["alpha"].DefaultModel)
	}
}

func TestPersistChatModelRemovalErrors(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := chatModelDeleteTestConfig(t, cfgPath, "alpha", "m-a", []string{"m-a", "m-b"})

	if err := persistChatModelRemoval(cfg, "ghost", "m-a"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("expected missing provider error, got %v", err)
	}
	if err := persistChatModelRemoval(cfg, "alpha", "m-zzz"); err == nil || !strings.Contains(err.Error(), "supported_models") {
		t.Fatalf("expected not-managed error, got %v", err)
	}
	// Nothing must have been written for failed removals.
	got := readProviderModelsInConfig(t, cfgPath, "alpha")
	if len(got) != 2 {
		t.Fatalf("expected config untouched, got %v", got)
	}
}

func TestFilterChatProviderModels(t *testing.T) {
	got := filterChatProviderModels([]string{"M-A", "m-b", "m-c"}, "m-b")
	if len(got) != 2 || got[0] != "M-A" || got[1] != "m-c" {
		t.Fatalf("expected [M-A m-c], got %v", got)
	}
	got = filterChatProviderModels([]string{"m-a", "m-b"}, "m-x")
	if len(got) != 2 || got[0] != "m-a" || got[1] != "m-b" {
		t.Fatalf("expected unchanged list, got %v", got)
	}
	// Input must not be mutated.
	original := []string{"m-a", "m-b"}
	_ = filterChatProviderModels(original, "m-a")
	if len(original) != 2 || original[0] != "m-a" {
		t.Fatalf("input slice mutated: %v", original)
	}
}