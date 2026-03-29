package config

import (
	"testing"
	"time"

	runtimeexecutor "github.com/ai-gateway/ai-agent-runtime/internal/executor"
	"github.com/stretchr/testify/require"
)

func TestRuntimeManager_VersionHistoryAndRollback(t *testing.T) {
	rm := NewRuntimeManager("")

	cfg := DefaultRuntimeConfig()
	cfg.Version = ""
	require.NoError(t, rm.Update(cfg))

	latest := rm.Get()
	require.NotEmpty(t, latest.Version)

	history := rm.ListHistory()
	require.Len(t, history, 1)
	firstVersion := history[0].Version
	require.Equal(t, latest.Version, firstVersion)

	cfg2 := DefaultRuntimeConfig()
	cfg2.Version = "v2"
	require.NoError(t, rm.Update(cfg2))

	history = rm.ListHistory()
	require.Len(t, history, 2)

	require.NoError(t, rm.Rollback(firstVersion))
	rolled := rm.Get()
	require.Equal(t, firstVersion, rolled.Version)
}

func TestValidateRolloutConfig_RequiresCandidateAndVersion(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	cfg.Version = "v1"
	cfg.Rollout.Enabled = true
	cfg.Rollout.Mode = "canary"
	cfg.Rollout.Percent = 20
	cfg.Rollout.CandidateVersion = ""
	cfg.Rollout.CandidateFile = ""

	err := ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg.Rollout.CandidateVersion = "v2"
	cfg.Rollout.CandidateFile = "candidate.yaml"
	err = ValidateRuntimeConfig(cfg)
	require.NoError(t, err)
}

func TestValidateSandboxConfig(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	cfg.Sandbox = runtimeexecutor.SandboxConfig{
		MaxExecutionTime: 10 * time.Second,
		AllowedPaths:     []string{"./workspace"},
		DeniedPaths:      []string{"./secret"},
		ReadOnlyPaths:    []string{"./readonly"},
		AllowedCommands:  []string{"git"},
		DeniedCommands:   []string{"powershell"},
		EnvWhitelist:     []string{"PATH"},
	}

	err := ValidateRuntimeConfig(cfg)
	require.NoError(t, err)

	cfg.Sandbox.EnvWhitelist = []string{"PATH", ""}
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg.Sandbox.EnvWhitelist = []string{"PATH"}
	cfg.Sandbox.DeniedHosts = []string{"duckduckgo.com", ""}
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)
}

func TestValidateContextConfig(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.Equal(t, "balanced", cfg.Context.Profile)

	cfg.Context.Profile = "compact"
	cfg.Context.MaxMessages = 12
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Context.Profile = "invalid"
	err := ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.Context.MaxRecallResults = -1
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.Context.Profile = "hot"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Context.Profile = "cold"
	require.NoError(t, ValidateRuntimeConfig(cfg))
}

func TestValidateCatalogConfig(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Catalog.Backend = "sqlite"
	cfg.Catalog.SnapshotPath = "catalog.sqlite"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Catalog.Backend = "file"
	cfg.Catalog.SnapshotPath = "catalog.json"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Catalog.Backend = "file"
	cfg.Catalog.SnapshotPath = ""
	err := ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.Catalog.Backend = "invalid"
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)
}
