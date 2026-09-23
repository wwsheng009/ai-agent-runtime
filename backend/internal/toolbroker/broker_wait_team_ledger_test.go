package toolbroker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// failingListTasksStore embeds a real store and fails only the ledger read, so
// the fail-open direction of plan §13.8 can be exercised without a broken team
// store: the wait itself must still succeed.
type failingListTasksStore struct {
	*team.SQLiteStore
	calls int
}

func (s *failingListTasksStore) ListTasks(ctx context.Context, filter team.TaskFilter) ([]team.Task, error) {
	s.calls++
	return nil, errors.New("ledger read failed")
}

// terminalWaitTeamLedgerFixture builds a team whose durable state is already
// terminal with a ready summary, so the wait resolves on the first snapshot
// read. It deliberately has no tasks: the terminal-state reconciliation only
// reaches ListTasks for a team that has tasks, and the fail-open test needs the
// ledger read (not the reconciliation) to be the failing call.
func terminalWaitTeamLedgerFixture(t *testing.T) (*team.SQLiteStore, string) {
	t.Helper()
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{ID: "team-ledger-terminal", Status: team.TeamStatusDone})
	require.NoError(t, err)
	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		Type:    "team.summary",
		TeamID:  teamID,
		Payload: map[string]interface{}{"summary": "ledger fixture", "summary_source": "lead"},
	})
	require.NoError(t, err)
	return store, teamID
}

// TestBrokerExecuteWaitTeamLedgerFinalizesDrainedTaskLedger 钉住 AC-P2-4g 的放行
// 方向：团队终态且任务账本无待办 ⇒ 立即返回并给出与 wait_agent 相同的
// next_action=finalize，行形状为 subject_kind=team_task。
func TestBrokerExecuteWaitTeamLedgerFinalizesDrainedTaskLedger(t *testing.T) {
	store, teamID := terminalWaitTeamLedgerFixture(t)
	ctx := context.Background()
	_, err := store.CreateTask(ctx, team.Task{ID: "task-done", TeamID: teamID, Status: team.TaskStatusDone})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{ID: "task-cancelled", TeamID: teamID, Status: team.TaskStatusCancelled})
	require.NoError(t, err)

	broker := &Broker{TeamStore: store}
	raw, _, err := broker.Execute(ctx, "lead-session", ToolWaitTeam, map[string]interface{}{
		"team_id":    teamID,
		"timeout_ms": 5000,
	})
	require.NoError(t, err)
	result, ok := raw.(WaitTeamResult)
	require.True(t, ok)
	require.True(t, result.Terminal)
	require.False(t, result.TimedOut)
	require.Len(t, result.Obligations, 2)
	for _, row := range result.Obligations {
		assert.Equal(t, "team_task", row.SubjectKind)
		assert.Equal(t, row.ObligationID, row.SubjectID)
		assert.True(t, row.Terminal, "row %s must be terminal", row.ObligationID)
	}
	assert.Equal(t, 2, result.TerminalCount)
	assert.Equal(t, 0, result.PendingCount)
	assert.Empty(t, result.TerminalDelta,
		"tasks already terminal before the segment are the baseline, not the delta")
	assert.Equal(t, "finalize", result.NextAction,
		"an all-terminal team ledger is decisive: wait_team shares the wait_agent finalize rule")
	assert.Less(t, result.WaitedMs, int64(2000),
		"a drained ledger returns immediately instead of spending the requested window")
}

