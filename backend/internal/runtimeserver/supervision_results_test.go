package runtimeserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// P0-4 改动 1/2 的 provider 读通道：batch task → execution run → mailbox
// completion payload，默认路径不改变。

type staticDescendantProvider struct {
	states []supervision.DescendantState
}

func (p *staticDescendantProvider) ListDescendants(_ context.Context, _ supervision.Scope) ([]supervision.DescendantState, error) {
	return p.states, nil
}

type fakeMailboxReader struct {
	messages []team.MailMessage
	err      error
	sessions []string
}

func (f *fakeMailboxReader) ListAgentControlMailbox(_ context.Context, sessionID string, _ int64, _ int) ([]team.MailMessage, error) {
	f.sessions = append(f.sessions, sessionID)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]team.MailMessage, 0, len(f.messages))
	for _, message := range f.messages {
		if parent := mailboxString(message.Metadata, "parent_session_id"); parent != "" && !strings.EqualFold(parent, sessionID) {
			continue
		}
		out = append(out, message)
	}
	return out, nil
}

func newResultTestBatchStore(t *testing.T) subagentbatch.BatchStore {
	t.Helper()
	store, err := subagentbatch.NewSQLiteBatchStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedResultBatch writes one terminal task with the durable capsule shape the
// coordinator produces (TaskResult JSON in ResultSummary + ArtifactRef +
// ErrorClass + FinishedAt).
func seedResultBatch(t *testing.T, store subagentbatch.BatchStore, batchID, parentSessionID, taskID, childSessionID string, status subagentbatch.TaskStatus, capsule *subagentbatch.TaskResult, artifactRef, errorClass string) {
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
	// The operator-visible artifact/error columns are set by the coordinator
	// alongside RecordTaskResult; mirror them for the reader test.
	_, err = store.UpdateTask(ctx, batchID, taskID, task.Version+1, func(task *subagentbatch.SubagentTaskRecord) {
		task.ArtifactRef = artifactRef
		task.ErrorClass = errorClass
	})
	require.NoError(t, err)
}

func TestSupervisionResultSourceLoadsBatchTaskResult(t *testing.T) {
	store := newResultTestBatchStore(t)
	capsule := &subagentbatch.TaskResult{
		TaskID:      "task-1",
		SessionID:   "child-1",
		Success:     false,
		Summary:     "2 tests failed",
		Findings:    []string{"finding one", "finding two"},
		Error:       "go test failed",
		UsageTotal:  1200,
		ArtifactRef: "artifact://capsule",
		Patches: []subagentbatch.PatchSpec{
			{Path: "backend/x.go", Summary: "fix", ApplyStatus: "applied", ArtifactRefs: []string{"artifact://patch"}},
		},
	}
	seedResultBatch(t, store, "batch-1", "parent-1", "task-1", "child-1", subagentbatch.TaskFailed, capsule, "artifact://task", "verification_failed")

	source := NewSupervisionResultSource(store, nil)
	record, found, err := source.LoadAgentResult(context.Background(), supervision.Scope{RootSessionID: "parent-1"}, "child-1", "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, supervision.ResultSourceTaskResult, record.Source)
	require.Equal(t, "task-1", record.TaskID)
	require.Equal(t, "child-1", record.SessionID)
	require.Equal(t, string(agentresult.StatusFailed), record.Status)
	require.Equal(t, "2 tests failed", record.Summary)
	require.Equal(t, []string{"finding one", "finding two"}, record.Findings)
	require.Len(t, record.Changes, 1)
	require.Equal(t, "applied", record.Changes[0].Status)
	require.Equal(t, []string{"artifact://patch"}, record.Changes[0].ArtifactRefs)
	require.Contains(t, record.Artifacts, "artifact://capsule")
	require.Contains(t, record.Artifacts, "artifact://task")
	require.Len(t, record.Errors, 1)
	require.Equal(t, "go test failed", record.Errors[0].Message)
	require.Equal(t, 1200, record.Usage.TotalTokens)

	// task_id narrows the same record; an unknown target is no_result_recorded.
	byTask, found, err := source.LoadAgentResult(context.Background(), supervision.Scope{RootSessionID: "parent-1"}, "child-1", "task-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "task-1", byTask.TaskID)
	_, found, err = source.LoadAgentResult(context.Background(), supervision.Scope{RootSessionID: "parent-1"}, "child-unknown", "")
	require.NoError(t, err)
	require.False(t, found)
	// A different parent scope cannot read this batch (model cannot widen scope).
	_, found, err = source.LoadAgentResult(context.Background(), supervision.Scope{RootSessionID: "other-parent"}, "child-1", "")
	require.NoError(t, err)
	require.False(t, found)
}

// TestSupervisionResultSourceProjectsAgentResultCapsule pins the mapping
// contract (plan §3.4 改动 3): a ResultSummary containing an agentresult.Result
// JSON projects to summary/findings/changes(status,artifact_refs)/errors/usage.
func TestSupervisionResultSourceProjectsAgentResultCapsule(t *testing.T) {
	store := newResultTestBatchStore(t)
	contract := agentresult.Result{
		Status:  agentresult.StatusFailed,
		Summary: "contract summary",
		Findings: []agentresult.Finding{
			{ID: "f1", Summary: "contract finding"},
		},
		Changes: []agentresult.Change{
			{Path: "backend/y.go", Summary: "change", Status: "applied", ArtifactRefs: []string{"artifact://change"}},
		},
		Artifacts: []agentresult.Artifact{{ID: "a1", URI: "artifact://result"}},
		Errors:    []agentresult.Error{{Code: "TOOL_TIMEOUT", Message: "timed out", Retryable: true}},
		Usage:     agentresult.Usage{TotalTokens: 77, ToolCalls: 4},
	}
	raw, err := json.Marshal(contract)
	require.NoError(t, err)
	// The coordinator writes TaskResult JSON; the reader must also tolerate a
	// result row whose ResultSummary is an agentresult.Result JSON directly.
	seedRawResult(t, store, "batch-3", "parent-3", "task-3", "child-3", raw)

	source := NewSupervisionResultSource(store, nil)
	record, found, err := source.LoadAgentResult(context.Background(), supervision.Scope{RootSessionID: "parent-3"}, "child-3", "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "contract summary", record.Summary)
	require.Equal(t, string(agentresult.StatusFailed), record.Status)
	require.Equal(t, []string{"contract finding"}, record.Findings)
	require.Len(t, record.Changes, 1)
	require.Equal(t, "applied", record.Changes[0].Status)
	require.Equal(t, []string{"artifact://change"}, record.Changes[0].ArtifactRefs)
	require.Contains(t, record.Artifacts, "artifact://result")
	require.Len(t, record.Errors, 1)
	require.Equal(t, "TOOL_TIMEOUT", record.Errors[0].Code)
	require.True(t, record.Errors[0].Retryable)
	require.Equal(t, 77, record.Usage.TotalTokens)
}

// seedRawResult writes ResultSummary verbatim so the agentresult.Result
// projection path is exercised (the coordinator writes TaskResult JSON, but the
// reader tolerates both shapes per plan §3.4 改动 3).
func seedRawResult(t *testing.T, store subagentbatch.BatchStore, batchID, parentSessionID, taskID, childSessionID string, raw []byte) {
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
		Status:         subagentbatch.TaskRunning,
		TaskDeadline:   time.Now().UTC().Add(time.Minute),
	}})
	require.True(t, created)
	_, err = store.UpdateTask(ctx, batchID, taskID, -1, func(task *subagentbatch.SubagentTaskRecord) {
		task.ResultSummary = raw
		task.Status = subagentbatch.TaskFailed
	})
	require.NoError(t, err)
}

func TestSupervisionResultSourceFallsBackToMailboxCompletion(t *testing.T) {
	mailboxes := &fakeMailboxReader{messages: []team.MailMessage{{
		ID:        "delivery-1",
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      agentcontrol.MailboxKindSubagentCompleted,
		Body:      "Subagent child-1 completed with status failed.",
		Metadata: map[string]interface{}{
			"session_id":         "child-1",
			"parent_session_id":  "parent-1",
			"status":             "failed",
			"success":            false,
			"error":              "exit code 1",
			"usage_total_tokens": 321,
		},
		CreatedAt: time.Now().UTC(),
	}}}
	source := NewSupervisionResultSource(nil, mailboxes)

	record, found, err := source.LoadAgentResult(context.Background(), supervision.Scope{RootSessionID: "parent-1"}, "child-1", "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, supervision.ResultSourceCompletionPayload, record.Source)
	require.Equal(t, "failed", record.Status)
	require.Equal(t, "exit code 1", record.Summary)
	require.Len(t, record.Errors, 1)
	require.Equal(t, 321, record.Usage.TotalTokens)

	// Another parent's scope must not surface this completion payload.
	_, found, err = source.LoadAgentResult(context.Background(), supervision.Scope{RootSessionID: "other-parent"}, "child-1", "")
	require.NoError(t, err)
	require.False(t, found)

	results, err := source.ListDescendantResults(context.Background(), supervision.Scope{RootSessionID: "parent-1"})
	require.NoError(t, err)
	require.Contains(t, results, "child-1")
	require.Equal(t, "failed", results["child-1"].Status)
}

// TestSupervisionResultSourceUsesCapsuleSessionIDWhenTaskColumnEmpty covers the
// production mapping: the batch coordinator never writes
// SubagentTaskRecord.ChildSessionID, the child session id lives inside the
// TaskResult capsule (SubagentResult.SessionID → TaskResult.SessionID). Both
// read entry points must resolve it.
func TestSupervisionResultSourceUsesCapsuleSessionIDWhenTaskColumnEmpty(t *testing.T) {
	store := newResultTestBatchStore(t)
	seedResultBatch(t, store, "batch-capsule", "parent-capsule", "task-capsule", "", subagentbatch.TaskSucceeded, &subagentbatch.TaskResult{
		TaskID:    "task-capsule",
		SessionID: "child-from-capsule",
		Success:   true,
		Summary:   "ok",
	}, "artifact://capsule", "")
	source := NewSupervisionResultSource(store, nil)
	ctx := context.Background()

	record, found, err := source.LoadAgentResult(ctx, supervision.Scope{RootSessionID: "parent-capsule"}, "child-from-capsule", "")
	require.NoError(t, err)
	require.True(t, found, "the capsule session id must resolve read_agent_result(id=...)")
	require.Equal(t, "child-from-capsule", record.SessionID)
	require.Equal(t, "ok", record.Summary)

	byTask, found, err := source.LoadAgentResult(ctx, supervision.Scope{RootSessionID: "parent-capsule"}, "", "task-capsule")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "child-from-capsule", byTask.SessionID)

	results, err := source.ListDescendantResults(ctx, supervision.Scope{RootSessionID: "parent-capsule"})
	require.NoError(t, err)
	require.Contains(t, results, "child-from-capsule", "include_results rows are keyed by the resolved child session id")
	require.Equal(t, "ok", results["child-from-capsule"].Summary)
}

func TestDescendantResultProviderIncludeResultsReadPath(t *testing.T) {
	store := newResultTestBatchStore(t)
	seedResultBatch(t, store, "batch-1", "parent-1", "task-1", "child-1", subagentbatch.TaskSucceeded, &subagentbatch.TaskResult{
		TaskID:    "task-1",
		SessionID: "child-1",
		Success:   true,
		Summary:   strings.Repeat("done ", 200),
	}, "artifact://task", "")

	base := &staticDescendantProvider{states: []supervision.DescendantState{
		{Kind: supervision.SubjectAgentSession, ID: "child-1", SupervisionState: supervision.SupervisionTerminated},
		{Kind: supervision.SubjectAgentSession, ID: "child-2", SupervisionState: supervision.SupervisionRunning},
	}}
	provider := NewDescendantResultProvider(base, NewSupervisionResultSource(store, nil), nil, nil)

	// Default read path: no result fields are attached.
	plain, err := provider.ListDescendants(context.Background(), supervision.Scope{RootSessionID: "parent-1"})
	require.NoError(t, err)
	require.Empty(t, plain[0].ResultStatus)

	resultProvider, ok := provider.(supervision.DescendantResultProvider)
	require.True(t, ok)
	rows, err := resultProvider.ListDescendantsWithResults(context.Background(), supervision.Scope{RootSessionID: "parent-1"})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, string(agentresult.StatusSucceeded), rows[0].ResultStatus)
	require.True(t, rows[0].ResultTruncated, "the long summary is bounded for the row")
	require.Len(t, []rune(rows[0].ResultSummary), supervision.MaxSnapshotResultSummaryRunes)
	require.Contains(t, rows[0].ArtifactRefs, "artifact://task")
	require.Empty(t, rows[1].ResultStatus, "a child without a durable result stays empty")
}

