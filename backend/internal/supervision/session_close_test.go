package supervision

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolvePendingWakesForSessionClearsOnlyThatSession(t *testing.T) {
	store := newTestStore(t, "session-close")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	for _, req := range []WakeRequest{
		{RootScopeID: "root-1", TargetParentSessionID: "sess-close", WakeReason: "child_failed"},
		{RootScopeID: "root-1", TargetParentSessionID: "sess-close", WakeReason: "background_job_failed"},
		{RootScopeID: "root-1", TargetParentSessionID: "sess-other", WakeReason: "child_failed"},
		{RootScopeID: "root-2", TargetParentSessionID: "sess-close", WakeReason: "child_failed"},
	} {
		_, err := scheduler.ScheduleWake(ctx, req)
		require.NoError(t, err)
	}

	resolved, err := ResolvePendingWakesForSession(ctx, store, "root-1", "sess-close")
	require.NoError(t, err)
	require.Equal(t, 2, resolved)

	// 关闭会话在本作用域下的 wake 全清（含已认领行：这里用不筛 claimed 的查询
	// 证明没有任何残留）。
	remaining, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-1",
		TargetParentSessionID: "sess-close",
	})
	require.NoError(t, err)
	require.Empty(t, remaining)

	// 其他会话 / 其他作用域的 wake 不受影响：清理必须严格按 (root, session) 定位。
	other, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-1", TargetParentSessionID: "sess-other"})
	require.NoError(t, err)
	require.Len(t, other, 1)

	otherScope, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-2", TargetParentSessionID: "sess-close"})
	require.NoError(t, err)
	require.Len(t, otherScope, 1)
}

func TestResolvePendingWakesForSessionIsSafeWithoutTarget(t *testing.T) {
	store := newTestStore(t, "session-close-noop")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	_, err := scheduler.ScheduleWake(ctx, WakeRequest{RootScopeID: "root-1", TargetParentSessionID: "sess-keep", WakeReason: "child_failed"})
	require.NoError(t, err)

	resolved, err := ResolvePendingWakesForSession(ctx, store, "root-1", "   ")
	require.NoError(t, err)
	require.Zero(t, resolved)

	resolved, err = ResolvePendingWakesForSession(ctx, nil, "root-1", "sess-keep")
	require.NoError(t, err)
	require.Zero(t, resolved)

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-1"})
	require.NoError(t, err)
	require.Len(t, pending, 1, "a no-op cleanup must leave the ledger untouched")
}
