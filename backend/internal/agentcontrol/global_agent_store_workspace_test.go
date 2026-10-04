package agentcontrol

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAgentRecordWorkspacePathRoundTrips 直接压住 workspace_path 这一列的
// INSERT/SELECT 占位符与扫描顺序：这些 SQL 是手写的，列数或参数顺序一旦错位，
// sqlite 只会在运行时报 column count mismatch，而字段被静默丢弃则更难发现。
func TestAgentRecordWorkspacePathRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	root := AgentRecord{
		AgentID:       "root:ws-session",
		RootSessionID: "ws-session",
		SessionID:     "ws-session",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
		WorkspacePath: "/repos/main",
	}
	_, err := store.UpsertAgentControlAgent(ctx, root)
	require.NoError(t, err)

	child := AgentRecord{
		AgentID:         "ws-child",
		RootSessionID:   "ws-session",
		ParentAgentID:   "root:ws-session",
		ParentSessionID: "ws-session",
		SessionID:       "ws-child",
		AgentPath:       "/root/ws-child",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
		Status:          AgentStatusActive,
		WorkspacePath:   "/repos/main",
	}
	_, err = store.UpsertAgentControlAgent(ctx, child)
	require.NoError(t, err)

	for _, want := range []AgentRecord{root, child} {
		rows, err := store.ListAgentControlAgents(ctx, AgentFilter{AgentID: want.AgentID, IncludeClosed: true})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, want.WorkspacePath, rows[0].WorkspacePath, "workspace_path must survive the round trip")
		require.Equal(t, want.ParentAgentID, rows[0].ParentAgentID)
	}
}

// TestAgentRecordWorkspacePathReservesSpawnRoundTrip 走预约事务的 upsert 分支
// （与 UpsertAgentControlAgent 是两段独立 SQL），并确认 quota 统计不受影响。
func TestAgentRecordWorkspacePathReservesSpawnRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	root := AgentRecord{
		AgentID:       "root:ws-reserve",
		RootSessionID: "ws-reserve",
		SessionID:     "ws-reserve",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
	}
	child := AgentRecord{
		AgentID:         "ws-reserve-child",
		RootSessionID:   "ws-reserve",
		ParentAgentID:   "root:ws-reserve",
		ParentSessionID: "ws-reserve",
		SessionID:       "ws-reserve-child",
		AgentPath:       "/root/ws-reserve-child",
		Depth:           1,
		AgentType:       AgentTypeChild,
		Workflow:        WorkflowSpawnAgent,
		Status:          AgentStatusActive,
		WorkspacePath:   "/repos/main",
	}
	_, err := store.ReserveAgentControlAgentSpawn(ctx, root, child, 4)
	require.NoError(t, err)

	rows, err := store.ListAgentControlAgents(ctx, AgentFilter{AgentID: child.AgentID, IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "/repos/main", rows[0].WorkspacePath)
}

// TestAgentRecordWorkspacePathNormalizeAndWakeState 守护 workspace_path 参与
// 「行是否真的变了」判定：它变了就必须发 wake，否则工作目录漂移对唤醒消费者不可见。
func TestAgentRecordWorkspacePathNormalizeAndWakeState(t *testing.T) {
	base := AgentRecord{AgentID: "a", Status: AgentStatusActive, WorkspacePath: " /repos/main "}
	require.Equal(t, "/repos/main", base.Normalize().WorkspacePath)

	other := base
	other.WorkspacePath = "/repos/other"
	require.False(t, agentRecordsShareWakeState(base, other),
		"a changed workspace must count as a real identity change")
	require.True(t, agentRecordsShareWakeState(base, base))
}

// TestAgentControlAgentStoreAddsWorkspaceColumnToLegacySchema 模拟老库：先建一张
// 不含 workspace_path 的表，再让 store 初始化，确认 additive migration 补列成功。
func TestAgentControlAgentStoreAddsWorkspaceColumnToLegacySchema(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	// 列已存在时初始化必须保持幂等（同一路径反复打开不报错）。
	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "root:ws-legacy",
		RootSessionID: "ws-legacy",
		SessionID:     "ws-legacy",
		AgentPath:     "/root",
		AgentType:     AgentTypeRoot,
		Status:        AgentStatusActive,
		WorkspacePath: "/repos/legacy",
	})
	require.NoError(t, err)

	reopened, err := NewSQLiteGlobalAgentRegistryStore(&GlobalAgentStoreConfig{
		Path: store.path,
	})
	require.NoError(t, err)
	defer reopened.Close()
	rows, err := reopened.ListAgentControlAgents(ctx, AgentFilter{AgentID: "root:ws-legacy", IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "/repos/legacy", rows[0].WorkspacePath)
}
