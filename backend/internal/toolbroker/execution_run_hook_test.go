package toolbroker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newHookTestSupervisor(t *testing.T, name string) *supervision.ExecutionSupervisor {
	t.Helper()
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		DSN: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return &supervision.ExecutionSupervisor{
		Store:     store,
		StoreFull: store,
		Config: supervision.ExecutionSupervisorConfig{
			Mode:                    "enforce",
			DefaultExecutionTimeout: 30 * time.Minute,
			DefaultProgressTimeout:  5 * time.Minute,
			DefaultApprovalTimeout:  1 * time.Hour,
			DefaultCancelGrace:      15 * time.Second,
		},
	}
}

func TestStartSpawnExecutionRunNoopWithoutSupervisor(t *testing.T) {
	runID, deadline, err := startSpawnExecutionRun(context.Background(), nil, "parent-1",
		SpawnAgentArgs{}, &AgentStatusResult{SessionID: "child-1"})
	require.NoError(t, err)
	require.Empty(t, runID)
	require.Empty(t, deadline)
}

func TestStartSpawnExecutionRunRegistersRun(t *testing.T) {
	supervisor := newHookTestSupervisor(t, "hook-register")
	result := &AgentStatusResult{SessionID: "child-1", ID: "child-1"}
	runID, deadline, err := startSpawnExecutionRun(context.Background(), supervisor, "parent-1",
		SpawnAgentArgs{
			TimeoutSec:         90,
			ProgressTimeoutSec: 30,
			ApprovalTimeoutSec: 600,
			CancelGraceSec:     10,
		}, result)
	require.NoError(t, err)
	require.NotEmpty(t, runID)
	require.NotEmpty(t, deadline)
	require.Equal(t, runID, result.RunID)
	require.Equal(t, deadline, result.ExecutionDeadlineAt)
	require.Equal(t, spawnSupervisionPolicyEnforce, result.SupervisionPolicy)

	// The run is durably registered with the requested deadlines.
	run, err := supervisor.Store.GetExecutionRun(context.Background(), runID)
	require.NoError(t, err)
	require.Equal(t, "child-1", run.SessionID)
	require.Equal(t, "parent-1", run.ParentSessionID)
	require.NotNil(t, run.ExecutionDeadlineAt)
	require.WithinDuration(t, time.Now().Add(90*time.Second), *run.ExecutionDeadlineAt, 2*time.Second)
	require.WithinDuration(t, time.Now().Add(30*time.Second), *run.ProgressDeadlineAt, 2*time.Second)
}

func TestStartSpawnExecutionRunDefaultsAndValidation(t *testing.T) {
	supervisor := newHookTestSupervisor(t, "hook-defaults")
	// Zero timeouts fall back to operator defaults.
	result := &AgentStatusResult{SessionID: "child-1"}
	runID, _, err := startSpawnExecutionRun(context.Background(), supervisor, "parent-1", SpawnAgentArgs{}, result)
	require.NoError(t, err)
	run, err := supervisor.Store.GetExecutionRun(context.Background(), runID)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(30*time.Minute), *run.ExecutionDeadlineAt, 2*time.Second)

	// Negative values are rejected without creating a run.
	_, _, err = startSpawnExecutionRun(context.Background(), supervisor, "parent-1",
		SpawnAgentArgs{TimeoutSec: -1}, &AgentStatusResult{SessionID: "child-2"})
	require.Error(t, err)
}

func TestStartSpawnExecutionRunStoreFailureIsReturned(t *testing.T) {
	// A supervisor without a store fails registration; the caller must be able
	// to surface it as metadata instead of blocking spawn.
	broken := &supervision.ExecutionSupervisor{}
	_, _, err := startSpawnExecutionRun(context.Background(), broken, "parent-1",
		SpawnAgentArgs{}, &AgentStatusResult{SessionID: "child-1"})
	require.Error(t, err)

	// Missing child session id is also an error.
	_, _, err = startSpawnExecutionRun(context.Background(), newHookTestSupervisor(t, "hook-noid"), "parent-1",
		SpawnAgentArgs{}, &AgentStatusResult{})
	require.Error(t, err)
}

// TestBrokerSpawnRegistersSupervisedRun verifies the P3 host integration:
// a successful spawn_agent tool call through the Broker registers a durable
// execution run and surfaces run_id / execution_deadline_at in metadata.
func TestBrokerSpawnRegistersSupervisedRun(t *testing.T) {
	ctx := context.Background()
	supervisor := newHookTestSupervisor(t, "broker-sup-run")
	broker := &Broker{
		AgentSessions:       &fakeAgentSessionController{},
		SessionContextStore: newFakeSessionContextStore(),
		ExecutionSupervisor: supervisor,
	}

	raw, meta, err := broker.ExecuteToolCall(ctx, "parent-1", types.ToolCall{
		ID:   "call-sup-1",
		Name: ToolSpawnAgent,
		Args: map[string]interface{}{
			"message":     "run a bounded task",
			"timeout_sec": int64(90),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, raw)
	require.NotEmpty(t, meta["run_id"], "spawn metadata must expose the supervised run_id")
	require.NotEmpty(t, meta["execution_deadline_at"])
	require.Equal(t, "interrupt_then_fail", meta["supervision_policy"])

	runID, _ := meta["run_id"].(string)
	run, err := supervisor.Store.GetExecutionRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "child-1", run.SessionID)
	require.Equal(t, "parent-1", run.ParentSessionID)
	require.Equal(t, supervision.RunStatusQueued, run.Status)
	require.NotNil(t, run.ExecutionDeadlineAt)
	require.False(t, run.ExecutionDeadlineAt.IsZero())
}

// TestBrokerSpawnWithoutSupervisorKeepsLegacyBehavior verifies that a broker
// without an ExecutionSupervisor still spawns successfully and exposes no
// supervision metadata.
func TestBrokerSpawnWithoutSupervisorKeepsLegacyBehavior(t *testing.T) {
	broker := &Broker{
		AgentSessions:       &fakeAgentSessionController{},
		SessionContextStore: newFakeSessionContextStore(),
	}
	_, meta, err := broker.ExecuteToolCall(context.Background(), "parent-1", types.ToolCall{
		ID:   "call-sup-2",
		Name: ToolSpawnAgent,
		Args: map[string]interface{}{
			"message": "legacy spawn",
		},
	})
	require.NoError(t, err)
	require.Empty(t, meta["run_id"])
	require.Empty(t, meta["supervision_error"])
}
