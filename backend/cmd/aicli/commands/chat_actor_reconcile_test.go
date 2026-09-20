package commands

import (
	"context"
	"path/filepath"
	"strings"
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
	// Isolate the worktree half of the pass: the sweep resolves the workspace
	// root from the runtime config, and a nil config falls back to the git root
	// of the test's working directory — an enforce pass would then reclaim
	// directories in the developer's real checkout.
	host.RuntimeConfig = &runtimecfg.RuntimeConfig{
		Workspace: runtimecfg.WorkspaceConfig{Root: t.TempDir()},
	}
	reconciler := host.startLocalRegistryReconcile()
	require.NotNil(t, reconciler)
	require.Same(t, reconciler, host.startLocalRegistryReconcile(), "reconciler is built once")
	require.Equal(t, agentcontrol.ReconcileModeEnforce, reconciler.Mode)

	// The startup pass runs immediately, so the dead binding converges without
	// waiting a full interval (the session store is empty, so it is missing).
	// The cached report is published only when the whole pass returns (the
	// worktree sweep runs last), so wait on the summary: the store row
	// converges mid-pass, and reading the summary at that instant would race
	// the report cache and see "not_run".
	require.Eventually(t, func() bool {
		return strings.Contains(host.localRegistryReconcileSummary(), "reconcile=enforce")
	}, 10*time.Second, 20*time.Millisecond, "startup pass must publish the enforce report")

	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: "root-1",
		IncludeClosed: true,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.True(t, records[0].Closed(), "startup pass must converge the missing session")

	summary := host.localRegistryReconcileSummary()
	require.Contains(t, summary, "reconcile=enforce")
	require.Contains(t, summary, "converged=1")
	require.Contains(t, summary, "last_reconcile=")
	// H3 的唤醒治理必须搭同一趟 pass 跑（retention.go 的接线点），否则
	// closed 行只能等身份行的 30 天窗口，active 行的存量也无人收敛。
	require.Contains(t, summary, "wake_prune_mode=enforce")

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

// TestLocalRegistryTerminalRetentionPrecedence locks the retention path with the
// same precedence as the sweep settings: environment > runtime config > shared
// default. AICLI_REGISTRY_RETENTION=off (or a negative config value) normalizes
// to 0, which is the documented "keep terminal rows forever" opt-out.
func TestLocalRegistryTerminalRetentionPrecedence(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.ActorRegistry = newLocalActorRegistry(host)

	require.Equal(t, agentcontrol.DefaultTerminalRetention, host.localRegistryTerminalRetention())

	host.RuntimeConfig = &runtimecfg.RuntimeConfig{
		Agents: runtimecfg.AgentsConfig{RegistryTerminalRetention: 72 * time.Hour},
	}
	require.Equal(t, 72*time.Hour, host.localRegistryTerminalRetention())

	t.Setenv(localRegistryRetentionEnv, "12h")
	require.Equal(t, 12*time.Hour, host.localRegistryTerminalRetention(), "environment overrides config")

	t.Setenv(localRegistryRetentionEnv, "off")
	require.Zero(t, host.localRegistryTerminalRetention(), "off disables the purge")

	// A malformed override must not silently disable the purge.
	t.Setenv(localRegistryRetentionEnv, "not-a-duration")
	require.Equal(t, 72*time.Hour, host.localRegistryTerminalRetention())

	// A negative config value is the same opt-out as "off".
	host.RuntimeConfig = &runtimecfg.RuntimeConfig{
		Agents: runtimecfg.AgentsConfig{RegistryTerminalRetention: -time.Hour},
	}
	t.Setenv(localRegistryRetentionEnv, "")
	require.Zero(t, host.localRegistryTerminalRetention())
}
