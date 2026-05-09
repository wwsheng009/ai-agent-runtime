package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
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

func TestValidateAgentConfig_AllowsNonPositiveMaxStepsAsUnlimited(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	cfg.Agent.MaxMaxSteps = 0
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Agent.MaxMaxSteps = -3
	require.NoError(t, ValidateRuntimeConfig(cfg))
}

func TestValidateAgentConfig_ParallelToolDefaults(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.False(t, cfg.Agent.EnableParallelTools)
	require.Equal(t, 1, cfg.Agent.MaxParallelToolCalls)
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Agent.MaxParallelToolCalls = 0
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Agent.MaxParallelToolCalls = -1
	require.Error(t, ValidateRuntimeConfig(cfg))
}

func TestValidateAgentsConfig_DefaultsAndValidation(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.Equal(t, 6, cfg.Agents.MaxThreads)
	require.Equal(t, 1, cfg.Agents.MaxDepth)
	require.Equal(t, int((30 * time.Second).Milliseconds()), cfg.Agents.DefaultWaitTimeoutMs)
	require.Equal(t, "none", cfg.Agents.DefaultForkTurns)
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Agents.MaxThreads = -1
	require.Error(t, ValidateRuntimeConfig(cfg))

	cfg = DefaultRuntimeConfig()
	cfg.Agents.MaxDepth = -1
	require.Error(t, ValidateRuntimeConfig(cfg))

	cfg = DefaultRuntimeConfig()
	cfg.Agents.DefaultWaitTimeoutMs = -1
	require.Error(t, ValidateRuntimeConfig(cfg))

	cfg = DefaultRuntimeConfig()
	cfg.Agents.DefaultForkTurns = "2"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Agents.DefaultForkTurns = "invalid"
	require.Error(t, ValidateRuntimeConfig(cfg))
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
	require.Equal(t, 32000, cfg.Context.FallbackMaxPromptTokens)
	require.False(t, cfg.Workspace.Enabled)
	require.Empty(t, cfg.Workspace.Mode)
	require.Empty(t, cfg.Context.WorkspaceMode)

	cfg.Context.Profile = "compact"
	cfg.Context.MaxMessages = 12
	cfg.Context.FallbackMaxPromptTokens = 32000
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Context.Profile = "invalid"
	err := ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.Context.MaxRecallResults = -1
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.Context.FallbackMaxPromptTokens = -1
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.Context.Profile = "hot"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Context.Profile = "cold"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg = DefaultRuntimeConfig()
	cfg.Context.WorkspaceMode = "signals"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Context.WorkspaceMode = "invalid"
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)
}

func TestValidateWorkspaceConfig(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	cfg.Workspace.Enabled = true
	cfg.Workspace.Mode = "broad"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.Workspace.Mode = "invalid"
	err := ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.Workspace.MaxFileSize = -1
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)
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

func TestValidateAgentControlConfig(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.AgentControl.StorePath = "agent-control.sqlite"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.AgentControl.StoreDSN = "file:agent-control?mode=memory&cache=shared"
	err := ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.AgentControl.StoreDSN = "file:agent-control?mode=memory&cache=shared"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg = DefaultRuntimeConfig()
	cfg.AgentControl.StorePath = "agent-control.sqlite"
	cfg.AgentControl.AgentStorePath = "agent-control-agents.sqlite"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg = DefaultRuntimeConfig()
	cfg.AgentControl.MailboxStorePath = "agent-control-mailbox.sqlite"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.AgentControl.MailboxStoreDSN = "file:agent-control-mailbox?mode=memory&cache=shared"
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.AgentControl.MailboxStoreDSN = "file:agent-control-mailbox?mode=memory&cache=shared"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg = DefaultRuntimeConfig()
	cfg.AgentControl.AgentStorePath = "agent-control-agents.sqlite"
	require.NoError(t, ValidateRuntimeConfig(cfg))

	cfg.AgentControl.AgentStoreDSN = "file:agent-control-agents?mode=memory&cache=shared"
	err = ValidateRuntimeConfig(cfg)
	require.Error(t, err)

	cfg = DefaultRuntimeConfig()
	cfg.AgentControl.AgentStoreDSN = "file:agent-control-agents?mode=memory&cache=shared"
	require.NoError(t, ValidateRuntimeConfig(cfg))
}
