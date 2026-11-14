package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestAPITerminalWaitDeliversResults(t *testing.T) {
	for _, mode := range []string{"target", "mailbox", "peek", "settles_during_wait"} {
		t.Run(mode, func(t *testing.T) {
			controller, _ := newAPIWaitLedgerFixture(t, subagentbatch.BatchCompleted, "turn-results")
			ctx := toolctx.WithSessionID(context.Background(), apiWaitLedgerTestSession)
			store := controller.handler.getSubagentBatchStore()
			batchID := "batch_results"
			_, err := store.CreateBatch(ctx, &subagentbatch.SubagentBatch{
				BatchID: batchID, ParentSessionID: apiWaitLedgerTestSession,
				ParentTurnID: "turn-results", Status: subagentbatch.BatchRunning, TaskCount: 2,
			}, []subagentbatch.SubagentTaskRecord{
				{TaskID: "first", BatchID: batchID, ChildSessionID: "child-first",
					Status: subagentbatch.TaskRunning},
				{TaskID: "second", BatchID: batchID, ChildSessionID: "child-second",
					Status: subagentbatch.TaskRunning, OrderIndex: 1},
			})
			require.NoError(t, err)
			for _, id := range []string{"first", "second"} {
				require.NoError(t, store.RecordTaskResult(ctx, batchID, id, -1, subagentbatch.TaskSucceeded,
					&subagentbatch.TaskResult{Success: true, Summary: id + " result"}))
			}
			if mode != "settles_during_wait" {
				_, err = store.UpdateBatch(ctx, batchID, -1, func(batch *subagentbatch.SubagentBatch) {
					batch.Status = subagentbatch.BatchCompleted
				})
				require.NoError(t, err)
			}
			require.NoError(t, store.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
				SessionID: apiWaitLedgerTestSession, TurnID: "turn-results",
				ObligationIDs: []string{batchID}, ResumeQueue: []string{batchID},
			}))
			if mode == "settles_during_wait" {
				controller.handler.SetSubagentBatchStore(&apiTerminalWaitTransitionStore{BatchStore: store, afterRead: func() {
					_, err := store.UpdateBatch(ctx, batchID, -1, func(batch *subagentbatch.SubagentBatch) {
						batch.Status = subagentbatch.BatchCompleted
					})
					require.NoError(t, err)
				}})
			}

			result, err := controller.Wait(ctx, toolbroker.WaitAgentArgs{
				ID: "child-second", MailboxOnly: mode == "mailbox", Peek: mode == "peek", TimeoutMs: 30000,
			})
			require.NoError(t, err)
			require.Len(t, result.Agents, 2)
			require.Equal(t, 2, result.ReadyCount)
			require.Equal(t, 0, result.PendingCount)
			require.False(t, result.TimedOut)
			if mode == "settles_during_wait" {
				require.Equal(t, 30000, result.WaitTimeoutMs, "preserve the observed wait window")
				require.Contains(t, result.TerminalDelta, batchID)
			} else {
				require.Zero(t, result.WaitTimeoutMs)
			}
			require.True(t, result.ChildrenSettled)
			require.False(t, result.FinalizationRequired)
			require.Equal(t, mode == "peek", result.Peek)
			require.NotNil(t, result.Synthesis)
			require.Contains(t, result.Synthesis.Children[0].Summary, "first result")
			require.Contains(t, result.Synthesis.Children[1].Summary, "second result")
		})
	}
}

func TestAPITerminalWaitDeliversDirectResult(t *testing.T) {
	controller, manager := newAPIAgentObligationFixture(t)
	ctx := context.Background()
	parent, err := manager.Create(ctx, "terminal-wait-parent")
	require.NoError(t, err)
	child, err := manager.Create(ctx, "terminal-wait-child")
	require.NoError(t, err)
	child.AddMessage(types.Message{Role: "assistant", Content: "direct child result"})
	require.NoError(t, manager.Update(ctx, child))
	_, err = supervision.ProjectAgentCompletion(ctx, controller.handler.getSupervisionStore(), nil,
		parent.ID, parent.ID, child.ID, "idle", "session_end")
	require.NoError(t, err)
	require.NoError(t, controller.handler.getSubagentBatchStore().ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		SessionID: parent.ID, TurnID: "turn-direct",
		ObligationIDs: []string{subagentbatch.AgentSessionObligationID(child.ID)},
	}))
	require.NoError(t, controller.handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID: parent.ID, Status: chat.SessionIdle, SuspendedTurnID: "turn-direct",
	}))
	result, err := controller.Wait(toolctx.WithSessionID(ctx, parent.ID), toolbroker.WaitAgentArgs{
		ID: child.ID, TimeoutMs: 30000,
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.ReadyCount)
	require.True(t, result.ChildrenSettled)
	require.False(t, result.FinalizationRequired)
	require.NotNil(t, result.Synthesis)
	require.Len(t, result.Synthesis.Children, 1)
	require.Equal(t, "direct child result", result.Synthesis.Children[0].Summary)
}

type apiTerminalWaitTransitionStore struct {
	subagentbatch.BatchStore
	afterRead func()
}

func (s *apiTerminalWaitTransitionStore) GetBatch(ctx context.Context, id string) (*subagentbatch.SubagentBatch, error) {
	batch, err := s.BatchStore.GetBatch(ctx, id)
	if err == nil && s.afterRead != nil {
		afterRead := s.afterRead
		s.afterRead = nil
		afterRead()
	}
	return batch, err
}
