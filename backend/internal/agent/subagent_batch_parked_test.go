package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

func durableBatchTestStore(t *testing.T) subagentbatch.BatchStore {
	t.Helper()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	require.True(t, store.IsDurable())
	t.Cleanup(func() { _ = store.Close() })
	return store
}

type scriptedBatchExecutor struct {
	mu      sync.Mutex
	scripts [][]SubagentResult
	calls   [][]SubagentTask
}

func (e *scriptedBatchExecutor) RunChildren(_ context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	e.mu.Lock()
	index := len(e.calls)
	e.calls = append(e.calls, append([]SubagentTask(nil), tasks...))
	var results []SubagentResult
	if index < len(e.scripts) {
		results = append([]SubagentResult(nil), e.scripts[index]...)
	}
	e.mu.Unlock()

	for _, task := range tasks {
		options.notifyTaskEvent(task.ID, "started")
		for _, result := range results {
			if result.ID == task.ID && result.SessionID != "" {
				options.notifyTaskBound(task.ID, result.SessionID)
			}
		}
	}
	return results, nil
}

func (e *scriptedBatchExecutor) Calls() [][]SubagentTask {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]SubagentTask, len(e.calls))
	for i := range e.calls {
		out[i] = append([]SubagentTask(nil), e.calls[i]...)
	}
	return out
}

func waitBatchStatus(t *testing.T, store subagentbatch.BatchStore, batchID string, want subagentbatch.BatchStatus) *subagentbatch.SubagentBatch {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		batch, err := store.GetBatch(context.Background(), batchID)
		require.NoError(t, err)
		if batch != nil && batch.Status == want {
			return batch
		}
		time.Sleep(10 * time.Millisecond)
	}
	batch, err := store.GetBatch(context.Background(), batchID)
	require.NoError(t, err)
	t.Fatalf("batch %s did not reach %s; last=%+v", batchID, want, batch)
	return nil
}

func seedObligationBatch(t *testing.T, store subagentbatch.BatchStore, batchID string, status subagentbatch.BatchStatus) *subagentbatch.SubagentBatch {
	t.Helper()
	now := subagentbatch.Now()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     "parent-parked",
		ParentSessionID: "parent-parked",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          status,
		TaskCount:       1,
		CompletedCount:  0,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
	}
	if status.Terminal() {
		batch.CompletedCount = 1
		batch.FinishedAt = &now
	}
	created, err := store.CreateBatch(context.Background(), batch, []subagentbatch.SubagentTaskRecord{{
		TaskID:     batchID + "-task",
		BatchID:    batchID,
		OrderIndex: 0,
		Status:     subagentbatch.TaskSucceeded,
		UpdatedAt:  now,
		Version:    1,
	}})
	require.NoError(t, err)
	require.True(t, created)
	return batch
}

func seedParkedBatch(t *testing.T, store subagentbatch.BatchStore, batchID, parentSessionID, taskID, childSessionID string) {
	t.Helper()
	now := subagentbatch.Now()
	spec, err := json.Marshal(subagentbatch.TaskSpec{ID: taskID, Role: "researcher", Goal: "resume nested work"})
	require.NoError(t, err)
	capsule, err := json.Marshal(subagentbatch.TaskResult{TaskID: taskID, SessionID: childSessionID, Summary: "paused"})
	require.NoError(t, err)
	created, err := store.CreateBatch(context.Background(), &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchParked,
		TaskCount:       1,
		CreatedAt:       now,
		UpdatedAt:       now,
		HeartbeatAt:     now,
	}, []subagentbatch.SubagentTaskRecord{{
		TaskID:         taskID,
		BatchID:        batchID,
		ChildSessionID: childSessionID,
		Role:           "researcher",
		Status:         subagentbatch.TaskParked,
		OrderIndex:     0,
		Spec:           spec,
		ResultSummary:  capsule,
		UpdatedAt:      now,
		Version:        1,
	}})
	require.NoError(t, err)
	require.True(t, created)
}

