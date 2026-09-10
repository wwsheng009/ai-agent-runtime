package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolatePresetTestEnv points the user home and the system preset directory
// at throwaway locations so tests never touch the real ~/.aicli/presets.yaml
// or an admin-deployed system directory.
func isolatePresetTestEnv(t *testing.T) {
	t.Helper()
	originalHome := UserHomeDirForTest()
	t.Cleanup(func() { SetUserHomeDirForTest(originalHome) })
	home := t.TempDir()
	SetUserHomeDirForTest(func() (string, error) {
		return home, nil
	})
	dir := filepath.Join(t.TempDir(), "system-presets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir system presets dir: %v", err)
	}
	t.Setenv(SystemPresetDirEnv, dir)
}

const mergePresetYAML = `
providers:
  headers:
    x-opencode-session: "{session_id}"
    x-opencode-client: "preset-client"
    user-agent: "preset-ua/1.0"
    x-preset-only: "keep"
  items:
    opencode.ai:
      enabled: true
      protocol: openai
      base_url: https://opencode.ai/zen/go
      headers:
        x-opencode-session: "{session_id}"
        x-opencode-client: "preset-client"
`

const mergeUserYAML = `
providers:
  headers:
    x-opencode-client: "my-client"
  items:
    opencode.ai:
      api_key: sk-user
      default_model: deepseek-v4-flash
      headers:
        x-opencode-project: "{project_id}"
`

func TestMergeConfigYAML_UserOverridesPreset(t *testing.T) {
	mergedYAML, err := MergeConfigYAML([]byte(mergePresetYAML), []byte(mergeUserYAML))
	if err != nil {
		t.Fatalf("MergeConfigYAML: %v", err)
	}
	var merged Config
	if err := unmarshalYAML(mergedYAML, &merged); err != nil {
		t.Fatalf("decode merged: %v\n%s", err, mergedYAML)
	}
	if got := merged.Providers.Headers["x-opencode-client"]; got != "my-client" {
		t.Fatalf("user override lost: got %q", got)
	}
	if got := merged.Providers.Headers["user-agent"]; got != "preset-ua/1.0" {
		t.Fatalf("preset baseline lost: got %q", got)
	}
	if got := merged.Providers.Headers["x-opencode-session"]; got != "{session_id}" {
		t.Fatalf("preset template lost: got %q", got)
	}
	if got := merged.Providers.Headers["x-preset-only"]; got != "keep" {
		t.Fatalf("preset-only header lost: got %q", got)
	}
	provider, ok := merged.Providers.Items["opencode.ai"]
	if !ok {
		t.Fatal("merged provider missing")
	}
	if !provider.Enabled || provider.BaseURL != "https://opencode.ai/zen/go" {
		t.Fatalf("preset provider fields lost: %+v", provider)
	}
	if provider.APIKey != "sk-user" || provider.DefaultModel != "deepseek-v4-flash" {
		t.Fatalf("user provider fields lost: %+v", provider)
	}
	if got := provider.Headers["x-opencode-project"]; got != "{project_id}" {
		t.Fatalf("user provider header lost: %+v", provider.Headers)
	}
	if got := provider.Headers["x-opencode-client"]; got != "preset-client" {
		t.Fatalf("preset provider header lost: %+v", provider.Headers)
	}
}

func TestMergeConfigYAML_ZeroValueFieldsDoNotShadow(t *testing.T) {
	// The user fragment does not mention base_url/enabled; the zero-value
	// struct fields of an unmarshalled fragment must not wipe preset values.
	base := []byte("providers:\n  items:\n    p:\n      enabled: true\n      base_url: https://gw.example\n      default_model: m1\n")
	overlay := []byte("providers:\n  items:\n    p:\n      api_key: sk\n")
	mergedYAML, err := MergeConfigYAML(base, overlay)
	if err != nil {
		t.Fatalf("MergeConfigYAML: %v", err)
	}
	var merged Config
	if err := unmarshalYAML(mergedYAML, &merged); err != nil {
		t.Fatalf("decode merged: %v", err)
	}
	provider := merged.Providers.Items["p"]
	if !provider.Enabled || provider.BaseURL != "https://gw.example" {
		t.Fatalf("preset fields shadowed by user zero values: %+v", provider)
	}
	if provider.APIKey != "sk" || provider.DefaultModel != "m1" {
		t.Fatalf("merge result unexpected: %+v", provider)
	}
}

