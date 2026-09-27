package toolbroker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// recordingWaitBudget is a deterministic stand-in for the host-side
// agentcontrol.WaitBudget adapter (plan §16.3: wait_team shares wait_agent's
// per-turn counter through the broker-side WaitSegmentBudget seam).
type recordingWaitBudget struct {
	mu           sync.Mutex
	active       bool
	limit        int
	consecutive  int
	exhausted    bool
	observations []bool
}

func (f *recordingWaitBudget) BudgetVerdict(context.Context) (int, int, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.consecutive, f.limit, f.active, f.exhausted
}

func (f *recordingWaitBudget) ObserveWaitSegment(_ context.Context, progress bool) (int, int, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observations = append(f.observations, progress)
	if progress {
		f.consecutive = 0
		f.exhausted = false
		return 0, f.limit, f.active, false
	}
	f.consecutive++
	f.exhausted = f.consecutive >= f.limit
	return f.consecutive, f.limit, f.active, f.exhausted
}

func (f *recordingWaitBudget) observed() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.observations...)
}

func (f *recordingWaitBudget) counter() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.consecutive
}

// subSecondWaitPolicy keeps the budget tests on the expired-window path without
// waiting for the production default window.
func subSecondWaitPolicy() func() agentcontrol.WaitTimeoutPolicy {
	return func() agentcontrol.WaitTimeoutPolicy {
		return agentcontrol.WaitTimeoutPolicy{DefaultMs: 1, MinMs: 1, MaxMs: 5000}
	}
}

func TestBrokerExecuteWaitTeamExhaustedBudgetSkipsWindow(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-wait-budget-exhausted",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{ID: "task-1", TeamID: teamID, Status: team.TaskStatusRunning})
	require.NoError(t, err)

	budget := &recordingWaitBudget{active: true, limit: 2, consecutive: 2, exhausted: true}
	broker := &Broker{TeamStore: store, WaitBudget: budget, WaitTimeoutPolicy: subSecondWaitPolicy()}

	started := time.Now()
	raw, _, err := broker.Execute(ctx, "lead-session", ToolWaitTeam, map[string]interface{}{
		"team_id":    teamID,
		"timeout_ms": 5000,
	})
	require.NoError(t, err)
	result := raw.(WaitTeamResult)
	assert.True(t, result.WaitBudgetExhausted)
	assert.Contains(t, result.NextAction, "suspend:")
	assert.Contains(t, result.NextAction, "Do not call wait_team again")
	assert.False(t, result.TimedOut, "no observation window was opened, so nothing timed out")
	assert.Less(t, time.Since(started), 500*time.Millisecond,
		"an exhausted wait budget must return immediately instead of blocking")
	// The task ledger view survives the budget verdict (I1 still needs it).
	require.Len(t, result.Obligations, 1)
	assert.Equal(t, "task-1", result.Obligations[0].ObligationID)
	assert.Equal(t, 1, result.PendingCount)
	assert.Empty(t, budget.observed(), "an exhausted budget must not open (or bill) a window")
}

func TestBrokerExecuteWaitTeamTimeoutBillsNoProgressWindow(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-wait-budget-timeout",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{ID: "task-1", TeamID: teamID, Status: team.TaskStatusRunning})
	require.NoError(t, err)

	budget := &recordingWaitBudget{active: true, limit: 2}
	broker := &Broker{TeamStore: store, WaitBudget: budget, WaitTimeoutPolicy: subSecondWaitPolicy()}
	raw, _, err := broker.Execute(ctx, "lead-session", ToolWaitTeam, map[string]interface{}{
		"team_id":    teamID,
		"timeout_ms": 1,
	})
	require.NoError(t, err)
	result := raw.(WaitTeamResult)
	assert.True(t, result.TimedOut)
	assert.False(t, result.WaitBudgetExhausted, "the first no-progress window is still within budget")
	assert.Equal(t, []bool{false}, budget.observed())
	assert.Equal(t, 1, budget.counter())
}

func TestBrokerExecuteWaitTeamTaskCompletionCountsAsProgress(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-wait-budget-progress",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{ID: "task-1", TeamID: teamID, Status: team.TaskStatusRunning})
	require.NoError(t, err)

	// limit=1: any no-progress window would already be exhausted, so surviving
	// this wait proves the in-window completion was credited as progress.
	budget := &recordingWaitBudget{active: true, limit: 1}
	broker := &Broker{TeamStore: store, WaitBudget: budget, WaitTimeoutPolicy: subSecondWaitPolicy()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(30 * time.Millisecond)
		_ = store.UpdateTaskStatus(ctx, "task-1", team.TaskStatusDone, "done in window")
		_ = store.UpdateTeamStatus(ctx, teamID, team.TeamStatusDone)
		_, _ = store.AppendTeamEvent(ctx, team.TeamEvent{
			Type:   "team.completed",
			TeamID: teamID,
			Payload: map[string]interface{}{
				"status": string(team.TeamStatusDone),
			},
		})
	}()

	raw, _, err := broker.Execute(ctx, "lead-session", ToolWaitTeam, map[string]interface{}{
		"team_id":    teamID,
		"timeout_ms": 3000,
	})
	require.NoError(t, err)
	<-done
	result := raw.(WaitTeamResult)
	assert.True(t, result.Terminal)
	assert.False(t, result.WaitBudgetExhausted,
		"a task that reached terminal state during the window is progress, even at limit=1")
	assert.Equal(t, []bool{true}, budget.observed())
	assert.Contains(t, result.TerminalDelta, "task-1")
}

func TestBrokerExecuteWaitTeamWithoutBudgetKeepsLegacyResult(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:            "team-wait-no-budget",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusActive,
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, team.Task{ID: "task-1", TeamID: teamID, Status: team.TaskStatusRunning})
	require.NoError(t, err)

	// nil budget (host did not inject one) and a disarmed budget must both leave
	// the legacy wait_team contract untouched: no stamp, no accounting.
	for name, budget := range map[string]WaitSegmentBudget{
		"nil":      nil,
		"disarmed": &recordingWaitBudget{active: false, limit: 2},
	} {
		t.Run(name, func(t *testing.T) {
			recorder, _ := budget.(*recordingWaitBudget)
			broker := &Broker{TeamStore: store, WaitBudget: budget, WaitTimeoutPolicy: subSecondWaitPolicy()}
			raw, _, err := broker.Execute(ctx, "lead-session", ToolWaitTeam, map[string]interface{}{
				"team_id":    teamID,
				"timeout_ms": 1,
			})
			require.NoError(t, err)
			result := raw.(WaitTeamResult)
			assert.True(t, result.TimedOut)
			assert.False(t, result.WaitBudgetExhausted)
			assert.NotContains(t, result.NextAction, "suspend:")
			if recorder != nil {
				assert.Empty(t, recorder.observed())
			}
		})
	}
}
