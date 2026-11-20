package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func providerFragmentFixture(t *testing.T) (userConfig, userProviders, projectConfig, projectProviders string) {
	t.Helper()
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	userConfig = filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, userConfig, `
aicli:
  chat:
    default_model: user-model
`)
	userProviders = filepath.Join(home, ".aicli", ProviderConfigFileName)
	writeConfigLayerFile(t, userProviders, `
providers:
  items:
    user-prov:
      protocol: openai
      base_url: https://user.example/v1
      enabled: true
`)

	projectDir := t.TempDir()
	projectConfig = filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, projectConfig, `
aicli:
  chat:
    stream: true
`)
	projectProviders = filepath.Join(projectDir, ".aicli", ProviderConfigFileName)
	writeConfigLayerFile(t, projectProviders, `
providers:
  items:
    project-prov:
      protocol: openai
      base_url: https://project.example/v1
      enabled: true
`)
	chdirTest(t, projectDir)
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "on")
	return userConfig, userProviders, projectConfig, projectProviders
}

func TestProviderFragmentMergesAndKeepsMainConfigAsSource(t *testing.T) {
	_, userProviders, projectConfig, projectProviders := providerFragmentFixture(t)

	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	if cfg.ConfigFilePath != projectConfig {
		t.Fatalf("ConfigFilePath = %q, want main config %q", cfg.ConfigFilePath, projectConfig)
	}
	if _, ok := cfg.Providers.Items["user-prov"]; !ok {
		t.Fatalf("user provider from providers.yaml missing: %#v", cfg.Providers.Items)
	}
	if _, ok := cfg.Providers.Items["project-prov"]; !ok {
		t.Fatalf("project provider from providers.yaml missing: %#v", cfg.Providers.Items)
	}
	if got := cfg.ConfigOrigins["providers.items.user-prov.protocol"]; got != string(LayerKindUserProviders) {
		t.Fatalf("user provider origin = %q, want %q", got, LayerKindUserProviders)
	}
	if got := cfg.ConfigOrigins["providers.items.project-prov.protocol"]; got != string(LayerKindProjectProviders) {
		t.Fatalf("project provider origin = %q, want %q", got, LayerKindProjectProviders)
	}
	presentFragments := 0
	for _, layer := range cfg.ConfigLayers {
		if layer.Fragment && layer.Present {
			presentFragments++
		}
	}
	if presentFragments != 2 {
		t.Fatalf("present provider fragments = %d, want 2 (user+project): %#v", presentFragments, cfg.ConfigLayers)
	}
	if got := readFileOrFail(t, userProviders); !strings.Contains(got, "user-prov") {
		t.Fatalf("user providers file unexpectedly changed:\n%s", got)
	}
	if got := readFileOrFail(t, projectProviders); !strings.Contains(got, "project-prov") {
		t.Fatalf("project providers file unexpectedly changed:\n%s", got)
	}
}

func TestProviderFragmentRejectsUnsupportedTopLevelKeys(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, configPath, "aicli:\n  chat:\n    stream: true\n")
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", ProviderConfigFileName), "aicli:\n  chat:\n    stream: false\n")
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "on")

	_, err := InitGlobalConfigLayered(configPath, "")
	if err == nil || !strings.Contains(err.Error(), "unsupported top-level key") {
		t.Fatalf("expected unsupported top-level key error, got %v", err)
	}
}

func TestProviderFragmentNewProviderRoutesToFragment(t *testing.T) {
	_, _, projectConfig, projectProviders := providerFragmentFixture(t)
	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	protocol := "openai"
	baseURL := "https://new.example/v1"
	if _, err := UpdateProviderConfig(cfg.ConfigFilePath, ProviderConfigUpdate{
		Name:     "new-prov",
		Protocol: &protocol,
		BaseURL:  &baseURL,
	}); err != nil {
		t.Fatalf("UpdateProviderConfig new provider: %v", err)
	}
	if got := readFileOrFail(t, projectProviders); !strings.Contains(got, "new-prov") {
		t.Fatalf("new provider must land in project providers.yaml:\n%s", got)
	}
	if got := readFileOrFail(t, projectConfig); strings.Contains(got, "new-prov") {
		t.Fatalf("new provider must not land in main config:\n%s", got)
	}
}

