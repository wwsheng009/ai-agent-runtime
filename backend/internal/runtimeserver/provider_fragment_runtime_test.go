package runtimeserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
)

func TestProviderFragmentRuntimeLoadAndConfigDocumentSave(t *testing.T) {
	previousGlobal := agentconfig.GetGlobalConfig()
	t.Cleanup(func() { agentconfig.SetGlobalConfig(previousGlobal) })

	home := t.TempDir()
	previousHome := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, userConfig, "aicli:\n  chat:\n    default_model: user-model\n")
	userProviders := filepath.Join(home, ".aicli", agentconfig.ProviderConfigFileName)
	writeLayeredConfigFile(t, userProviders, `
providers:
  items:
    user-prov:
      protocol: openai
      base_url: https://user.example/v1
`)

	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, projectConfig, "aicli:\n  chat:\n    stream: true\n")
	projectProviders := filepath.Join(projectDir, ".aicli", agentconfig.ProviderConfigFileName)
	writeLayeredConfigFile(t, projectProviders, `
providers:
  items:
    project-prov:
      protocol: openai
      base_url: https://project.example/v1
`)
	t.Chdir(projectDir)
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")
	t.Setenv(agentconfig.ProviderConfigEnvVar, "on")

	cfg, _, err := LoadRuntimeAgentConfigWithOptions(AgentConfigLoadOptions{Path: projectConfig})
	require.NoError(t, err)
	require.Contains(t, cfg.Providers.Items, "user-prov")
	require.Contains(t, cfg.Providers.Items, "project-prov")

	service := NewLocalConfigDocumentService(projectConfig)
	doc, err := service.LoadDocument()
	require.NoError(t, err)
	require.Contains(t, doc.Layers, runtimeapi.ConfigDocumentLayer{
		Kind:     string(agentconfig.LayerKindUserProviders),
		Path:     resolveAbsolutePath(userProviders),
		Present:  true,
		Fragment: true,
		Section:  "providers",
	})
	require.Contains(t, doc.Layers, runtimeapi.ConfigDocumentLayer{
		Kind:     string(agentconfig.LayerKindProjectProviders),
		Path:     resolveAbsolutePath(projectProviders),
		Present:  true,
		Fragment: true,
		Section:  "providers",
	})
	require.Equal(t, string(agentconfig.LayerKindProjectProviders), doc.Origins["providers.items.project-prov.base_url"])

	parsed := copyDocumentMap(t, doc.Parsed)
	items := parsed["providers"].(map[string]interface{})["items"].(map[string]interface{})
	items["project-prov"].(map[string]interface{})["base_url"] = "https://edited.example/v1"
	items["runtime-new"] = map[string]interface{}{
		"protocol": "openai",
		"base_url": "https://runtime-new.example/v1",
	}
	savedDoc, err := service.SaveDocument(runtimeapi.ConfigDocumentSaveRequest{Parsed: parsed, Mode: "structured"})
	require.NoError(t, err)
	require.Equal(t, string(agentconfig.LayerKindProjectProviders),
		savedDoc.RuntimeImpact.PathLayers["providers.items.runtime-new"])

	projectRaw := mustReadRuntimeFile(t, projectProviders)
	require.Contains(t, projectRaw, "edited.example")
	require.Contains(t, projectRaw, "runtime-new")
	mainRaw := mustReadRuntimeFile(t, projectConfig)
	require.NotContains(t, mainRaw, "runtime-new")

	explicitCfg, _, err := LoadRuntimeAgentConfigWithOptions(AgentConfigLoadOptions{
		Path:     userConfig,
		Explicit: true,
	})
	require.NoError(t, err)
	require.NotContains(t, explicitCfg.Providers.Items, "user-prov")
	require.NotContains(t, explicitCfg.Providers.Items, "project-prov")
	explicitDoc, err := NewLocalConfigDocumentService(userConfig).LoadDocument()
	require.NoError(t, err)
	require.Empty(t, explicitDoc.Layers)

	signatureBefore := ConfigSourceSignatureForOptions(projectConfig, false)
	require.Contains(t, signatureBefore, agentconfig.ProviderConfigFileName)
	require.NoError(t, os.WriteFile(projectProviders, append([]byte(projectRaw), []byte("\n# provider fragment changed\n")...), 0o600))
	require.NotEqual(t, signatureBefore, ConfigSourceSignatureForOptions(projectConfig, false))
	require.NotContains(t, ConfigSourceSignatureForOptions(userConfig, true), agentconfig.ProviderConfigFileName)
}

func mustReadRuntimeFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(raw)
}
