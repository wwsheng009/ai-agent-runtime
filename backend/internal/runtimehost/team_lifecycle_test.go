package runtimehost

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestBuildTeamSupervisorConfigUsesSharedRuntimeConfig(t *testing.T) {
	backoff := []time.Duration{time.Second, 3 * time.Second}
	config := &runtimecfg.RuntimeConfig{
		Team: runtimecfg.TeamConfig{
			Orchestrator: runtimecfg.TeamOrchestratorConfig{
				ReconcileInterval: 7 * time.Second,
				RestartBackoff:    backoff,
				MaxRestartBackoff: 42 * time.Second,
			},
		},
	}

	got := BuildTeamSupervisorConfig(config)
	require.Equal(t, 7*time.Second, got.ScanInterval)
	require.Equal(t, backoff, got.RestartBackoff)
	require.Equal(t, 42*time.Second, got.MaxRestartBackoff)

	require.Zero(t, BuildTeamSupervisorConfig(nil).ScanInterval)
}
