package agentconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeProviderManagementConfig(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(raw)+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func readProviderManagementConfig(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(content)
}

func TestListProviderSummaries_SortsAndFilters(t *testing.T) {
	enabled := true
	cfg := &Config{
		Providers: ProvidersConfig{
			DefaultProvider: "beta",
			Items: map[string]Provider{
				"beta": {
					Enabled:          true,
					Protocol:         "anthropic",
					AuthMode:         "api_key",
					APIKeyRef:        "beta-ref",
					BaseURL:          "https://beta.example.com",
					DefaultModel:     "claude-sonnet",
					SupportedModels:  []string{"claude-sonnet", "claude-haiku"},
					ModelsVerifiedAt: "2026-05-14T00:00:00Z",
				},
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					APIKey:       "sk-alpha",
					BaseURL:      "https://alpha.example.com",
					DefaultModel: "gpt-5",
				},
				"gamma": {
					Enabled:  false,
					Protocol: "openai",
					AuthRef:  "gamma-oauth",
				},
			},
		},
		ProviderGroups: []ProviderGroup{{
			Name:      "fast",
			Providers: []GroupProvider{{Name: "beta", Weight: 1}},
		}},
	}

	summaries := ListProviderSummaries(cfg, ProviderListFilter{Enabled: &enabled})
	if gotNames := []string{summaries[0].Name, summaries[1].Name}; !reflect.DeepEqual(gotNames, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected sorted names: %+v", gotNames)
	}
	if summaries[0].HasAPIKey != true || summaries[0].HasAPIKeyRef || summaries[0].SupportedModelsCount != 0 {
		t.Fatalf("unexpected alpha summary: %+v", summaries[0])
	}
	if !summaries[1].Default || summaries[1].APIKeyRef != "beta-ref" || strings.Join(summaries[1].Groups, ",") != "fast" {
		t.Fatalf("unexpected beta summary: %+v", summaries[1])
	}

	openai := ListProviderSummaries(cfg, ProviderListFilter{Protocol: "openai"})
	if gotNames := []string{openai[0].Name, openai[1].Name}; !reflect.DeepEqual(gotNames, []string{"alpha", "gamma"}) {
		t.Fatalf("unexpected protocol filtered names: %+v", gotNames)
	}
}

func TestDeleteProvidersConfig_BlocksDefaultUnlessExplicit(t *testing.T) {
	path := writeProviderManagementConfig(t, `
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
    beta:
      enabled: true
      protocol: anthropic
`)

	result, err := DeleteProvidersConfig(path, ProviderDeleteRequest{Names: []string{"alpha"}})
	if err != nil {
		t.Fatalf("DeleteProvidersConfig: %v", err)
	}
	if len(result.Blocked) != 1 || result.Blocked[0].Code != "default_provider" {
		t.Fatalf("expected default provider blocker, got %+v", result)
	}
	if !strings.Contains(readProviderManagementConfig(t, path), "alpha:") {
		t.Fatal("blocked delete should not modify config")
	}
}

func TestDeleteProvidersConfig_ClearAndReplaceDefault(t *testing.T) {
	path := writeProviderManagementConfig(t, `
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
    beta:
      enabled: true
      protocol: anthropic
aicli:
  chat:
    default_provider: alpha
skills_runtime:
  gateway_provider_name: alpha
`)

	result, err := DeleteProvidersConfig(path, ProviderDeleteRequest{
		Names:              []string{"alpha"},
		ReplacementDefault: "beta",
	})
	if err != nil {
		t.Fatalf("DeleteProvidersConfig: %v", err)
	}
	if len(result.Blocked) != 0 || strings.Join(result.Deleted, ",") != "alpha" || result.ReplacementDefault != "beta" {
		t.Fatalf("unexpected delete result: %+v", result)
	}
	text := readProviderManagementConfig(t, path)
	for _, expected := range []string{
		"default_provider: beta",
		"default_provider: beta",
		"gateway_provider_name: beta",
		"beta:",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in config:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "\n    alpha:") {
		t.Fatalf("alpha provider should be deleted:\n%s", text)
	}
}

func TestDeleteProvidersConfig_ClearDefault(t *testing.T) {
	path := writeProviderManagementConfig(t, `
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
    beta:
      enabled: true
      protocol: anthropic
`)

	result, err := DeleteProvidersConfig(path, ProviderDeleteRequest{
		Names:        []string{"alpha"},
		ClearDefault: true,
	})
	if err != nil {
		t.Fatalf("DeleteProvidersConfig: %v", err)
	}
	if strings.Join(result.Deleted, ",") != "alpha" || strings.Join(result.ClearedDefaults, ",") != "providers.default_provider" {
		t.Fatalf("unexpected clear-default result: %+v", result)
	}
	text := readProviderManagementConfig(t, path)
	if !strings.Contains(text, "default_provider: \"\"") || strings.Contains(text, "\n    alpha:") {
		t.Fatalf("expected cleared default and deleted provider:\n%s", text)
	}
}

