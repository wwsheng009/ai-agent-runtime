package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

func TestBuildExecFinalizationIncompletePreservesChildResults(t *testing.T) {
	ctx := context.Background()
	store := newTestSubagentBatchStore(t)
	const (
		parentSessionID = "parent-exec-timeout"
		turnID          = "turn-exec-timeout"
		batchID         = "batch_exec_timeout"
	)
	created, err := store.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ParentTurnID:    turnID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       2,
		CreatedAt:       time.Now().UTC().Add(-2 * time.Second),
	}, []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-a", BatchID: batchID, ChildSessionID: "child-a", Status: subagentbatch.TaskRunning},
		{TaskID: "task-b", BatchID: batchID, ChildSessionID: "child-b", Status: subagentbatch.TaskRunning},
	})
	require.True(t, created)

	for _, result := range []struct {
		taskID  string
		status  subagentbatch.TaskStatus
		capsule *subagentbatch.TaskResult
	}{
		{
			taskID: "task-a",
			status: subagentbatch.TaskSucceeded,
			capsule: &subagentbatch.TaskResult{
				TaskID: "task-a", SessionID: "child-a", Success: true, Summary: "analysis complete",
			},
		},
		{
			taskID: "task-b",
			status: subagentbatch.TaskFailed,
			capsule: &subagentbatch.TaskResult{
				TaskID: "task-b", SessionID: "child-b", Error: "build failed",
			},
		},
	} {
		task, err := store.GetTask(ctx, batchID, result.taskID)
		require.NoError(t, err)
		require.NoError(t, store.RecordTaskResult(ctx, batchID, result.taskID, task.Version, result.status, result.capsule))
	}
	finishedAt := time.Now().UTC()
	_, err = store.UpdateBatch(ctx, batchID, -1, func(batch *subagentbatch.SubagentBatch) {
		batch.Status = subagentbatch.BatchPartiallyCompleted
		batch.FinishedAt = &finishedAt
	})
	require.NoError(t, err)
	require.NoError(t, store.ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        turnID,
		SessionID:     parentSessionID,
		RootScopeID:   parentSessionID,
		ObligationIDs: []string{batchID},
		ResumeQueue:   []string{batchID},
	}))

	host := &localChatRuntimeHost{SubagentBatches: store}
	session := &ExecSession{ChatSession: &ChatSession{
		LocalRuntimeHost: host,
		RuntimeSession:   &runtimechat.Session{ID: parentSessionID},
	}}

	finalization := buildExecFinalizationIncomplete(ctx, session)
	require.Equal(t, "parent_exec_timeout", finalization.Reason)
	require.Equal(t, []string{batchID}, finalization.BatchIDs)
	require.Len(t, finalization.ChildResults, 2)
	require.Equal(t, 2, finalization.CompletedObligations)
	require.Zero(t, finalization.PendingObligations)

	message := finalization.Message()
	require.Contains(t, message, `"status":"finalization_incomplete"`)
	require.Contains(t, message, `"reason":"parent_exec_timeout"`)
	require.Contains(t, message, `"task_id":"task-a"`)
	require.Contains(t, message, "analysis complete")

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(message), &decoded))
	require.Equal(t, "finalization_incomplete", decoded["status"])
	require.Equal(t, float64(2), decoded["completed_obligations"])
}

func TestBuildExecFinalizationIncompleteWithoutControlPlane(t *testing.T) {
	finalization := buildExecFinalizationIncomplete(context.Background(), &ExecSession{
		ChatSession: &ChatSession{},
	})
	require.NotNil(t, finalization)
	require.Equal(t, "parent_exec_timeout", finalization.Reason)
	require.Empty(t, finalization.ChildResults)
	require.Contains(t, finalization.Message(), `"status":"finalization_incomplete"`)
}

func TestHandleExecTurnErrorEmitsStructuredFinalizationIncomplete(t *testing.T) {
	var output bytes.Buffer
	processor := NewExecEventProcessor(true, &output, "")
	store := newTestSubagentBatchStore(t)
	session := &ExecSession{
		Options:   &ExecOptions{JSONMode: true},
		Processor: processor,
		ChatSession: &ChatSession{
			ProviderName:     "test-provider",
			Model:            "test-model",
			RuntimeSession:   &runtimechat.Session{ID: "parent-exec-timeout"},
			LocalRuntimeHost: &localChatRuntimeHost{SubagentBatches: store},
		},
	}
	deadlineCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err := handleExecTurnError(deadlineCtx, session, processor, "turn-timeout", time.Second, session.Options, context.DeadlineExceeded)
	require.Equal(t, execExitTimeout, execExitCode(err))
	require.ErrorIs(t, err, context.DeadlineExceeded)

	var failed *TurnFailedEvent
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event ThreadEvent
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		if event.Type != EventTypeTurnFailed {
			continue
		}
		var decoded TurnFailedEvent
		require.NoError(t, json.Unmarshal(event.Data, &decoded))
		failed = &decoded
	}
	require.NotNil(t, failed, "timeout must emit a turn.failed event")
	require.Equal(t, "finalization_incomplete", failed.Status)
	require.Equal(t, "TIMEOUT", failed.Code)
	require.Equal(t, "parent_exec_timeout", failed.Reason)
	require.Contains(t, failed.Error, `"status":"finalization_incomplete"`)
	require.Contains(t, failed.Error, `"reason":"parent_exec_timeout"`)
}
