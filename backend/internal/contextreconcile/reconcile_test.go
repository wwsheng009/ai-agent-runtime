package contextreconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestReconcileAppendsCanonicalStateForMissingDurableReferences(t *testing.T) {
	replacement := []types.Message{*types.NewAssistantMessage("Compacted context from earlier turns: work continues.")}
	result, report := Reconcile(replacement, Snapshot{
		SessionID:    "session-1",
		Goal:         &GoalSnapshot{GoalID: "goal-1", Objective: "finish reliability", Status: "active"},
		Todos:        []TodoSnapshot{{Content: "run tests", Status: "in_progress"}},
		Run:          RunSnapshot{Status: "running", TurnID: "turn-1"},
		Jobs:         []JobSnapshot{{JobID: "job-1", Status: "running"}},
		EvidenceRefs: []string{"event:goal", "job:job-1"},
	})
	require.True(t, report.CorrectionMade)
	require.Equal(t, 4, report.DriftCount)
	require.Len(t, result, 2)
	require.Equal(t, "correction", result[1].Metadata["context_stage"])
	require.Contains(t, result[1].Content, "goal-1")
	require.Contains(t, result[1].Content, "job-1")
}

func TestReconcileLeavesAlreadyCanonicalContextUntouched(t *testing.T) {
	goal := types.NewAssistantMessage("goal-1 job-1")
	goal.Metadata["goal_id"] = "goal-1"
	todos := types.NewAssistantMessage("todos")
	todos.Metadata["context_stage"] = "todo_state"
	run := types.NewAssistantMessage("run")
	run.Metadata["context_stage"] = "active_execution"
	result, report := Reconcile([]types.Message{*goal, *todos, *run}, Snapshot{
		Goal:  &GoalSnapshot{GoalID: "goal-1"},
		Todos: []TodoSnapshot{{Content: "done", Status: "completed"}},
		Run:   RunSnapshot{Status: "running"},
		Jobs:  []JobSnapshot{{JobID: "job-1", Status: "running"}},
	})
	require.False(t, report.CorrectionMade)
	require.Zero(t, report.DriftCount)
	require.Len(t, result, 3)
}