func TestMergeConfigYAML_SliceReplacedNotAppended(t *testing.T) {
	base := []byte("providers:\n  items:\n    p:\n      supported_models: [a, b]\n")
	overlay := []byte("providers:\n  items:\n    p:\n      supported_models: [c]\n")
	mergedYAML, err := MergeConfigYAML(base, overlay)
	if err != nil {
		t.Fatalf("MergeConfigYAML: %v", err)
	}
	var merged Config
	if err := unmarshalYAML(mergedYAML, &merged); err != nil {
		t.Fatalf("decode merged: %v", err)
	}
	got := merged.Providers.Items["p"].SupportedModels
	if len(got) != 1 || got[0] != "c" {
		t.Fatalf("slice not replaced: %+v", got)
	}
}

func TestMergeConfigYAML_EmptyInputs(t *testing.T) {
	overlay := []byte("providers:\n  default_provider: alpha\n")
	merged, err := MergeConfigYAML(nil, overlay)
	if err != nil || string(merged) == "" {
		t.Fatalf("nil base: %s err=%v", merged, err)
	}
	merged, err = MergeConfigYAML(overlay, nil)
	if err != nil || string(merged) == "" {
		t.Fatalf("nil overlay: %s err=%v", merged, err)
	}
}

func TestMergeConfigYAML_FieldCaseVariantsMerge(t *testing.T) {
	// "Headers" (preset) vs "headers" (user) collide case-insensitively and
	// must merge into one entry instead of producing duplicate keys.
	base := []byte("providers:\n  Headers:\n    X-A: \"1\"\n")
	overlay := []byte("providers:\n  headers:\n    X-B: \"2\"\n")
	mergedYAML, err := MergeConfigYAML(base, overlay)
	if err != nil {
		t.Fatalf("MergeConfigYAML: %v", err)
	}
	var merged Config
	if err := unmarshalYAML(mergedYAML, &merged); err != nil {
		t.Fatalf("decode merged: %v\n%s", err, mergedYAML)
	}
	if got := merged.Providers.Headers["X-A"]; got != "1" {
		t.Fatalf("preset header lost: %+v", merged.Providers.Headers)
	}
	if got := merged.Providers.Headers["X-B"]; got != "2" {
		t.Fatalf("user header lost: %+v", merged.Providers.Headers)
	}
}

func TestMergeConfigYAML_DataKeysKeepCase(t *testing.T) {
	// Provider names and header names are data keys and must keep their
	// original casing through a merge.
	base := []byte("providers:\n  items:\n    MyProvider:\n      headers:\n        X-Trace-Id: \"a\"\n      model_mappings:\n        GPT-4.1: gpt-4.1-2025\n")
	overlay := []byte("providers:\n  items:\n    MyProvider:\n      headers:\n        X-Trace-Id: \"b\"\n")
	mergedYAML, err := MergeConfigYAML(base, overlay)
	if err != nil {
		t.Fatalf("MergeConfigYAML: %v", err)
	}
	var merged Config
	if err := unmarshalYAML(mergedYAML, &merged); err != nil {
		t.Fatalf("decode merged: %v\n%s", err, mergedYAML)
	}
	provider, ok := merged.Providers.Items["MyProvider"]
	if !ok {
		t.Fatalf("provider name casing changed: %+v", merged.Providers.Items)
	}
	if provider.Headers["X-Trace-Id"] != "b" {
		t.Fatalf("header value not overridden: %+v", provider.Headers)
	}
	if got := provider.ModelMappings["GPT-4.1"]; got != "gpt-4.1-2025" {
		t.Fatalf("model mapping key casing changed: %+v", provider.ModelMappings)
	}
}

