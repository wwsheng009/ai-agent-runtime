package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// parkMergeTeamResolver stubs the team control plane for the park-epoch
// decision (TeamObligations resolver).
type parkMergeTeamResolver map[string]bool

func (s parkMergeTeamResolver) TeamTerminal(_ context.Context, teamID string) (bool, bool, error) {
	terminal, found := s[teamID]
	return terminal, found, nil
}

func newParkMergeHarness(t *testing.T) (context.Context, subagentbatch.BatchStore, *ReActLoop) {
	t.Helper()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	require.True(t, store.IsDurable())
	t.Cleanup(func() { _ = store.Close() })

	ag := newSuspensionGateAgent(t, nil)
	ag.SetSubagentBatchCoordinator(&SubagentBatchCoordinator{store: store})
	loop := NewReActLoop(ag, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	loop.turnID = "turn-merge"
	return context.Background(), store, loop
}

func seedParkBatch(t *testing.T, store subagentbatch.BatchStore, sessionID, batchID, taskID string, status subagentbatch.BatchStatus) *subagentbatch.SubagentBatch {
	t.Helper()
	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     sessionID,
		ParentSessionID: sessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          status,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := store.CreateBatch(context.Background(), batch, []subagentbatch.SubagentTaskRecord{
		{TaskID: taskID, BatchID: batchID, OrderIndex: 1, UpdatedAt: now},
	})
	require.NoError(t, err)
	require.True(t, created)
	return batch
}

// TestParkBackgroundTurnMergesSameEpisodeDispatches pins the 2026-10-13 gap:
// two background batches dispatched in one turn used to overwrite each other's
// parked record. ResumeQueue is the authoritative batch list of the settle
// verdict (TurnSuspension.ObligationBatchIDs reads it first), so the overwrite
// hid the earlier still-running batch from the settlement check (premature
// resume) and then lost its terminal edge entirely.
func TestParkBackgroundTurnMergesSameEpisodeDispatches(t *testing.T) {
	ctx, store, loop := newParkMergeHarness(t)
	b1 := seedParkBatch(t, store, "sess-1", "batch-1", "task-1", subagentbatch.BatchRunning)
	b2 := seedParkBatch(t, store, "sess-1", "batch-2", "task-2", subagentbatch.BatchRunning)

	loop.parkBackgroundTurn(ctx, b1, "sess-1")
	first, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"batch-1", "task-1"}, first.ObligationIDs)
	require.Equal(t, []string{"batch-1"}, first.ResumeQueue)

	loop.parkBackgroundTurn(ctx, b2, "sess-1")
	merged, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"batch-1", "task-1", "batch-2", "task-2"}, merged.ObligationIDs,
		"a same-episode dispatch must not drop the earlier batch")
	require.Equal(t, []string{"batch-1", "batch-2"}, merged.ResumeQueue,
		"the settle verdict reads ResumeQueue, so every running batch must stay in it")
	require.Equal(t, first.ParkedAt.UTC(), merged.ParkedAt.UTC(),
		"the same park episode keeps its epoch: the settlement identity must stay stable")

	// Idempotent re-park of the same batch must not duplicate entries.
	loop.parkBackgroundTurn(ctx, b2, "sess-1")
	again, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, merged.ObligationIDs, again.ObligationIDs)
	require.Equal(t, merged.ResumeQueue, again.ResumeQueue)
}

// TestParkBackgroundTurnStartsFreshEpochAfterSettle pins the epoch rule that
// keeps the settlement wake deliverable: once the previous episode's obligations
// are terminal, a new dispatch must start a fresh park epoch instead of reusing
// the settled one (whose notify key was already delivered and would suppress the
// new settlement as an event replay).
func TestParkBackgroundTurnStartsFreshEpochAfterSettle(t *testing.T) {
	ctx, store, loop := newParkMergeHarness(t)
	b1 := seedParkBatch(t, store, "sess-1", "batch-1", "task-1", subagentbatch.BatchRunning)
	loop.parkBackgroundTurn(ctx, b1, "sess-1")
	first, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)

	_, err = store.UpdateBatch(ctx, "batch-1", -1, func(batch *subagentbatch.SubagentBatch) {
		batch.Status = subagentbatch.BatchCompleted
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	b2 := seedParkBatch(t, store, "sess-1", "batch-2", "task-2", subagentbatch.BatchRunning)
	loop.parkBackgroundTurn(ctx, b2, "sess-1")
	fresh, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"batch-2", "task-2"}, fresh.ObligationIDs,
		"a new episode carries only its own obligations")
	require.Equal(t, []string{"batch-2"}, fresh.ResumeQueue)
	require.True(t, fresh.ParkedAt.After(first.ParkedAt),
		"a new episode must advance the park epoch so its settlement wake stays deliverable")
}

// TestParkTeamObligationEpochAndMerge covers the team half of the same rule: a
// team dispatch joins a still-running episode's epoch, while a settled record
// must be replaced by a fresh epoch (the team park path always appended before,
// so a settled epoch was kept and the team settlement could be suppressed).
func TestParkTeamObligationEpochAndMerge(t *testing.T) {
	ctx, store, loop := newParkMergeHarness(t)
	// The team control plane must be readable for the epoch verdict: team-1 is
	// terminal (the settled cohort), team-2 is not yet known.
	loop.config = &LoopReActConfig{TeamObligations: parkMergeTeamResolver{"team-1": true}}
	b1 := seedParkBatch(t, store, "sess-1", "batch-1", "task-1", subagentbatch.BatchRunning)
	loop.parkBackgroundTurn(ctx, b1, "sess-1")
	first, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)

	// Same episode: the team obligation joins the running batch epoch.
	loop.parkTeamObligation(ctx, "sess-1", "team-1")
	joined, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"batch-1", "task-1", subagentbatch.TeamObligationID("team-1")}, joined.ObligationIDs)
	require.Equal(t, first.ParkedAt.UTC(), joined.ParkedAt.UTC(), "same episode keeps the epoch")

	// The whole cohort settles; the next dispatch starts a fresh epoch.
	_, err = store.UpdateBatch(ctx, "batch-1", -1, func(batch *subagentbatch.SubagentBatch) {
		batch.Status = subagentbatch.BatchCompleted
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	loop.parkTeamObligation(ctx, "sess-1", "team-2")
	fresh, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{subagentbatch.TeamObligationID("team-2")}, fresh.ObligationIDs)
	require.True(t, fresh.ParkedAt.After(first.ParkedAt),
		"a settled episode must not keep its epoch for the next team dispatch")
}
