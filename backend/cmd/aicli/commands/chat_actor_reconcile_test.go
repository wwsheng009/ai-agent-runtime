package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// TestLocalRegistryReconcileTuningPrecedence locks the P2-9 configuration path:
// environment > runtime config > shared default (10m / observe).
func TestLocalRegistryReconcileTuningPrecedence(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.ActorRegistry = newLocalActorRegistry(host)

	mode, interval := host.localRegistryReconcileTuning()
	require.Equal(t, agentcontrol.ReconcileModeObserve, mode)
	require.Equal(t, agentcontrol.DefaultReconcileInterval, interval)

	host.RuntimeConfig = &runtimecfg.RuntimeConfig{
		Agents: runtimecfg.AgentsConfig{
			RegistryReconcileMode:     "enforce",
			RegistryReconcileInterval: 30 * time.Minute,
		},
	}
	mode, interval = host.localRegistryReconcileTuning()
	require.Equal(t, agentcontrol.ReconcileModeEnforce, mode)
	require.Equal(t, 30*time.Minute, interval)

	t.Setenv(localRegistryReconcileModeEnv, "observe")
	t.Setenv(localRegistryReconcileIntervalEnv, "45m")
	mode, interval = host.localRegistryReconcileTuning()
	require.Equal(t, agentcontrol.ReconcileModeObserve, mode, "environment overrides config")
	require.Equal(t, 45*time.Minute, interval)

	// A malformed override must not silently disable the sweep.
	t.Setenv(localRegistryReconcileIntervalEnv, "not-a-duration")
	mode, interval = host.localRegistryReconcileTuning()
	require.Equal(t, agentcontrol.ReconcileModeObserve, mode)
	require.Equal(t, 30*time.Minute, interval)
}

// TestLocalRegistryReconcileEnforcesAndStops covers the host wiring: the loop
// starts with the host, converges ACTIVE_AGENT_SESSION_MISSING drift left by a
// dead session, exposes the cached report for `/debug`, and stops on Close.
func TestLocalRegistryReconcileEnforcesAndStops(t *testing.T) {
	ctx := context.Background()
	host := newLocalSupervisionTestHost(t)
	host.ActorRegistry = newLocalActorRegistry(host)
	host.lifecycleCtx, host.lifecycleCancel = context.WithCancel(context.Background())

	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent-registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	host.AgentRegistryStore = store

	_, err = store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       "agent-a",
		RootSessionID: "root-1",
		AgentPath:     "/root/worker-a",
		SessionID:     "sess-a",
		AgentType:     agentcontrol.AgentTypeChild,
		Status:        agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)

	t.Setenv(localRegistryReconcileModeEnv, "enforce")
	reconciler := host.startLocalRegistryReconcile()
	require.NotNil(t, reconciler)
	require.Same(t, reconciler, host.startLocalRegistryReconcile(), "reconciler is built once")
	require.Equal(t, agentcontrol.ReconcileModeEnforce, reconciler.Mode)

	// The startup pass runs immediately, so the dead binding converges without
	// waiting a full interval (the session store is empty, so it is missing).
	require.Eventually(t, func() bool {
		records, listErr := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			RootSessionID: "root-1",
			IncludeClosed: true,
		})
		return listErr == nil && len(records) == 1 && records[0].Closed()
	}, 10*time.Second, 20*time.Millisecond, "startup pass must converge the missing session")

	summary := host.localRegistryReconcileSummary()
	require.Contains(t, summary, "reconcile=enforce")
	require.Contains(t, summary, "converged=1")
	require.Contains(t, summary, "last_reconcile=")

	host.Close()
	require.ErrorIs(t, host.lifecycleCtx.Err(), context.Canceled)
	// A second pass after convergence is idempotent: nothing left to write.
	report, err := reconciler.RunOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, report.IssueCount)
}

// TestLocalRegistryReconcileWithoutStoreStaysInactive keeps the previous spawn
// behavior when no durable registry is configured.
func TestLocalRegistryReconcileWithoutStoreStaysInactive(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.ActorRegistry = newLocalActorRegistry(host)

	require.Nil(t, host.startLocalRegistryReconcile())
	require.Nil(t, host.localRegistryReconcile())
	require.Equal(t, "reconcile=not_run", host.localRegistryReconcileSummary())

	require.Nil(t, (&localChatRuntimeHost{}).startLocalRegistryReconcile())
	require.Equal(t, "reconcile=not_run", (*localChatRuntimeHost)(nil).localRegistryReconcileSummary())
}
