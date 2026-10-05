package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// EC-E7 / EC-C5：挂起期间用户 ESC / interrupt ⇒ 放弃挂起 turn —— 级联取消账本
// （obligation 推入终态 `canceled`）+ 清 §6.12 挂起记录 + 清派生缓存。
//
// 用例复用 resumeEpisodeHarness：只有文件型 SQLite store 才承载 §6.12 记录
// （I9 探测），内存 store 会让整条路径静默降级为 no-op。

func TestAbandonSuspendedTurnCancelsObligationsAndClearsRecord(t *testing.T) {
	ctx := context.Background()
	h := newResumeEpisodeHarness(t)
	h.seedSuspendedTurn(t, "turn_parked")

	created, err := h.batches.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch-1",
		ParentSessionID: h.session.ID,
		ParentTurnID:    "turn_parked",
		Status:          subagentbatch.BatchRunning,
	}, nil)
	require.NoError(t, err)
	require.True(t, created)

	result, err := h.actor.AbandonSuspendedTurn(ctx, "turn_parked", "user interrupt")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Cleared)
	require.Equal(t, []string{"batch-1"}, result.CanceledBatchIDs)

	batch, err := h.batches.GetBatch(ctx, "batch-1")
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, subagentbatch.BatchCanceled, batch.Status)
	require.Equal(t, "user interrupt", batch.CancelReason)
	require.NotNil(t, batch.CancelRequestedAt)

	_, ok, err := h.batches.GetTurnSuspension(ctx, h.session.ID, "turn_parked")
	require.NoError(t, err)
	require.False(t, ok, "abandon must clear the parked-turn record")

	state, err := h.store.LoadState(ctx, h.session.ID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Empty(t, state.SuspendedTurnID, "derived cache must not keep the abandoned turn id")

	// 幂等：记录已清 ⇒ 再次放弃是 no-op，不报错、不谎报 Cleared。
	again, err := h.actor.AbandonSuspendedTurn(ctx, "turn_parked", "user interrupt")
	require.NoError(t, err)
	require.NotNil(t, again)
	require.False(t, again.Cleared)
}

func TestAbandonSuspendedTurnSkipsTerminalAndMissingObligations(t *testing.T) {
	ctx := context.Background()
	h := newResumeEpisodeHarness(t)
	h.seedSuspendedTurn(t, "turn_parked")

	// batch-1 已终态 ⇒ 不得重复取消；batch-2 无账本行（从未创建 / 已 GC）⇒
	// 记入 MissingBatchIDs。两者都不阻止放弃：这是用户的显式决定。
	created, err := h.batches.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch-1",
		ParentSessionID: h.session.ID,
		Status:          subagentbatch.BatchCompleted,
	}, nil)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, h.batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn_parked",
		SessionID:     h.session.ID,
		ObligationIDs: []string{"batch-1", "batch-2"},
		ResumeQueue:   []string{"batch-1", "batch-2"},
	}))

	result, err := h.actor.AbandonSuspendedTurn(ctx, "turn_parked", "user interrupt")
	require.NoError(t, err)
	require.True(t, result.Cleared)
	require.Empty(t, result.CanceledBatchIDs)
	require.Equal(t, []string{"batch-1"}, result.AlreadyTerminalBatchIDs)
	require.Equal(t, []string{"batch-2"}, result.MissingBatchIDs)

	batch, err := h.batches.GetBatch(ctx, "batch-1")
	require.NoError(t, err)
	require.Equal(t, subagentbatch.BatchCompleted, batch.Status, "terminal batch must not be re-canceled")
	require.Empty(t, batch.CancelReason)
}

// TestInterruptAbandonsSuspendedTurn 覆盖 EC-E7 的接线：挂起态没有正在执行的
// run，`Interrupt`（用户 ESC）必须放弃挂起 turn，而不是只取消一个不存在的 run。
func TestInterruptAbandonsSuspendedTurn(t *testing.T) {
	ctx := context.Background()
	h := newResumeEpisodeHarness(t)
	h.seedSuspendedTurn(t, "turn_parked")

	created, err := h.batches.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch-1",
		ParentSessionID: h.session.ID,
		ParentTurnID:    "turn_parked",
		Status:          subagentbatch.BatchRunning,
	}, nil)
	require.NoError(t, err)
	require.True(t, created)

	require.NoError(t, h.actor.Interrupt(ctx))

	batch, err := h.batches.GetBatch(ctx, "batch-1")
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, subagentbatch.BatchCanceled, batch.Status)
	require.Equal(t, "user interrupt", batch.CancelReason)

	_, ok, err := h.batches.GetTurnSuspension(ctx, h.session.ID, "turn_parked")
	require.NoError(t, err)
	require.False(t, ok, "interrupt must not leave the parked-turn record behind")

	state, err := h.store.LoadState(ctx, h.session.ID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Empty(t, state.SuspendedTurnID)
	require.Equal(t, SessionStopped, state.Status)
}

