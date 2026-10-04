package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// TestInFlightLocalChildSessionsExcludesProfiledTeammates is the regression
// guard for the team-identification bug in inFlightLocalChildSessions.
//
// Team identity rows are persisted with
// `firstNonEmptyString(mate.Profile, AgentTypeTeamTeammate)`, so a teammate
// configured with a profile stores e.g. "researcher" in AgentType — never
// "team_teammate". The removed `AgentType == AgentTypeTeamTeammate` check
// therefore let profiled teammates into the in-flight child list, which parks
// the parent turn waiting for a Team worker that the Team orchestrator owns.
func TestInFlightLocalChildSessionsExcludesProfiledTeammates(t *testing.T) {
	host := newLocalAgentObligationTestHost(t, "teammate")
	ctx := context.Background()
	root := host.BaseSession.RuntimeSession.ID

	seed := func(record agentcontrol.AgentRecord) {
		t.Helper()
		_, err := host.AgentRegistryStore.UpsertAgentControlAgent(ctx, record)
		require.NoError(t, err)
	}

	// Root row for the tree itself.
	seed(agentcontrol.AgentRecord{
		AgentID: "root", SessionID: root, RootSessionID: root,
		AgentPath: "/root", Depth: 0, AgentType: agentcontrol.AgentTypeRoot,
		Status: agentcontrol.AgentStatusActive,
	})

	// A real spawn_agent child: must stay in the in-flight list.
	seed(agentcontrol.AgentRecord{
		AgentID: "child-1", SessionID: "child-1", RootSessionID: root,
		AgentPath: "/root/child-1", Depth: 1, AgentType: "general",
		Workflow: agentcontrol.WorkflowSpawnAgent, Status: agentcontrol.AgentStatusActive,
	})

	// The regression row: a teammate whose profile overwrote AgentType.
	seed(agentcontrol.AgentRecord{
		AgentID: "mate-1", SessionID: "mate-1", RootSessionID: root,
		AgentPath: "/root/mate-1", Depth: 1, AgentType: "researcher",
		Workflow: agentcontrol.WorkflowSpawnTeam, TeamID: "team-a", TeammateID: "member-1",
		Status: agentcontrol.AgentStatusActive,
	})

	// A teammate without a profile (the only shape the old check caught).
	seed(agentcontrol.AgentRecord{
		AgentID: "mate-2", SessionID: "mate-2", RootSessionID: root,
		AgentPath: "/root/mate-2", Depth: 1, AgentType: agentcontrol.AgentTypeTeamTeammate,
		Workflow: agentcontrol.WorkflowSpawnTeam, TeamID: "team-a", TeammateID: "member-2",
		Status: agentcontrol.AgentStatusActive,
	})

	inFlight := host.inFlightLocalChildSessions(ctx, root)
	require.Equal(t, []string{"child-1"}, inFlight,
		"only the spawn_agent child is in flight; teammates are owned by the Team orchestrator")
}
