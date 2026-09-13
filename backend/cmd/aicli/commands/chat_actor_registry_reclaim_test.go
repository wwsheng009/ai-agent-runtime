package commands

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// newLocalQuotaHarness builds the smallest durable-registry host that reaches
// the P2-8 quota gate: one root session plus one live-but-idle child session
// whose registry row still holds a thread slot.
func newLocalQuotaHarness(t *testing.T, maxThreads int, reclaimIdleMs int) (*localChatRuntimeHost, *runtimechat.Session, *agentcontrol.SQLiteGlobalAgentRegistryStore) {
	t.Helper()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(manager.Stop)

	rootSession, err := manager.Create(context.Background(), userID)
	require.NoError(t, err)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agent_control_agents.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = agentStore.Close() })

	_, err = agentStore.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:       "root:" + rootSession.ID,
		RootSessionID: rootSession.ID,
		SessionID:     rootSession.ID,
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)

	childSession := runtimechat.NewSession(userID)
	childSession.ID = "held-child-session"
	// The session context and the durable row below must describe the same
	// binding: the projection derives the row identity from the session (agent id
	// = session id, path from agent_path), and a mismatched binding is closed as
	// stale by CloseStaleAgentSessionBindings before the quota gate sees it.
	const childPath = "/root/held-child"
	childSession.SetContext(toolbroker.AgentSessionContextParentSessionID, rootSession.ID)
	childSession.SetContext(toolbroker.AgentSessionContextRootSessionID, rootSession.ID)
	childSession.SetContext(toolbroker.AgentSessionContextPath, childPath)
	require.NoError(t, manager.GetStorage().Save(context.Background(), childSession))

	_, err = agentStore.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:         childSession.ID,
		RootSessionID:   rootSession.ID,
		ParentAgentID:   "root:" + rootSession.ID,
		ParentSessionID: rootSession.ID,
		SessionID:       childSession.ID,
		AgentPath:       childPath,
		Depth:           1,
		AgentType:       agentcontrol.AgentTypeChild,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		Status:          agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.AgentRegistryStore = agentStore
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.MaxThreads = maxThreads
	host.RuntimeConfig.Agents.ReclaimIdleMs = reclaimIdleMs
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	return host, rootSession, agentStore
}

func TestLocalActorRegistry_QuotaErrorCarriesNextActionAndOccupants(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)

	_, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "blocked-child"})
	require.Error(t, err)
	message := err.Error()
	require.Contains(t, message, "agent spawn thread limit reached: max_threads=1 active_children=1")
	require.Contains(t, message, "next_action="+agentcontrol.ThreadLimitNextAction)
	require.Contains(t, message, "occupants=[")
	require.Contains(t, message, "path=/root/held-child")
	require.Contains(t, message, "status=active")
}

func TestLocalActorRegistry_ReclaimsIdleChildWhenEvictionEnabled(t *testing.T) {
	// 1ms idle timeout: the harness child is already older than that, so the
	// opt-in eviction policy must free the slot and let the spawn through.
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 1)

	watchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake, unwatch := agentStore.WatchAgentControlAgentWake(watchCtx, agentcontrol.AgentWakeFilter{
		RootSessionID: rootSession.ID,
		AgentPath:     "/root/held-child",
	})
	defer unwatch()
	// Drain before Spawn: wake notifications only get one buffered slot, so a
	// frame emitted while the projection upsert still sits in the channel would
	// be dropped instead of observed.
	wakes := &localWakeLog{}
	go wakes.drain(watchCtx, wake)

	_, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "recovered-child"})
	require.NoError(t, err)

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
		require.True(t, record.Closed(), "evicted child must end in the terminal closed state")
		require.Equal(t, agentcontrol.AgentStatusClosed, record.Status)
	}
	require.True(t, reclaimed, "evicted child row must remain visible for diagnostics")

	// Spawn materializes the projection before it evicts, so drain the stream
	// instead of asserting on the first frame: the eviction must be reported by
	// its own reclaim kind and never as an orderly close_agent.
	require.Eventually(t, func() bool {
		for _, event := range wakes.snapshot() {
			require.NotEqual(t, "closed", event.EventKind, "an automatic eviction must not look like an orderly close")
			if strings.HasPrefix(event.EventKind, "reclaimed:") {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "timed out waiting for reclaim wake event")

	var reclaimWake agentcontrol.AgentWakeEvent
	for _, event := range wakes.snapshot() {
		if strings.HasPrefix(event.EventKind, "reclaimed:") {
			reclaimWake = event
		}
	}
	require.Equal(t, "reclaimed:"+agentcontrol.ReclaimReasonIdleTimeout, reclaimWake.EventKind)
	require.Equal(t, "/root/held-child", reclaimWake.AgentPath)

	// The durable wake stream carries the eviction reason, so a parent reading
	// read_agent_events can tell an automatic reclaim from close_agent.
	seq, err := agentStore.LastAgentControlAgentWakeSeq(context.Background(), agentcontrol.AgentWakeFilter{
		RootSessionID: rootSession.ID,
		AgentPath:     "/root/held-child",
	})
	require.NoError(t, err)
	require.Greater(t, seq, int64(0))
	require.True(t, strings.HasPrefix(agentcontrol.ReclaimDecision{Reason: agentcontrol.ReclaimReasonIdleTimeout}.EventKind(), "reclaimed:"))
}

// localWakeLog keeps every wake frame a test observed. The registry wake
// channel is a one-slot notification stream, so callers must drain it in the
// background while the code under test runs.
type localWakeLog struct {
	mu     sync.Mutex
	events []agentcontrol.AgentWakeEvent
}

func (l *localWakeLog) drain(ctx context.Context, wake <-chan agentcontrol.AgentWakeEvent) {
	for {
		select {
		case event, ok := <-wake:
			if !ok {
				return
			}
			l.mu.Lock()
			l.events = append(l.events, event)
			l.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

func (l *localWakeLog) snapshot() []agentcontrol.AgentWakeEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]agentcontrol.AgentWakeEvent(nil), l.events...)
}
