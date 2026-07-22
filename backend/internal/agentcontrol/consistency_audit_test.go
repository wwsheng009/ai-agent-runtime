package agentcontrol

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuditAgentSessionConsistencyReportsActiveBindingDriftWithoutMutation(t *testing.T) {
	report, err := AuditAgentSessionConsistency(context.Background(), []AgentRecord{
		{AgentID: "active-missing", SessionID: "session-missing", Status: AgentStatusActive},
		{AgentID: "active-terminal", SessionID: "session-closed", Status: AgentStatusActive},
		{AgentID: "closed", SessionID: "session-old", Status: AgentStatusClosed},
	}, func(_ context.Context, sessionID string) (SessionBindingSnapshot, error) {
		if sessionID == "session-missing" {
			return SessionBindingSnapshot{SessionID: sessionID}, nil
		}
		return SessionBindingSnapshot{SessionID: sessionID, Exists: true, Closed: true, Status: "closed"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, report.RecordsChecked)
	require.Equal(t, 2, report.ActiveChecked)
	require.Equal(t, 2, report.IssueCount)
	require.Equal(t, "ACTIVE_AGENT_SESSION_MISSING", report.Issues[0].Code)
	require.Equal(t, "ACTIVE_AGENT_SESSION_TERMINAL", report.Issues[1].Code)
}

func TestAuditAgentSessionConsistencyDoesNotInventProviderOrBillingSemantics(t *testing.T) {
	report, err := AuditAgentSessionConsistency(context.Background(), []AgentRecord{{AgentID: "a", SessionID: "s", Status: AgentStatusActive}}, nil)
	require.NoError(t, err)
	require.Equal(t, "SESSION_LOOKUP_UNAVAILABLE", report.Issues[0].Code)
}
