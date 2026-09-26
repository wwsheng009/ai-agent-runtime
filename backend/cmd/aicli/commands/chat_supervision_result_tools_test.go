package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// P0-4 改动 1/2 的 CLI 宿主验收：include_results 从宿主 batch store 取数，
// read_agent_result 按 batch task → mailbox completion payload 的优先级读取。
// HTTP 宿主的同契约用例在 internal/api/runtimeapi/supervision_tool_controller_test.go。
//
// 两个宿主都调用 supervision.BuildReadResultPayload 渲染同一条
// runtimeserver.SupervisionResultSource 记录，因此宿主间差异只可能是
// scope/wiring 差异；这里钉住 CLI 侧的 wiring（host.SubagentBatches）。

func seedCLIBatchTaskResult(t *testing.T, store subagentbatch.BatchStore, batchID, parentSessionID, taskID, childSessionID string, capsule *subagentbatch.TaskResult, status subagentbatch.TaskStatus) {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateBatch(ctx, &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}, []subagentbatch.SubagentTaskRecord{{
		TaskID:         taskID,
		BatchID:        batchID,
		ChildSessionID: childSessionID,
		Role:           "worker",
		Status:         subagentbatch.TaskRunning,
		TaskDeadline:   time.Now().UTC().Add(time.Minute),
	}})
	require.True(t, created)
	task, err := store.GetTask(ctx, batchID, taskID)
	require.NoError(t, err)
	require.NoError(t, store.RecordTaskResult(ctx, batchID, taskID, task.Version, status, capsule))
}

func TestLocalSupervisionToolController_ReadAgentResultUsesHostBatchStore(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.SubagentBatches = newTestSubagentBatchStore(t)
	seedCLIBatchTaskResult(t, host.SubagentBatches, "batch-read-1", "parent-session", "task-1", "child-read-1", &subagentbatch.TaskResult{
		TaskID:      "task-1",
		SessionID:   "child-read-1",
		Success:     false,
		Summary:     "2 tests failed",
		Findings:    []string{"finding one"},
		Error:       "go test failed",
		ArtifactRef: "artifact://cli",
	}, subagentbatch.TaskFailed)
	// A second parent's batch must never be readable from this scope.
	seedCLIBatchTaskResult(t, host.SubagentBatches, "batch-foreign", "other-parent", "task-9", "child-foreign", &subagentbatch.TaskResult{
		TaskID: "task-9", SessionID: "child-foreign", Summary: "not yours",
	}, subagentbatch.TaskSucceeded)

	session := newChatDebugSupervisionSession(host, "parent-session")
	controller := newLocalSupervisionToolController(host, session)
	require.NotNil(t, controller)

	payload, err := controller.ReadAgentResult(context.Background(), "parent-session", toolbroker.ReadAgentResultArgs{SessionID: "child-read-1"})
	require.NoError(t, err)
	require.Equal(t, supervision.ResultSourceTaskResult, payload.Source)
	require.Equal(t, "failed", payload.Status)
	require.Equal(t, "2 tests failed", payload.Summary)
	require.Equal(t, []string{"finding one"}, payload.Findings)
	require.Contains(t, payload.Artifacts, "artifact://cli")
	require.Len(t, payload.Errors, 1)
	require.Equal(t, "go test failed", payload.Errors[0].Message)

	// task_id narrows the read.
	byTask, err := controller.ReadAgentResult(context.Background(), "parent-session", toolbroker.ReadAgentResultArgs{SessionID: "child-read-1", TaskID: "task-1"})
	require.NoError(t, err)
	require.Equal(t, supervision.ResultSourceTaskResult, byTask.Source)
	require.Equal(t, "task-1", byTask.TaskID)

	// Sections selection is enforced by the shared builder.
	summaryOnly, err := controller.ReadAgentResult(context.Background(), "parent-session", toolbroker.ReadAgentResultArgs{
		SessionID: "child-read-1",
		Sections:  []string{supervision.ReadResultSectionSummary},
	})
	require.NoError(t, err)
	require.NotEmpty(t, summaryOnly.Summary)
	require.Empty(t, summaryOnly.Findings)

	// The scope cannot be widened: another parent's child is not readable.
	foreign, err := controller.ReadAgentResult(context.Background(), "parent-session", toolbroker.ReadAgentResultArgs{SessionID: "child-foreign"})
	require.NoError(t, err)
	require.Equal(t, supervision.ResultSourceNone, foreign.Source)
	require.Equal(t, "no_result_recorded", foreign.ErrorCode)
	require.Contains(t, foreign.NextAction, "read_agent_events")

	missing, err := controller.ReadAgentResult(context.Background(), "parent-session", toolbroker.ReadAgentResultArgs{SessionID: "child-missing"})
	require.NoError(t, err)
	require.Equal(t, supervision.ResultSourceNone, missing.Source)
	require.Equal(t, "no_result_recorded", missing.ErrorCode)
	require.Contains(t, missing.NextAction, "wait_agent")
	require.Equal(t, "child-missing", missing.SessionID)
}

