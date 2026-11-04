package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMarkTurnFinalizingIsScopedAndIdempotent pins the phase transition used
// before the durable sequence gate: only the owning turn may enter FINALIZING,
// and already terminal phases are never overwritten.
func TestMarkTurnFinalizingIsScopedAndIdempotent(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	ctx := context.Background()
	const turnID = "turn-finalizing-state"
	require.NoError(t, h.actor.UpdateStateForTest(ctx, func(state *RuntimeState) error {
		state.Status = SessionRunning
		state.CurrentTurnID = turnID
		state.CurrentEpisodeID = "ep-finalizing-state"
		state.TurnPhase = ParentTurnPhaseRunning
		return nil
	}))

	require.True(t, h.actor.markTurnFinalizing(ctx, turnID))
	state := h.actor.StateForInspection()
	require.NotNil(t, state)
	require.Equal(t, ParentTurnPhaseFinalizing, state.TurnPhase)
	require.Equal(t, turnID, state.CurrentTurnID)
	require.True(t, h.actor.markTurnFinalizing(ctx, turnID), "re-entering FINALIZING is idempotent")
	require.False(t, h.actor.markTurnFinalizing(ctx, "other-turn"),
		"a non-owning turn must not mutate the phase")

	require.NoError(t, h.actor.UpdateStateForTest(ctx, func(state *RuntimeState) error {
		state.TurnPhase = ParentTurnPhaseFinalized
		state.FinalizedTurnID = turnID
		state.FinalizedEpisodeID = "ep-finalizing-state"
		return nil
	}))
	require.False(t, h.actor.markTurnFinalizing(ctx, turnID),
		"a converged terminal phase must not be reopened as finalizing")
	state = h.actor.StateForInspection()
	require.Equal(t, ParentTurnPhaseFinalized, state.TurnPhase)
	require.Equal(t, turnID, state.FinalizedTurnID)
}

func TestContinuationKindClassifiesWakeWithoutChangingResume(t *testing.T) {
	require.Equal(t, "user", continuationKind("普通用户输入", false))
	require.Equal(t, "user_resume", continuationKind("", true))
	require.Equal(t, "supervision_wake", continuationKind("[supervision] resume：settled", false))
	require.Equal(t, "supervision_wake", continuationKind("[Child lifecycle preflight]\nsnapshot_seq: 1", false))
	require.Equal(t, "supervision_wake", continuationKind("[子任务与后台作业进展] 子任务已完成", false))
}
