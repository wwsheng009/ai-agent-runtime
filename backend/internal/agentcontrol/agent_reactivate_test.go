package agentcontrol

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReactivateAgentControlAgentRevivesOnlyTerminalRows 钉住"恢复运行的会话
// 必须复位账本"的写侧语义：active 行与未知 id 永不触碰，stale/closed 行回到
// active 并清空 closed_at，且写一条 "active" wake 事件，供 /agents、面板与父流
// 解释状态回摆（真机 2026-09-30：重启竞态下子代理被标 stale，恢复运行后行未复位）。
func TestReactivateAgentControlAgentRevivesOnlyTerminalRows(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	record, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "agent-revive",
		RootSessionID: "root-revive",
		ParentAgentID: "root:root-revive",
		SessionID:     "agent-revive",
		AgentPath:     "/root/agent-revive",
		Status:        AgentStatusActive,
	})
	require.NoError(t, err)

	wake, unwatch := store.WatchAgentControlAgentWake(ctx, AgentWakeFilter{})
	defer unwatch()

	// active 行永不触碰。
	_, changed, err := store.ReactivateAgentControlAgent(ctx, record.AgentID)
	require.NoError(t, err)
	require.False(t, changed, "active 行不得被复位写入")

	// 未知 id 是 no-op，不是错误。
	_, changed, err = store.ReactivateAgentControlAgent(ctx, "agent-missing")
	require.NoError(t, err)
	require.False(t, changed)

	// 判死（stale）→ 恢复。
	rows, err := store.MarkAgentControlAgentSubtreeStale(ctx, record.RootSessionID, record.AgentPath, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	require.Equal(t, "stale", (<-wake).EventKind)

	revived, changed, err := store.ReactivateAgentControlAgent(ctx, record.AgentID)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, AgentStatusActive, revived.Status)
	require.Nil(t, revived.ClosedAt, "复位必须清空 closed_at")

	select {
	case event := <-wake:
		require.Equal(t, "active", event.EventKind)
		require.Equal(t, AgentStatusActive, event.Status)
		require.Equal(t, record.AgentID, event.AgentID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for revival wake")
	}

	// 幂等：已经 active 的行不再写、不再发事件。
	_, changed, err = store.ReactivateAgentControlAgent(ctx, record.AgentID)
	require.NoError(t, err)
	require.False(t, changed)

	// closed 行同样可以被复位（终态行不因 stale/closed 之分而不同）。
	_, err = store.CloseAgentControlAgentSubtree(ctx, record.RootSessionID, record.AgentPath, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, "closed", (<-wake).EventKind)
	revived, changed, err = store.ReactivateAgentControlAgent(ctx, record.AgentID)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, AgentStatusActive, revived.Status)
	require.Nil(t, revived.ClosedAt)
}