// TestLocalSupervisionToolController_ReadAgentResultFallsBackToMailbox pins the
// completion-payload fallback on the CLI host: a single spawn_agent child that
// only produced a terminal mailbox row is still readable.
func TestLocalSupervisionToolController_ReadAgentResultFallsBackToMailbox(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	// The completion dispatcher appends to the session AgentControl mailbox; the
	// fallback reader must observe the same store.
	eventStore := runtimechat.NewInMemoryRuntimeStore(256)
	host.EventStore = eventStore
	session := newChatDebugSupervisionSession(host, "parent-session")
	message := toolbroker.BuildSubagentCompletionMailboxMessage(
		"parent-session", "child-mail-1", "/root/child-mail-1", "worker", "session.end",
		map[string]interface{}{"status": "failed", "success": false, "error": "exit code 1", "usage_total_tokens": 42},
	)
	_, _, err := runtimechat.SessionEventMailboxStore{Events: eventStore}.AppendAgentControlMailbox(context.Background(), "parent-session", message)
	require.NoError(t, err)

	controller := newLocalSupervisionToolController(host, session)
	payload, err := controller.ReadAgentResult(context.Background(), "parent-session", toolbroker.ReadAgentResultArgs{SessionID: "child-mail-1"})
	require.NoError(t, err)
	require.Equal(t, supervision.ResultSourceCompletionPayload, payload.Source)
	require.Equal(t, "failed", payload.Status)
	require.Equal(t, "exit code 1", payload.Summary)
	require.Len(t, payload.Errors, 1)
	require.NotNil(t, payload.Usage)
	require.Equal(t, 42, payload.Usage.TotalTokens)

	// Another parent session's mailbox is never in scope.
	foreign, err := controller.ReadAgentResult(context.Background(), "other-parent", toolbroker.ReadAgentResultArgs{SessionID: "child-mail-1"})
	require.NoError(t, err)
	require.Equal(t, supervision.ResultSourceNone, foreign.Source)
	require.Equal(t, "no_result_recorded", foreign.ErrorCode)
}

func TestLocalSupervisionToolController_DescendantsIncludeResultsFromHostBatchStore(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.SubagentBatches = newTestSubagentBatchStore(t)
	seedCLIBatchTaskResult(t, host.SubagentBatches, "batch-results-1", "parent-session", "task-1", "worker-1", &subagentbatch.TaskResult{
		TaskID:      "task-1",
		SessionID:   "worker-1",
		Success:     true,
		Summary:     "everything green",
		ArtifactRef: "artifact://worker-1",
	}, subagentbatch.TaskSucceeded)

	provider := &recordingDescendantProvider{states: []supervision.DescendantState{
		{Kind: supervision.SubjectAgentSession, ID: "worker-1", ExecutionStatus: "succeeded", SupervisionState: supervision.SupervisionTerminated},
	}}
	host.Supervision.Provider = provider
	session := newChatDebugSupervisionSession(host, "parent-session")
	controller := newLocalSupervisionToolController(host, session)

	// Default: the legacy row payload (no result fields, provider untouched by
	// the result decoration).
	plain, err := controller.SupervisionDescendants(context.Background(), "parent-session", toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.Len(t, plain.Descendants, 1)
	require.Empty(t, plain.Descendants[0].ResultStatus)
	require.Empty(t, plain.Descendants[0].ResultSummary)

	withResults, err := controller.SupervisionDescendants(context.Background(), "parent-session", toolbroker.SupervisionDescendantsArgs{
		IncludeResults: true,
	})
	require.NoError(t, err)
	require.Len(t, withResults.Descendants, 1)
	row := withResults.Descendants[0]
	require.Equal(t, "succeeded", row.ResultStatus)
	require.Equal(t, "everything green", row.ResultSummary)
	require.Contains(t, row.ArtifactRefs, "artifact://worker-1")
}
