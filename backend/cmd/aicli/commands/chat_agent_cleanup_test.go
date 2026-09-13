package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// markLocalQuotaHarnessChildTerminal turns the harness child container into a
// terminal session, which is the "session_terminal" drift P2-9 must reclaim.
func markLocalQuotaHarnessChildTerminal(t *testing.T, host *localChatRuntimeHost) {
	t.Helper()
	require.NotNil(t, host)
	require.NotNil(t, host.SessionStore)
	stored, err := host.SessionStore.Load(context.Background(), "held-child-session")
	require.NoError(t, err)
	require.NotNil(t, stored)
	stored.UpdateState(runtimechat.StateClosed)
	require.NoError(t, host.SessionStore.Save(context.Background(), stored))
}

func chatAgentCleanupHarnessSession(host *localChatRuntimeHost, rootSession *runtimechat.Session) *ChatSession {
	return &ChatSession{LocalRuntimeHost: host, RuntimeSession: rootSession}
}

// TestStructuredAgentsCleanup_ReclaimsTerminalChildAndReleasesQuota is the P2-9
// 方案 3 acceptance shape: the manual one-shot cleanup goes through the /agents
// verb, closes the terminal row through the same reclaim path as the spawn gate
// (so the wake stream reports reclaimed:session_terminal, not "closed"), and the
// freed slot lets the next spawn through.
func TestStructuredAgentsCleanup_ReclaimsTerminalChildAndReleasesQuota(t *testing.T) {
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 0)
	markLocalQuotaHarnessChildTerminal(t, host)
	session := chatAgentCleanupHarnessSession(host, rootSession)

	result := executeStructuredAgentsCommand(session, "/agents cleanup")
	require.Equal(t, CommandContinue, result.Action)

	records, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		RootSessionID: rootSession.ID,
		IncludeClosed: true,
	})
	require.NoError(t, err)
	reclaimed := false
	for _, record := range records {
		if record.AgentPath != "/root/held-child" {
			continue
		}
		reclaimed = true
		require.True(t, record.Closed(), "cleaned child must end in the terminal closed state")
		require.Equal(t, agentcontrol.AgentStatusClosed, record.Status)
	}
	require.True(t, reclaimed, "cleaned child row must remain visible for diagnostics")

	_, err = host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "after-cleanup-child"})
	require.NoError(t, err, "cleanup must release the thread slot")
}

// TestChatAgentCleanup_DefaultPolicyKeepsLiveChild pins the conservative
// default: without an opt-in idle policy only provably gone/terminal containers
// are reclaimable, so a live child keeps its slot and the spawn gate still
// refuses the next spawn.
func TestChatAgentCleanup_DefaultPolicyKeepsLiveChild(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)

	text, err := runChatAgentCleanupCommand(session, "cleanup")
	require.NoError(t, err)
	require.Contains(t, text, "quota_children=1")
	require.Contains(t, text, "reclaimable=0")
	require.Contains(t, text, "reclaimed=0")
	require.Contains(t, text, "remaining_quota_children=1")
	require.Contains(t, text, "idle_policy=disabled")
	require.Contains(t, text, "没有可安全回收的子 agent")

	_, err = host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "still-blocked-child"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "agent spawn thread limit reached")
}

// TestChatAgentCleanup_DryRunPreviewsIdleWithoutClosing covers the preview and
// the opt-in idle policy: --idle makes the live-but-idle child reclaimable on
// paper, --dry-run keeps the durable row untouched.
func TestChatAgentCleanup_DryRunPreviewsIdleWithoutClosing(t *testing.T) {
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)

	text, err := runChatAgentCleanupCommand(session, "cleanup --dry-run --idle 1ns")
	require.NoError(t, err)
	require.Contains(t, text, "reclaimable=1")
	require.Contains(t, text, "idle_policy=1ns")
	require.Contains(t, text, "would_reclaim path=/root/held-child session=held-child-session reason="+agentcontrol.ReclaimReasonIdleTimeout)
	require.Contains(t, text, "dry_run=true")

	records, err := agentStore.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		RootSessionID: rootSession.ID,
		IncludeClosed: true,
	})
	require.NoError(t, err)
	for _, record := range records {
		if record.AgentPath == "/root/held-child" {
			require.False(t, record.Closed(), "dry-run must not close anything")
		}
	}
}

// TestChatAgentCleanup_IdlePolicyReclaimsLiveChildThenSpawns proves the opt-in
// path also executes (not just previews) and still reports the eviction reason.
func TestChatAgentCleanup_IdlePolicyReclaimsLiveChildThenSpawns(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)

	text, err := runChatAgentCleanupCommand(session, "cleanup --idle 1ns")
	require.NoError(t, err)
	require.Contains(t, text, "reclaimed=1")
	require.Contains(t, text, "reclaim_reasons="+agentcontrol.ReclaimReasonIdleTimeout)
	require.Contains(t, text, "remaining_quota_children=0")

	_, err = host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "after-idle-cleanup-child"})
	require.NoError(t, err)
}

// TestChatAgentCleanup_UnknownFlagReturnsUsage keeps typos from turning into a
// silent no-op pass.
func TestChatAgentCleanup_UnknownFlagReturnsUsage(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)

	text, err := runChatAgentCleanupCommand(session, "cleanup --nope")
	require.NoError(t, err)
	require.Contains(t, text, "用法: /agents cleanup")
	require.Contains(t, text, "错误: 未知参数: --nope")

	text, err = runChatAgentCleanupCommand(session, "cleanup --idle")
	require.NoError(t, err)
	require.Contains(t, text, "--idle 需要时长参数")

	text, err = runChatAgentCleanupCommand(session, "cleanup --idle soon")
	require.NoError(t, err)
	require.Contains(t, text, "无效的 --idle 时长: soon")
}

// TestChatAgentCleanup_UnavailableWithoutDurableRegistry documents the
// graceful degradation: hosts without a durable registry keep working and the
// command explains why there is nothing to clean.
func TestChatAgentCleanup_UnavailableWithoutDurableRegistry(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)
	host.AgentRegistryStore = nil
	session := chatAgentCleanupHarnessSession(host, rootSession)

	text, err := runChatAgentCleanupCommand(session, "cleanup")
	require.NoError(t, err)
	require.Contains(t, text, "reclaim=unavailable")
}
