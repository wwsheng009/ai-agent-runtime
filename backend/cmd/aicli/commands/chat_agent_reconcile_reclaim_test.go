package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// P2-9 方案 3「自动 close」：周期对账调用宿主钩子
// （localActorRegistry.reclaimLocalAgentRegistryQuota）时必须与 spawn 闸门、
// /agents cleanup 走同一套判定与事件口径，并且 observe 模式只报候选、不关闭。

func localAgentReclaimRoster(t *testing.T, store *agentcontrol.SQLiteGlobalAgentRegistryStore, rootSessionID string) []agentcontrol.AgentRecord {
	t.Helper()
	records, err := store.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		RootSessionID: rootSessionID,
		IncludeClosed: true,
	})
	require.NoError(t, err)
	return records
}

func localAgentReclaimChildRow(t *testing.T, store *agentcontrol.SQLiteGlobalAgentRegistryStore, rootSessionID, agentPath string) agentcontrol.AgentRecord {
	t.Helper()
	for _, record := range localAgentReclaimRoster(t, store, rootSessionID) {
		if strings.EqualFold(strings.TrimSpace(record.AgentPath), agentPath) {
			return record.Normalize()
		}
	}
	t.Fatalf("agent row %s not found under root %s", agentPath, rootSessionID)
	return agentcontrol.AgentRecord{}
}

// TestLocalReconcileReclaim_ObserveReportsCandidatesWithoutClosing pins the
// safe default: the periodic sweep in observe mode tells the operator what an
// enforce pass would release, but leaves the row (and the quota it holds) alone.
func TestLocalReconcileReclaim_ObserveReportsCandidatesWithoutClosing(t *testing.T) {
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 0)
	markLocalQuotaHarnessChildTerminal(t, host)

	outcome, err := host.ActorRegistry.reclaimLocalAgentRegistryQuota(
		context.Background(),
		localAgentReclaimRoster(t, agentStore, rootSession.ID),
		false,
		time.Now().UTC(),
	)
	require.NoError(t, err)
	require.Equal(t, 1, outcome.Candidates)
	require.Zero(t, outcome.Reclaimed())
	require.Equal(t, []string{agentcontrol.ReclaimReasonSessionTerminal}, outcome.Reasons)
	require.Contains(t, outcome.Summary(), "reclaim_candidates=1")
	require.Contains(t, outcome.Summary(), "reclaim_reasons=session_terminal")

	row := localAgentReclaimChildRow(t, agentStore, rootSession.ID, "/root/held-child")
	require.False(t, row.Closed(), "observe mode must not close anything")
	require.Contains(t, host.localAgentReclaimSummary(), "reclaim events=0",
		"an evaluate-only pass publishes no product event")

	// The row still holds the slot: the registry listing still counts it as a
	// quota child, so the observe-only sweep really left the quota untouched.
	occupants := agentcontrol.QuotaChildren(localAgentReclaimRoster(t, agentStore, rootSession.ID))
	require.Len(t, occupants, 1)
	require.Equal(t, "/root/held-child", occupants[0].AgentPath)

	// Only the next admission attempt frees it, and that path re-projects the
	// registry before it evaluates the quota: the terminal row is closed there
	// (source=reconcile) exactly once, while the observe sweep above contributed
	// no event of its own.
	_, err = host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "after-observe-child"})
	require.NoError(t, err)
	summary := host.localAgentReclaimSummary()
	require.Contains(t, summary, "reclaim events=1")
	require.Contains(t, summary, "reasons="+agentcontrol.ReclaimReasonSessionTerminal)
}

// TestLocalReconcileReclaim_EnforceClosesTerminalChildAndReleasesQuota is the
// automatic-close acceptance shape: in enforce mode the sweep closes the
// terminal subtree through the shared reclaim store (reclaimed:session_terminal,
// not a bare "closed"), publishes the P2-8 product event with source=reconcile
// and frees the slot for the next spawn.
func TestLocalReconcileReclaim_EnforceClosesTerminalChildAndReleasesQuota(t *testing.T) {
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 0)
	markLocalQuotaHarnessChildTerminal(t, host)

	outcome, err := host.ActorRegistry.reclaimLocalAgentRegistryQuota(
		context.Background(),
		localAgentReclaimRoster(t, agentStore, rootSession.ID),
		true,
		time.Now().UTC(),
	)
	require.NoError(t, err)
	require.Equal(t, 1, outcome.Reclaimed())
	require.Equal(t, int64(1), outcome.Rows)
	require.Len(t, outcome.Decisions, 1)
	require.Equal(t, agentcontrol.ReclaimReasonSessionTerminal, outcome.Decisions[0].Reason)

	row := localAgentReclaimChildRow(t, agentStore, rootSession.ID, "/root/held-child")
	require.True(t, row.Closed(), "enforce mode closes the terminal row")
	require.Equal(t, agentcontrol.AgentStatusClosed, row.Status)

	summary := host.localAgentReclaimSummary()
	require.Contains(t, summary, "source="+agentcontrol.ReclaimSourceReconcile)
	require.Contains(t, summary, "reasons="+agentcontrol.ReclaimReasonSessionTerminal)

	_, err = host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "after-sweep-child"})
	require.NoError(t, err, "the automatic sweep must release the thread slot")
}