func TestPresetMatchesHost(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"", "any-host", true},
		{"", "", true},
		{"workstation", "workstation", true},
		{"workstation", "WorkStation", true},
		{"workstation", "other", false},
		{"*.corp.example.com", "dev-1.corp.example.com", true},
		{"*.corp.example.com", "corp.example.com", false},
		{"*.corp.example.com", "dev-1.corp.example.com.evil.com", false},
		{"dev-*", "dev-box", true},
		{"dev-*", "", false},
		{"?", "a", true},
		{"?", "ab", false},
	}
	for _, tc := range cases {
		preset := Preset{Match: PresetMatch{Hostname: tc.pattern}}
		if got := PresetMatchesHost(preset, tc.host); got != tc.want {
			t.Fatalf("PresetMatchesHost(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
		}
	}
}

func TestLoadSystemPresets_BuiltinOnlyWhenNoDirectory(t *testing.T) {
	isolatePresetTestEnv(t)
	presets, err := LoadSystemPresets()
	if err != nil {
		t.Fatalf("LoadSystemPresets: %v", err)
	}
	if len(presets) == 0 {
		t.Fatal("built-in presets missing")
	}
	if presets[0].Name != "opencode-gateway" {
		t.Fatalf("unexpected first preset %q", presets[0].Name)
	}
	if !presets[0].IsEnabled() {
		t.Fatal("built-in opencode-gateway preset must be enabled by default")
	}
	cfg, err := presets[0].DecodedConfig()
	if err != nil {
		t.Fatalf("DecodedConfig: %v", err)
	}
	provider := cfg.Providers.Items["opencode.ai"]
	if provider.BaseURL != "https://opencode.ai/zen/go" {
		t.Fatalf("built-in opencode-gateway config unexpected: %+v", provider)
	}
	if !provider.Enabled || provider.Protocol != "openai" {
		t.Fatalf("built-in provider flags unexpected: %+v", provider)
	}
	if provider.Compatibility.Profile != CompatibilityProfileOpenCodeConsoleGo {
		t.Fatalf("built-in profile unexpected: %+v", provider.Compatibility)
	}
	headers := provider.Headers
	if headers["x-opencode-session"] != "{session_id}" || headers["x-opencode-client"] != "{client}" {
		t.Fatalf("built-in headers unexpected: %+v", headers)
	}
}

func TestApplyMatchingSystemPresets_HostnameGated(t *testing.T) {
	isolatePresetTestEnv(t)
	dir := SystemPresetDir()
	content := `
presets:
  - name: opencode-gateway
    enabled: true
    match:
      hostname: "*.corp.example.com"
  - name: corp-only
    enabled: true
    match:
      hostname: "dev-*"
    config:
      providers:
        headers:
          x-session-affinity: "{session_id}"
`
	if err := os.WriteFile(filepath.Join(dir, "presets.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write presets.yaml: %v", err)
	}

	onHost, err := ApplyMatchingSystemPresets(&Config{}, "dev-1.corp.example.com")
	if err != nil {
		t.Fatalf("ApplyMatchingSystemPresets: %v", err)
	}
	if _, ok := onHost.Providers.Items["opencode.ai"]; !ok {
		t.Fatal("opencode-gateway preset not applied on matching host")
	}
	if got := onHost.Providers.Headers["x-session-affinity"]; got != "{session_id}" {
		t.Fatalf("corp-only preset not applied: got %q", got)
	}

	otherHost, err := ApplyMatchingSystemPresets(&Config{}, "home-box")
	if err != nil {
		t.Fatalf("ApplyMatchingSystemPresets: %v", err)
	}
	if len(otherHost.Providers.Headers) != 0 {
		t.Fatalf("hostname-gated preset headers leaked to non-matching host: %+v", otherHost.Providers.Headers)
	}
	leaked, ok := otherHost.Providers.Items["opencode.ai"]
	if ok {
		if leaked.BaseURL != "" || leaked.Enabled || len(leaked.Headers) != 0 {
			t.Fatalf("hostname-gated opencode-gateway preset leaked to non-matching host: %+v", leaked)
		}
		if len(leaked.ResponseMarkerRules) == 0 {
			t.Fatal("always-on marker-cleanup preset must apply on every host")
		}
	}
}

