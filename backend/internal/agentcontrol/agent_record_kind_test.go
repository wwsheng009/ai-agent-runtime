package agentcontrol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAgentRecordIsTeamTeammate pins the structural criteria for team rows.
//
// Regression guard for the AgentType overload: team identity rows are written
// with `firstNonEmptyString(mate.Profile, AgentTypeTeamTeammate)`, so a
// teammate with a profile stores that profile name (e.g. "researcher") in
// AgentType and NOT "team_teammate". Any `AgentType == AgentTypeTeamTeammate`
// check therefore misses every profiled teammate.
func TestAgentRecordIsTeamTeammate(t *testing.T) {
	// Regression row: profile lands in AgentType, so the old check returned false.
	profiled := AgentRecord{
		AgentID:    "mate-1",
		AgentPath:  "/root/team-a/mate-1",
		Depth:      2,
		AgentType:  "researcher",
		Workflow:   WorkflowSpawnTeam,
		TeamID:     "team-a",
		TeammateID: "member-1",
	}
	require.False(t,
		teammateByAgentTypeOnly(profiled),
		"fixture precondition: the AgentType-only check must miss a profiled teammate",
	)
	require.True(t, profiled.IsTeamTeammate())

	// Each structural signal is independently sufficient.
	require.True(t, AgentRecord{Workflow: WorkflowSpawnTeam}.IsTeamTeammate(),
		"spawn_team workflow alone identifies a team row")
	require.True(t, AgentRecord{TeamID: "team-a"}.IsTeamTeammate(),
		"a bound team id alone identifies a team row")
	require.True(t, AgentRecord{TeammateID: "member-1"}.IsTeamTeammate(),
		"a teammate id alone identifies a team row")

	// Legacy fallback: rows persisted before the workflow column existed.
	require.True(t, AgentRecord{AgentType: AgentTypeTeamTeammate}.IsTeamTeammate(),
		"historical rows without workflow/team ids must still be recognized")

	// Spawn_agent children and root must never be read as teammates.
	require.False(t, AgentRecord{
		AgentType: "general", Workflow: WorkflowSpawnAgent, Depth: 1,
	}.IsTeamTeammate(), "a spawn_agent child with an agent definition is not a teammate")
	require.False(t, AgentRecord{
		AgentType: AgentTypeChild, Depth: 1,
	}.IsTeamTeammate(), "a plain spawn_agent child is not a teammate")
	require.False(t, AgentRecord{AgentPath: "/root", AgentType: AgentTypeRoot}.IsTeamTeammate(),
		"the root row is not a teammate")
	require.False(t, AgentRecord{}.IsTeamTeammate(), "an empty row is not a teammate")
}

// teammateByAgentTypeOnly reproduces the removed buggy comparison so the test
// above stays a real regression guard rather than a tautology.
func teammateByAgentTypeOnly(r AgentRecord) bool {
	return r.AgentType == AgentTypeTeamTeammate
}

// TestAgentRecordIsRootAgent pins root detection across both structural
// signals: the root row hardcodes AgentType=root, and /root is the
// authoritative tree position (older rows may predate one of the two).
func TestAgentRecordIsRootAgent(t *testing.T) {
	require.True(t, AgentRecord{AgentPath: "/root", AgentType: AgentTypeRoot}.IsRootAgent())
	require.True(t, AgentRecord{AgentPath: "/root"}.IsRootAgent(),
		"the agent path alone identifies the root row")
	require.True(t, AgentRecord{AgentType: AgentTypeRoot}.IsRootAgent(),
		"the hardcoded agent type alone identifies the root row")

	require.False(t, AgentRecord{
		AgentPath: "/root/child-1", AgentType: AgentTypeChild, Depth: 1,
	}.IsRootAgent(), "a depth-1 child path is not root")
	require.False(t, AgentRecord{AgentType: "general", Workflow: WorkflowSpawnAgent}.IsRootAgent(),
		"a spawn_agent child type is not root")
	require.False(t, AgentRecord{AgentType: AgentTypeTeamTeammate}.IsRootAgent(),
		"a teammate is not root")
	require.False(t, AgentRecord{}.IsRootAgent())
}
