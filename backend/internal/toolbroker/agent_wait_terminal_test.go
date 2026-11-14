package toolbroker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildTerminalAgentWaitResultStatuses(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "failed_with_result", "canceled", "timed_out", "skipped", "closed", "running", "missing"} {
		t.Run(status, func(t *testing.T) {
			obligation := AgentWaitObligation{ObligationID: "batch-a", SubjectID: "batch-a", SubjectKind: "batch", Terminal: true}
			result, err := BuildTerminalAgentWaitResult(context.Background(), nil, []AgentWaitObligation{obligation}, []string{"batch-a"},
				func(context.Context, AgentWaitObligation) ([]AgentStatusResult, error) {
					return []AgentStatusResult{{ID: "task", SessionID: "child", Exists: status != "missing",
						CurrentTaskID: "task", CurrentTaskStatus: status, Output: "durable result"}}, nil
				})
			require.NoError(t, err)
			terminal := status != "running" && status != "missing"
			require.Equal(t, terminal, result.ChildrenSettled)
			require.False(t, result.FinalizationRequired)
			require.False(t, result.TimedOut)
			require.Zero(t, result.WaitTimeoutMs)
			require.Empty(t, result.TerminalDelta)
			require.Equal(t, "durable result", result.Synthesis.Children[0].Summary)
			if terminal {
				require.Equal(t, 1, result.ReadyCount)
				require.Zero(t, result.PendingCount)
			} else {
				require.Zero(t, result.ReadyCount)
				require.Equal(t, 1, result.PendingCount)
				require.Contains(t, result.NextAction, "inspect")
			}
		})
	}
}

func TestBuildTerminalAgentWaitResultHydratesWholeCohortAndPreservesWindow(t *testing.T) {
	rows := []AgentWaitObligation{
		{ObligationID: "batch-a", SubjectID: "batch-a", SubjectKind: "batch", Terminal: true},
		{ObligationID: "batch-b", SubjectID: "batch-b", SubjectKind: "batch", Terminal: true},
		{ObligationID: "agent_session:direct", SubjectID: "direct", SubjectKind: "agent_session", Terminal: true},
	}
	observed := &AgentWaitResult{
		Agents:     []AgentStatusResult{{ID: "stale", Status: "running"}},
		PendingIDs: []string{"stale"}, PendingCount: 1,
		WaitedMs: 23, WaitTimeoutMs: 10000, WaitTimeoutRequestedMs: 5, WaitTimeoutClamped: true,
	}
	var read []string
	result, err := BuildTerminalAgentWaitResult(context.Background(), observed, rows, []string{"batch-a"},
		func(_ context.Context, row AgentWaitObligation) ([]AgentStatusResult, error) {
			read = append(read, row.SubjectID)
			// Two batches can have the same unbound task id. Neither result
			// may be dropped when marking a single per-turn consumption bit.
			return []AgentStatusResult{{ID: "reused-task", Exists: true, Status: "stopped",
				CurrentTaskID: "reused-task", CurrentTaskStatus: "skipped", Output: row.SubjectID}}, nil
		})
	require.NoError(t, err)
	require.Equal(t, []string{"batch-a", "batch-b", "direct"}, read)
	require.Len(t, result.Agents, 3)
	require.Equal(t, 3, result.ReadyCount)
	require.Zero(t, result.PendingCount)
	require.Empty(t, result.PendingIDs)
	require.True(t, result.ChildrenSettled)
	require.Equal(t, int64(23), result.WaitedMs)
	require.Equal(t, 10000, result.WaitTimeoutMs)
	require.Equal(t, 5, result.WaitTimeoutRequestedMs)
	require.True(t, result.WaitTimeoutClamped)
	require.ElementsMatch(t, []string{"batch-b", "agent_session:direct"}, result.TerminalDelta)
	require.Equal(t, "stale", observed.Agents[0].ID, "do not mutate the observed snapshot")
}

func TestBuildTerminalAgentWaitResultDoesNotHideReadErrorsOrPendingLedger(t *testing.T) {
	readErr := errors.New("durable results unavailable")
	for _, terminal := range []bool{false, true} {
		row := AgentWaitObligation{ObligationID: "batch-a", SubjectID: "batch-a", SubjectKind: "batch", Terminal: terminal}
		called := false
		result, err := BuildTerminalAgentWaitResult(context.Background(), nil, []AgentWaitObligation{row}, nil,
			func(context.Context, AgentWaitObligation) ([]AgentStatusResult, error) {
				called = true
				return nil, readErr
			})
		require.Error(t, err)
		require.Nil(t, result)
		require.Equal(t, terminal, called)
		if terminal {
			require.ErrorIs(t, err, readErr)
		}
	}
}
