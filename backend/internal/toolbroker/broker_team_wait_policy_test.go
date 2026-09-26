package toolbroker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// waitTeamPolicyBroker builds a broker whose wait target is already terminal
// (team.completed + team.summary are durable before the call), so every call
// returns on the first snapshot read. The assertions below are therefore about
// the normalized observation window, never about wall-clock blocking: a
// regression that stopped normalizing would show up as an out-of-range echo
// rather than as a hung test.
func waitTeamPolicyBroker(t *testing.T, policy func() agentcontrol.WaitTimeoutPolicy) (*Broker, string) {
	t.Helper()
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{
		ID:     "team-wait-policy",
		Status: team.TeamStatusDone,
	})
	require.NoError(t, err)
	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		Type:   "team.completed",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"status": string(team.TeamStatusDone),
		},
	})
	require.NoError(t, err)
	_, err = store.AppendTeamEvent(ctx, team.TeamEvent{
		Type:   "team.summary",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"summary":        "policy fixture",
			"summary_source": "lead",
		},
	})
	require.NoError(t, err)
	return &Broker{TeamStore: store, WaitTimeoutPolicy: policy}, teamID
}

func executeWaitTeamPolicyCall(t *testing.T, broker *Broker, teamID string, args map[string]interface{}) (WaitTeamResult, map[string]interface{}, error) {
	t.Helper()
	callArgs := map[string]interface{}{"team_id": teamID}
	for key, value := range args {
		callArgs[key] = value
	}
	raw, meta, err := broker.Execute(context.Background(), "lead-session", ToolWaitTeam, callArgs)
	if err != nil {
		return WaitTeamResult{}, nil, err
	}
	result, ok := raw.(WaitTeamResult)
	require.True(t, ok)
	// Guard the fixture itself: if terminal state stopped being detected the
	// wait would block for the whole window instead of failing loudly.
	require.True(t, result.Terminal, "fixture team must report terminal state")
	return result, meta, nil
}

// wait_team must obey agents.maxWaitTimeoutMs exactly like wait_agent and
// read_agent_events: an oversized request is pinned and the result says so
// instead of silently holding the parent for hours.
func TestBrokerExecuteWaitTeamClampsToSharedMaximum(t *testing.T) {
	policy := agentcontrol.WaitTimeoutPolicy{MinMs: 1, MaxMs: 250}
	broker, teamID := waitTeamPolicyBroker(t, func() agentcontrol.WaitTimeoutPolicy { return policy })

	result, meta, err := executeWaitTeamPolicyCall(t, broker, teamID, map[string]interface{}{"timeout_ms": 7200000})
	require.NoError(t, err)
	assert.Equal(t, 250, result.WaitTimeoutMs)
	assert.Equal(t, 7200000, result.WaitTimeoutRequestedMs)
	assert.True(t, result.WaitTimeoutClamped)
	assert.False(t, result.TimedOut)
	require.NotNil(t, meta)
	assert.Equal(t, 250, meta["wait_timeout_ms"])
	assert.Equal(t, 7200000, meta["wait_timeout_requested_ms"])
	assert.Equal(t, true, meta["wait_timeout_clamped"])
}

// The shared default policy is agents.defaultWaitTimeoutMs=30000 with
// agents.minWaitTimeoutMs=10000 as the floor, so a sub-second request must be
// raised rather than honored by another wait tool.
func TestBrokerExecuteWaitTeamClampsToSharedMinimum(t *testing.T) {
	broker, teamID := waitTeamPolicyBroker(t, nil)

	result, meta, err := executeWaitTeamPolicyCall(t, broker, teamID, map[string]interface{}{"timeout_ms": 1000})
	require.NoError(t, err)
	assert.Equal(t, agentcontrol.MinWaitTimeoutMs, result.WaitTimeoutMs)
	assert.Equal(t, 1000, result.WaitTimeoutRequestedMs)
	assert.True(t, result.WaitTimeoutClamped)
	require.NotNil(t, meta)
	assert.Equal(t, agentcontrol.MinWaitTimeoutMs, meta["wait_timeout_ms"])
	assert.Equal(t, true, meta["wait_timeout_clamped"])
}

