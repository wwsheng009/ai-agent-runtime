package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSessionActorReconcilesGoalTodoAndRunAfterCompact(t *testing.T) {
	session := NewSession("user")
	session.ID = "session-1"
	session.SetContext("aicli.goal", map[string]interface{}{
		"goal_id": "goal-1", "objective": "finish reliability", "status": "active",
	})
	todoMessage := types.NewToolMessage("call-1", "updated todos")
	todoMessage.Metadata["goal_id"] = "goal-1"
	todoMessage.Metadata["todos"] = []map[string]interface{}{{"content": "run tests", "status": "in_progress"}}
	session.AddMessage(*todoMessage)

	actor := &SessionActor{id: session.ID, state: &RuntimeState{
		SessionID: session.ID, Status: SessionRunning, CurrentTurnID: "turn-1",
	}}
	result := &compactruntime.Result{ReplacementHistory: []types.Message{
		*types.NewAssistantMessage("Compacted context from earlier turns."),
	}}
	actor.reconcileCompactResult(context.Background(), session, result)

	require.NotNil(t, result.Reconciliation)
	require.True(t, result.Reconciliation.CorrectionMade)
	require.Equal(t, 3, result.Reconciliation.DriftCount)
	require.Equal(t, "correction", result.ReplacementHistory[1].Metadata["context_stage"])
	require.Contains(t, result.ReplacementHistory[1].Content, "goal-1")
	require.Contains(t, result.ReplacementHistory[1].Content, "run tests")
}
