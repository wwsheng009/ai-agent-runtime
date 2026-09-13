package supervision

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWakeIDsStayUniqueWithinOneClockTick reproduces the coarse-clock
// regression: when the host clock does not advance between two calls, wake ids
// built from time.Now().UnixNano() collide, and the conflicting INSERT drops
// the second wake. Dropping a wake silently starves the parent session of the
// notification it must answer (including blocking approvals), which is exactly
// what the tiered wake budget is meant to prevent.
func TestWakeIDsStayUniqueWithinOneClockTick(t *testing.T) {
	store := newTestStore(t, "wake-id-uniqueness")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{
		RateWindow:           time.Hour,
		MaxAutoWakePerWindow: 1,
	})

	const events = 32
	seen := make(map[string]struct{}, events)
	for i := 0; i < events; i++ {
		result, err := scheduler.ScheduleWake(ctx, WakeRequest{
			RootScopeID:           "root-scope",
			TargetParentSessionID: "root-scope",
			WakeReason:            fmt.Sprintf("child_failed_%d", i),
			NotificationSeq:       int64(i + 1),
		})
		require.NoError(t, err)
		_, duplicate := seen[result.WakeID]
		require.Falsef(t, duplicate, "wake id %q was generated twice", result.WakeID)
		seen[result.WakeID] = struct{}{}
	}

	pending, err := store.ListWakePending(ctx, WakeFilter{RootScopeID: "root-scope", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, pending, events, "every scheduled wake must stay durable")
}

// TestMemoryStoreDSNsAreUnique guards the same clock assumption for the
// default in-memory store. Two stores created inside one clock tick used to
// resolve to the same cache=shared database and could observe each other's
// sessions.
func TestMemoryStoreDSNsAreUnique(t *testing.T) {
	first, _, err := resolveSupervisionDSN(&StoreConfig{})
	require.NoError(t, err)
	second, _, err := resolveSupervisionDSN(&StoreConfig{})
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}
