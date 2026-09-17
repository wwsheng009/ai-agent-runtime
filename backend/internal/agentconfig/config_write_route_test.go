package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// preserveGlobalConfig keeps write routing tests from leaking the layered
// global config into the rest of the package's tests.
func preserveGlobalConfig(t *testing.T) {
	t.Helper()
	previous := globalConfig
	t.Cleanup(func() { globalConfig = previous })
}

func TestWriteTargetForKeysPrefersSpecificThenHigherLayer(t *testing.T) {
	userFile := filepath.Join("home", ".aicli", "config.yaml")
	projectFile := filepath.Join("proj", ".aicli", "config.yaml")
	cfg := &Config{
		ConfigMergeMode: MergeModeOn,
		ConfigLayers: []ConfigLayer{
			{Kind: LayerKindUser, Path: userFile, Present: true},
			{Kind: LayerKindProject, Path: projectFile, Present: true},
		},
		ConfigOriginFiles: map[string]string{
			"providers.items.openai":              projectFile,
			"providers.items.openai.api_key":      userFile,
			"providers.items.openai.api_key_ref":  projectFile,
			"providers.default_provider":          userFile,
			"aicli.chat.default_model":            userFile,
		},
	}

	// Only the user layer defines aicli.chat: the write must go there.
	if got := cfg.WriteTargetForKeys("aicli.chat"); got != userFile {
		t.Fatalf("aicli.chat target = %q, want %q", got, userFile)
	}
	// The deepest matching origin wins over the shallower one.
	if got := cfg.WriteTargetForKeys("providers.items.openai.api_key"); got != userFile {
		t.Fatalf("api_key target = %q, want %q", got, userFile)
	}
	// A write spanning layers resolves to the highest-precedence layer.
	if got := cfg.WriteTargetForKeys("aicli.chat.default_model", "providers.default_provider"); got != userFile {
		t.Fatalf("multi-key target = %q, want %q", got, userFile)
	}
	// Unknown keys have no origin: callers keep their default target.
	if got := cfg.WriteTargetForKeys("providers.items.brand-new"); got != "" {
		t.Fatalf("unknown key must not be routed, got %q", got)
	}
	// Routing is inert outside layered mode.
	off := *cfg
	off.ConfigMergeMode = MergeModeOff
	if got := off.WriteTargetForKeys("aicli.chat"); got != "" {
		t.Fatalf("off mode must not route, got %q", got)
	}
}

func TestWriteTargetForKeysTieBreakIsDeterministic(t *testing.T) {
	userFile := filepath.Join("home", ".aicli", "config.yaml")
	projectFile := filepath.Join("proj", ".aicli", "config.yaml")
	cfg := &Config{
		ConfigMergeMode: MergeModeOn,
		ConfigLayers: []ConfigLayer{
			{Kind: LayerKindUser, Path: userFile, Present: true},
			{Kind: LayerKindProject, Path: projectFile, Present: true},
		},
		ConfigOriginFiles: map[string]string{
			"providers.items.openai.header_a": userFile,
			"providers.items.openai.header_b": projectFile,
		},
	}
	for attempt := 0; attempt < 20; attempt++ {
		if got := cfg.WriteTargetForKeys("providers.items.openai"); got != projectFile {
			t.Fatalf("equal-depth origins must resolve to the higher layer, got %q", got)
		}
	}
}

func TestRouteConfigWritePathOnlyReroutesStackMembers(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, userConfig, "providers:\n  items:\n    openai:\n      api_key: user-key\n")
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, projectConfig, "providers:\n  items:\n    other:\n      api_key: project-key\n")
	t.Chdir(projectDir)
	t.Setenv(MergeConfigEnvVar, "on")

	if _, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), ""); err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}

	// The provider lives in the user layer, so a write initiated through the
	// project path must be rerouted to the user file.
	if got := routeConfigWritePath(projectConfig, "providers.items.openai"); got != userConfig {
		t.Fatalf("routed target = %q, want %q", got, userConfig)
	}
	// Project-owned keys stay in the project file.
	if got := routeConfigWritePath(projectConfig, "providers.items.other"); got != projectConfig {
		t.Fatalf("routed target = %q, want %q", got, projectConfig)
	}
	// New keys keep the caller's target (the highest layer).
	if got := routeConfigWritePath(projectConfig, "providers.items.brand-new"); got != projectConfig {
		t.Fatalf("new key must keep the caller target, got %q", got)
	}
	// Paths outside the active stack are never touched (custom --config files,
	// temp files, runtime-server paths).
	custom := filepath.Join(t.TempDir(), "custom.yaml")
	if got := routeConfigWritePath(custom, "providers.items.openai"); got != custom {
		t.Fatalf("non-stack path must not be rerouted, got %q", got)
	}
}

func TestUpdateProviderConfigWritesBackToOriginLayer(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, userConfig, `
providers:
  items:
    openai:
      api_key: user-key
      base_url: https://user.example
`)
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, projectConfig, `
providers:
  items:
    other:
      api_key: project-key
`)
	t.Chdir(projectDir)
	t.Setenv(MergeConfigEnvVar, "on")

	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	// The effective config resolves the project file as its source...
	if cfg.ConfigFilePath != projectConfig {
		t.Fatalf("ConfigFilePath = %q, want %q", cfg.ConfigFilePath, projectConfig)
	}

	newURL := "https://routed.example"
	if _, err := UpdateProviderConfig(cfg.ConfigFilePath, ProviderConfigUpdate{Name: "openai", BaseURL: &newURL}); err != nil {
		t.Fatalf("UpdateProviderConfig: %v", err)
	}

	// ...but the edit landed in the layer that owns the provider.
	userRaw, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(userRaw), "routed.example") {
		t.Fatalf("expected the user layer to receive the edit, got:\n%s", userRaw)
	}
	projectRaw, err := os.ReadFile(projectConfig)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(projectRaw), "openai") {
		t.Fatalf("project layer must stay untouched, got:\n%s", projectRaw)
	}
	// Comment/placeholder preservation is the existing contract: the user file
	// keeps its other keys.
	if !strings.Contains(string(userRaw), "api_key: user-key") {
		t.Fatalf("unrelated keys must be preserved, got:\n%s", userRaw)
	}
}
