package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func TestConfigSplitProvidersDryRunAndWrite(t *testing.T) {
	previousGlobal := agentconfig.GetGlobalConfig()
	t.Cleanup(func() { agentconfig.SetGlobalConfig(previousGlobal) })
	previousHome := agentconfig.UserHomeDirForTest()
	home := t.TempDir()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })
	t.Setenv(agentconfig.SystemPresetDirEnv, filepath.Join(t.TempDir(), "absent-system-presets"))
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")
	t.Setenv(agentconfig.ProviderConfigEnvVar, "on")

	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeCommandsTestFile(t, configPath, `
providers:
  default_provider: alpha
  items:
    alpha:
      protocol: openai
      base_url: ${PROVIDER_BASE:-https://alpha.example/v1}
provider_groups:
  - name: pool
    strategy: round_robin
    providers:
      - name: alpha
        weight: 1
        enabled: true
aicli:
  chat:
    stream: true
`)
	providerPath := filepath.Join(home, ".aicli", agentconfig.ProviderConfigFileName)

	result, err := runConfigSplitProviders(configSplitProvidersOptions{})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !result.DryRun || result.WroteTarget {
		t.Fatalf("dry-run result = %#v", result)
	}
	if result.Providers != 1 || result.ProviderGroups != 1 {
		t.Fatalf("dry-run counts = providers %d groups %d", result.Providers, result.ProviderGroups)
	}
	if _, err := os.Stat(providerPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create providers.yaml: %v", err)
	}

	result, err = runConfigSplitProviders(configSplitProvidersOptions{Write: true})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !result.WroteTarget || result.PrunedOwner {
		t.Fatalf("write result = %#v", result)
	}
	providerRaw := readCommandsTestFile(t, providerPath)
	if !strings.Contains(providerRaw, "alpha") || !strings.Contains(providerRaw, "${PROVIDER_BASE") {
		t.Fatalf("providers.yaml must preserve provider content and env placeholders:\n%s", providerRaw)
	}
	if !strings.Contains(readCommandsTestFile(t, configPath), "providers:") {
		t.Fatalf("--keep-main must leave the main provider section in place")
	}

	// Reset the target and prune the main config.
	if err := os.Remove(providerPath); err != nil {
		t.Fatalf("remove provider target: %v", err)
	}
	writeCommandsTestFile(t, configPath, `
providers:
  items:
    beta:
      protocol: openai
      base_url: https://beta.example/v1
aicli:
  chat:
    stream: true
`)
	result, err = runConfigSplitProviders(configSplitProvidersOptions{Write: true, PruneMain: true})
	if err != nil {
		t.Fatalf("write+prune: %v", err)
	}
	if !result.PrunedOwner || result.Backup == "" {
		t.Fatalf("prune result = %#v", result)
	}
	mainRaw := readCommandsTestFile(t, configPath)
	if strings.Contains(mainRaw, "providers:") {
		t.Fatalf("--prune-main must remove main providers section:\n%s", mainRaw)
	}
	if !strings.Contains(readCommandsTestFile(t, result.Backup), "providers:") {
		t.Fatalf("prune must keep a backup with the original provider section")
	}
	if !strings.Contains(readCommandsTestFile(t, providerPath), "beta") {
		t.Fatalf("migrated provider missing from providers.yaml")
	}
}

func TestConfigSplitProvidersRejectsNonEmptyTarget(t *testing.T) {
	previousGlobal := agentconfig.GetGlobalConfig()
	t.Cleanup(func() { agentconfig.SetGlobalConfig(previousGlobal) })
	previousHome := agentconfig.UserHomeDirForTest()
	home := t.TempDir()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })
	t.Setenv(agentconfig.SystemPresetDirEnv, filepath.Join(t.TempDir(), "absent-system-presets"))
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")
	t.Setenv(agentconfig.ProviderConfigEnvVar, "on")

	configPath := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeCommandsTestFile(t, configPath, "providers:\n  items:\n    alpha:\n      protocol: openai\n")
	writeCommandsTestFile(t, filepath.Join(home, ".aicli", agentconfig.ProviderConfigFileName), "providers:\n  items:\n    existing:\n      protocol: openai\n")
	_, err := runConfigSplitProviders(configSplitProvidersOptions{Write: true})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected non-empty target rejection, got %v", err)
	}
}

func TestConfigSplitProvidersProjectOwnerTargetsProjectProviderFile(t *testing.T) {
	previousGlobal := agentconfig.GetGlobalConfig()
	t.Cleanup(func() { agentconfig.SetGlobalConfig(previousGlobal) })
	previousHome := agentconfig.UserHomeDirForTest()
	home := t.TempDir()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })
	t.Setenv(agentconfig.SystemPresetDirEnv, filepath.Join(t.TempDir(), "absent-system-presets"))
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")
	t.Setenv(agentconfig.ProviderConfigEnvVar, "on")

	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeCommandsTestFile(t, projectConfig, "providers:\n  items:\n    project-only:\n      protocol: openai\n")
	t.Chdir(projectDir)

	result, err := runConfigSplitProviders(configSplitProvidersOptions{})
	if err != nil {
		t.Fatalf("dry-run project owner: %v", err)
	}
	want := filepath.Join(projectDir, ".aicli", agentconfig.ProviderConfigFileName)
	if !sameLayerPathForCommand(result.Target, want) {
		t.Fatalf("project owner must target project providers.yaml: got %q want %q", result.Target, want)
	}
}

func TestConfigSplitProvidersRejectsMultipleOwnerLayers(t *testing.T) {
	previousGlobal := agentconfig.GetGlobalConfig()
	t.Cleanup(func() { agentconfig.SetGlobalConfig(previousGlobal) })
	previousHome := agentconfig.UserHomeDirForTest()
	home := t.TempDir()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })
	t.Setenv(agentconfig.SystemPresetDirEnv, filepath.Join(t.TempDir(), "absent-system-presets"))
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")
	t.Setenv(agentconfig.ProviderConfigEnvVar, "on")

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeCommandsTestFile(t, userConfig, "providers:\n  items:\n    user-p:\n      protocol: openai\n")
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeCommandsTestFile(t, projectConfig, "providers:\n  items:\n    project-p:\n      protocol: openai\n")
	t.Chdir(projectDir)

	_, err := runConfigSplitProviders(configSplitProvidersOptions{})
	if err == nil || !strings.Contains(err.Error(), "span multiple layers") {
		t.Fatalf("expected multi-owner rejection, got %v", err)
	}
}
