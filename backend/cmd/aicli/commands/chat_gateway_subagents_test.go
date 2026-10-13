package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// seedGatewaySubagentBatch creates one running batch under parentSessionID with
// a live task and a terminal task (only the live one may become a child row).
func seedGatewaySubagentBatch(t *testing.T, store subagentbatch.BatchStore, parentSessionID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	created, err := store.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         "batch-live",
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}, []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-live", BatchID: "batch-live", ChildSessionID: "subagent_live", TaskType: "implement", TaskSubject: "切片 5 · 前端归属", Status: subagentbatch.TaskPending, OrderIndex: 1},
		{TaskID: "task-done", BatchID: "batch-live", ChildSessionID: "subagent_done", Status: subagentbatch.TaskPending, OrderIndex: 2},
	})
	require.NoError(t, err)
	require.True(t, created)
	_, err = store.UpdateTask(ctx, "batch-live", "task-live", -1, func(task *subagentbatch.SubagentTaskRecord) {
		task.Status = subagentbatch.TaskRunning
		task.StartedAt = &now
	})
	require.NoError(t, err)
	_, err = store.UpdateTask(ctx, "batch-live", "task-done", -1, func(task *subagentbatch.SubagentTaskRecord) {
		task.Status = subagentbatch.TaskCanceled
	})
	require.NoError(t, err)
}

// TestChatGatewaySessionsAppendsLiveSubagentRows pins the opt-in child rows:
// with AICLI_GATEWAY_REPORT_SUBAGENTS the session list carries one display-only
// row per live child task, linked to its parent; the default stays parent-only.
func TestChatGatewaySessionsAppendsLiveSubagentRows(t *testing.T) {
	const parentID = "parent-subagents"
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	seedGatewaySubagentBatch(t, store, parentID)

	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: parentID}}
	session.LocalRuntimeHost = &localChatRuntimeHost{SubagentBatches: store}

	// 默认开启：父行 + 一条活子行。
	got := chatGatewaySessions(session)
	require.Len(t, got, 2, "parent + one live child (terminal children are never listed)")
	require.Equal(t, parentID, got[0].SessionID)
	require.Nil(t, got[0].Parent)

	child := got[1]
	require.Equal(t, "subagent_live", child.SessionID)
	require.Equal(t, "切片 5 · 前端归属", child.Title)
	require.Equal(t, wire.SessionStateRunning, child.State)
	require.NotNil(t, child.Parent)
	require.Equal(t, parentID, child.Parent.ParentSessionID)
	require.Equal(t, "task-live", child.Parent.AgentID)
	require.Equal(t, "implement", child.Parent.AgentType)

	// 显式关闭：回到只有父行的历史行为。
	t.Setenv(gatewayagent.EnvReportSubagents, "0")
	require.Len(t, chatGatewaySessions(session), 1)
}
