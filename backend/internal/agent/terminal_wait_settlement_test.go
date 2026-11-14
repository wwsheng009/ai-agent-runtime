package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// The live smoke test exposed this ordering: a lightweight child finishes
// before the next batch is dispatched. Its unread result must survive the new
// park epoch, while the epoch still advances to keep the next wake deliverable.
func TestParkBackgroundTurnPreservesUnreadTerminalChild(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		name := "unread"
		if consumed {
			name = "consumed"
		}
		t.Run(name, func(t *testing.T) {
			ctx, store, loop := newParkMergeHarness(t)
			loop.config.AgentSessionObligations = resolverProbeAgentSessions{}
			previous := &subagentbatch.TurnSuspension{
				SessionID: "sess-1", TurnID: loop.turnID,
				ObligationIDs:   []string{subagentbatch.AgentSessionObligationID("direct")},
				ParkedAt:        time.Now().UTC().Add(-time.Minute),
				ResultsConsumed: consumed,
			}
			require.NoError(t, store.ParkTurnSuspension(ctx, previous))
			batch := seedParkBatch(t, store, "sess-1", "batch-next", "task-next", subagentbatch.BatchRunning)
			loop.parkBackgroundTurn(ctx, batch, "sess-1")
			current, found, err := store.GetTurnSuspension(ctx, "sess-1", loop.turnID)
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, current.ParkedAt.After(previous.ParkedAt))
			require.False(t, current.ResultsConsumed, "the new batch has not delivered results yet")
			want := []string{"batch-next", "task-next"}
			if !consumed {
				want = append([]string{subagentbatch.AgentSessionObligationID("direct")}, want...)
			}
			require.Equal(t, want, current.ObligationIDs)
			rows, err := subagentbatch.BuildWaitLedgerWithResolvers(ctx, store, current, loop.obligationResolvers())
			require.NoError(t, err)
			if consumed {
				require.Len(t, rows, 1)
			} else {
				require.Len(t, rows, 2)
				require.Equal(t, "direct", rows[1].SubjectID)
				require.True(t, rows[1].Terminal)
			}
		})
	}
}

// Reproduce terminal batch + direct-child obligations, an already delivered
// settlement wake, and results_consumed=0. A result-bearing wait must let the
// next normal run-end settle without another child event or manual DB repair.
func TestTerminalWaitResultsReleaseParkedTurn(t *testing.T) {
	for _, status := range []string{"succeeded", "failed_with_result", "skipped"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
				Path: filepath.Join(t.TempDir(), "batches.sqlite"),
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			seedAgentSettleBatch(t, store, "batch-done", subagentbatch.BatchCompleted)
			record := &subagentbatch.TurnSuspension{
				SessionID: "sess-1", TurnID: "turn-parked", EpisodeID: "episode",
				ObligationIDs: []string{"batch-done", subagentbatch.AgentSessionObligationID("direct")},
				ResumeQueue:   []string{"batch-done"},
			}
			require.NoError(t, store.ParkTurnSuspension(ctx, record))
			ag := NewAgentWithLLM(&Config{Name: "settle"}, nil, llm.NewLLMRuntime(nil))
			ag.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store}))
			loop := &ReActLoop{agent: ag, turnID: record.TurnID,
				config: &LoopReActConfig{AgentSessionObligations: resolverProbeAgentSessions{}}}

			loop.settleParkedTurnOnRunEnd(ctx, record.SessionID, record.TurnID)
			require.True(t, loop.turnStillParked(ctx, record.SessionID, record.TurnID),
				"terminal status alone must not discard unread results")

			obligations := []toolbroker.AgentWaitObligation{
				{ObligationID: "batch-done", SubjectID: "batch-done", SubjectKind: "batch", Terminal: true},
				{ObligationID: "agent_session:direct", SubjectID: "direct", SubjectKind: "agent_session", Terminal: true},
			}
			wait, err := toolbroker.BuildTerminalAgentWaitResult(ctx, nil, obligations, nil,
				func(_ context.Context, row toolbroker.AgentWaitObligation) ([]toolbroker.AgentStatusResult, error) {
					return []toolbroker.AgentStatusResult{{
						ID: row.SubjectID, SessionID: row.SubjectID, Exists: true,
						CurrentTaskStatus: status, Output: "result for " + row.SubjectID,
					}}, nil
				})
			require.NoError(t, err)
			encoded, err := json.Marshal(wait)
			require.NoError(t, err)
			loop.observeChildWaitResults(ctx, "trace", record.SessionID, []toolExecutionResult{{
				Call: types.ToolCall{Name: toolbroker.ToolSubagentWait}, Output: string(encoded),
			}})
			persisted, found, err := store.GetTurnSuspension(ctx, record.SessionID, record.TurnID)
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, persisted.ResultsConsumed)
			loop.settleParkedTurnOnRunEnd(ctx, record.SessionID, record.TurnID)
			require.False(t, loop.turnStillParked(ctx, record.SessionID, record.TurnID))
		})
	}
}
