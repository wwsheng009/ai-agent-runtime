package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// layeredProviderFixture builds two config layers:
//
//	user layer    — owns providers.items.user-prov
//	project layer — owns providers.items.project-prov and pins
//	                providers.default_provider
//
// It returns the two file paths after loading the merged configuration.
func layeredProviderFixture(t *testing.T) (userConfig, projectConfig string) {
	t.Helper()
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	userConfig = filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, userConfig, `
providers:
  default_provider: user-prov
  items:
    user-prov:
      protocol: openai
      base_url: https://user.example.invalid/v1
      enabled: true
`)
	projectDir := t.TempDir()
	projectConfig = filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, projectConfig, `
providers:
  default_provider: project-prov
  items:
    project-prov:
      protocol: openai
      base_url: https://project.example.invalid/v1
      enabled: true
`)
	chdirTest(t, projectDir)
	t.Setenv(MergeConfigEnvVar, "on")
	return userConfig, projectConfig
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// TestSetDefaultProviderConfigWritesAcrossLayers pins the cross-layer case:
// providers.default_provider is pinned by the project layer while the provider
// entry itself lives in the user layer. The write must go to the layer that
// owns the key (so it stays effective) and the merged view must resolve.
func TestSetDefaultProviderConfigWritesAcrossLayers(t *testing.T) {
	userConfig, projectConfig := layeredProviderFixture(t)
	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	if cfg.ConfigFilePath != projectConfig {
		t.Fatalf("ConfigFilePath = %q, want %q", cfg.ConfigFilePath, projectConfig)
	}
	userBefore := readFileOrFail(t, userConfig)

	result, err := SetDefaultProviderConfig(cfg.ConfigFilePath, "user-prov")
	if err != nil {
		t.Fatalf("SetDefaultProviderConfig: %v", err)
	}
	if result.ConfigPath != projectConfig {
		t.Fatalf("write target = %q, want %q", result.ConfigPath, projectConfig)
	}
	if got := readFileOrFail(t, projectConfig); !strings.Contains(got, "default_provider: user-prov") {
		t.Fatalf("project layer must receive the default provider, got:\n%s", got)
	}
	if got := readFileOrFail(t, userConfig); got != userBefore {
		t.Fatalf("user layer must stay untouched, got:\n%s", got)
	}

	// The merged view must report the new default (the whole point of routing).
	reloaded, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("reload merged config: %v", err)
	}
	if reloaded.Providers.DefaultProvider != "user-prov" {
		t.Fatalf("merged default provider = %q, want user-prov", reloaded.Providers.DefaultProvider)
	}
}

// TestSetProviderProxyConfigRoutesToProviderLayer pins the proxy write path:
// the provider lives in the user layer, so the proxy must be written there and
// the project layer must stay byte-identical.
func TestSetProviderProxyConfigRoutesToProviderLayer(t *testing.T) {
	userConfig, projectConfig := layeredProviderFixture(t)
	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	projectBefore := readFileOrFail(t, projectConfig)

	httpProxy := "http://127.0.0.1:7890"
	result, err := SetProviderProxyConfig(cfg.ConfigFilePath, "user-prov", ProviderProxyUpdate{HTTP: &httpProxy})
	if err != nil {
		t.Fatalf("SetProviderProxyConfig: %v", err)
	}
	if result.ConfigPath != userConfig {
		t.Fatalf("write target = %q, want %q", result.ConfigPath, userConfig)
	}
	if got := readFileOrFail(t, userConfig); !strings.Contains(got, "7890") {
		t.Fatalf("user layer must receive the proxy, got:\n%s", got)
	}
	if got := readFileOrFail(t, projectConfig); got != projectBefore {
		t.Fatalf("project layer must stay untouched, got:\n%s", got)
	}

	// Remove must follow the same route and clear the proxy again.
	if _, err := RemoveProviderProxyConfig(cfg.ConfigFilePath, "user-prov"); err != nil {
		t.Fatalf("RemoveProviderProxyConfig: %v", err)
	}
	if got := readFileOrFail(t, userConfig); strings.Contains(got, "7890") {
		t.Fatalf("user layer must lose the proxy, got:\n%s", got)
	}
	if got := readFileOrFail(t, projectConfig); got != projectBefore {
		t.Fatalf("project layer must stay untouched after remove, got:\n%s", got)
	}
}

// TestProviderWritesRejectUnknownProviders keeps the not-found guard intact in
// layered mode: routing must never invent a provider that no layer defines.
func TestProviderWritesRejectUnknownProviders(t *testing.T) {
	_, projectConfig := layeredProviderFixture(t)
	cfg, err := InitGlobalConfigLayered(ResolveConfigPath(DefaultConfigSearchPaths()), "")
	if err != nil {
		t.Fatalf("InitGlobalConfigLayered: %v", err)
	}
	projectBefore := readFileOrFail(t, projectConfig)

	if _, err := SetDefaultProviderConfig(cfg.ConfigFilePath, "ghost"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown provider must stay rejected, got %v", err)
	}
	httpProxy := "http://127.0.0.1:7890"
	if _, err := SetProviderProxyConfig(cfg.ConfigFilePath, "ghost", ProviderProxyUpdate{HTTP: &httpProxy}); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown provider proxy write must stay rejected, got %v", err)
	}
	if got := readFileOrFail(t, projectConfig); got != projectBefore {
		t.Fatalf("failed writes must not modify the target, got:\n%s", got)
	}
}

// TestProviderProxyWriteStaysFileLocalWithoutLayering pins the single-file
// contract: without layered merging the provider lookup stays file-local.
func TestProviderProxyWriteStaysFileLocalWithoutLayering(t *testing.T) {
	userConfig, projectConfig := layeredProviderFixture(t)
	t.Setenv(MergeConfigEnvVar, "off")
	cfg, err := InitGlobalConfig(resolveSingleConfigPathForTest(t, projectConfig))
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if cfg.ConfigFilePath != projectConfig {
		t.Fatalf("ConfigFilePath = %q, want %q", cfg.ConfigFilePath, projectConfig)
	}
	userBefore := readFileOrFail(t, userConfig)
	httpProxy := "http://127.0.0.1:7890"
	if _, err := SetProviderProxyConfig(cfg.ConfigFilePath, "user-prov", ProviderProxyUpdate{HTTP: &httpProxy}); err == nil {
		t.Fatalf("single-file mode must not see providers from other layers")
	}
	if got := readFileOrFail(t, userConfig); got != userBefore {
		t.Fatalf("user layer must stay untouched, got:\n%s", got)
	}
}

func resolveSingleConfigPathForTest(t *testing.T, projectConfig string) string {
	t.Helper()
	if _, err := os.Stat(projectConfig); err != nil {
		t.Fatalf("stat project config: %v", err)
	}
	return projectConfig
}
