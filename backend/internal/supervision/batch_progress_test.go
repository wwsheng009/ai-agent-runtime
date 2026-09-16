package supervision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

// fakeBatchStore embeds the interface so only the read methods used by the P0-B
// projection need an implementation.
type fakeBatchStore struct {
	subagentbatch.BatchStore
	batches   []subagentbatch.SubagentBatch
	tasks     map[string][]subagentbatch.SubagentTaskRecord
	listErr   error
	taskErr   error
	filters   []subagentbatch.BatchFilter
	taskCalls []string
}

func (f *fakeBatchStore) ListBatches(_ context.Context, filter subagentbatch.BatchFilter) ([]subagentbatch.SubagentBatch, error) {
	f.filters = append(f.filters, filter)
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]subagentbatch.SubagentBatch, 0, len(f.batches))
	for _, batch := range f.batches {
		if filter.ParentSessionID != "" && batch.ParentSessionID != filter.ParentSessionID {
			continue
		}
		if len(filter.Status) > 0 && !containsBatchStatus(filter.Status, batch.Status) {
			continue
		}
		if len(filter.ExecutionMode) > 0 && !containsExecutionMode(filter.ExecutionMode, batch.ExecutionMode) {
			continue
		}
		out = append(out, batch)
	}
	return out, nil
}

func (f *fakeBatchStore) ListTasks(_ context.Context, batchID string) ([]subagentbatch.SubagentTaskRecord, error) {
	f.taskCalls = append(f.taskCalls, batchID)
	if f.taskErr != nil {
		return nil, f.taskErr
	}
	return f.tasks[batchID], nil
}

func containsBatchStatus(values []subagentbatch.BatchStatus, want subagentbatch.BatchStatus) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsExecutionMode(values []subagentbatch.ExecutionMode, want subagentbatch.ExecutionMode) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type fakeMessageProvider struct {
	messages map[string]string
}

func (f fakeMessageProvider) LastProgressMessage(childSessionID string) string {
	return f.messages[childSessionID]
}

// P0-B acceptance: an active background batch reads as "2/3 completed, worker-3
// still running with progress 12s ago" — derived from the durable batch control
// plane alone, no new table and no wake.
func TestBatchProgressSource_ActiveBatch(t *testing.T) {
	now := time.Now().UTC()
	lastProgress := now.Add(-12 * time.Second)
	store := &fakeBatchStore{
		batches: []subagentbatch.SubagentBatch{{
			BatchID:         "batch-b-123",
			ParentSessionID: "root-session-1",
			RootScopeID:     "root-session-1",
			ExecutionMode:   subagentbatch.ExecutionModeBackground,
			Status:          subagentbatch.BatchRunning,
			TaskCount:       3,
			CompletedCount:  2,
			RunningCount:    1,
			HeartbeatAt:     lastProgress,
			UpdatedAt:       lastProgress,
		}},
		tasks: map[string][]subagentbatch.SubagentTaskRecord{
			"batch-b-123": {
				{TaskID: "worker-1", BatchID: "batch-b-123", ChildSessionID: "session-1", Status: subagentbatch.TaskSucceeded},
				{TaskID: "worker-2", BatchID: "batch-b-123", ChildSessionID: "session-2", Status: subagentbatch.TaskSucceeded},
				{TaskID: "worker-3", BatchID: "batch-b-123", ChildSessionID: "session-3", Status: subagentbatch.TaskRunning, LastProgressAt: &lastProgress},
			},
		},
	}
	source := &BatchProgressSource{
		Store:    store,
		Messages: fakeMessageProvider{messages: map[string]string{"session-3": "bash: go test ./internal/supervision/"}},
	}

	groups, err := source.ListProgress(context.Background(), ProgressRequest{ParentSessionID: "root-session-1", RootScopeID: "root-session-1"})
	require.NoError(t, err)
	require.Len(t, groups, 1)

	group := groups[0]
	require.Equal(t, "batch-b-123", group.GroupID)
	require.Equal(t, 3, group.Total)
	require.Equal(t, 2, group.Completed)
	require.Equal(t, 1, group.Running)
	require.Equal(t, 0, group.Failed)
	require.False(t, group.Terminal)
	require.Len(t, group.RunningTasks, 1, "only non-terminal tasks are listed")
	require.Equal(t, "worker-3", group.RunningTasks[0].TaskID)
	require.Equal(t, "session-3", group.RunningTasks[0].ChildSessionID)
	require.Equal(t, "running", group.RunningTasks[0].State)
	require.Equal(t, "bash: go test ./internal/supervision/", group.RunningTasks[0].LastMessage)

	require.Len(t, store.filters, 2, "active and recently-finished reads stay separate")
	require.Equal(t, "root-session-1", store.filters[0].ParentSessionID)
	require.Equal(t, activeBatchStatuses, store.filters[0].Status)
	require.Equal(t, []string{"batch-b-123"}, store.taskCalls)

	// End to end through the digest: the parent turn text carries the rollup even
	// though no lifecycle notification exists (that is the P0-B point — normal
	// progress used to be invisible).
	digest, err := BuildDigest(context.Background(), testDigestStore(t, "supervision-batch-progress"), DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		Progress:              source,
	})
	require.NoError(t, err)
	require.Contains(t, digest.Text, "batch-b-123: 2/3 completed, 1 running")
	require.Contains(t, digest.Text, "worker-3: running; session=session-3; last progress 12s ago; bash: go test ./internal/supervision/")
}

