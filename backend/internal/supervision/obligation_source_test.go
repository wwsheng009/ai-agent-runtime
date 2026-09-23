package supervision

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestBatchObligationSource_MapsBatchAndTaskState pins the §16.4 mapping: the
// batch vocabulary becomes the obligation vocabulary, partially_completed stays
// non-terminal, and task-level evidence only enters the terminal rollup.
func TestBatchObligationSource_MapsBatchAndTaskState(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeBatchStore{
		batches: []subagentbatch.SubagentBatch{
			{
				BatchID: "batch-running", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
				Status: subagentbatch.BatchRunning, TaskCount: 3, RunningCount: 1, CompletedCount: 1, FailedCount: 1,
				HeartbeatAt: now, ResultSummaryRef: "art-batch-1", ResultSummary: []byte("partial progress"),
			},
			{
				BatchID: "batch-partial", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
				Status: subagentbatch.BatchPartiallyCompleted, TaskCount: 4, CompletedCount: 2, CanceledCount: 1,
			},
			{
				BatchID: "batch-done", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
				Status: subagentbatch.BatchCompleted, TaskCount: 1, CompletedCount: 1, ResultSummaryRef: "art-batch-3",
			},
			{
				BatchID: "batch-canceled", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
				Status: subagentbatch.BatchCanceled, TaskCount: 2, CanceledCount: 2,
			},
			{
				BatchID: "batch-other", ParentSessionID: "root-session-2",
				Status: subagentbatch.BatchFailed, TaskCount: 1, FailedCount: 1,
			},
		},
		tasks: map[string][]subagentbatch.SubagentTaskRecord{
			"batch-running": {
				{TaskID: "t1", Status: subagentbatch.TaskSucceeded, ArtifactRef: "art-t1"},
				{TaskID: "t2", Status: subagentbatch.TaskFailed, ErrorClass: "tool_error", ErrorCode: "exit_status", ResultSummary: []byte("boom")},
				{TaskID: "t3", Status: subagentbatch.TaskRunning},
			},
			"batch-canceled": {
				{TaskID: "t9", Status: subagentbatch.TaskCanceled},
				{TaskID: "t10", Status: subagentbatch.TaskSkipped},
			},
			"batch-done": {
				{TaskID: "t8", Status: subagentbatch.TaskSucceeded, ArtifactRef: "art-t8"},
			},
		},
	}
	source := &BatchObligationSource{Store: store}

	rows, err := source.ListObligations(context.Background(), " root-session-1 ")
	require.NoError(t, err)
	require.Len(t, rows, 4, "the projection is scoped to the parent session and keeps newest-first order")
	require.Equal(t, []string{"batch-running", "batch-partial", "batch-done", "batch-canceled"}, []string{rows[0].ID, rows[1].ID, rows[2].ID, rows[3].ID})
	require.Len(t, store.filters, 1)
	require.Equal(t, "root-session-1", store.filters[0].ParentSessionID, "the session id is trimmed before querying")
	require.Equal(t, defaultObligationMaxBatches, store.filters[0].Limit)

	running := rows[0]
	require.Equal(t, "batch", running.Kind)
	require.Equal(t, "turn-1", running.ParentTurnID, "G6: the dispatch turn is carried for I3 re-anchoring")
	require.Equal(t, ObligationStateRunning, running.State)
	require.False(t, running.Terminal)
	require.Equal(t, 3, running.Total)
	require.Equal(t, 1, running.Completed)
	require.Equal(t, 1, running.Failed)
	require.Equal(t, "partial progress", running.ResultSummary)
	require.Equal(t, []string{"art-batch-1"}, running.ArtifactRefs)
	require.Empty(t, running.Failures, "a live batch must not freeze a partial failure list")

	partial := rows[1]
	require.Equal(t, ObligationStateRunning, partial.State, "partially_completed is deliberately not terminal")
	require.False(t, partial.Terminal)
	require.Equal(t, 2, partial.Failed, "a partial batch counts its remainder so the rollup is not silently green")
	require.Equal(t, 0, partial.Skipped, "a live row reports counts only; task-level detail arrives at terminal time")

	done := rows[2]
	require.Equal(t, ObligationStateCompleted, done.State)
	require.True(t, done.Terminal)
	require.Empty(t, done.Failures)
	require.Equal(t, []string{"art-batch-3", "art-t8"}, done.ArtifactRefs, "the batch summary ref and task artifacts stay citable")
	require.Equal(t, []string{"batch-done", "batch-canceled"}, store.taskCalls, "only terminal batches read their task rows (bounded I/O)")

	canceled := rows[3]
	require.Equal(t, ObligationStateCanceled, canceled.State)
	require.True(t, canceled.Terminal)
	require.Equal(t, 3, canceled.Skipped, "the canceled batch count plus its skipped task")
	require.Len(t, canceled.Failures, 1, "a canceled task is reported as a non-retryable item")
	require.Equal(t, ObligationStateCanceled, canceled.Failures[0].State)
	require.False(t, canceled.Failures[0].Retryable)
}