func TestDurableChildSessionIdentityIsStable(t *testing.T) {
	first := buildDurableSubagentSessionID("batch-1", "task-1")
	second := buildDurableSubagentSessionID("batch-1", "task-1")
	other := buildDurableSubagentSessionID("batch-1", "task-2")
	require.Equal(t, first, second)
	require.NotEqual(t, first, other)
	require.Contains(t, first, "subagent_task-1_batch-1")
}

func TestChildLoopReturnsTurnParkedForOutstandingSuspension(t *testing.T) {
	store := durableBatchTestStore(t)
	obligation := seedObligationBatch(t, store, "nested-running", subagentbatch.BatchRunning)
	childSessionID := buildDurableSubagentSessionID("batch-1", "task-1")
	childTurnID := childTaskTurnID("task-1")
	require.NoError(t, store.ParkTurnSuspension(context.Background(), &subagentbatch.TurnSuspension{
		TurnID:        childTurnID,
		EpisodeID:     childTaskEpisodeID("task-1"),
		SessionID:     childSessionID,
		RootScopeID:   childSessionID,
		ObligationIDs: []string{obligation.BatchID},
		ParkedAt:      subagentbatch.Now(),
	}))

	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name:      "parked-child-provider",
		responses: []*llm.LLMResponse{{Content: "I paused after dispatch.", Model: "test-model"}},
	}
	require.NoError(t, llmRuntime.RegisterProvider(provider.Name(), provider))
	ag := NewAgentWithLLM(&Config{Name: "child", Provider: provider.Name(), Model: "test-model"}, nil, llmRuntime)
	ag.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store}))
	loop := NewReActLoop(ag, llmRuntime, &LoopReActConfig{MaxSteps: 1, EnableToolCalls: false})

	ctx := WithTurnID(context.Background(), childTurnID)
	ctx = WithEpisodeID(ctx, childTaskEpisodeID("task-1"))
	result, err := loop.run(ctx, "continue after dispatch", loopRunOptions{
		TraceID:       "trace-child-park",
		SessionID:     childSessionID,
		IncludePrompt: true,
		Depth:         1,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.TurnParked, "an outstanding required obligation must keep the logical child turn parked")
	_, ok, err := store.GetTurnSuspension(context.Background(), childSessionID, childTurnID)
	require.NoError(t, err)
	require.True(t, ok, "the resume defer must not clear a still-running obligation")
}

func TestParkedChildTaskKeepsBatchNonTerminal(t *testing.T) {
	store := durableBatchTestStore(t)
	exec := &scriptedBatchExecutor{scripts: [][]SubagentResult{{
		{
			ID:         "t1",
			Role:       "researcher",
			SessionID:  "child-t1",
			TurnParked: true,
			TurnID:     childTaskTurnID("t1"),
			EpisodeID:  childTaskEpisodeID("t1"),
			Summary:    "waiting for nested required job",
		},
	}}}
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	c.executor = exec

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		TraceID:         "trace-park",
		ParentSessionID: "parent-parked",
		RootScopeID:     "parent-parked",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "t1", Role: "researcher", Goal: "do parked work"}})
	require.NoError(t, err)

	parked := waitBatchStatus(t, store, batch.BatchID, subagentbatch.BatchParked)
	require.False(t, parked.Status.Terminal())
	require.Nil(t, parked.FinishedAt)

	record, err := store.GetTask(context.Background(), batch.BatchID, "t1")
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, subagentbatch.TaskParked, record.Status)
	require.Equal(t, "child-t1", record.ChildSessionID)
	require.Nil(t, record.FinishedAt, "parked is not a terminal result")

	var capsule subagentbatch.TaskResult
	require.NoError(t, json.Unmarshal(record.ResultSummary, &capsule))
	require.Equal(t, "waiting for nested required job", capsule.Summary)
	require.Equal(t, "child-t1", capsule.SessionID)

	var summary subagentbatch.BatchSummary
	require.NoError(t, json.Unmarshal(parked.ResultSummary, &summary))
	require.Equal(t, subagentbatch.BatchParked, summary.Status)
	require.Equal(t, 1, summary.ParkedCount)
}