func TestProviderFragmentWritesChatAndProxyToCorrectFiles(t *testing.T) {
	_, _, projectConfig, projectProviders := providerFragmentFixture(t)
	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	stream := true
	streamPtr := &stream
	if _, err := UpdateAICLIChatPreferences(cfg.ConfigFilePath, AICLIChatPreferenceUpdate{Stream: &streamPtr}); err != nil {
		t.Fatalf("UpdateAICLIChatPreferences: %v", err)
	}
	mainRaw := readFileOrFail(t, projectConfig)
	if !strings.Contains(mainRaw, "stream: true") {
		t.Fatalf("chat preference must land in main config:\n%s", mainRaw)
	}
	providersRaw := readFileOrFail(t, projectProviders)
	if strings.Contains(providersRaw, "aicli:") {
		t.Fatalf("providers.yaml must never receive aicli section:\n%s", providersRaw)
	}

	proxy := "http://127.0.0.1:7890"
	if _, err := SetGlobalProxyConfig(cfg.ConfigFilePath, GlobalProxyUpdate{HTTP: &proxy}); err != nil {
		t.Fatalf("SetGlobalProxyConfig: %v", err)
	}
	if got := readFileOrFail(t, projectProviders); !strings.Contains(got, "7890") {
		t.Fatalf("global proxy must land in providers.yaml:\n%s", got)
	}
}

func TestProviderFragmentProviderManagementWrites(t *testing.T) {
	_, userProviders, _, projectProviders := providerFragmentFixture(t)
	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	if _, err := SetProvidersEnabledConfig(cfg.ConfigFilePath, []string{"project-prov"}, false); err != nil {
		t.Fatalf("SetProvidersEnabledConfig: %v", err)
	}
	if got := readFileOrFail(t, projectProviders); !strings.Contains(got, "enabled: false") {
		t.Fatalf("enable toggle must land in project providers.yaml:\n%s", got)
	}

	if _, err := SetDefaultProviderConfig(cfg.ConfigFilePath, "user-prov"); err != nil {
		t.Fatalf("SetDefaultProviderConfig: %v", err)
	}
	if got := readFileOrFail(t, userProviders); !strings.Contains(got, "default_provider: user-prov") {
		t.Fatalf("default provider must land in its owner fragment:\n%s", got)
	}

	proxy := "http://127.0.0.1:7891"
	if _, err := SetProviderProxyConfig(cfg.ConfigFilePath, "user-prov", ProviderProxyUpdate{HTTP: &proxy}); err != nil {
		t.Fatalf("SetProviderProxyConfig: %v", err)
	}
	if got := readFileOrFail(t, userProviders); !strings.Contains(got, "7891") {
		t.Fatalf("provider proxy must land in its owner fragment:\n%s", got)
	}

	if _, err := DeleteProvidersConfig(cfg.ConfigFilePath, ProviderDeleteRequest{Names: []string{"project-prov"}}); err != nil {
		t.Fatalf("DeleteProvidersConfig: %v", err)
	}
	if got := readFileOrFail(t, projectProviders); strings.Contains(got, "project-prov") {
		t.Fatalf("deleted provider must be removed from project fragment:\n%s", got)
	}
}

