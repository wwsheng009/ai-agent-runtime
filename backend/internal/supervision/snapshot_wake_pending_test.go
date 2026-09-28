package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBuildSnapshotSurfacesOverdueWakePending 钉住 §2.5 滞留告警：父会话存在
// 长期未 claim 的 wake_pending 时，快照必须呈现积压数、最老年龄与超阈计数
// （计划 §12.1"父会话存在 wake_pending 但长时间没有可运行 turn"）。
func TestBuildSnapshotSurfacesOverdueWakePending(t *testing.T) {
	store := newTestStore(t, "supervision-snapshot-wake-pending")
	ctx := context.Background()
	now := time.Now().UTC()

	// dedup_key 不同才会落成两行；InsertWakePending 会用 now 覆盖 CreatedAt，
	// 因此滞留年龄在插入后直接回填（同包测试可访问 store 句柄）。
	require.NoError(t, store.InsertWakePending(ctx, WakePending{
		WakeID:      "wake-old",
		RootScopeID: "root-session-1",
		WakeReason:  "terminal",
	}))
	require.NoError(t, store.InsertWakePending(ctx, WakePending{
		WakeID:      "wake-fresh",
		RootScopeID: "root-session-1",
		WakeReason:  "progress",
	}))
	db, err := store.dbOrErr()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`UPDATE supervision_wake_pending SET created_at = ? WHERE wake_id = ?`,
		formatSupervisionTime(now.Add(-2*WakePendingOverdueThreshold)), "wake-old")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`UPDATE supervision_wake_pending SET created_at = ? WHERE wake_id = ?`,
		formatSupervisionTime(now.Add(-time.Minute)), "wake-fresh")
	require.NoError(t, err)

	snapshot, err := BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:       Scope{RootSessionID: "root-session-1"},
		RootScopeID: "root-session-1",
	})
	require.NoError(t, err)
	require.Equal(t, 2, snapshot.Summary.PendingWakes)
	require.Equal(t, 1, snapshot.Summary.WakePendingOverdue)
	require.GreaterOrEqual(t, snapshot.OldestWakePendingAgeMs, WakePendingOverdueThreshold.Milliseconds())

	// 被 claim 的 wake 不再计入滞留：turn 侧已接手，剩余一条新鲜行不构成告警。
	claimed, err := store.ClaimWakePending(ctx, "wake-old", "test", now)
	require.NoError(t, err)
	require.True(t, claimed)
	snapshot, err = BuildSnapshot(ctx, store, SnapshotRequest{
		Scope:       Scope{RootSessionID: "root-session-1"},
		RootScopeID: "root-session-1",
	})
	require.NoError(t, err)
	require.Equal(t, 1, snapshot.Summary.PendingWakes)
	require.Zero(t, snapshot.Summary.WakePendingOverdue)
	require.Less(t, snapshot.OldestWakePendingAgeMs, WakePendingOverdueThreshold.Milliseconds())
}
