package toolsurface

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type fakeBroker struct{}

func (fakeBroker) IsBrokerTool(name string) bool {
	switch name {
	case "job_start", "spawn_subagents", "team_spawn":
		return true
	}
	return false
}

func TestClassifyLabelsByPriority(t *testing.T) {
	ledger := Ledger{
		Broker:          fakeBroker{},
		CapabilityTools: map[string]bool{"get_goal": true, "update_goal": true},
	}
	cases := []struct {
		def  runtimetypes.ToolDefinition
		want SourceLabel
	}{
		{runtimetypes.ToolDefinition{Name: "shell"}, SourceCore},
		{runtimetypes.ToolDefinition{Name: "job_start"}, SourceOrchestration},
		{runtimetypes.ToolDefinition{Name: "get_goal"}, SourceCapability},
		{runtimetypes.ToolDefinition{Name: "take_snapshot", Metadata: map[string]interface{}{"mcp_name": "chrome-devtools"}}, SourceLabel("mcp:chrome-devtools")},
		// 包装层优先：goal 经 aicli_goal 包装时按包装层标注。
		{runtimetypes.ToolDefinition{Name: "get_goal", Metadata: map[string]interface{}{"mcp_name": "aicli_goal"}}, SourceLabel("mcp:aicli_goal")},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, ledger.Classify(tc.def), "tool %s", tc.def.Name)
	}
}

func TestClassifyWithoutBrokerKeepsCore(t *testing.T) {
	require.Equal(t, SourceCore, Ledger{}.Classify(runtimetypes.ToolDefinition{Name: "job_start"}))
}

func TestBuildDedupesSortsAndStampsLedger(t *testing.T) {
	catalog := []runtimetypes.ToolDefinition{
		{Name: "view", Description: "read"},
		{Name: "job_start", Description: "job"},
		{Name: "view", Description: "duplicate ignored"},
		{Name: "get_goal", Description: "goal"},
		{Name: " ", Description: "blank name dropped"},
	}
	tools, ok := Build(catalog, Ledger{
		Broker:          fakeBroker{},
		CapabilityTools: map[string]bool{"get_goal": true},
	})
	require.True(t, ok)
	require.Len(t, tools, 3)
	// 排序稳定（字典序）：get_goal < job_start < view。
	require.Equal(t, []string{"get_goal", "job_start", "view"}, []string{tools[0].Name, tools[1].Name, tools[2].Name})
	byName := make(map[string]runtimetypes.ToolDefinition, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	require.Equal(t, "orchestration", byName["job_start"].Metadata["surface_source"])
	require.Equal(t, "capability", byName["get_goal"].Metadata["surface_source"])
	require.Equal(t, "core", byName["view"].Metadata["surface_source"])
	// 去重保留首个定义的描述。
	require.Equal(t, "read", byName["view"].Description)
}

func TestBuildDoesNotOverwriteExistingLedger(t *testing.T) {
	catalog := []runtimetypes.ToolDefinition{
		{Name: "job_start", Metadata: map[string]interface{}{"surface_source": "frozen"}},
	}
	tools, ok := Build(catalog, Ledger{Broker: fakeBroker{}})
	require.True(t, ok)
	require.Equal(t, "frozen", tools[0].Metadata["surface_source"], "冻结面已带的账本不被覆盖")
}

func TestBuildEmptyCatalogReturnsNotOK(t *testing.T) {
	tools, ok := Build(nil, Ledger{Broker: fakeBroker{}})
	require.False(t, ok)
	require.Nil(t, tools)
}

func TestSummarizeSourcesPrefersFrozenLedger(t *testing.T) {
	catalog := []runtimetypes.ToolDefinition{
		{Name: "shell"},
		{Name: "job_start"},
		{Name: "view", Metadata: map[string]interface{}{"surface_source": "frozen"}},
	}
	sources := SummarizeSources(catalog, Ledger{Broker: fakeBroker{}})
	require.Equal(t, map[string]string{
		"shell":     "core",
		"job_start": "orchestration",
		"view":      "frozen",
	}, sources)
}

func TestSummarizeSourcesEmptyCatalog(t *testing.T) {
	require.Nil(t, SummarizeSources(nil, Ledger{Broker: fakeBroker{}}))
}
