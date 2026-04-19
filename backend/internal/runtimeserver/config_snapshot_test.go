package runtimeserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadRuntimeAgentConfigPrefersSnapshotFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  host: base.local\n"), 0o644))

	snapshotPath := ResolveAgentConfigSnapshotInfo(configPath).SnapshotPath
	require.NoError(t, os.WriteFile(snapshotPath, []byte("server:\n  host: snapshot.local\n"), 0o644))

	cfg, info, err := LoadRuntimeAgentConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.True(t, info.SnapshotExists)
	require.Equal(t, snapshotPath, info.ActivePath)
	require.Equal(t, "snapshot.local", cfg.Server.Host)
}

func TestLoadRuntimeAgentConfigRecoversSparseSnapshot(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	baseConfig := `
server:
  host: base.local
providers:
  default_provider: openai
  items:
    openai:
      base_url: https://api.openai.com
`
	require.NoError(t, os.WriteFile(configPath, []byte(baseConfig), 0o644))

	snapshotPath := ResolveAgentConfigSnapshotInfo(configPath).SnapshotPath
	snapshotConfig := `
providers:
  proxy:
    enabled: true
    http: http://127.0.0.1:10810
`
	require.NoError(t, os.WriteFile(snapshotPath, []byte(snapshotConfig), 0o644))

	cfg, info, err := LoadRuntimeAgentConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.True(t, info.SnapshotExists)
	require.Equal(t, snapshotPath, info.ActivePath)
	require.Equal(t, "base.local", cfg.Server.Host)
	require.Equal(t, "openai", cfg.Providers.DefaultProvider)
	require.Contains(t, cfg.Providers.Items, "openai")
	require.Equal(t, "http://127.0.0.1:10810", cfg.Providers.Proxy.HTTP)
	require.True(t, cfg.Providers.Proxy.Enabled)
}