func TestApplyMergedDocumentChangesRoutesNewProviderToFragment(t *testing.T) {
	_, _, _, projectProviders := providerFragmentFixture(t)
	merged, err := LoadMergedConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedConfigDocument: %v", err)
	}
	if !merged.MultiLayer() {
		t.Fatalf("provider fragment must count as a present layer: %#v", merged.PresentLayers)
	}
	updated := mustDocumentCopy(t, merged.Merged)
	providers := updated["providers"].(map[string]interface{})
	items := providers["items"].(map[string]interface{})
	items["web-new"] = map[string]interface{}{
		"protocol": "openai",
		"base_url": "https://web.example/v1",
	}
	if _, err := ApplyMergedDocumentChanges(merged, updated, merged.SourcePath); err != nil {
		t.Fatalf("ApplyMergedDocumentChanges: %v", err)
	}
	if got := readFileOrFail(t, projectProviders); !strings.Contains(got, "web-new") {
		t.Fatalf("web-created provider must land in providers.yaml:\n%s", got)
	}
	if kind := merged.WriteLayerKindFor("providers.items.web-new"); kind != string(LayerKindProjectProviders) {
		t.Fatalf("WriteLayerKindFor(new provider) = %q, want %q", kind, LayerKindProjectProviders)
	}
}

func TestProviderFragmentDeleteBlocksCrossFileReferences(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, configPath, `
providers:
  default_provider: p
aicli:
  chat:
    default_provider: p
`)
	providerPath := filepath.Join(home, ".aicli", ProviderConfigFileName)
	writeConfigLayerFile(t, providerPath, `
providers:
  items:
    p:
      protocol: openai
      base_url: https://p.example/v1
`)
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "on")
	cfg, err := InitGlobalConfigLayered(configPath, "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	result, err := DeleteProvidersConfig(cfg.ConfigFilePath, ProviderDeleteRequest{
		Names:        []string{"p"},
		ClearDefault: true,
	})
	if err != nil {
		t.Fatalf("DeleteProvidersConfig: %v", err)
	}
	if len(result.Blocked) == 0 {
		t.Fatalf("expected cross-file reference blockers, got %#v", result)
	}
	if got := readFileOrFail(t, providerPath); !strings.Contains(got, "p:") {
		t.Fatalf("blocked delete must not remove provider:\n%s", got)
	}
}

func TestProviderFragmentDeleteBlocksGroupAndGatewayReferences(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, configPath, `
provider_groups:
  - name: pool
    providers:
      - name: p
        weight: 1
skills_runtime:
  gateway_provider_name: p
`)
	providerPath := filepath.Join(home, ".aicli", ProviderConfigFileName)
	writeConfigLayerFile(t, providerPath, `
providers:
  items:
    p:
      protocol: openai
      base_url: https://p.example/v1
`)
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "on")
	cfg, err := InitGlobalConfigLayered(configPath, "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	result, err := DeleteProvidersConfig(cfg.ConfigFilePath, ProviderDeleteRequest{
		Names:   []string{"p"},
		Cascade: true,
	})
	if err != nil {
		t.Fatalf("DeleteProvidersConfig: %v", err)
	}
	codes := map[string]bool{}
	for _, blocker := range result.Blocked {
		codes[blocker.Code] = true
	}
	if !codes["provider_group_reference_other_layer"] || !codes["gateway_provider_other_layer"] {
		t.Fatalf("expected cross-file group+gateway blockers, got %#v", result.Blocked)
	}
	if got := readFileOrFail(t, providerPath); !strings.Contains(got, "p:") {
		t.Fatalf("blocked delete must not remove provider:\n%s", got)
	}
}

func TestProviderFragmentDeleteCascadeGroupsSameFile(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, configPath, "aicli:\n  chat:\n    stream: true\n")
	providerPath := filepath.Join(home, ".aicli", ProviderConfigFileName)
	writeConfigLayerFile(t, providerPath, `
providers:
  items:
    p:
      protocol: openai
      base_url: https://p.example/v1
provider_groups:
  - name: pool
    providers:
      - name: p
        weight: 1
`)
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "on")
	cfg, err := InitGlobalConfigLayered(configPath, "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	if _, err := DeleteProvidersConfig(cfg.ConfigFilePath, ProviderDeleteRequest{
		Names:   []string{"p"},
		Cascade: true,
	}); err != nil {
		t.Fatalf("DeleteProvidersConfig cascade: %v", err)
	}
	raw := readFileOrFail(t, providerPath)
	if strings.Contains(raw, "p:") || strings.Contains(raw, "pool") {
		t.Fatalf("cascade delete must remove provider and empty group from fragment:\n%s", raw)
	}
}