// TestBatchObligationSource_ClassifiesFailures pins H3: every terminal failure
// carries the error class/code and the retry verdict the parent's next control
// action needs.
func TestBatchObligationSource_ClassifiesFailures(t *testing.T) {
	store := &fakeBatchStore{
		batches: []subagentbatch.SubagentBatch{
			{
				BatchID: "batch-failed", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
				Status: subagentbatch.BatchFailed, TaskCount: 2, CompletedCount: 0, FailedCount: 1,
				ErrorClass: "timeout", ErrorDetail: "batch exceeded its deadline", ResultSummaryRef: "art-batch-f1",
			},
			{
				BatchID: "batch-policy", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
				Status: subagentbatch.BatchFailed, TaskCount: 1, FailedCount: 1,
			},
		},
		tasks: map[string][]subagentbatch.SubagentTaskRecord{
			"batch-failed": {
				{TaskID: "t1", Status: subagentbatch.TaskFailed, ErrorClass: "network", ErrorCode: "connection_reset", ArtifactRef: "art-t1"},
				{TaskID: "t2", Status: subagentbatch.TaskCanceled},
			},
			"batch-policy": {
				{TaskID: "t3", Status: subagentbatch.TaskFailed, ErrorClass: "approval_denied", ErrorCode: "policy_conflict"},
			},
		},
	}
	rows, err := (&BatchObligationSource{Store: store}).ListObligations(context.Background(), "root-session-1")
	require.NoError(t, err)
	require.Len(t, rows, 2)

	failed := rows[0]
	require.True(t, failed.Terminal)
	require.Len(t, failed.Failures, 3, "the batch-level error and both task rows are reported")
	require.Equal(t, "timeout", failed.Failures[0].ErrorClass)
	require.True(t, failed.Failures[0].Retryable, "a deadline failure can be retried")
	require.Equal(t, "art-batch-f1", failed.Failures[0].ResultRef)
	require.Equal(t, "art-batch-f1", failed.ArtifactRefs[0])

	task := failed.Failures[1]
	require.Equal(t, "t1", task.TaskID)
	require.Equal(t, "network", task.ErrorClass)
	require.Equal(t, "connection_reset", task.ErrorCode)
	require.True(t, task.Retryable)
	require.Equal(t, "art-t1", task.ResultRef)
	require.Equal(t, "batch-failed", task.BatchID)

	require.Equal(t, "t2", failed.Failures[2].TaskID)
	require.False(t, failed.Failures[2].Retryable, "a deliberately canceled task is a new decision, not a retry")

	policy := rows[1]
	require.Len(t, policy.Failures, 1)
	require.Equal(t, "t3", policy.Failures[0].TaskID)
	require.False(t, policy.Failures[0].Retryable, "a policy conflict is structural: repeating the same work cannot converge it")
	require.Equal(t, "batch:batch-policy/task:t3", policy.Failures[0].ResultRef, "a missing artifact falls back to a durable reference")
}

// TestBatchObligationSource_BoundsReadsAndPreviews pins I7: the projection is
// bounded per read, per task list and per inline preview.
func TestBatchObligationSource_BoundsReadsAndPreviews(t *testing.T) {
	tasks := make([]subagentbatch.SubagentTaskRecord, 0, 6)
	for i := 0; i < 6; i++ {
		tasks = append(tasks, subagentbatch.SubagentTaskRecord{
			TaskID:        "task-" + string(rune('a'+i)),
			Status:        subagentbatch.TaskFailed,
			ErrorClass:    "tool_error",
			ResultSummary: []byte(strings.Repeat("verbose child output ", 20)),
		})
	}
	store := &fakeBatchStore{
		batches: []subagentbatch.SubagentBatch{
			{
				BatchID: "batch-1", ParentSessionID: "root-session-1", Status: subagentbatch.BatchFailed,
				TaskCount: 6, FailedCount: 6,
				ResultSummary: []byte("line one\n\n  line two   line three"),
			},
			{BatchID: "batch-2", ParentSessionID: "root-session-1", Status: subagentbatch.BatchFailed, TaskCount: 1, FailedCount: 1},
		},
		tasks: map[string][]subagentbatch.SubagentTaskRecord{"batch-1": tasks},
	}
	source := &BatchObligationSource{Store: store, MaxBatches: 1, MaxTasksPerBatch: 2, MaxPreviewRunes: 24}

	rows, err := source.ListObligations(context.Background(), "root-session-1")
	require.NoError(t, err)
	require.Len(t, rows, 1, "MaxBatches bounds the per-read page")
	require.Equal(t, 1, store.filters[0].Limit)

	row := rows[0]
	preview := func(text string) string { return string([]rune(text)[:24]) + "…" }
	require.Len(t, row.Failures, 2, "MaxTasksPerBatch bounds the failure list")
	require.Equal(t, preview("line one line two line three"), row.ResultSummary, "previews collapse whitespace and cut on a rune boundary")
	require.Equal(t, 25, len([]rune(row.ResultSummary)))
	require.Equal(t, preview(strings.TrimSpace(string(tasks[0].ResultSummary))), row.Failures[0].Summary)
}

