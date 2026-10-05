package supervision

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// stubTeamObligationResolver maps team id -> terminal; an absent id models "no
// durable team row" (strict: never evidence of completion).
type stubTeamObligationResolver map[string]bool

func (s stubTeamObligationResolver) TeamTerminal(_ context.Context, teamID string) (bool, bool, error) {
	terminal, found := s[teamID]
	return terminal, found, nil
}

// TestScheduleSettledTurnWakeWithTeams pins the team half of the settlement
// wake: a turn parked on a team: obligation may only schedule its resume once
// the team control plane reports terminal; a running or missing team keeps the
// turn parked, and an unwired team resolver never settles on a guess.
func TestScheduleSettledTurnWakeWithTeams(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, "settled-turn-wake-teams")
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	batches, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batches.Close() })

	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-team",
		SessionID:     "parent-1",
		RootScopeID:   "parent-1",
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-1")},
	}))

	teams := stubTeamObligationResolver{
		"team-1": true,
		"team-2": false,
	}

	pendingWakes := func() int {
		wakes, err := store.ListWakePending(ctx, WakeFilter{
			RootScopeID:           "parent-1",
			TargetParentSessionID: "parent-1",
			UnclaimedOnly:         true,
			Limit:                 16,
		})
		require.NoError(t, err)
		return len(wakes)
	}

	// A running team keeps the parked turn: no wake may be scheduled.
	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-team",
		SessionID:     "parent-1",
		RootScopeID:   "parent-1",
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-2")},
	}))
	scheduled, err := ScheduleSettledTurnWakeWithTeams(ctx, batches, nil, teams, scheduler, "parent-1", "parent-1")
	require.NoError(t, err)
	require.False(t, scheduled, "a running team must not settle the parked turn")
	require.Zero(t, pendingWakes())

	// A missing team row is not evidence of completion.
	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-team",
		SessionID:     "parent-1",
		RootScopeID:   "parent-1",
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-missing")},
	}))
	scheduled, err = ScheduleSettledTurnWakeWithTeams(ctx, batches, nil, teams, scheduler, "parent-1", "parent-1")
	require.NoError(t, err)
	require.False(t, scheduled, "a missing team row must not settle the parked turn")
	require.Zero(t, pendingWakes())

	// An unwired team resolver keeps the conservative behavior.
	require.NoError(t, batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-team",
		SessionID:     "parent-1",
		RootScopeID:   "parent-1",
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-1")},
	}))
	scheduled, err = ScheduleSettledTurnWakeWithTeams(ctx, batches, nil, nil, scheduler, "parent-1", "parent-1")
	require.NoError(t, err)
	require.False(t, scheduled, "nil team resolver must never clear a team obligation")
	require.Zero(t, pendingWakes())

	// Terminal team ⇒ exactly one durable settlement wake for the parked turn.
	scheduled, err = ScheduleSettledTurnWakeWithTeams(ctx, batches, nil, teams, scheduler, "parent-1", "parent-1")
	require.NoError(t, err)
	require.True(t, scheduled, "a terminal team must schedule the resume")
	require.Equal(t, 1, pendingWakes())
	wakes, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "parent-1",
		TargetParentSessionID: "parent-1",
		UnclaimedOnly:         true,
		Limit:                 16,
	})
	require.NoError(t, err)
	require.Equal(t, WakeReasonObligationSettled, wakes[0].WakeReason)
	require.Equal(t, "turn-team", wakes[0].TurnID)
}
