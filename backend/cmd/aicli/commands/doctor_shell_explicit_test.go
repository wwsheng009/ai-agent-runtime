package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func TestLoadDoctorShellConfigUsesExplicitRuntimeOverride(t *testing.T) {
	home := t.TempDir()
	previousHome := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })

	previousConfig := agentconfig.GetGlobalConfig()
	t.Cleanup(func() { agentconfig.SetGlobalConfig(previousConfig) })
	explicit := filepath.Join(t.TempDir(), "custom-runtime.yaml")
	require.NoError(t, os.WriteFile(explicit, []byte("shell:\n  prefer: [cmd]\n"), 0o644))
	agentconfig.SetGlobalConfig(&agentconfig.Config{
		SkillsRuntime: &agentconfig.SkillsRuntimeConfig{ConfigFile: explicit},
	})

	cfg, path, layer, writable, err := loadDoctorShellConfig()
	require.NoError(t, err)
	require.Equal(t, []string{runtimeexecutor.ShellIDCmd}, cfg.NormalizedPrefer())
	require.Equal(t, explicit, path)
	require.Equal(t, "explicit", layer)
	require.True(t, writable)
}
