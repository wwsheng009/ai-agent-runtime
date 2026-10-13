package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestAppendGatewaySubagentSessionsBoundedLiveOnly pins the API host half of the
// opt-in child rows: live tasks become display-only child summaries linked to
// their parent, terminal tasks are skipped, and a missing batch store leaves the
// parent rows untouched (fail-open).
func TestAppendGatewaySubagentSessionsBoundedLiveOnly(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	store := newAPISupervisionBatchStore(t)
	handler.SetSubagentBatchStore(store)

	ctx := context.Background()
	now := time.Now().UTC()
	created, err := store.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch-api-live",
		RootScopeID:     "p1",
		ParentSessionID: "p1",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}, []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-live", BatchID: "batch-api-live", ChildSessionID: "subagent_api_live", TaskType: "implement", TaskSubject: "宿主接线", Status: subagentbatch.TaskPending, OrderIndex: 1},
		{TaskID: "task-done", BatchID: "batch-api-live", ChildSessionID: "subagent_api_done", Status: subagentbatch.TaskPending, OrderIndex: 2},
	})
	require.NoError(t, err)
	require.True(t, created)
	_, err = store.UpdateTask(ctx, "batch-api-live", "task-live", -1, func(task *subagentbatch.SubagentTaskRecord) {
		task.Status = subagentbatch.TaskRunning
		task.StartedAt = &now
	})
	require.NoError(t, err)
	_, err = store.UpdateTask(ctx, "batch-api-live", "task-done", -1, func(task *subagentbatch.SubagentTaskRecord) {
		task.Status = subagentbatch.TaskCanceled
	})
	require.NoError(t, err)

	parents := []wire.SessionSummary{{SessionID: "p1"}, {SessionID: "p2"}}
	out := handler.appendGatewaySubagentSessions(ctx, parents)
	require.Len(t, out, 3, "two parents + one live child")
	require.Equal(t, "p1", out[0].SessionID)
	require.Equal(t, "p2", out[1].SessionID)

	child := out[2]
	require.Equal(t, "subagent_api_live", child.SessionID)
	require.Equal(t, "宿主接线", child.Title)
	require.Equal(t, wire.SessionStateRunning, child.State)
	require.NotNil(t, child.Parent)
	require.Equal(t, "p1", child.Parent.ParentSessionID)
	require.Equal(t, "task-live", child.Parent.AgentID)
	require.Equal(t, "implement", child.Parent.AgentType)

	// Fail-open: no batch store wired → the list is returned unchanged.
	bare := NewHandler(skill.NewRegistry(nil), nil, nil)
	require.Len(t, bare.appendGatewaySubagentSessions(ctx, parents), 2)
}
