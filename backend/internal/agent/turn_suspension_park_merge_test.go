package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
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

// parkMergeEventBus wires an event bus and returns a snapshot accessor for the
// recorded turn.suspended events.
func parkMergeEventBus(t *testing.T, ag *Agent) func() []runtimeevents.Event {
	t.Helper()
	bus := runtimeevents.NewBus()
	var mu sync.Mutex
	var suspended []runtimeevents.Event
	bus.Subscribe(runtimeevents.EventTurnSuspended, func(event runtimeevents.Event) {
		mu.Lock()
		suspended = append(suspended, event)
		mu.Unlock()
	})
	ag.SetEventBus(bus)
	return func() []runtimeevents.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]runtimeevents.Event(nil), suspended...)
	}
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

// TestDurableParkAnnouncesFreshEpochAfterSettle pins the pairing contract behind
// the "agent.turn.finished while still waiting for subagents" conflict: every
// FRESH park epoch announces turn.suspended, so a run-end finished event always
// has a matching suspension edge. Within one live episode the edge stays single
// (merges are silent); a settled episode's successor announces again.
func TestDurableParkAnnouncesFreshEpochAfterSettle(t *testing.T) {
	ctx, store, loop := newParkMergeHarness(t)
	suspended := parkMergeEventBus(t, loop.agent)

	b1 := seedParkBatch(t, store, "sess-1", "batch-1", "task-1", subagentbatch.BatchRunning)
	loop.parkBackgroundTurn(ctx, b1, "sess-1")
	// Same episode: an idempotent re-park must not announce a second edge.
	loop.parkBackgroundTurn(ctx, b1, "sess-1")
	require.Len(t, suspended(), 1, "同一纪元的追加/重挂不重复播报")

	_, err := store.UpdateBatch(ctx, "batch-1", -1, func(batch *subagentbatch.SubagentBatch) {
		batch.Status = subagentbatch.BatchCompleted
	})
	require.NoError(t, err)

	b2 := seedParkBatch(t, store, "sess-1", "batch-2", "task-2", subagentbatch.BatchRunning)
	loop.parkBackgroundTurn(ctx, b2, "sess-1")
	events := suspended()
	require.Len(t, events, 2, "新纪元必须重新播报挂起边（agent.turn.finished 才有配对）")
	require.Equal(t, "batch-2", events[1].Payload["batch_id"])
	require.Equal(t, 2, events[1].Payload["obligation_count"])
	require.Equal(t, "turn-merge", events[1].Payload["turn_id"])
	require.Equal(t, "sess-1", events[1].SessionID)
}

// TestParkTeamObligationAnnouncesFreshEpochAfterSettle is the team half of the
// same pairing contract.
func TestParkTeamObligationAnnouncesFreshEpochAfterSettle(t *testing.T) {
	ctx, store, loop := newParkMergeHarness(t)
	loop.config = &LoopReActConfig{TeamObligations: parkMergeTeamResolver{"team-1": true}}
	suspended := parkMergeEventBus(t, loop.agent)

	b1 := seedParkBatch(t, store, "sess-1", "batch-1", "task-1", subagentbatch.BatchRunning)
	loop.parkBackgroundTurn(ctx, b1, "sess-1")
	loop.parkTeamObligation(ctx, "sess-1", "team-1")
	require.Len(t, suspended(), 1, "同一纪元的 team 追加不重复播报")

	_, err := store.UpdateBatch(ctx, "batch-1", -1, func(batch *subagentbatch.SubagentBatch) {
		batch.Status = subagentbatch.BatchCompleted
	})
	require.NoError(t, err)

	loop.parkTeamObligation(ctx, "sess-1", "team-2")
	events := suspended()
	require.Len(t, events, 2, "新纪元必须重新播报挂起边")
	require.Equal(t, "team-2", events[1].Payload["team_id"])
	require.Equal(t, "turn-merge", events[1].Payload["turn_id"])
}