func TestParkedBatchSurvivesRestartAndResumesOnlyParkedTask(t *testing.T) {
	store := durableBatchTestStore(t)
	firstExec := &scriptedBatchExecutor{scripts: [][]SubagentResult{{
		{ID: "done", Role: "researcher", SessionID: "child-done", Success: true, Summary: "already complete"},
		{
			ID:         "waiting",
			Role:       "researcher",
			SessionID:  "child-waiting",
			TurnParked: true,
			TurnID:     childTaskTurnID("waiting"),
			EpisodeID:  childTaskEpisodeID("waiting"),
			Summary:    "paused after dispatching nested batch",
		},
	}}}
	first := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	first.executor = firstExec
	batch, err := first.StartBackground(context.Background(), BatchStartOptions{
		TraceID:         "trace-restart",
		ParentSessionID: "parent-parked",
		RootScopeID:     "parent-parked",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{
		{ID: "done", Role: "researcher", Goal: "finish"},
		{ID: "waiting", Role: "researcher", Goal: "wait for nested"},
	})
	require.NoError(t, err)
	waitBatchStatus(t, store, batch.BatchID, subagentbatch.BatchParked)

	obligation := seedObligationBatch(t, store, "nested-batch-finished", subagentbatch.BatchCompleted)
	require.NoError(t, store.ParkTurnSuspension(context.Background(), &subagentbatch.TurnSuspension{
		TurnID:        childTaskTurnID("waiting"),
		EpisodeID:     childTaskEpisodeID("waiting"),
		SessionID:     "child-waiting",
		RootScopeID:   "child-waiting",
		ObligationIDs: []string{obligation.BatchID},
		ParkedAt:      subagentbatch.Now(),
	}))

	// A restart recovery pass must not orphan an explicit parked wait.
	recovery := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store: store,
		TerminalSink: func(_ context.Context, notification BatchTerminalNotification) BatchTerminalDelivery {
			return BatchTerminalDelivery{Status: BatchTerminalDeliveryPersisted, DeliveryKey: notification.DeliveryKey}
		},
	})
	changed, err := recovery.RecoverStaleBatches(context.Background(), 0, "parent-parked", 10)
	require.NoError(t, err)
	require.Zero(t, changed)
	stillParked, err := store.GetBatch(context.Background(), batch.BatchID)
	require.NoError(t, err)
	require.Equal(t, subagentbatch.BatchParked, stillParked.Status)

	secondExec := &scriptedBatchExecutor{scripts: [][]SubagentResult{{
		{ID: "waiting", Role: "researcher", SessionID: "child-waiting", Success: true, Summary: "continued and completed"},
	}}}
	second := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	second.executor = secondExec
	resumed, err := second.ResumeParkedBatches(context.Background(), "parent-parked", 10)
	require.NoError(t, err)
	require.Equal(t, 1, resumed.Scanned)
	require.Equal(t, 1, resumed.Resumed)
	require.Zero(t, resumed.StillParked)

	terminal := waitTerminal(t, store, batch.BatchID)
	require.Equal(t, subagentbatch.BatchCompleted, terminal.Status)
	require.Equal(t, 2, terminal.CompletedCount)
	require.Equal(t, 0, terminal.FailedCount)

	calls := secondExec.Calls()
	require.Len(t, calls, 1)
	require.Len(t, calls[0], 1, "completed sibling must not be re-run")
	require.Equal(t, "waiting", calls[0][0].ID)
	require.Equal(t, "child-waiting", calls[0][0].ChildSessionID)
	require.Contains(t, calls[0][0].Goal, "Durable continuation")

	waiting, err := store.GetTask(context.Background(), batch.BatchID, "waiting")
	require.NoError(t, err)
	require.Equal(t, subagentbatch.TaskSucceeded, waiting.Status)
	require.Equal(t, "child-waiting", waiting.ChildSessionID)
}

