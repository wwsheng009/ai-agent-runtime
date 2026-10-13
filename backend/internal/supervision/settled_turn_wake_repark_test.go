package supervision

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestScheduleSettledTurnWakePerParkEpisode pins the 2026-10-13 field
// regression: a managed turn that parks twice (its resumed episode spawned new
// obligations and parked again under the same turn id) must be able to wake the
// parent after the second settlement even though the first settlement wake was
// already delivered.
//
// Before the fix both settlements derived the same notify key (turn_id +
// lifecycle + seq 0). The second ScheduleWake hit the delivered ledger and was
// suppressed as an event replay; no wake row was persisted for the fallback
// sweep to drain either, so the session stayed parked on "Waiting for
// subagents" until a manual prompt.
func TestScheduleSettledTurnWakePerParkEpisode(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "settled-turn-wake-repark")
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	batches, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batches.Close() })

	teams := stubTeamObligationResolver{"team-1": true}

	pendingWakes := func() []WakePending {
		wakes, err := store.ListWakePending(ctx, WakeFilter{
			RootScopeID:           "parent-1",
			TargetParentSessionID: "parent-1",
			UnclaimedOnly:         true,
			Limit:                 16,
		})
		require.NoError(t, err)
		return wakes
	}

	// Episode A parks the turn; its settlement wake is scheduled and then
	// delivered (the parent resumes and starts a new episode).
	episodeA := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-repark",
		SessionID:     "parent-1",
		RootScopeID:   "parent-1",
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-1")},
		ParkedAt:      episodeA,
	}))
	scheduled, err := ScheduleSettledTurnWakeWithTeams(ctx, batches, nil, teams, scheduler, "parent-1", "parent-1")
	require.NoError(t, err)
	require.True(t, scheduled)
	wakes := pendingWakes()
	require.Len(t, wakes, 1, "the first settlement must schedule a wake")
	firstKey := wakes[0].NotifyKey
	require.NotEmpty(t, firstKey)
	require.NoError(t, store.MarkWakeDelivered(ctx, WakeDelivered{
		NotifyKey:             firstKey,
		RootScopeID:           "parent-1",
		TargetParentSessionID: "parent-1",
		WakeID:                wakes[0].WakeID,
		TurnID:                "turn-repark",
		EventKind:             WakeEventLifecycle,
		DeliveredBy:           "wake_consumer",
	}))
	require.NoError(t, store.ResolveWakePending(ctx, wakes[0].WakeID))
	require.Empty(t, pendingWakes())

	// Episode B: the resumed episode spawned new obligations and parked the
	// same turn again. The second settlement must produce a fresh, deliverable
	// wake identity instead of being suppressed by episode A's delivery.
	episodeB := episodeA.Add(10 * time.Minute)
	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-repark",
		SessionID:     "parent-1",
		RootScopeID:   "parent-1",
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-1")},
		ParkedAt:      episodeB,
	}))
	scheduled, err = ScheduleSettledTurnWakeWithTeams(ctx, batches, nil, teams, scheduler, "parent-1", "parent-1")
	require.NoError(t, err)
	require.True(t, scheduled)
	wakes = pendingWakes()
	require.Len(t, wakes, 1, "the second park episode must schedule its own settlement wake")
	require.NotEqual(t, firstKey, wakes[0].NotifyKey,
		"a later park episode must not reuse the delivered identity of an earlier one")

	// Replays of the same episode keep collapsing onto one pending wake.
	scheduled, err = ScheduleSettledTurnWakeWithTeams(ctx, batches, nil, teams, scheduler, "parent-1", "parent-1")
	require.NoError(t, err)
	require.True(t, scheduled)
	require.Len(t, pendingWakes(), 1, "same-episode replays stay idempotent")
}
