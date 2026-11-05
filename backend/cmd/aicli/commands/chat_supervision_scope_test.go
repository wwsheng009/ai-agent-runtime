package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestLocalSupervisionCurrentTurnSubjectIDs(t *testing.T) {
	host, batchID := newLocalWaitLedgerHost(t, subagentbatch.BatchCompleted, "turn-current-scope")
	controller := &localSupervisionToolController{host: host}

	ids := controller.currentTurnSubjectIDs(context.Background(), localWaitLedgerTestSession)
	require.Contains(t, ids, batchID)
}

func TestLocalSupervisionCurrentBatchSubjectIDs(t *testing.T) {
	host, _ := newLocalWaitLedgerHost(t, subagentbatch.BatchCompleted, "turn-batch-scope")
	ctx := context.Background()
	now := subagentbatch.Now()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         subagentbatch.NewID("batch"),
		RootScopeID:     localWaitLedgerTestSession,
		ParentSessionID: localWaitLedgerTestSession,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchCompleted,
		TaskCount:       1,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
		Version:         1,
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{{
		TaskID:         "task-batch-scope",
		BatchID:        batch.BatchID,
		ChildSessionID: "child-batch-scope",
		Status:         subagentbatch.TaskSucceeded,
		UpdatedAt:      now,
		Version:        1,
	}})
	require.NoError(t, err)
	require.True(t, created)

	controller := &localSupervisionToolController{host: host}
	ids := controller.currentBatchSubjectIDs(ctx, localWaitLedgerTestSession, batch.BatchID)
	require.ElementsMatch(t, []string{batch.BatchID, "child-batch-scope"}, ids)
	require.Nil(t, controller.currentBatchSubjectIDs(ctx, "other-session", batch.BatchID))
}

func TestLocalSupervisionAutoScopeUsesCurrentTurnWhenSubjectsExist(t *testing.T) {
	host, batchID := newLocalWaitLedgerHost(t, subagentbatch.BatchRunning, "turn-auto-scope")
	host.Supervision.Provider = &recordingDescendantProvider{states: []supervision.DescendantState{
		{Kind: supervision.SubjectAgentRun, ID: batchID, ExecutionStatus: "running", SupervisionState: supervision.SupervisionRunning},
		{Kind: supervision.SubjectAgentRun, ID: "historical", ExecutionStatus: "completed", SupervisionState: supervision.SupervisionTerminated},
	}}
	controller := newLocalSupervisionToolController(host, newChatDebugSupervisionSession(host, localWaitLedgerTestSession))
	require.NotNil(t, controller)

	snapshot, err := controller.SupervisionDescendants(context.Background(), localWaitLedgerTestSession, toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.Equal(t, "current_turn", snapshot.FilterScope)
	require.Len(t, snapshot.Descendants, 1)
	require.Equal(t, batchID, snapshot.Descendants[0].ID)

	all, err := controller.SupervisionDescendants(context.Background(), localWaitLedgerTestSession, toolbroker.SupervisionDescendantsArgs{Scope: "all"})
	require.NoError(t, err)
	require.Equal(t, "all", all.FilterScope)
	require.Len(t, all.Descendants, 2)
}

func TestLocalSupervisionAutoScopeFallsBackToAllWithoutCurrentTurn(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.Supervision.Provider = &recordingDescendantProvider{states: []supervision.DescendantState{
		{Kind: supervision.SubjectAgentRun, ID: "historical", ExecutionStatus: "completed", SupervisionState: supervision.SupervisionTerminated},
	}}
	controller := newLocalSupervisionToolController(host, newChatDebugSupervisionSession(host, "parent-no-turn"))
	require.NotNil(t, controller)

	snapshot, err := controller.SupervisionDescendants(context.Background(), "parent-no-turn", toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.Equal(t, "all", snapshot.FilterScope)
	require.Len(t, snapshot.Descendants, 1)
}
