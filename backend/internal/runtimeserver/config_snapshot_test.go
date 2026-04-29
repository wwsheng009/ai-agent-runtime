package runtimeserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestLoadRuntimeAgentConfigUsesBaseConfigOnly(t *testing.T) {
	previous := agentconfig.GetGlobalConfig()
	t.Cleanup(func() {
		agentconfig.SetGlobalConfig(previous)
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  host: base.local\n"), 0o644))

	cfg, info, err := LoadRuntimeAgentConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.False(t, info.SnapshotExists)
	require.Empty(t, info.SnapshotPath)
	require.Equal(t, configPath, info.ActivePath)
	require.Equal(t, "base.local", cfg.Server.Host)
	require.NotNil(t, agentconfig.GetGlobalConfig())
	require.Equal(t, "base.local", agentconfig.GetGlobalConfig().Server.Host)
}

func TestLoadRuntimeAgentConfigIgnoresSnapshotFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  host: base.local\n"), 0o644))

	snapshotPath := filepath.Join(filepath.Dir(configPath), "config.runtime.snapshot.yaml")
	require.NoError(t, os.WriteFile(snapshotPath, []byte("server:\n  host: snapshot.local\n"), 0o644))

	cfg, info, err := LoadRuntimeAgentConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.False(t, info.SnapshotExists)
	require.Empty(t, info.SnapshotPath)
	require.Equal(t, configPath, info.ActivePath)
	require.Equal(t, "base.local", cfg.Server.Host)
}