func TestApplyMatchingSystemPresets_FileEnablesBuiltin(t *testing.T) {
	isolatePresetTestEnv(t)
	dir := SystemPresetDir()
	content := `
presets:
  - name: opencode-gateway
    enabled: true
`
	if err := os.WriteFile(filepath.Join(dir, "presets.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write presets.yaml: %v", err)
	}
	merged, err := ApplyMatchingSystemPresets(&Config{}, "")
	if err != nil {
		t.Fatalf("ApplyMatchingSystemPresets: %v", err)
	}
	provider, ok := merged.Providers.Items["opencode.ai"]
	if !ok {
		t.Fatal("built-in preset not enabled by file override")
	}
	if !provider.Enabled || provider.BaseURL != "https://opencode.ai/zen/go" {
		t.Fatalf("built-in provider unexpected: %+v", provider)
	}
	if got := provider.Headers["x-opencode-session"]; got != "{session_id}" {
		t.Fatalf("built-in header unexpected: %+v", provider.Headers)
	}
}

func TestInitGlobalConfig_NoSystemDirIsUnchanged(t *testing.T) {
	isolatePresetTestEnv(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
providers:
  default_provider: alpha
  items:
    alpha:
      protocol: openai
      default_model: gpt-4.1
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if cfg.Providers.DefaultProvider != "alpha" || len(cfg.Providers.Items) != 1 {
		t.Fatalf("unexpected config: %+v", cfg.Providers)
	}
}

func TestInitGlobalConfig_MergesEnabledPresetUnderUserConfig(t *testing.T) {
	isolatePresetTestEnv(t)
	dir := SystemPresetDir()
	if err := os.WriteFile(filepath.Join(dir, "presets.yaml"), []byte("presets:\n  - name: opencode-gateway\n    enabled: true\n"), 0o644); err != nil {
		t.Fatalf("write presets.yaml: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
providers:
  default_provider: opencode.ai
  items:
    opencode.ai:
      api_key: sk-user
      default_model: deepseek-v4-flash
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	provider, ok := cfg.Providers.Items["opencode.ai"]
	if !ok {
		t.Fatalf("provider missing: %+v", cfg.Providers.Items)
	}
	if provider.BaseURL != "https://opencode.ai/zen/go" {
		t.Fatalf("preset base_url lost: %+v", provider)
	}
	if !provider.Enabled {
		t.Fatalf("preset enabled flag lost: %+v", provider)
	}
	if provider.APIKey != "sk-user" {
		t.Fatalf("user api_key lost: %+v", provider)
	}
	if provider.DefaultModel != "deepseek-v4-flash" {
		t.Fatalf("user model lost: %+v", provider)
	}
	if got := provider.Headers["x-opencode-session"]; got != "{session_id}" {
		t.Fatalf("preset header lost: %+v", provider.Headers)
	}
	if cfg.ConfigFilePath != configPath {
		t.Fatalf("ConfigFilePath = %q, want %q", cfg.ConfigFilePath, configPath)
	}
}

func TestSystemPresetDir_EnvOverridesDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom")
	t.Setenv(SystemPresetDirEnv, dir)
	if got := SystemPresetDir(); got != dir {
		t.Fatalf("SystemPresetDir = %q, want %q", got, dir)
	}
	t.Setenv(SystemPresetDirEnv, "  ")
	if got := SystemPresetDir(); got == "" {
		t.Fatal("SystemPresetDir fell back to empty with whitespace env")
	}
}

func TestLoadSystemPresets_SortedFileApplication(t *testing.T) {
	isolatePresetTestEnv(t)
	dir := SystemPresetDir()
	presetsDir := filepath.Join(dir, "presets")
	if err := os.MkdirAll(presetsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		"b.yaml": "presets:\n  - name: shared\n    enabled: true\n    config:\n      providers:\n        headers:\n          x-session-affinity: \"{session_id}\"\n",
		"a.yaml": "presets:\n  - name: shared\n    config:\n      providers:\n        headers:\n          x-custom: \"from-a\"\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(presetsDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	presets, err := LoadSystemPresets()
	if err != nil {
		t.Fatalf("LoadSystemPresets: %v", err)
	}
	var shared *Preset
	for index := range presets {
		if presets[index].Name == "shared" {
			shared = &presets[index]
			break
		}
	}
	if shared == nil {
		t.Fatal("shared preset missing")
	}
	if !shared.IsEnabled() {
		t.Fatal("shared preset not enabled")
	}
	cfg, err := shared.DecodedConfig()
	if err != nil {
		t.Fatalf("DecodedConfig: %v", err)
	}
	headers := cfg.Providers.Headers
	if headers["x-custom"] != "from-a" || headers["x-session-affinity"] != "{session_id}" {
		t.Fatalf("merged preset headers unexpected: %+v", headers)
	}
}

func TestApplyMatchingSystemPresets_MinimaxMarkerCleanupPreset(t *testing.T) {
	isolatePresetTestEnv(t)
	dir := SystemPresetDir()
	content := `
presets:
  - name: opencode-gateway-minimax-marker-cleanup
    enabled: true
`
	if err := os.WriteFile(filepath.Join(dir, "presets.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write presets.yaml: %v", err)
	}
	merged, err := ApplyMatchingSystemPresets(&Config{}, "")
	if err != nil {
		t.Fatalf("ApplyMatchingSystemPresets: %v", err)
	}
	provider, ok := merged.Providers.Items["opencode.ai"]
	if !ok {
		t.Fatal("marker-cleanup preset did not create opencode.ai item")
	}
	if len(provider.ResponseMarkerRules) != 1 {
		t.Fatalf("expected 1 response_marker_rule, got %+v", provider.ResponseMarkerRules)
	}
	rule := provider.ResponseMarkerRules[0]
	if len(rule.Models) != 1 || rule.Models[0] != "*minimax*" {
		t.Fatalf("unexpected models: %+v", rule.Models)
	}
	if len(rule.Markers) != 1 || rule.Markers[0] != "]<]minimax[>[" {
		t.Fatalf("unexpected markers: %+v", rule.Markers)
	}
}

func TestEnsureUserPresetsFile_CreatesFromEmbedded(t *testing.T) {
	isolatePresetTestEnv(t)
	path, created, err := EnsureUserPresetsFile()
	if err != nil {
		t.Fatalf("EnsureUserPresetsFile: %v", err)
	}
	if !created || path == "" || !fileExists(path) {
		t.Fatalf("expected created file, got path=%q created=%v", path, created)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created presets: %v", err)
	}
	if len(raw) == 0 || !strings.Contains(string(raw), "opencode-gateway") {
		t.Fatalf("created presets content unexpected:\n%s", raw)
	}
	presets, err := parsePresetYAML(raw)
	if err != nil {
		t.Fatalf("created presets unparsable: %v", err)
	}
	if len(presets) != 2 {
		t.Fatalf("created presets unexpected: %+v", presets)
	}
	names := make([]string, 0, len(presets))
	for _, preset := range presets {
		names = append(names, preset.Name)
	}
	if names[0] != "opencode-gateway" || names[1] != "opencode-gateway-minimax-marker-cleanup" {
		t.Fatalf("created presets unexpected: %+v", names)
	}
}

func TestEnsureUserPresetsFile_KeepsExistingFile(t *testing.T) {
	isolatePresetTestEnv(t)
	userFile := UserPresetsPath()
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "presets:\n  - name: custom\n    enabled: true\n    config:\n      providers:\n        headers:\n          x-custom: \"mine\"\n"
	if err := os.WriteFile(userFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write user presets: %v", err)
	}
	path, created, err := EnsureUserPresetsFile()
	if err != nil {
		t.Fatalf("EnsureUserPresetsFile: %v", err)
	}
	if created || path != userFile {
		t.Fatalf("existing file must be kept: path=%q created=%v", path, created)
	}
	raw, _ := os.ReadFile(userFile)
	if string(raw) != content {
		t.Fatalf("existing file modified:\n%s", raw)
	}
}

func TestMergeWithPresets_InactiveWithoutPresetLayer(t *testing.T) {
	isolatePresetTestEnv(t)
	merged, err := MergeWithPresets([]byte("providers:\n  default_provider: alpha\n"))
	if err != nil {
		t.Fatalf("MergeWithPresets: %v", err)
	}
	if merged != nil {
		t.Fatalf("expected nil when no preset layer is deployed, got %s", merged)
	}
}

func TestMergeWithPresets_ActiveThroughUserPresetsFile(t *testing.T) {
	isolatePresetTestEnv(t)
	userFile := UserPresetsPath()
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `
presets:
  - name: opencode-gateway
    enabled: true
  - name: corp-only
    enabled: true
    config:
      providers:
        headers:
          x-session-affinity: "{session_id}"
`
	if err := os.WriteFile(userFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write user presets: %v", err)
	}
	mergedYAML, err := MergeWithPresets([]byte("providers:\n  default_provider: opencode.ai\n"))
	if err != nil {
		t.Fatalf("MergeWithPresets: %v", err)
	}
	if mergedYAML == nil {
		t.Fatal("expected merged YAML")
	}
	var merged Config
	if err := unmarshalYAML(mergedYAML, &merged); err != nil {
		t.Fatalf("decode merged: %v", err)
	}
	if merged.Providers.DefaultProvider != "opencode.ai" {
		t.Fatalf("user default_provider lost: %+v", merged.Providers)
	}
	if got := merged.Providers.Headers["x-session-affinity"]; got != "{session_id}" {
		t.Fatalf("user-preset header missing: %+v", merged.Providers.Headers)
	}
	provider, ok := merged.Providers.Items["opencode.ai"]
	if !ok {
		t.Fatal("built-in provider missing after user preset enable")
	}
	if provider.BaseURL != "https://opencode.ai/zen/go" {
		t.Fatalf("built-in provider unexpected: %+v", provider)
	}
}

func TestLoadSystemPresets_UserFileOverridesSystemDirectory(t *testing.T) {
	isolatePresetTestEnv(t)
	systemFile := filepath.Join(SystemPresetDir(), "presets.yaml")
	if err := os.WriteFile(systemFile, []byte("presets:\n  - name: shared\n    enabled: true\n    config:\n      providers:\n        headers:\n          x-shared: \"from-system\"\n"), 0o644); err != nil {
		t.Fatalf("write system presets: %v", err)
	}
	userFile := UserPresetsPath()
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(userFile, []byte("presets:\n  - name: shared\n    config:\n      providers:\n        headers:\n          x-shared: \"from-user\"\n          x-user-only: \"yes\"\n"), 0o644); err != nil {
		t.Fatalf("write user presets: %v", err)
	}
	presets, err := LoadSystemPresets()
	if err != nil {
		t.Fatalf("LoadSystemPresets: %v", err)
	}
	var shared *Preset
	for index := range presets {
		if presets[index].Name == "shared" {
			shared = &presets[index]
			break
		}
	}
	if shared == nil {
		t.Fatal("shared preset missing")
	}
	cfg, err := shared.DecodedConfig()
	if err != nil {
		t.Fatalf("DecodedConfig: %v", err)
	}
	headers := cfg.Providers.Headers
	if headers["x-shared"] != "from-user" {
		t.Fatalf("user preset should override system preset: %+v", headers)
	}
	if headers["x-user-only"] != "yes" {
		t.Fatalf("user-only header missing: %+v", headers)
	}
}
