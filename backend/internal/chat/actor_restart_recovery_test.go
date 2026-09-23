package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// C4-3 / AC-P3-3a：宿主重启后，挂起 turn 的恢复路径必须逐项可读回：
//
//   - 账本：§6.12 挂起记录（obligation_ids / resume_queue）落在 durable batch
//     控制面上，新进程用新句柄打开同一份文件即可读回；
//   - 恢复路径：重建的 actor（内存缓存为空）提交时仍复用挂起 turn 的 turn_id
//     —— 冷缓存回查 durable 状态，命中后再回账本验证记录仍在（`suspendedTurnID`）；
//   - 无"假空闲"：重启现场读回的 `RuntimeState.SuspendedTurnID` 让 turn 继续按
//     busy 语义对外呈现（AC-P3-3c），否则新的 resume / wake 会被当成普通新 turn。

func TestRestartedActorResumesParkedTurnFromDurableLedger(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	ctx := context.Background()
	record := h.seedSuspendedTurn(t, "turn_parked")

	// 上一个进程退出：actor 停掉、账本句柄关闭。
	h.actor.Stop()
	require.NoError(t, h.batches.Close())

	// 新进程打开同一份账本文件（新句柄，没有任何进程内缓存）。
	reopened, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{Path: h.batchesPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	require.True(t, reopened.IsDurable())

	// 账本读回：挂起记录逐字段还在（obligation_ids / resume_queue 是 resume 的依据）。
	reloaded, ok, err := reopened.GetTurnSuspension(ctx, h.session.ID, "turn_parked")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, record.TurnID, reloaded.TurnID)
	require.Equal(t, record.RootScopeID, reloaded.RootScopeID)
	require.Equal(t, record.ObligationIDs, reloaded.ObligationIDs)
	require.Equal(t, record.ResumeQueue, reloaded.ResumeQueue)
	require.Equal(t, record.ParkedAt.UTC(), reloaded.ParkedAt.UTC())

	// 重建 actor：接同一份 durable 状态与新的账本句柄（内存缓存全空）。
	h.apiAgent.SetSubagentBatchCoordinator(agent.NewSubagentBatchCoordinator(agent.SubagentBatchCoordinatorConfig{
		Store: reopened,
	}))
	var restarted *SessionActor
	var preparedTurnIDs []string
	restarted, err = NewSessionActor(h.session.ID, SessionActorConfig{
		Agent:        h.apiAgent,
		LLMRuntime:   h.llmRuntime,
		SessionStore: h.storage,
		StateStore:   h.store,
		EventStore:   h.store,
		PrepareRun: func(_ context.Context, _ *Session, _ bool) error {
			if summary, ok := restarted.StateSummary(); ok {
				preparedTurnIDs = append(preparedTurnIDs, summary.CurrentTurnID)
			}
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(restarted.Stop)

	// 冷缓存解析：挂起 turn 来自 durable 状态 + 账本验证，而不是上一个 actor 的内存。
	require.Equal(t, "turn_parked", restarted.suspendedTurnID(ctx))

	// 重启现场没有"假空闲"：挂起 turn 仍按 busy 语义对外呈现，同时仍接受 resume。
	summary, ok := restarted.StateSummary()
	require.True(t, ok)
	require.True(t, summary.AwaitingObligations())
	require.True(t, summary.Busy(), "重启后挂起 turn 不得读成空闲")
	require.True(t, summary.AcceptsResume(), "同一 turn 的 resume episode 必须放行")

	// 恢复路径不变：提交（resume / steer）复用挂起 turn 的 turn_id。
	_, err = restarted.SubmitPrompt(ctx, "resume: 汇总子任务结果", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"turn_parked"}, preparedTurnIDs)

	// episode 收尾后账本与挂起标记都未被动过：turn 仍在挂起态，可继续 resume。
	after, ok, err := reopened.GetTurnSuspension(ctx, h.session.ID, "turn_parked")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, record.ObligationIDs, after.ObligationIDs, "resume episode 不得改写 obligation 账本")
	require.Equal(t, record.ResumeQueue, after.ResumeQueue)
	require.Equal(t, record.ParkedAt.UTC(), after.ParkedAt.UTC(), "resume episode 不是重新挂起")

	state := restarted.StateForInspection()
	require.NotNil(t, state)
	require.Equal(t, "", state.CurrentTurnID, "episode 结束后没有在途 run")
	require.Equal(t, "turn_parked", state.SuspendedTurnID)
}
