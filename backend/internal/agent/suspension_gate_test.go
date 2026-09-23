package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// newSuspensionGateAgent builds an agent with a scheduler and, when store is
// non-nil, a coordinator over that store. A nil store models a host that never
// wired a batch control plane at all.
func newSuspensionGateAgent(t *testing.T, store subagentbatch.BatchStore) *Agent {
	t.Helper()
	apiAgent := NewAgent(&Config{Name: "suspension-gate", Model: "test-model"}, nil)
	apiAgent.SetSubagentScheduler(NewSubagentScheduler(apiAgent, SubagentSchedulerConfig{MaxConcurrent: 1, MaxDepth: 1}))
	if store != nil {
		apiAgent.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
			Store:     store,
			Scheduler: apiAgent.GetSubagentScheduler(),
		}))
	}
	return apiAgent
}

// TestSuspensionProbeRequiresDurableStore pins AC-C0-1a/b at the probe level:
// no store, an in-memory store (including the lazy default) are all degradations;
// only a store that survives a restart satisfies I9 (§6.13).
func TestSuspensionProbeRequiresDurableStore(t *testing.T) {
	t.Run("no coordinator", func(t *testing.T) {
		apiAgent := newSuspensionGateAgent(t, nil)
		reason, ok := apiAgent.SuspensionProbe()
		require.False(t, ok)
		require.Equal(t, SuspensionReasonNoCoordinator, reason)
		require.False(t, apiAgent.SupportsSuspension())
	})

	t.Run("in-memory store", func(t *testing.T) {
		store, err := subagentbatch.NewSQLiteBatchStore(nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		require.False(t, store.IsDurable())

		apiAgent := newSuspensionGateAgent(t, store)
		reason, ok := apiAgent.SuspensionProbe()
		require.False(t, ok)
		require.Equal(t, SuspensionReasonNotDurable, reason)
	})

	t.Run("lazy default stays non-durable", func(t *testing.T) {
		apiAgent := newSuspensionGateAgent(t, nil)
		// The probe must not accept the lazily created process-local default:
		// materialising it is exactly the cross-process amnesia I9 forbids.
		require.NotNil(t, apiAgent.GetSubagentBatchCoordinator())
		reason, ok := apiAgent.SuspensionProbe()
		require.False(t, ok)
		require.Equal(t, SuspensionReasonNotDurable, reason)
	})

	t.Run("file-backed store", func(t *testing.T) {
		store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
			Path: filepath.Join(t.TempDir(), "batches.db"),
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		require.True(t, store.IsDurable())

		apiAgent := newSuspensionGateAgent(t, store)
		reason, ok := apiAgent.SuspensionProbe()
		require.True(t, ok)
		require.Empty(t, reason)
		require.True(t, apiAgent.SupportsSuspension())
	})
}

// assertDegradedSuspensionDispatch drives the dispatch gate of a degraded host
// and pins the observable consequences from §6.13 after P3 / C4-1: the dispatch
// is still asynchronous (no blocking fallback is left), exactly one
// SeverityWarning-family projection is produced (never per call), and no
// suspension is recorded or announced.
func assertDegradedSuspensionDispatch(t *testing.T, apiAgent *Agent, wantReason string, store subagentbatch.BatchStore) {
	t.Helper()
	ctx := context.Background()

	bus := runtimeevents.NewBus()
	var events []runtimeevents.Event
	bus.Subscribe("", func(event runtimeevents.Event) { events = append(events, event) })
	apiAgent.SetEventBus(bus)

	var projections []SuspensionDegradation
	apiAgent.SetSuspensionDegradationProjector(func(_ context.Context, degradation SuspensionDegradation) error {
		projections = append(projections, degradation)
		return nil
	})

	// Visibility is a policy question and must not change: the tool stays on the
	// surface, only the suspension capability degrades.
	require.True(t, shouldExposeSpawnSubagents(apiAgent, nil), "durability must not hide the tool")

	loop := NewReActLoop(apiAgent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	loop.turnID = "turn-1"
	// P3 / C4-1: the blocking fallback is gone, so a degraded host still
	// dispatches asynchronously and only loses the resumable parked turn.
	require.True(t, loop.dispatchSubagentsAsync(), "degraded hosts must still dispatch asynchronously")
	require.Equal(t, wantReason, loop.backgroundDegradationReason())

	// Two dispatches in the same session project one warning, not two.
	apiAgent.reportSuspensionDegraded(ctx, "sess-1", wantReason)
	apiAgent.reportSuspensionDegraded(ctx, "sess-1", wantReason)
	require.Len(t, projections, 1, "degradation must be projected exactly once per session")
	require.Equal(t, "sess-1", projections[0].ParentSessionID)
	require.Equal(t, wantReason, projections[0].Reason)
	require.Equal(t, SuspensionSeverityWarning, projections[0].Severity)

	var unavailable, suspended int
	for _, event := range events {
		switch event.Type {
		case "subagent.suspension.unavailable":
			unavailable++
		case "turn.suspended":
			suspended++
		}
	}
	require.Equal(t, 1, unavailable, "one degradation event per session")
	require.Zero(t, suspended, "a degraded host must never announce a suspended turn")

	if store != nil {
		_, ok, err := store.GetTurnSuspension(ctx, "sess-1", "turn-1")
		require.NoError(t, err)
		require.False(t, ok, "degraded hosts must not write parked state")
	}
}

// TestBackgroundDispatchDegradesWithoutDurableStore covers AC-C0-1a/b/d:
// store=nil and a process-local store both take the legacy path, project exactly
// one warning and never emit turn.suspended.
func TestBackgroundDispatchDegradesWithoutDurableStore(t *testing.T) {
	t.Run("store nil", func(t *testing.T) {
		// The dispatch gate materialises the lazy in-memory coordinator, so the
		// reason reported at dispatch time is the store one, not "no coordinator".
		assertDegradedSuspensionDispatch(t, newSuspensionGateAgent(t, nil), SuspensionReasonNotDurable, nil)
	})

	t.Run("in-memory store", func(t *testing.T) {
		store, err := subagentbatch.NewSQLiteBatchStore(nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		assertDegradedSuspensionDispatch(t, newSuspensionGateAgent(t, store), SuspensionReasonNotDurable, store)
	})

	t.Run("explicit rollback switch refuses dispatch", func(t *testing.T) {
		// Disabling the feature on purpose (SubagentBackgroundEnabled=false) is
		// not a degradation: with the blocking path deleted there is nothing to
		// fall back to, so the dispatch must fail loudly instead of blocking.
		apiAgent := newSuspensionGateAgent(t, nil)
		apiAgent.SetSubagentBackgroundEnabled(false)
		loop := NewReActLoop(apiAgent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
		require.False(t, loop.dispatchSubagentsAsync())
		require.Empty(t, loop.backgroundDegradationReason())
	})
}

// TestDurableSuspensionParksTurnAndSurvivesReopen covers AC-C0-1c: with a
// durable store the turn parks and the §6.12 record is readable after the store
// is closed and reopened (i.e. it would survive a restart).
func TestDurableSuspensionParksTurnAndSurvivesReopen(t *testing.T) {
	path := t.TempDir() + "/batches.db"
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{Path: path})
	require.NoError(t, err)
	require.True(t, store.IsDurable())

	exec := &fakeExecutor{
		results: []SubagentResult{{ID: "t1", Role: "researcher", SessionID: "child-1", Success: true, Summary: "ok"}},
		done:    make(chan struct{}),
	}
	coordinator := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		emitter:  func(string, map[string]interface{}) {},
		deadline: time.Minute,
		cancels:  make(map[string]context.CancelFunc),
	}
	apiAgent := newSuspensionGateAgent(t, nil)
	apiAgent.SetSubagentBatchCoordinator(coordinator)
	require.True(t, apiAgent.SupportsSuspension())

	loop := NewReActLoop(apiAgent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	loop.turnID = "turn-9"
	args := map[string]interface{}{"execution_mode": "background"}
	require.True(t, loop.dispatchSubagentsAsync())

	tc := types.ToolCall{ID: "call-1", Name: "spawn_subagents", Args: args}
	batch, err := loop.startBackgroundSubagentBatch(context.Background(),
		[]SubagentTask{{ID: "t1", Role: "researcher", Goal: "investigate"}}, "sess-9", tc, 1, "trace-9", 1)
	require.NoError(t, err)
	require.NotNil(t, batch)

	ctx := context.Background()
	record, ok, err := store.GetTurnSuspension(ctx, "sess-9", "turn-9")
	require.NoError(t, err)
	require.True(t, ok, "a durable dispatch must park the turn")
	require.Equal(t, []string{batch.BatchID, "t1"}, record.ObligationIDs)
	require.Equal(t, []string{batch.BatchID}, record.ResumeQueue)
	require.Equal(t, "sess-9", record.RootScopeID)
	require.False(t, record.ParkedAt.IsZero())

	// Re-parking the same turn is idempotent (crash/retry safe).
	require.NoError(t, store.ParkTurnSuspension(ctx, record))
	rereadParked, ok, err := store.GetTurnSuspension(ctx, "sess-9", "turn-9")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, record.ObligationIDs, rereadParked.ObligationIDs)

	select {
	case <-exec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("background worker did not finish in time")
	}

	require.NoError(t, store.Close())
	reopened, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	reread, ok, err := reopened.GetTurnSuspension(ctx, "sess-9", "turn-9")
	require.NoError(t, err)
	require.True(t, ok, "the parked turn must survive a store reopen")
	require.Equal(t, record.ObligationIDs, reread.ObligationIDs)
	require.Equal(t, record.ResumeQueue, reread.ResumeQueue)
	require.Equal(t, record.RootScopeID, reread.RootScopeID)
	require.Equal(t, record.ParkedAt.UTC(), reread.ParkedAt.UTC())

	durableBatch, err := reopened.GetBatch(ctx, batch.BatchID)
	require.NoError(t, err)
	require.NotNil(t, durableBatch, "the dispatched batch must survive the reopen too")
}

// TestSpawnSubagentsDescriptionStatesSuspensionAvailability pins the model-visible
// half of §6.13: a degraded session must say so, a durable session must keep the
// original contract text.
func TestSpawnSubagentsDescriptionStatesSuspensionAvailability(t *testing.T) {
	degraded := spawnSubagentsToolDefinition(false)
	require.Contains(t, degraded.Description, "当前会话不支持托管挂起")
	properties, ok := degraded.Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	mode, ok := properties["execution_mode"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, mode["description"], "当前会话不支持托管挂起")

	durable := spawnSubagentsToolDefinition(true)
	require.NotContains(t, durable.Description, "当前会话不支持托管挂起")
	require.Contains(t, durable.Description, "Dispatch is asynchronous")
	durableProperties, ok := durable.Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	durableMode, ok := durableProperties["execution_mode"].(map[string]interface{})
	require.True(t, ok)
	// P3 / C4-1: the parameter is deprecated and no longer offers a choice.
	require.Contains(t, durableMode["description"], "Deprecated")
	require.NotContains(t, durableMode, "enum")
}

// TestExecutionModeLegacyValuesStayReadableAndWarn pins AC-P3-1a/b (P3 / C4-1):
// the deprecated execution_mode values stay readable (old callers keep working)
// and are answered with a deprecation notice instead of a silent
// reinterpretation, while typos are still rejected.
func TestExecutionModeLegacyValuesStayReadableAndWarn(t *testing.T) {
	for _, value := range []string{"", "wait", "sync", "background", "async"} {
		mode, err := subagentbatch.ParseExecutionMode(value)
		require.NoError(t, err, "value %q must stay readable", value)
		require.NotEmpty(t, mode)
	}
	for _, value := range []string{"wait", "sync", " WAIT "} {
		notice := subagentbatch.LegacyExecutionModeNotice(value)
		require.Contains(t, notice, "deprecated", "value %q must warn", value)
		require.Contains(t, notice, "no longer blocks")
	}
	require.Empty(t, subagentbatch.LegacyExecutionModeNotice("background"))
	require.Empty(t, subagentbatch.LegacyExecutionModeNotice(""))

	_, err := subagentbatch.ParseExecutionMode("backgroud")
	require.Error(t, err, "typos must still be rejected")
}

// TestSpawnSubagentsLedgerAlwaysRecordsBackground pins the P3 / C4-1 dispatch
// contract: even a legacy execution_mode=wait call lands in the durable ledger as
// background, because the supervision queries filter on that value and the
// blocking semantics no longer exists.
func TestSpawnSubagentsLedgerAlwaysRecordsBackground(t *testing.T) {
	path := filepath.Join(t.TempDir(), "batches.db")
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	exec := &fakeExecutor{
		results: []SubagentResult{{ID: "t1", Success: true, Summary: "ok"}},
		done:    make(chan struct{}),
	}
	coordinator := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		emitter:  func(string, map[string]interface{}) {},
		deadline: time.Minute,
		cancels:  make(map[string]context.CancelFunc),
	}
	apiAgent := newSuspensionGateAgent(t, nil)
	apiAgent.SetSubagentBatchCoordinator(coordinator)

	loop := NewReActLoop(apiAgent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	loop.turnID = "turn-wait"
	args := map[string]interface{}{"execution_mode": "wait"}
	tc := types.ToolCall{ID: "call-wait", Name: "spawn_subagents", Args: args}
	batch, err := loop.startBackgroundSubagentBatch(context.Background(),
		[]SubagentTask{{ID: "t1", Goal: "investigate"}}, "sess-wait", tc, 1, "trace-wait", 1)
	require.NoError(t, err)
	require.Equal(t, subagentbatch.ExecutionModeBackground, batch.ExecutionMode)

	ctx := context.Background()
	stored, err := store.GetBatch(ctx, batch.BatchID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, subagentbatch.ExecutionModeBackground, stored.ExecutionMode)

	select {
	case <-exec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("background worker did not finish in time")
	}
}