func TestProviderFragmentProviderGroupsWriteRoutesToFragment(t *testing.T) {
	_, _, _, projectProviders := providerFragmentFixture(t)
	merged, err := LoadMergedConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedConfigDocument: %v", err)
	}
	updated := mustDocumentCopy(t, merged.Merged)
	updated["provider_groups"] = []interface{}{
		map[string]interface{}{
			"name": "web-pool",
			"providers": []interface{}{
				map[string]interface{}{"name": "project-prov", "weight": 1},
			},
		},
	}
	if _, err := ApplyMergedDocumentChanges(merged, updated, merged.SourcePath); err != nil {
		t.Fatalf("ApplyMergedDocumentChanges provider_groups: %v", err)
	}
	if got := readFileOrFail(t, projectProviders); !strings.Contains(got, "web-pool") {
		t.Fatalf("provider_groups must route to providers.yaml:\n%s", got)
	}
}

func TestProviderFragmentDisabledWritesStayInMainConfig(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, configPath, "aicli:\n  chat:\n    stream: true\n")
	providerPath := filepath.Join(home, ".aicli", ProviderConfigFileName)
	writeConfigLayerFile(t, providerPath, "providers:\n  items:\n    existing:\n      protocol: openai\n")
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "off")
	cfg, err := InitGlobalConfigLayered(configPath, "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	protocol := "openai"
	baseURL := "https://disabled.example/v1"
	if _, err := UpdateProviderConfig(cfg.ConfigFilePath, ProviderConfigUpdate{
		Name:     "disabled-new",
		Protocol: &protocol,
		BaseURL:  &baseURL,
	}); err != nil {
		t.Fatalf("UpdateProviderConfig: %v", err)
	}
	if got := readFileOrFail(t, configPath); !strings.Contains(got, "disabled-new") {
		t.Fatalf("disabled fragment must fall back to main config:\n%s", got)
	}
	if got := readFileOrFail(t, providerPath); strings.Contains(got, "disabled-new") {
		t.Fatalf("disabled fragment must not receive writes:\n%s", got)
	}
}

func TestExplicitConfigIgnoresProviderFragmentAfterReload(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, configPath, "aicli:\n  chat:\n    stream: true\n")
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", ProviderConfigFileName), `
providers:
  items:
    explicit-hidden:
      protocol: openai
      base_url: https://hidden.example/v1
