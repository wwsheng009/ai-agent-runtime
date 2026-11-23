package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"gopkg.in/yaml.v3"
)

func TestMemoryOnboardingDryRunDoesNotCreateRuntimeConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".aicli", "runtime.yaml")
	result, _, err := runMemoryOnboarding(memoryOnboardingOptions{
		Mode: "shadow", DryRun: true, RuntimeConfig: path,
	})
	require.NoError(t, err)
	require.True(t, result.DryRun)
	require.False(t, result.Applied)
	require.Equal(t, "shadow", result.Mode)
	require.Len(t, result.Changed, 13)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestMemoryOnboardingProtectsExistingValuesAndPreservesOtherRuntimeConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	original := []byte(`version: v1
shell:
  prefer:
    - cmd
memory:
  mode: off
`)
	require.NoError(t, os.WriteFile(path, original, 0o600))

	_, _, err := runMemoryOnboarding(memoryOnboardingOptions{
		Mode: "on", RuntimeConfig: path,
	})
	require.ErrorContains(t, err, "pass --force")
	stillOriginal, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, stillOriginal)

	preview, _, err := runMemoryOnboarding(memoryOnboardingOptions{
		Mode: "on", DryRun: true, RuntimeConfig: path,
	})
	require.NoError(t, err)
	require.Len(t, preview.Conflicting, 1)
	stillOriginal, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, stillOriginal)

	applied, _, err := runMemoryOnboarding(memoryOnboardingOptions{
		Mode: "on", Force: true, RuntimeConfig: path,
	})
	require.NoError(t, err)
	require.True(t, applied.Applied)
	require.Contains(t, applied.Changed, "memory.mode")

	written, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(written), "prefer:")
	require.Contains(t, string(written), "- cmd")
	config := runtimecfg.DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal(written, config))
	require.NoError(t, runtimecfg.ValidateRuntimeConfig(config))
	require.Equal(t, "on", config.Memory.Mode)
	require.Equal(t, "project", config.Memory.Scope)
	require.True(t, config.Memory.Generation.Enabled)
	require.Equal(t, "both", config.Memory.Generation.Trigger)
	require.True(t, config.Memory.Generation.TurnDigest.Enabled)
	require.True(t, config.Memory.Generation.TurnDigest.RecoveryOnStartup)
	require.True(t, config.Memory.Maintenance.CaptureExtraction.Enabled)
	require.True(t, config.Memory.Injection.Enabled)
	require.True(t, config.Memory.Redaction.Enabled)
	require.True(t, config.Memory.Redaction.DropOnSecret)
	require.True(t, config.Memory.Tools.Enabled, "mode=on must enable the user-visible memory tools")
	require.False(t, config.Memory.Maintenance.JobDeadlineRepair.Enabled,
		"onboarding must not mutate historical jobs before an explicit dry-run and apply")
	require.Empty(t, config.Memory.Generation.Provider)
	require.Empty(t, config.Memory.Generation.Model)
	require.NotContains(t, string(written), "api_key")
	require.NotContains(t, string(written), "storePath")

	repeated, _, err := runMemoryOnboarding(memoryOnboardingOptions{
		Mode: "on", RuntimeConfig: path,
	})
	require.NoError(t, err)
	require.False(t, repeated.Applied)
	require.Empty(t, repeated.Changed)
	require.Contains(t, repeated.Message, "already configured")
}

func TestMemoryOnboardingRejectsInvalidModeAndKeepsRuntimeModeOffByDefault(t *testing.T) {
	_, _, err := runMemoryOnboarding(memoryOnboardingOptions{
		Mode: "invalid", RuntimeConfig: filepath.Join(t.TempDir(), "runtime.yaml"),
	})
	require.ErrorContains(t, err, "mode must be off, shadow or on")

	require.Equal(t, "off", runtimecfg.DefaultRuntimeConfig().Memory.Mode)
}

func TestMemoryOnboardingCommandIsRegisteredAndDocumentsSafeMode(t *testing.T) {
	cmd := NewOnboardingCommand()
	memoryCmd, _, err := cmd.Find([]string{"memory"})
	require.NoError(t, err)
	require.Equal(t, "memory", memoryCmd.Name())
	require.Contains(t, memoryCmd.Long, "off")
	require.Contains(t, memoryCmd.Long, "shadow")
	require.Equal(t, "off", memoryCmd.Flag("mode").DefValue)
	require.Contains(t, memoryCmd.Example, "--dry-run")
}
