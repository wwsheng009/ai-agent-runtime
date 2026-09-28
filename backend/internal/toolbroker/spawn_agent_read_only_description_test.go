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

// TestSpawnAgentReadOnlyDescriptionCarriesDispatchAdvice 钉住 doc1 §7.10 ②：
// 派发建议必须让父代理在**派发前**就知道只读子代理的 shell 面是逐段白名单
// （重定向/命令替换/动态展开一律拒绝），从而给"要跑命令"的子代理留
// read_only 未设置，而不是等子代理运行中反复被拒后再上浮重派。
func TestSpawnAgentReadOnlyDescriptionCarriesDispatchAdvice(t *testing.T) {
	for _, clause := range []string{
		"redirection, command substitution",
		"compound commands",
		"leave read_only unset for children that need writes or general shell syntax",
	} {
		require.Contains(t, runtimepolicy.ReadOnlyChildOptionDescription, clause,
			"read_only dispatch advice clause drifted from the enforced shell boundary")
	}
}