`)
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "on")

	cfg, err := InitGlobalConfigLayered(configPath, configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered explicit: %v", err)
	}
	if _, ok := cfg.Providers.Items["explicit-hidden"]; ok {
		t.Fatal("explicit --config must ignore providers.yaml")
	}
	reloaded, err := ReloadGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("ReloadGlobalConfig: %v", err)
	}
	if _, ok := reloaded.Providers.Items["explicit-hidden"]; ok {
		t.Fatal("explicit --config reload must stay single-file")
	}
}

func TestProviderFragmentEmptyFileAcceptsNewProvider(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, configPath, "aicli:\n  chat:\n    stream: true\n")
	providerPath := filepath.Join(home, ".aicli", ProviderConfigFileName)
	if err := os.WriteFile(providerPath, nil, 0o600); err != nil {
		t.Fatalf("write empty providers.yaml: %v", err)
	}
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "on")
	cfg, err := InitGlobalConfigLayered(configPath, "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	protocol := "openai"
	baseURL := "https://empty.example/v1"
	if _, err := UpdateProviderConfig(cfg.ConfigFilePath, ProviderConfigUpdate{
		Name:     "empty-new",
		Protocol: &protocol,
		BaseURL:  &baseURL,
	}); err != nil {
		t.Fatalf("UpdateProviderConfig with empty fragment: %v", err)
	}
	if got := readFileOrFail(t, providerPath); !strings.Contains(got, "empty-new") {
		t.Fatalf("new provider must route to empty fragment:\n%s", got)
	}
	if got := readFileOrFail(t, configPath); strings.Contains(got, "empty-new") {
		t.Fatalf("new provider must not fall back to main config:\n%s", got)
	}
}

func TestProviderFragmentCreationUsesMinimalSkeleton(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	path := filepath.Join(home, ".aicli", ProviderConfigFileName)
	t.Setenv(ProviderConfigEnvVar, "on")
	unlock := LockConfigFileWrite(path)
	err := func() error {
		defer unlock()
		_, _, err := ensureProviderFragmentAtPathLocked(path)
		return err
	}()
	if err != nil {
		t.Fatalf("ensureProviderFragmentAtPathLocked: %v", err)
	}
	raw := readFileOrFail(t, path)
	if !strings.Contains(raw, "providers:") || !strings.Contains(raw, "items:") {
		t.Fatalf("fragment skeleton missing providers/items:\n%s", raw)
	}
	if strings.Contains(raw, "aicli:") || strings.Contains(raw, "skills_runtime:") {
		t.Fatalf("provider fragment must not contain the full starter config:\n%s", raw)
	}
}

func TestProviderFragmentKillSwitchIgnoresFile(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, configPath, "aicli:\n  chat:\n    stream: true\n")
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", ProviderConfigFileName), `
providers:
  items:
    hidden:
      protocol: openai
      base_url: https://hidden.example/v1
`)
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "off")
	cfg, err := InitGlobalConfigLayered(configPath, "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	if _, ok := cfg.Providers.Items["hidden"]; ok {
		t.Fatal("AICLI_PROVIDERS_FILE=off must ignore providers.yaml")
	}
	for _, layer := range cfg.ConfigLayers {
		if layer.Fragment {
			t.Fatalf("disabled provider fragment must not be in the layer stack: %#v", layer)
		}
	}
}

func TestProviderFragmentOverridesSameLevelMainProvider(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, configPath, `
providers:
  items:
    same:
      protocol: openai
      base_url: https://main.example/v1
`)
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", ProviderConfigFileName), `
providers:
  items:
    same:
      base_url: https://fragment.example/v1
`)
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "on")
	cfg, err := InitGlobalConfigLayered(configPath, "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	if got := cfg.Providers.Items["same"].BaseURL; got != "https://fragment.example/v1" {
		t.Fatalf("same-level provider fragment must win, base_url = %q", got)
	}
	if got := cfg.ConfigOrigins["providers.items.same.base_url"]; got != string(LayerKindUserProviders) {
		t.Fatalf("fragment field origin = %q, want %q", got, LayerKindUserProviders)
	}
	if got := cfg.ConfigOrigins["providers.items.same.protocol"]; got != string(LayerKindUser) {
		t.Fatalf("main-only field origin = %q, want %q", got, LayerKindUser)
	}
}

func TestProviderFragmentOnlyStillNeedsMergedView(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", ProviderConfigFileName), `
providers:
  items:
    only-fragment:
      protocol: openai
      base_url: https://fragment-only.example/v1
`)
	t.Setenv(MergeConfigEnvVar, "on")
	t.Setenv(ProviderConfigEnvVar, "on")
	merged, err := LoadMergedConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedConfigDocument: %v", err)
	}
	if !merged.MultiLayer() {
		t.Fatalf("a single provider fragment must still use the merged view: %#v", merged.PresentLayers)
	}
	parsed := merged.Merged["providers"].(map[string]interface{})
	items := parsed["items"].(map[string]interface{})
	if _, ok := items["only-fragment"]; !ok {
		t.Fatalf("fragment-only provider missing from merged document: %#v", merged.Merged)
	}
}