// TestLocalReconcileReclaim_IdlePolicyComesFromAgentsConfig pins the "与 P2-8
// 共用开关" requirement: the periodic sweep evicts idle children only when
// agents.reclaimIdleMs is configured, and then reports reason=idle_timeout.
func TestLocalReconcileReclaim_IdlePolicyComesFromAgentsConfig(t *testing.T) {
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 1)
	// The child stays live (never marked terminal): only the opt-in idle policy
	// can select it, and the evaluation instant is pushed past the 1ms timeout.
	now := time.Now().UTC().Add(2 * time.Second)

	outcome, err := host.ActorRegistry.reclaimLocalAgentRegistryQuota(
		context.Background(),
		localAgentReclaimRoster(t, agentStore, rootSession.ID),
		true,
		now,
	)
	require.NoError(t, err)
	require.Equal(t, 1, outcome.Reclaimed())
	require.Len(t, outcome.Decisions, 1)
	require.Equal(t, agentcontrol.ReclaimReasonIdleTimeout, outcome.Decisions[0].Reason)

	row := localAgentReclaimChildRow(t, agentStore, rootSession.ID, "/root/held-child")
	require.True(t, row.Closed())
}

// TestLocalRegistrySweep_PublishesReclaimEventForTerminalChild covers the other
// automatic entry point: the projection sweep (materializeLocalAgentRegistry)
// used to close terminal rows silently, which made an automatic eviction
// indistinguishable from an orderly close_agent in the parent stream.
func TestLocalRegistrySweep_PublishesReclaimEventForTerminalChild(t *testing.T) {
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 0)
	markLocalQuotaHarnessChildTerminal(t, host)

	require.NoError(t, host.ActorRegistry.materializeLocalAgentRegistry(context.Background()))

	row := localAgentReclaimChildRow(t, agentStore, rootSession.ID, "/root/held-child")
	require.True(t, row.Closed())

	summary := host.localAgentReclaimSummary()
	require.Contains(t, summary, "source="+agentcontrol.ReclaimSourceReconcile)
	require.Contains(t, summary, "reasons="+agentcontrol.ReclaimReasonSessionTerminal)
}

// TestLocalRegistryReconciler_EnforcePassReclaimsIdleChild closes the loop on
// the host wiring: a pass built by the CLI host runs the audit and then the
// reclaimed-hook, so the cached report an operator reads from /debug explains
// the eviction, and the durable row + parent event stream agree with it.
func TestLocalRegistryReconciler_EnforcePassReclaimsIdleChild(t *testing.T) {
	// agents.reclaimIdleMs=1 makes the harness child idle-evictable; the sleep
	// guarantees the age check is past the timeout instead of racing it.
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 1)
	time.Sleep(10 * time.Millisecond)

	reconciler := host.buildLocalRegistryReconciler()
	require.NotNil(t, reconciler)
	reconciler.Mode = agentcontrol.ReconcileModeEnforce

	report, err := reconciler.RunOnce(context.Background())
	require.NoError(t, err)
	require.Empty(t, report.ReclaimError)
	require.Equal(t, 1, report.Reclaimed)
	require.Equal(t, int64(1), report.ReclaimedRows)
	require.Equal(t, []string{agentcontrol.ReclaimReasonIdleTimeout}, report.ReclaimReasons)

	row := localAgentReclaimChildRow(t, agentStore, rootSession.ID, "/root/held-child")
	require.True(t, row.Closed())

	summary := host.localAgentReclaimSummary()
	require.Contains(t, summary, "source="+agentcontrol.ReclaimSourceReconcile)
	require.Contains(t, summary, "reasons="+agentcontrol.ReclaimReasonIdleTimeout)
	// The host's /debug line renders this cached report; the loop itself is only
	// bound by startLocalRegistryReconcile, which an on-demand pass skips.
	require.Contains(t, reconciler.ReconcileSummary(), "reclaimed=1")

	_, err = host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "after-reconcile-child"})
	require.NoError(t, err, "the periodic pass must have released the thread slot")
}