func TestDescendantResultProviderResolvesAgentPath(t *testing.T) {
	store := newResultTestBatchStore(t)
	seedResultBatch(t, store, "batch-1", "parent-1", "task-1", "child-session-1", subagentbatch.TaskSucceeded, &subagentbatch.TaskResult{
		TaskID:    "task-1",
		SessionID: "child-session-1",
		Success:   true,
		Summary:   "ok",
	}, "", "")

	agents := &fakeAgentRegistry{records: []agentcontrol.AgentRecord{{
		AgentID:       "agent-1",
		RootSessionID: "parent-1",
		SessionID:     "child-session-1",
		AgentPath:     "/root/worker",
	}}}
	provider := NewDescendantResultProvider(
		&staticDescendantProvider{},
		NewSupervisionResultSource(store, nil),
		nil,
		agents,
	)
	source, ok := provider.(supervision.ResultSource)
	require.True(t, ok)

	record, found, err := source.LoadAgentResult(context.Background(), supervision.Scope{RootSessionID: "parent-1"}, "/root/worker", "")
	require.NoError(t, err)
	require.True(t, found, "an agent path resolves to the durable child session inside the scope")
	require.Equal(t, "child-session-1", record.SessionID)
}

type fakeAgentRegistry struct {
	records []agentcontrol.AgentRecord
	err     error
}

func (f *fakeAgentRegistry) ListAgentControlAgents(_ context.Context, filter agentcontrol.AgentFilter) ([]agentcontrol.AgentRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]agentcontrol.AgentRecord, 0, len(f.records))
	for _, record := range f.records {
		if filter.RootSessionID != "" && !strings.EqualFold(record.RootSessionID, filter.RootSessionID) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}
