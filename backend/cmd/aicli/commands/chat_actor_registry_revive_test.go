package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// newLocalAgentReviveFixture 组装 revival 收敛所需的最小宿主：durable agent
// registry（行状态）+ runtime store（运行状态 + 执行 lease）。
func newLocalAgentReviveFixture(t *testing.T) (*localActorRegistry, *agentcontrol.SQLiteGlobalAgentRegistryStore, *runtimechat.InMemoryRuntimeStore) {
	t.Helper()
	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agents.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = agentStore.Close() })
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(8)
	registry := &localActorRegistry{Host: &localChatRuntimeHost{
		RuntimeStore:       runtimeStore,
		AgentRegistryStore: agentStore,
	}}
	return registry, agentStore, runtimeStore
}

func seedStaleAgentRow(t *testing.T, ctx context.Context, store *agentcontrol.SQLiteGlobalAgentRegistryStore, sessionID string) agentcontrol.AgentRecord {
	t.Helper()
	record, err := store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         sessionID,
		RootSessionID:   "root-revive",
		ParentAgentID:   "root:root-revive",
		ParentSessionID: "root-revive",
		SessionID:       sessionID,
		AgentPath:       "/root/" + sessionID,
		Depth:           1,
		Status:          agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)
	rows, err := store.MarkAgentControlAgentSubtreeStale(ctx, record.RootSessionID, record.AgentPath, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	return record
}

// TestReviveLiveLocalAgentRegistryRowsRevivesRunningSession 钉住重启竞态的逆
// 漂移收敛：sweep 在旧 lease 过期时把子代理行标 stale，随后恢复运行的会话重新
// 持有 lease —— revival 必须把该行复位为 active，否则 /agents、web 面板与父会话
// 把运行中的子代理报成已结束（真机 2026-09-30 现场）。
func TestReviveLiveLocalAgentRegistryRowsRevivesRunningSession(t *testing.T) {
	ctx := context.Background()
	registry, agentStore, runtimeStore := newLocalAgentReviveFixture(t)

	const sessionID = "session-live-again"
	record := seedStaleAgentRow(t, ctx, agentStore, sessionID)

	// 会话恢复运行并重新持有 lease（恢复/续跑路径的实际状态）。
	require.NoError(t, runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:     sessionID,
		Status:        runtimechat.SessionRunning,
		CurrentTurnID: "turn-live-again",
	}))
	_, err := runtimeStore.AcquireLease(ctx, runtimechat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   "owner-live",
		OwnerKind: "aicli-actor",
		TTL:       time.Minute,
	})
	require.NoError(t, err)

	existing, err := agentStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	revived, err := registry.reviveLiveLocalAgentRegistryRows(ctx, agentStore, existing)
	require.NoError(t, err)
	require.EqualValues(t, 1, revived)

	after, err := agentStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, record.AgentID, after[0].AgentID)
	require.Equal(t, agentcontrol.AgentStatusActive, after[0].Status)
	require.Nil(t, after[0].ClosedAt)
}

// TestReviveLiveLocalAgentRegistryRowsKeepsDeadSessionTerminal 是反向守卫：
// 没有未过期 lease（进程真的死了）时，stale 行必须保持终态，revival 不能复活
// 没有 owner 的历史行。
func TestReviveLiveLocalAgentRegistryRowsKeepsDeadSessionTerminal(t *testing.T) {
	ctx := context.Background()
	registry, agentStore, runtimeStore := newLocalAgentReviveFixture(t)

	const sessionID = "session-still-dead"
	seedStaleAgentRow(t, ctx, agentStore, sessionID)

	// 状态还在 running（崩溃遗留），但 lease 已过期。
	require.NoError(t, runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: sessionID,
		Status:    runtimechat.SessionRunning,
	}))
	_, err := runtimeStore.AcquireLease(ctx, runtimechat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   "owner-dead",
		OwnerKind: "aicli-actor",
		TTL:       time.Millisecond,
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	existing, err := agentStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	revived, err := registry.reviveLiveLocalAgentRegistryRows(ctx, agentStore, existing)
	require.NoError(t, err)
	require.Zero(t, revived)

	after, err := agentStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, agentcontrol.AgentStatusStale, after[0].Status)
}

// TestResumeReactivatesTerminalAgentRow 钉住"恢复即复位"的即时路径：resume 让
// 会话重新被持有后，账本行必须当场回到 active，而不是等下一次 materialize。
func TestResumeReactivatesTerminalAgentRow(t *testing.T) {
	ctx := context.Background()
	registry, agentStore, runtimeStore := newLocalAgentReviveFixture(t)
	host := registry.Host
	sessionStore := runtimechat.NewInMemoryStorage()
	host.SessionStore = sessionStore
	host.SessionHub = buildCleanupTestSessionHub(t, host, sessionStore)

	const sessionID = "session-resumed"
	seedStaleAgentRow(t, ctx, agentStore, sessionID)
	require.NoError(t, runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: sessionID,
		Status:    runtimechat.SessionStopped,
	}))

	if _, err := registry.Resume(ctx, sessionID); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	after, err := agentStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, agentcontrol.AgentStatusActive, after[0].Status)
	require.Nil(t, after[0].ClosedAt)
}