// TestBrokerExecuteWaitTeamLedgerKeepsPendingRowsOutOfFinalize 钉住 I1 方向：只要
// 还有非终态任务行，超时返回也绝不给出 finalize，且保留既有指引。
func TestBrokerExecuteWaitTeamLedgerKeepsPendingRowsOutOfFinalize(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-ledger-pending",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{ID: "task-done", TeamID: teamID, Status: team.TaskStatusDone})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{ID: "task-running", TeamID: teamID, Status: team.TaskStatusRunning})
	require.NoError(t, err)

	// This test is about the ledger view on the expired-window path, not about
	// the shared bounds: pin a sub-second policy so the wait expires immediately.
	broker := &Broker{
		TeamStore: store,
		WaitTimeoutPolicy: func() agentcontrol.WaitTimeoutPolicy {
			return agentcontrol.WaitTimeoutPolicy{DefaultMs: 1, MinMs: 1, MaxMs: 1000}
		},
	}
	raw, _, err := broker.Execute(ctx, "lead-session", ToolWaitTeam, map[string]interface{}{
		"team_id":    teamID,
		"timeout_ms": 1,
	})
	require.NoError(t, err)
	result, ok := raw.(WaitTeamResult)
	require.True(t, ok)
	require.True(t, result.TimedOut)
	require.False(t, result.Terminal)
	require.Len(t, result.Obligations, 2)
	assert.Equal(t, 1, result.PendingCount)
	assert.Equal(t, 1, result.TerminalCount)
	assert.Empty(t, result.TerminalDelta)
	assert.NotEqual(t, "finalize", result.NextAction,
		"a pending task row must never produce finalize")
	assert.Contains(t, result.NextAction, "wait timeout only ended this observation")
}

// TestBrokerExecuteWaitTeamLedgerReportsTaskThatFinishedDuringWait 钉住
// terminal_delta 的语义：只报本次等待段内到达终态的任务。
func TestBrokerExecuteWaitTeamLedgerReportsTaskThatFinishedDuringWait(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-ledger-delta",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{ID: "task-flip", TeamID: teamID, Status: team.TaskStatusRunning})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{ID: "task-slow", TeamID: teamID, Status: team.TaskStatusRunning})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(50 * time.Millisecond)
		_ = store.UpdateTaskStatus(ctx, "task-flip", team.TaskStatusDone, "finished mid-wait")
	}()

	broker := &Broker{
		TeamStore: store,
		WaitTimeoutPolicy: func() agentcontrol.WaitTimeoutPolicy {
			return agentcontrol.WaitTimeoutPolicy{DefaultMs: 400, MinMs: 1, MaxMs: 1000}
		},
	}
	raw, _, err := broker.Execute(ctx, "lead-session", ToolWaitTeam, map[string]interface{}{
		"team_id":    teamID,
		"timeout_ms": 400,
	})
	require.NoError(t, err)
	<-done
	result, ok := raw.(WaitTeamResult)
	require.True(t, ok)
	require.True(t, result.TimedOut)
	require.Len(t, result.Obligations, 2)
	assert.Equal(t, 1, result.PendingCount, "task-slow is still running")
	assert.Equal(t, 1, result.TerminalCount)
	assert.Equal(t, []string{"task-flip"}, result.TerminalDelta,
		"terminal_delta must report the task that reached terminal state during this wait segment")
	assert.NotEqual(t, "finalize", result.NextAction)
	assert.GreaterOrEqual(t, result.WaitedMs, int64(300),
		"waited_ms must report the window actually spent waiting (AC-P2-4b)")
	assert.Less(t, result.WaitedMs, int64(2000), "waited_ms must stay close to the requested window")
}

// TestBrokerExecuteWaitTeamLedgerFailsOpenOnTaskStoreError 钉住 fail-open：账本读
// 失败不得把一次成功的观测变成错误，只是没有账本视图（plan §13.8）。
func TestBrokerExecuteWaitTeamLedgerFailsOpenOnTaskStoreError(t *testing.T) {
	store, teamID := terminalWaitTeamLedgerFixture(t)
	wrapper := &failingListTasksStore{SQLiteStore: store}

	broker := &Broker{TeamStore: wrapper}
	raw, _, err := broker.Execute(context.Background(), "lead-session", ToolWaitTeam, map[string]interface{}{
		"team_id":    teamID,
		"timeout_ms": 5000,
	})
	require.NoError(t, err, "a ledger read failure must not fail the wait itself")
	require.GreaterOrEqual(t, wrapper.calls, 1, "the ledger read must actually have been attempted")
	result, ok := raw.(WaitTeamResult)
	require.True(t, ok)
	require.True(t, result.Terminal)
	assert.Empty(t, result.Obligations)
	assert.Equal(t, 0, result.TerminalCount)
	assert.Equal(t, 0, result.PendingCount)
	assert.Empty(t, result.TerminalDelta)
	assert.NotEqual(t, "finalize", result.NextAction, "without a ledger there is no ledger-derived next_action")
}
