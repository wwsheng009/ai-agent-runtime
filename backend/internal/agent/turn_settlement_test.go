package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestSettleParkedTurnOnRunEnd covers the clear half of the §6.12 parked-turn
// lifecycle (EC-E1 "turn 永不结束"): the run boundary removes the durable record
// once every obligation batch is terminal — so the chat actor stops reusing the
// parked turn_id — while a still-running obligation keeps the record for the
// next resume episode.
func TestSettleParkedTurnOnRunEnd(t *testing.T) {
	ctx := context.Background()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.True(t, store.IsDurable())

	ag := NewAgentWithLLM(&Config{Name: "settle-agent", Model: "gpt-4"}, nil, llm.NewLLMRuntime(nil))
	ag.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store}))
	loop := &ReActLoop{agent: ag}

	parkAgentSettleTurn(t, store, "turn-parked", "batch-running")
	seedAgentSettleBatch(t, store, "batch-running", subagentbatch.BatchRunning)

	loop.settleParkedTurnOnRunEnd(ctx, "sess-1", "turn-parked")
	_, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-parked")
	require.NoError(t, err)
	require.True(t, ok, "a still-running obligation must keep the parked record")

	_, err = store.UpdateBatch(ctx, "batch-running", -1, func(batch *subagentbatch.SubagentBatch) {
		batch.Status = subagentbatch.BatchCompleted
	})
	require.NoError(t, err)

	loop.settleParkedTurnOnRunEnd(ctx, "sess-1", "turn-parked")
	_, ok, err = store.GetTurnSuspension(ctx, "sess-1", "turn-parked")
	require.NoError(t, err)
	require.False(t, ok, "terminal obligations must clear the parked record")

	// The run ctx is routinely canceled on this path (ESC / deadline / step
	// limit), so the clear must still land through the detached-context fallback.
	parkAgentSettleTurn(t, store, "turn-canceled-ctx", "batch-done")
	seedAgentSettleBatch(t, store, "batch-done", subagentbatch.BatchCompleted)
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	loop.settleParkedTurnOnRunEnd(canceledCtx, "sess-1", "turn-canceled-ctx")
	_, ok, err = store.GetTurnSuspension(ctx, "sess-1", "turn-canceled-ctx")
	require.NoError(t, err)
	require.False(t, ok, "a canceled run ctx must not block the settle clear")

	// Unresolvable state is a no-op: no record, no session id, no coordinator.
	loop.settleParkedTurnOnRunEnd(ctx, "sess-1", "turn-unknown")
	loop.settleParkedTurnOnRunEnd(ctx, "", "turn-parked")
	(&ReActLoop{}).settleParkedTurnOnRunEnd(ctx, "sess-1", "turn-parked")
}

func parkAgentSettleTurn(t *testing.T, store subagentbatch.BatchStore, turnID, batchID string) {
	t.Helper()
	require.NoError(t, store.ParkTurnSuspension(context.Background(), &subagentbatch.TurnSuspension{
		TurnID:        turnID,
		SessionID:     "sess-1",
		RootScopeID:   "sess-1",
		ObligationIDs: []string{batchID, "task-1"},
		ResumeQueue:   []string{batchID},
	}))
}

func seedAgentSettleBatch(t *testing.T, store subagentbatch.BatchStore, batchID string, status subagentbatch.BatchStatus) {
	t.Helper()
	_, err := store.CreateBatch(context.Background(), &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     "sess-1",
		ParentSessionID: "sess-1",
		Status:          status,
	}, nil)
	require.NoError(t, err)
}