// TestAbandonSuspendedTurnCascadesNonBatchObligations pins the Step-3 cascade:
// a parked turn's agent_session:/team: obligations are cancelled through the
// host hooks before the record is cleared, and the canceled ids are reported.
func TestAbandonSuspendedTurnCascadesNonBatchObligations(t *testing.T) {
	ctx := context.Background()
	h := newResumeEpisodeHarness(t)
	h.seedSuspendedTurn(t, "turn_parked")
	require.NoError(t, h.batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:     "turn_parked",
		SessionID:  h.session.ID,
		ObligationIDs: []string{
			subagentbatch.AgentSessionObligationID("child-1"),
			subagentbatch.TeamObligationID("team-1"),
		},
	}))

	var agentCalls, teamCalls []string
	h.actor.abandonHooks = SuspendedTurnAbandonHooks{
		CancelAgentSession: func(_ context.Context, sessionID, reason string) error {
			agentCalls = append(agentCalls, sessionID+"|"+reason)
			return nil
		},
		CancelTeam: func(_ context.Context, teamID, reason string) error {
			teamCalls = append(teamCalls, teamID+"|"+reason)
			return nil
		},
	}

	result, err := h.actor.AbandonSuspendedTurn(ctx, "turn_parked", "user interrupt")
	require.NoError(t, err)
	require.True(t, result.Cleared)
	require.Equal(t, []string{"child-1"}, result.CanceledAgentSessionIDs)
	require.Equal(t, []string{"team-1"}, result.CanceledTeamIDs)
	require.Equal(t, []string{"child-1|user interrupt"}, agentCalls)
	require.Equal(t, []string{"team-1|user interrupt"}, teamCalls)

	_, ok, err := h.batches.GetTurnSuspension(ctx, h.session.ID, "turn_parked")
	require.NoError(t, err)
	require.False(t, ok, "a fully cascaded abandon must clear the parked-turn record")
}

// TestAbandonSuspendedTurnKeepsRecordWhenCascadeFails pins the retry contract:
// a failing non-batch cascade keeps the durable record instead of silently
// dropping the obligation; the next (idempotent) ESC retries.
func TestAbandonSuspendedTurnKeepsRecordWhenCascadeFails(t *testing.T) {
	ctx := context.Background()
	h := newResumeEpisodeHarness(t)
	h.seedSuspendedTurn(t, "turn_parked")
	require.NoError(t, h.batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn_parked",
		SessionID:     h.session.ID,
		ObligationIDs: []string{subagentbatch.AgentSessionObligationID("child-stuck")},
	}))
	h.actor.abandonHooks = SuspendedTurnAbandonHooks{
		CancelAgentSession: func(context.Context, string, string) error {
			return errors.New("child still busy")
		},
	}

	_, err := h.actor.AbandonSuspendedTurn(ctx, "turn_parked", "user interrupt")
	require.Error(t, err)

	_, ok, readErr := h.batches.GetTurnSuspension(ctx, h.session.ID, "turn_parked")
	require.NoError(t, readErr)
	require.True(t, ok, "partial cascade failure must keep the parked record for retry")
}

// TestAbandonSuspendedTurnRequiresHooksForNonBatchObligations pins the strict
// reading: an unwired host cascade never silently drops an obligation it cannot
// cancel.
func TestAbandonSuspendedTurnRequiresHooksForNonBatchObligations(t *testing.T) {
	ctx := context.Background()
	h := newResumeEpisodeHarness(t)
	h.seedSuspendedTurn(t, "turn_parked")
	require.NoError(t, h.batches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn_parked",
		SessionID:     h.session.ID,
		ObligationIDs: []string{subagentbatch.TeamObligationID("team-unwired")},
	}))

	_, err := h.actor.AbandonSuspendedTurn(ctx, "turn_parked", "user interrupt")
	require.Error(t, err)

	_, ok, readErr := h.batches.GetTurnSuspension(ctx, h.session.ID, "turn_parked")
	require.NoError(t, readErr)
	require.True(t, ok, "an unwired cascade must keep the record, never clear on a guess")
}