func TestParkedBatchStaysParkedWhileRequiredObligationRuns(t *testing.T) {
	store := durableBatchTestStore(t)
	exec := &scriptedBatchExecutor{scripts: [][]SubagentResult{{
		{
			ID:         "waiting",
			Role:       "researcher",
			SessionID:  "child-live",
			TurnParked: true,
			TurnID:     childTaskTurnID("waiting"),
			EpisodeID:  childTaskEpisodeID("waiting"),
		},
	}}}
	first := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	first.executor = exec
	batch, err := first.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "parent-parked",
		RootScopeID:     "parent-parked",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "waiting", Role: "researcher", Goal: "wait"}})
	require.NoError(t, err)
	waitBatchStatus(t, store, batch.BatchID, subagentbatch.BatchParked)

	obligation := seedObligationBatch(t, store, "nested-batch-running", subagentbatch.BatchRunning)
	require.NoError(t, store.ParkTurnSuspension(context.Background(), &subagentbatch.TurnSuspension{
		TurnID:        childTaskTurnID("waiting"),
		EpisodeID:     childTaskEpisodeID("waiting"),
		SessionID:     "child-live",
		RootScopeID:   "child-live",
		ObligationIDs: []string{obligation.BatchID},
		ParkedAt:      subagentbatch.Now(),
	}))

	secondExec := &scriptedBatchExecutor{scripts: [][]SubagentResult{{
		{ID: "waiting", Role: "researcher", SessionID: "child-live", Success: true, Summary: "done"},
	}}}
	second := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	second.executor = secondExec
	resumed, err := second.ResumeParkedBatches(context.Background(), "parent-parked", 10)
	require.NoError(t, err)
	require.Equal(t, 1, resumed.StillParked)
	require.Zero(t, resumed.Resumed)
	require.Empty(t, secondExec.Calls())
	stillParked, err := store.GetBatch(context.Background(), batch.BatchID)
	require.NoError(t, err)
	require.Equal(t, subagentbatch.BatchParked, stillParked.Status)
}

func TestParkedResumeFollowsNestedBatchObligation(t *testing.T) {
	store := durableBatchTestStore(t)
	seedParkedBatch(t, store, "batch-parent", "parent-root", "parent-wait", "child-parent")
	seedParkedBatch(t, store, "batch-nested", "child-parent", "nested-wait", "child-nested")
	require.NoError(t, store.ParkTurnSuspension(context.Background(), &subagentbatch.TurnSuspension{
		TurnID:        childTaskTurnID("parent-wait"),
		EpisodeID:     childTaskEpisodeID("parent-wait"),
		SessionID:     "child-parent",
		RootScopeID:   "child-parent",
		ObligationIDs: []string{"batch-nested"},
		ParkedAt:      subagentbatch.Now(),
	}))

	exec := &scriptedBatchExecutor{scripts: [][]SubagentResult{
		{{ID: "nested-wait", Role: "researcher", SessionID: "child-nested", Success: true, Summary: "nested complete"}},
		{{ID: "parent-wait", Role: "researcher", SessionID: "child-parent", Success: true, Summary: "parent complete"}},
	}}
	c := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	c.executor = exec

	first, err := c.ResumeParkedBatches(context.Background(), "parent-root", 10)
	require.NoError(t, err)
	require.Equal(t, 2, first.Scanned, "nested parked batch must join the scoped resume tree")
	require.Equal(t, 1, first.Resumed)
	require.Equal(t, 1, first.StillParked)
	waitTerminal(t, store, "batch-nested")

	second, err := c.ResumeParkedBatches(context.Background(), "parent-root", 10)
	require.NoError(t, err)
	require.Equal(t, 1, second.Scanned)
	require.Equal(t, 1, second.Resumed)
	terminal := waitTerminal(t, store, "batch-parent")
	require.Equal(t, subagentbatch.BatchCompleted, terminal.Status)

	calls := exec.Calls()
	require.Len(t, calls, 2)
	require.Equal(t, "nested-wait", calls[0][0].ID)
	require.Equal(t, "parent-wait", calls[1][0].ID)
}

