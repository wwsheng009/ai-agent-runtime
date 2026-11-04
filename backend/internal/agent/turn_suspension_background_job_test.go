package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// parkJobsResolver is the required-job control plane used by the loop tests.
// JobStoreDurable models the I9 probe the real background.Manager implements.
type parkJobsResolver struct {
	terminal map[string]bool
	durable  bool
}

func (r parkJobsResolver) BackgroundJobTerminal(_ context.Context, jobID string) (bool, bool, error) {
	terminal, found := r.terminal[jobID]
	return terminal, found, nil
}

func (r parkJobsResolver) JobStoreDurable() bool {
	return r.durable
}

func TestParkBackgroundJobObligationAndSettle(t *testing.T) {
	ctx, store, loop := newParkMergeHarness(t)
	resolver := parkJobsResolver{
		terminal: map[string]bool{"job-1": false},
		durable:  true,
	}
	loop.config.BackgroundJobObligations = resolver
	suspended := parkMergeEventBus(t, loop.agent)

	loop.parkBackgroundJobObligation(ctx, "sess-1", "job-1")
	loop.parkBackgroundJobObligation(ctx, "sess-1", "job-1")
	record, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{subagentbatch.BackgroundJobObligationID("job-1")}, record.ObligationIDs)
	require.Empty(t, record.ResumeQueue, "job obligations must never be read as batch ids")
	events := suspended()
	require.Len(t, events, 1, "the same job replayed must not announce a second park edge")
	require.Equal(t, "job-1", events[0].Payload["job_id"])

	settled, err := subagentbatch.TurnObligationsAllTerminalWithResolvers(ctx, store, record, loop.obligationResolvers())
	require.NoError(t, err)
	require.False(t, settled, "a running job must keep the parent parked")

	resolver.terminal["job-1"] = true
	loop.config.BackgroundJobObligations = resolver
	loop.settleParkedTurnOnRunEnd(ctx, "sess-1", "turn-merge")
	_, ok, err = store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.False(t, ok, "a terminal job must let the run-end settle pass clear the record")
}

func TestParkBackgroundJobObligationsFromResultsHonorsPolicyAndStatus(t *testing.T) {
	ctx, store, loop := newParkMergeHarness(t)
	loop.config.BackgroundJobObligations = parkJobsResolver{
		terminal: map[string]bool{"job-required": false},
		durable:  true,
	}
	results := []toolExecutionResult{
		backgroundJobStartResult("job-detached", "detached", "running"),
		backgroundJobStartResult("job-terminal", "required", "completed"),
		backgroundJobStartResult("job-required", "required", "pending"),
	}
	loop.parkBackgroundJobObligationsFromResults(ctx, "sess-1", results)
	record, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{subagentbatch.BackgroundJobObligationID("job-required")}, record.ObligationIDs)
}

func TestParkBackgroundJobObligationDegradesWithoutDurableJobStore(t *testing.T) {
	ctx, store, loop := newParkMergeHarness(t)
	loop.config.BackgroundJobObligations = parkJobsResolver{
		terminal: map[string]bool{"job-1": false},
		durable:  false,
	}
	loop.parkBackgroundJobObligation(ctx, "sess-1", "job-1")
	_, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-merge")
	require.NoError(t, err)
	require.False(t, ok, "an in-memory job store must not leave an unresolvable durable suspension")
}

func backgroundJobStartResult(jobID, policy, status string) toolExecutionResult {
	return toolExecutionResult{
		Call: types.ToolCall{Name: toolbroker.ToolJobStart},
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"tool_metadata": map[string]interface{}{
				"job_id":              jobID,
				"finalization_policy": policy,
				"status":              status,
			},
		}},
	}
}