// An omitted timeout_ms means "host default", and the echo must stay
// distinguishable from an explicit request of the same value.
func TestBrokerExecuteWaitTeamWithoutTimeoutUsesSharedDefaultWindow(t *testing.T) {
	broker, teamID := waitTeamPolicyBroker(t, nil)

	result, meta, err := executeWaitTeamPolicyCall(t, broker, teamID, nil)
	require.NoError(t, err)
	assert.Equal(t, agentcontrol.DefaultWaitTimeoutMs, result.WaitTimeoutMs)
	assert.Equal(t, 0, result.WaitTimeoutRequestedMs)
	assert.False(t, result.WaitTimeoutClamped)
	require.NotNil(t, meta)
	assert.Equal(t, agentcontrol.DefaultWaitTimeoutMs, meta["wait_timeout_ms"])
	assert.Equal(t, 0, meta["wait_timeout_requested_ms"])
	assert.Equal(t, false, meta["wait_timeout_clamped"])
}

// A request inside [min,max] must be honored verbatim and must not be reported
// as clamped; false-positive clamp flags would train the model to ignore them.
func TestBrokerExecuteWaitTeamInRangeRequestIsNotClamped(t *testing.T) {
	policy := agentcontrol.WaitTimeoutPolicy{MinMs: 100, MaxMs: 1000}
	broker, teamID := waitTeamPolicyBroker(t, func() agentcontrol.WaitTimeoutPolicy { return policy })

	result, _, err := executeWaitTeamPolicyCall(t, broker, teamID, map[string]interface{}{"timeout_ms": 250})
	require.NoError(t, err)
	assert.Equal(t, 250, result.WaitTimeoutMs)
	assert.Equal(t, 250, result.WaitTimeoutRequestedMs)
	assert.False(t, result.WaitTimeoutClamped)
}

// agents.waitTimeoutMode=error must reject an out-of-range team wait with the
// same actionable message the agent waits use, and must reject it before any
// side effect: no result payload is produced at all.
func TestBrokerExecuteWaitTeamErrorModeRejectsOutOfRangeWindow(t *testing.T) {
	policy := agentcontrol.WaitTimeoutPolicy{Mode: agentcontrol.WaitTimeoutModeError}
	broker, teamID := waitTeamPolicyBroker(t, func() agentcontrol.WaitTimeoutPolicy { return policy })

	_, _, err := executeWaitTeamPolicyCall(t, broker, teamID, map[string]interface{}{"timeout_ms": 1000})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agents.minWaitTimeoutMs=10000")
	assert.Contains(t, err.Error(), "pass 0 to use the default")
	assert.Contains(t, err.Error(), "agents.waitTimeoutMode=clamp")

	_, _, err = executeWaitTeamPolicyCall(t, broker, teamID, map[string]interface{}{"timeout_ms": 7200000})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agents.maxWaitTimeoutMs=120000")
	assert.Contains(t, err.Error(), "agents.waitTimeoutMode=clamp")

	// A zero request is never out of range: error mode only rejects explicit
	// values it cannot honor.
	result, _, err := executeWaitTeamPolicyCall(t, broker, teamID, nil)
	require.NoError(t, err)
	assert.Equal(t, agentcontrol.DefaultWaitTimeoutMs, result.WaitTimeoutMs)
}

// The host owns the policy source, so the broker must re-read the provider on
// every call: flipping agents.waitTimeoutMode without restarting the session has
// to take effect on the next team wait.
func TestBrokerExecuteWaitTeamReadsPolicyProviderPerCall(t *testing.T) {
	policy := agentcontrol.WaitTimeoutPolicy{MinMs: 1, MaxMs: 250}
	broker, teamID := waitTeamPolicyBroker(t, func() agentcontrol.WaitTimeoutPolicy { return policy })

	result, _, err := executeWaitTeamPolicyCall(t, broker, teamID, map[string]interface{}{"timeout_ms": 60000})
	require.NoError(t, err)
	assert.Equal(t, 250, result.WaitTimeoutMs)
	assert.True(t, result.WaitTimeoutClamped)

	policy = agentcontrol.WaitTimeoutPolicy{Mode: agentcontrol.WaitTimeoutModeError, MinMs: 1, MaxMs: 250}
	_, _, err = executeWaitTeamPolicyCall(t, broker, teamID, map[string]interface{}{"timeout_ms": 60000})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agents.maxWaitTimeoutMs=250")
}