func TestDeleteProvidersConfig_CascadeRemovesProviderGroupRefs(t *testing.T) {
	path := writeProviderManagementConfig(t, `
providers:
  default_provider: beta
  items:
    alpha:
      enabled: true
      protocol: openai
    beta:
      enabled: true
      protocol: anthropic
provider_groups:
  - name: mixed
    providers:
      - name: alpha
        weight: 1
      - name: beta
        weight: 2
  - name: only-alpha
    providers:
      - name: alpha
        weight: 1
`)

	blocked, err := DeleteProvidersConfig(path, ProviderDeleteRequest{Names: []string{"alpha"}})
	if err != nil {
		t.Fatalf("DeleteProvidersConfig blocked: %v", err)
	}
	if len(blocked.Blocked) != 1 || blocked.Blocked[0].Code != "provider_group_reference" {
		t.Fatalf("expected provider group blocker, got %+v", blocked)
	}

	result, err := DeleteProvidersConfig(path, ProviderDeleteRequest{Names: []string{"alpha"}, Cascade: true})
	if err != nil {
		t.Fatalf("DeleteProvidersConfig cascade: %v", err)
	}
	if strings.Join(result.Deleted, ",") != "alpha" || strings.Join(result.RemovedGroups, ",") != "only-alpha" || len(result.RemovedGroupRefs) != 2 {
		t.Fatalf("unexpected cascade result: %+v", result)
	}
	text := readProviderManagementConfig(t, path)
	if strings.Contains(text, "only-alpha") || strings.Contains(text, "name: alpha") || strings.Contains(text, "\n    alpha:") {
		t.Fatalf("alpha references should be removed:\n%s", text)
	}
	if !strings.Contains(text, "name: beta") {
		t.Fatalf("beta group reference should remain:\n%s", text)
	}
}

func TestDeleteProvidersConfig_DryRunDoesNotWriteConfigOrAuthStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  default_provider: beta
  items:
    alpha:
      enabled: true
      protocol: openai
      api_key_ref: alpha-ref
    beta:
      enabled: true
      protocol: anthropic
      api_key_ref: beta-ref
`)
	if err := os.WriteFile(configPath, []byte(raw+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	authPath := filepath.Join(dir, "auth.json")
	for ref, key := range map[string]string{"alpha-ref": "sk-alpha", "beta-ref": "sk-beta"} {
		if err := SaveProviderAuthToPath(authPath, ref, ProviderAuthRecord{
			KeyType:  AuthKeyTypeAPIKey,
			AuthMode: AuthKeyTypeAPIKey,
			APIKey:   key,
		}); err != nil {
			t.Fatalf("SaveProviderAuthToPath %s: %v", ref, err)
		}
	}

	result, err := DeleteProvidersConfig(configPath, ProviderDeleteRequest{
		Names:         []string{"alpha"},
		PruneAuth:     true,
		DryRun:        true,
		AuthStorePath: authPath,
	})
	if err != nil {
		t.Fatalf("DeleteProvidersConfig dry-run: %v", err)
	}
	if strings.Join(result.Deleted, ",") != "alpha" || strings.Join(result.AuthPruned, ",") != "alpha-ref" {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	if got := readProviderManagementConfig(t, configPath); got != raw+"\n" {
		t.Fatalf("dry-run should not modify config:\n%s", got)
	}
	if loaded, err := LoadProviderAuthFromPath(authPath, "alpha-ref"); err != nil || loaded.APIKey != "sk-alpha" {
		t.Fatalf("dry-run should keep auth ref, loaded=%+v err=%v", loaded, err)
	}
}

func TestDeleteProvidersConfig_PruneAuthSkipsSharedRefs(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(`
providers:
  default_provider: beta
  items:
    alpha:
      enabled: true
      protocol: openai
      api_key_ref: shared-ref
      auth_ref: alpha-oauth
    beta:
      enabled: true
      protocol: anthropic
      api_key_ref: shared-ref
`)+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	authPath := filepath.Join(dir, "auth.json")
	for ref, key := range map[string]string{"shared-ref": "sk-shared", "alpha-oauth": "access-alpha"} {
		record := ProviderAuthRecord{KeyType: AuthKeyTypeAPIKey, AuthMode: AuthKeyTypeAPIKey, APIKey: key}
		if ref == "alpha-oauth" {
			record = ProviderAuthRecord{KeyType: AuthKeyTypeOAuth, AuthMode: AuthKeyTypeOAuth, AccessToken: key}
		}
		if err := SaveProviderAuthToPath(authPath, ref, record); err != nil {
			t.Fatalf("SaveProviderAuthToPath %s: %v", ref, err)
		}
	}

	result, err := DeleteProvidersConfig(configPath, ProviderDeleteRequest{
		Names:         []string{"alpha"},
		PruneAuth:     true,
		AuthStorePath: authPath,
	})
	if err != nil {
		t.Fatalf("DeleteProvidersConfig prune auth: %v", err)
	}
	if strings.Join(result.AuthPruned, ",") != "alpha-oauth" || len(result.AuthSkipped) != 1 || result.AuthSkipped[0].Ref != "shared-ref" {
		t.Fatalf("unexpected auth prune result: %+v", result)
	}
	if _, err := LoadProviderAuthFromPath(authPath, "alpha-oauth"); err == nil {
		t.Fatal("expected exclusive auth ref to be deleted")
	}
	if loaded, err := LoadProviderAuthFromPath(authPath, "shared-ref"); err != nil || loaded.APIKey != "sk-shared" {
		t.Fatalf("expected shared auth ref to remain, loaded=%+v err=%v", loaded, err)
	}
}
