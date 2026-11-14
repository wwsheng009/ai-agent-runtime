package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// A drained ledger must return the results, not just "finalize": the loop can
// only clear ResultsConsumed after receiving a synthesis packet.
func TestLocalTerminalWaitDeliversResults(t *testing.T) {
	for _, mode := range []string{"target", "mailbox", "peek", "settles_during_wait"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			host, _ := newLocalWaitLedgerHost(t, subagentbatch.BatchCompleted, "turn-results")
			batchID := "batch_results"
			_, err := host.SubagentBatches.CreateBatch(ctx, &subagentbatch.SubagentBatch{
				BatchID: batchID, ParentSessionID: localWaitLedgerTestSession,
				ParentTurnID: "turn-results", Status: subagentbatch.BatchRunning, TaskCount: 2,
			}, []subagentbatch.SubagentTaskRecord{
				{TaskID: "first", BatchID: batchID, ChildSessionID: "child-first",
					Status: subagentbatch.TaskRunning},
				{TaskID: "second", BatchID: batchID, ChildSessionID: "child-second",
					Status: subagentbatch.TaskRunning, OrderIndex: 1},
			})
			require.NoError(t, err)
			for _, id := range []string{"first", "second"} {
				require.NoError(t, host.SubagentBatches.RecordTaskResult(ctx, batchID, id, -1, subagentbatch.TaskSucceeded,
					&subagentbatch.TaskResult{Success: true, Summary: id + " result"}))
			}
			if mode != "settles_during_wait" {
				_, err = host.SubagentBatches.UpdateBatch(ctx, batchID, -1, func(batch *subagentbatch.SubagentBatch) {
					batch.Status = subagentbatch.BatchCompleted
				})
				require.NoError(t, err)
			}
			// Match the field failure: a batch plus a completed lightweight
			// child. SQLite metadata snapshots deliberately omit its history.
			storageConfig := runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir())
			storageConfig.ImportLegacyJSON = false
			storage, err := runtimechat.NewSQLiteSessionStorage(storageConfig)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, storage.CloseStorage()) })
			host.SessionStore = storage
			child := runtimechat.NewSession("test")
			child.ID = "direct-child"
			child.AddMessage(types.Message{Role: "assistant", Content: "direct child result"})
			require.NoError(t, storage.Save(ctx, child))
			_, err = supervision.ProjectAgentCompletion(ctx, host.Supervision.Store, nil,
				localWaitLedgerTestSession, localWaitLedgerTestSession, child.ID, "idle", "session_end")
			require.NoError(t, err)
			require.NoError(t, host.SubagentBatches.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
				SessionID: localWaitLedgerTestSession, TurnID: "turn-results",
				ObligationIDs: []string{batchID, subagentbatch.AgentSessionObligationID(child.ID)}, ResumeQueue: []string{batchID},
			}))
			if mode == "settles_during_wait" {
				store := host.SubagentBatches
				// First ledger read sees pending. Commit terminal state after
				// that snapshot, before observation/refresh, without sleeps.
				host.SubagentBatches = &localTerminalWaitTransitionStore{BatchStore: store, afterRead: func() {
					_, err := store.UpdateBatch(ctx, batchID, -1, func(batch *subagentbatch.SubagentBatch) {
						batch.Status = subagentbatch.BatchCompleted
					})
					require.NoError(t, err)
				}}
			}

			// Even a wait on just the last child must deliver the whole parked
			// cohort before the single per-turn consumption gate can be closed.
			result, err := newLocalActorRegistry(host).Wait(ctx, toolbroker.WaitAgentArgs{
				ID: "child-second", MailboxOnly: mode == "mailbox", Peek: mode == "peek", TimeoutMs: 30000,
			})
			require.NoError(t, err)
			require.Len(t, result.Agents, 3)
			require.Equal(t, 3, result.ReadyCount)
			require.Equal(t, 0, result.PendingCount)
			require.False(t, result.TimedOut)
			if mode == "settles_during_wait" {
				require.Equal(t, 30000, result.WaitTimeoutMs, "preserve the observed wait window")
				require.Contains(t, result.TerminalDelta, batchID)
			} else {
				require.Zero(t, result.WaitTimeoutMs, "terminal reads must not open a wait window")
			}
			require.True(t, result.ChildrenSettled)
			require.False(t, result.FinalizationRequired, "receiving results must not stop the parent's own work")
			require.Equal(t, mode == "peek", result.Peek)
			require.NotNil(t, result.Synthesis)
			require.Contains(t, result.Synthesis.Children[0].Summary, "first result")
			require.Contains(t, result.Synthesis.Children[1].Summary, "second result")
			require.Equal(t, "direct child result", result.Synthesis.Children[2].Summary)
		})
	}
}

type localTerminalWaitTransitionStore struct {
	subagentbatch.BatchStore
	afterRead func()
}

func (s *localTerminalWaitTransitionStore) GetBatch(ctx context.Context, id string) (*subagentbatch.SubagentBatch, error) {
	batch, err := s.BatchStore.GetBatch(ctx, id)
	if err == nil && s.afterRead != nil {
		afterRead := s.afterRead
		s.afterRead = nil
		afterRead()
	}
	return batch, err
}