func TestRecoverStaleRunningBatchWithParkedTaskDemotesToParked(t *testing.T) {
	store := durableBatchTestStore(t)
	now := subagentbatch.Now()
	batchID := "batch-crashed-before-park"
	created, err := store.CreateBatch(context.Background(), &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     "parent-crash",
		ParentSessionID: "parent-crash",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		OwnerID:         "dead-worker",
		FencingToken:    "dead-worker/token",
		CreatedAt:       now.Add(-time.Hour),
		UpdatedAt:       now.Add(-time.Hour),
		HeartbeatAt:     now.Add(-time.Hour),
	}, []subagentbatch.SubagentTaskRecord{{
		TaskID:     "waiting",
		BatchID:    batchID,
		Status:     subagentbatch.TaskParked,
		OrderIndex: 0,
		UpdatedAt:  now.Add(-time.Hour),
		Version:    1,
	}})
	require.NoError(t, err)
	require.True(t, created)

	recovery := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store})
	changed, err := recovery.RecoverStaleBatches(context.Background(), 0, "parent-crash", 10)
	require.NoError(t, err)
	require.Equal(t, 1, changed)
	recovered, err := store.GetBatch(context.Background(), batchID)
	require.NoError(t, err)
	require.Equal(t, subagentbatch.BatchParked, recovered.Status)
	require.Nil(t, recovered.FinishedAt)
	require.Empty(t, recovered.OwnerID)
	require.Empty(t, recovered.FencingToken)
}

func TestRecoverExpiredParkedBatchTimesOutParkedTask(t *testing.T) {
	store := durableBatchTestStore(t)
	now := subagentbatch.Now()
	batchID := "batch-parked-expired"
	created, err := store.CreateBatch(context.Background(), &subagentbatch.SubagentBatch{
		BatchID:         batchID,
		RootScopeID:     "parent-expired",
		ParentSessionID: "parent-expired",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchParked,
		TaskCount:       1,
		BatchDeadline:   now.Add(-time.Minute),
		CreatedAt:       now.Add(-time.Hour),
		UpdatedAt:       now.Add(-time.Hour),
		HeartbeatAt:     now.Add(-time.Hour),
	}, []subagentbatch.SubagentTaskRecord{{
		TaskID:     "waiting",
		BatchID:    batchID,
		Status:     subagentbatch.TaskParked,
		OrderIndex: 0,
		UpdatedAt:  now.Add(-time.Hour),
		Version:    1,
	}})
	require.NoError(t, err)
	require.True(t, created)

	recovery := NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store: store,
		TerminalSink: func(_ context.Context, notification BatchTerminalNotification) BatchTerminalDelivery {
			return BatchTerminalDelivery{Status: BatchTerminalDeliveryPersisted, DeliveryKey: notification.DeliveryKey}
		},
	})
	changed, err := recovery.RecoverStaleBatches(context.Background(), 0, "parent-expired", 10)
	require.NoError(t, err)
	require.Equal(t, 1, changed)
	recovered, err := store.GetBatch(context.Background(), batchID)
	require.NoError(t, err)
	require.Equal(t, subagentbatch.BatchTimedOut, recovered.Status)
	task, err := store.GetTask(context.Background(), batchID, "waiting")
	require.NoError(t, err)
	require.Equal(t, subagentbatch.TaskTimedOut, task.Status)
	require.NotNil(t, task.FinishedAt)
}
