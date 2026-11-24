package toolbroker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type waitSummaryPersistenceFailingStore struct {
	team.Store
}

func (s waitSummaryPersistenceFailingStore) AppendTeamEvent(ctx context.Context, event team.TeamEvent) (int64, error) {
	if event.Type == "team.summary" || event.Type == "team.summary.failed" {
		return 0, errors.New("test summary persistence failure")
	}
	return s.Store.AppendTeamEvent(ctx, event)
}

func newTerminalWaitWithPendingSummary(t *testing.T, teamID string) (*team.SQLiteStore, team.Store) {
	t.Helper()
	base := newTeamStore(t)
	ctx := context.Background()
	_, err := base.CreateTeam(ctx, team.Team{ID: teamID, Status: team.TeamStatusDone})
	require.NoError(t, err)
	_, err = base.AppendTeamEvent(ctx, team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	})
	require.NoError(t, err)
	return base, waitSummaryPersistenceFailingStore{Store: base}
}

func waitSummaryTestPolicy() agentcontrol.WaitTimeoutPolicy {
	return agentcontrol.WaitTimeoutPolicy{DefaultMs: 250, MinMs: 1, MaxMs: 1000}
}

func TestBrokerExecuteWaitTeamWithoutPlannerDoesNotWaitForPendingSummaryByDefault(t *testing.T) {
	_, store := newTerminalWaitWithPendingSummary(t, "team-wait-default-pending-summary")
	broker := &Broker{
		TeamStore:         store,
		WaitTimeoutPolicy: waitSummaryTestPolicyProvider,
	}

	raw, _, err := broker.Execute(context.Background(), "lead-session", ToolTeamWait, map[string]interface{}{
		"team_id":    "team-wait-default-pending-summary",
		"timeout_ms": 250,
	})
	require.NoError(t, err)
	result, ok := raw.(WaitTeamResult)
	require.True(t, ok)
	assert.True(t, result.Terminal)
	assert.False(t, result.TimedOut, "summary readiness is not the default wait condition without a Team Planner")
	assert.False(t, result.SummaryReady)
	assert.Equal(t, "pending", result.SummaryState)
	assert.Contains(t, result.NextAction, "did not require summary readiness")
	assert.Contains(t, result.NextAction, "no summary retry action is exposed")
}

func TestBrokerExecuteWaitTeamExplicitFalseOverridesPlannerSummaryDefault(t *testing.T) {
	_, store := newTerminalWaitWithPendingSummary(t, "team-wait-explicit-summary-false")
	broker := &Broker{
		TeamStore:         store,
		TeamPlanner:       &team.LeadPlanner{},
		WaitTimeoutPolicy: waitSummaryTestPolicyProvider,
	}

	raw, _, err := broker.Execute(context.Background(), "lead-session", ToolTeamWait, map[string]interface{}{
		"team_id":         "team-wait-explicit-summary-false",
		"timeout_ms":      250,
		"require_summary": false,
	})
	require.NoError(t, err)
	result, ok := raw.(WaitTeamResult)
	require.True(t, ok)
	assert.True(t, result.Terminal)
	assert.False(t, result.TimedOut, "explicit require_summary=false must override the configured planner default")
	assert.False(t, result.SummaryReady)
	assert.Equal(t, "pending", result.SummaryState)
	assert.Contains(t, result.NextAction, "did not require summary readiness")
}

func TestBrokerExecuteWaitTeamSummaryFailureDoesNotSuggestRetry(t *testing.T) {
	base, store := newTerminalWaitWithPendingSummary(t, "team-wait-summary-failed-next-action")
	_, err := base.AppendTeamEvent(context.Background(), team.TeamEvent{
		Type:   "team.summary.failed",
		TeamID: "team-wait-summary-failed-next-action",
		Payload: map[string]interface{}{
			"error_type": "lead_session_error",
			"error":      "lead session unavailable",
		},
	})
	require.NoError(t, err)

	broker := &Broker{
		TeamStore:         store,
		WaitTimeoutPolicy: waitSummaryTestPolicyProvider,
	}
	raw, _, err := broker.Execute(context.Background(), "lead-session", ToolTeamWait, map[string]interface{}{
		"team_id":    "team-wait-summary-failed-next-action",
		"timeout_ms": 250,
	})
	require.NoError(t, err)
	result, ok := raw.(WaitTeamResult)
	require.True(t, ok)
	assert.True(t, result.Terminal)
	assert.False(t, result.TimedOut)
	assert.False(t, result.SummaryReady)
	assert.Equal(t, "failed", result.SummaryState)
	assert.Contains(t, result.NextAction, "this tool does not expose a summary retry action")
}

func waitSummaryTestPolicyProvider() agentcontrol.WaitTimeoutPolicy {
	return waitSummaryTestPolicy()
}
