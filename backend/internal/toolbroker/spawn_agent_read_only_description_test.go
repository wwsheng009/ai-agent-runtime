package toolbroker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// spawn_agent is defined twice (team-store branch and agent-sessions branch);
// both schemas must serve the same read_only contract text so the description
// cannot drift from the enforced boundary or from each other.
func TestSpawnAgentReadOnlyDescriptionMatchesPolicyConstant(t *testing.T) {
	brokers := map[string]*Broker{
		"agent_sessions": {AgentSessions: &fakeAgentSessionController{}},
		"team_store":     {AgentSessions: &fakeAgentSessionController{}, TeamStore: newTeamStore(t)},
	}
	for name, broker := range brokers {
		var description string
		for _, def := range broker.Definitions() {
			if def.Name != ToolSpawnAgent {
				continue
			}
			properties, ok := def.Parameters["properties"].(map[string]interface{})
			require.True(t, ok, "%s: spawn_agent properties missing", name)
			readOnly, ok := properties["read_only"].(map[string]interface{})
			require.True(t, ok, "%s: spawn_agent read_only property missing", name)
			description, _ = readOnly["description"].(string)
			break
		}
		require.NotEmpty(t, description, "%s: spawn_agent definition missing", name)
		require.True(t, strings.HasPrefix(description, runtimepolicy.ReadOnlyChildOptionDescription),
			"%s: read_only description must embed the shared contract, got %q", name, description)
	}
}