// TestBatchObligationSource_DegradesAndPropagates pins the two failure modes:
// a listing error is returned (the resume builder degrades), while a task-level
// read error only loses the richer evidence.
func TestBatchObligationSource_DegradesAndPropagates(t *testing.T) {
	ctx := context.Background()

	listErr := errors.New("batch store offline")
	source := &BatchObligationSource{Store: &fakeBatchStore{listErr: listErr}}
	rows, err := source.ListObligations(ctx, "root-session-1")
	require.ErrorIs(t, err, listErr)
	require.Empty(t, rows)

	store := &fakeBatchStore{
		batches: []subagentbatch.SubagentBatch{{
			BatchID: "batch-1", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
			Status: subagentbatch.BatchFailed, TaskCount: 2, FailedCount: 1,
			ErrorClass: "timeout", ErrorDetail: "batch exceeded its deadline",
		}},
		taskErr: errors.New("task rows unavailable"),
	}
	rows, err = (&BatchObligationSource{Store: store}).ListObligations(ctx, "root-session-1")
	require.NoError(t, err, "a task read failure must not fail the join verdict")
	require.Len(t, rows, 1)
	require.Equal(t, ObligationStateFailed, rows[0].State)
	require.True(t, rows[0].Terminal)
	require.Len(t, rows[0].Failures, 1, "the batch-level error survives the degraded read")
	require.Equal(t, "timeout", rows[0].Failures[0].ErrorClass)

	// Blank batch ids never become obligations; an empty parent scope reads
	// nothing at all.
	blank := &fakeBatchStore{batches: []subagentbatch.SubagentBatch{{BatchID: "  ", ParentSessionID: "root-session-1"}}}
	rows, err = (&BatchObligationSource{Store: blank}).ListObligations(ctx, "root-session-1")
	require.NoError(t, err)
	require.Empty(t, rows)

	empty := &fakeBatchStore{}
	rows, err = (&BatchObligationSource{Store: empty}).ListObligations(ctx, "   ")
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Empty(t, empty.filters, "an empty parent scope must not query the store")
}

// TestBatchObligationSource_FeedsResumeContext is the C2-1 join closure: the
// durable batch control plane alone decides pending_count / can_finalize.
func TestBatchObligationSource_FeedsResumeContext(t *testing.T) {
	store := &fakeBatchStore{
		batches: []subagentbatch.SubagentBatch{
			{
				BatchID: "batch-live", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
				Status: subagentbatch.BatchRunning, TaskCount: 2, CompletedCount: 1,
			},
			{
				BatchID: "batch-done", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
				Status: subagentbatch.BatchPartiallyCompleted, TaskCount: 2, CompletedCount: 1, FailedCount: 1,
			},
		},
	}
	rc := BuildResumeContext(context.Background(), NewBatchObligationSource(store), nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
	})
	require.Equal(t, 2, rc.TotalCount)
	require.Equal(t, 2, rc.PendingCount, "a running batch and a converging partial batch both keep the join open")
	require.False(t, rc.Terminal)
	require.Equal(t, ObligationStateRunning, rc.Status)
	require.Contains(t, rc.Text, "can_finalize=false")
	require.Contains(t, rc.Text, "turn_id: turn-1")

	// Once every batch is terminal the same projection authorizes the final
	// report (pending_count == 0, §16.4).
	store.batches = []subagentbatch.SubagentBatch{{
		BatchID: "batch-done", ParentSessionID: "root-session-1", ParentTurnID: "turn-1",
		Status: subagentbatch.BatchCompleted, TaskCount: 2, CompletedCount: 2,
		ResultSummaryRef: "art-batch-done",
	}}
	rc = BuildResumeContext(context.Background(), NewBatchObligationSource(store), nil, ResumeContextRequest{
		ParentSessionID: "root-session-1",
		TurnID:          "turn-1",
	})
	require.Equal(t, 0, rc.PendingCount)
	require.True(t, rc.Terminal)
	require.Equal(t, ObligationStateCompleted, rc.Status)
	require.Contains(t, AutoWakePromptFor(rc), "所有 obligation 均已终态：请直接产出终局报告")
}
