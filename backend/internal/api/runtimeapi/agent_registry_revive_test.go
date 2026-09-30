package runtimeapi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// TestAPISweepRevivesLiveAgentRegistryRow 钉住 API 宿主侧的逆漂移收敛（与 CLI
// 同构）：sweep 在旧 lease 过期时把子代理行标 stale，恢复运行的会话重新持有
// lease 后必须复位为 active，否则 runtime-server 的 /agents 与父流把运行中的
// 子代理报成已结束（真机 2026-09-30 现场，CLI 侧同款修复）。
func TestAPISweepRevivesLiveAgentRegistryRow(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(16)
	handler.sessionRuntimeStore = runtimeStore

	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agents.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	const sessionID = "api-child-live-again"
	record, err := store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         sessionID,
		RootSessionID:   "api-root-revive",
		ParentAgentID:   "root:api-root-revive",
		ParentSessionID: "api-root-revive",
		SessionID:       sessionID,
		AgentPath:       "/root/" + sessionID,
		Depth:           1,
		Status:          agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)
	rows, err := store.MarkAgentControlAgentSubtreeStale(ctx, record.RootSessionID, record.AgentPath, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// 会话恢复运行并重新持有 lease。
	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID:     sessionID,
		Status:        chat.SessionRunning,
		CurrentTurnID: "turn-api-live-again",
	}))
	_, err = runtimeStore.AcquireLease(ctx, chat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   "api-owner",
		OwnerKind: "aicli-actor",
		TTL:       time.Minute,
	})
	require.NoError(t, err)

	existing, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.NoError(t, handler.reviveLiveAgentControlAgentRegistry(ctx, store, existing))

	after, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, agentcontrol.AgentStatusActive, after[0].Status)
	require.Nil(t, after[0].ClosedAt)
}

// TestAPISweepKeepsDeadSessionTerminal 是反向守卫：没有未过期 lease 时，stale
// 行保持终态（崩溃遗留不能因为状态还写着 running 就被复活）。
func TestAPISweepKeepsDeadSessionTerminal(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(16)
	handler.sessionRuntimeStore = runtimeStore

	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agents.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	const sessionID = "api-child-still-dead"
	record, err := store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       sessionID,
		RootSessionID: "api-root-revive",
		SessionID:     sessionID,
		AgentPath:     "/root/" + sessionID,
		Status:        agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)
	_, err = store.MarkAgentControlAgentSubtreeStale(ctx, record.RootSessionID, record.AgentPath, time.Now().UTC())
	require.NoError(t, err)

	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID: sessionID,
		Status:    chat.SessionRunning,
	}))
	_, err = runtimeStore.AcquireLease(ctx, chat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   "api-owner-dead",
		OwnerKind: "aicli-actor",
		TTL:       time.Millisecond,
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	existing, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.NoError(t, handler.reviveLiveAgentControlAgentRegistry(ctx, store, existing))

	after, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, agentcontrol.AgentStatusStale, after[0].Status)
}