func TestBatchProgressSource_KeepsRecentTerminalButDropsOldOnes(t *testing.T) {
	now := time.Now().UTC()
	finishedRecent := now.Add(-2 * time.Minute)
	finishedOld := now.Add(-90 * time.Minute)
	store := &fakeBatchStore{
		batches: []subagentbatch.SubagentBatch{
			{
				BatchID:         "batch-recent",
				ParentSessionID: "root-session-1",
				ExecutionMode:   subagentbatch.ExecutionModeBackground,
				Status:          subagentbatch.BatchCompleted,
				TaskCount:       3,
				CompletedCount:  3,
				FinishedAt:      &finishedRecent,
				UpdatedAt:       finishedRecent,
			},
			{
				BatchID:         "batch-old",
				ParentSessionID: "root-session-1",
				ExecutionMode:   subagentbatch.ExecutionModeBackground,
				Status:          subagentbatch.BatchCompleted,
				TaskCount:       2,
				CompletedCount:  2,
				FinishedAt:      &finishedOld,
				UpdatedAt:       finishedOld,
			},
			{
				BatchID:         "batch-wait",
				ParentSessionID: "root-session-1",
				ExecutionMode:   subagentbatch.ExecutionModeWait,
				Status:          subagentbatch.BatchCompleted,
				TaskCount:       1,
				CompletedCount:  1,
				FinishedAt:      &finishedRecent,
				UpdatedAt:       finishedRecent,
			},
			{
				BatchID:         "batch-other-parent",
				ParentSessionID: "some-other-session",
				ExecutionMode:   subagentbatch.ExecutionModeBackground,
				Status:          subagentbatch.BatchRunning,
				TaskCount:       1,
				RunningCount:    1,
				UpdatedAt:       now,
			},
		},
	}
	source := &BatchProgressSource{Store: store}

	groups, err := source.ListProgress(context.Background(), ProgressRequest{ParentSessionID: "root-session-1"})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, "batch-recent", groups[0].GroupID)
	require.True(t, groups[0].Terminal)
	require.Equal(t, 3, groups[0].Completed)
	require.Empty(t, store.taskCalls, "terminal batches are not expanded task-by-task")

	// The wait-mode batch is synchronous by contract: it is never a patrol
	// subject, so the rollup must not list it (scope discipline).
	for _, group := range groups {
		require.NotEqual(t, "batch-wait", group.GroupID)
		require.NotEqual(t, "batch-old", group.GroupID)
		require.NotEqual(t, "batch-other-parent", group.GroupID)
	}
}

func TestBatchProgressSource_ReadFailuresDegradeGracefully(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeBatchStore{
		batches: []subagentbatch.SubagentBatch{{
			BatchID:         "batch-b-1",
			ParentSessionID: "root-session-1",
			Status:          subagentbatch.BatchRunning,
			TaskCount:       2,
			RunningCount:    2,
			UpdatedAt:       now,
		}},
		taskErr: errors.New("tasks unavailable"),
	}
	source := &BatchProgressSource{Store: store}

	groups, err := source.ListProgress(context.Background(), ProgressRequest{ParentSessionID: "root-session-1"})
	require.NoError(t, err, "a task-detail failure must not fail the projection")
	require.Len(t, groups, 1)
	require.Equal(t, 2, groups[0].Running, "batch counters survive the degraded read")
	require.Empty(t, groups[0].RunningTasks)

	store.listErr = errors.New("store unavailable")
	_, err = source.ListProgress(context.Background(), ProgressRequest{ParentSessionID: "root-session-1"})
	require.Error(t, err, "a batch read failure surfaces to the caller (digest stays best-effort)")
}

func TestBatchProgressSource_RequiresScope(t *testing.T) {
	require.Nil(t, NewBatchProgressSource(nil), "an unwired host gets a nil source, not a dangling one")

	source := NewBatchProgressSource(&fakeBatchStore{})
	require.NotNil(t, source)
	groups, err := source.ListProgress(context.Background(), ProgressRequest{})
	require.NoError(t, err)
	require.Nil(t, groups, "no parent session means no scope to project")
}

func TestMirrorProgressMessages_ReadsLatestStamp(t *testing.T) {
	mirror := NewSubagentProgressMirror(time.Second)
	now := time.Now().UTC()

	event := runtimeevents.Event{
		Type:    toolprotocol.EventTypeProgress,
		TraceID: "trace-1",
		Payload: map[string]interface{}{
			"tool_call_id": "call-1",
			"tool_name":    "bash",
			"kind":         "progress",
			"message":      "npm test",
		},
	}
	_, ok := mirror.Observe(SubagentProgressTarget{ParentSessionID: "root-session-1", ChildSessionID: "session-3"}, event, now.Add(-3*time.Second))
	require.True(t, ok)

	// A second, newer tool call must win: the rollup shows what the child is
	// doing now, not the first thing it ever ran.
	newer := event
	newer.Payload = map[string]interface{}{"tool_call_id": "call-2", "tool_name": "edit", "kind": "progress", "message": "apply_patch"}
	_, ok = mirror.Observe(SubagentProgressTarget{ParentSessionID: "root-session-1", ChildSessionID: "session-3"}, newer, now)
	require.True(t, ok)

	state, message, at, ok := mirror.Latest("session-3")
	require.True(t, ok)
	require.Equal(t, "progress", state)
	require.Equal(t, "apply_patch", message)
	require.WithinDuration(t, now, at, time.Second)

	provider := MirrorProgressMessages{Mirror: mirror}
	require.Equal(t, "apply_patch", provider.LastProgressMessage("session-3"))
	require.Empty(t, provider.LastProgressMessage("session-unknown"))
	require.Empty(t, MirrorProgressMessages{}.LastProgressMessage("session-3"))

	_, _, _, ok = mirror.Latest("")
	require.False(t, ok)
}
