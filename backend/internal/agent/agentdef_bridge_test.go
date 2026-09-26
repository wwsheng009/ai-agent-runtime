package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeAgentBridgeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestApplyAgentdefTaskDefaultsFillsOmittedFields(t *testing.T) {
	projectRoot := t.TempDir()
	writeAgentBridgeTestFile(t, filepath.Join(projectRoot, ".agents", "agents"), "reviewer.md", `---
name: reviewer
description: Reviews diffs
tools: ["view", "grep"]
model: def-model
provider: def-provider
reasoningEffort: high
maxTurns: 7
completionRequirement: complete_task
sandbox: read-only
---
Review.
`)
	parent := NewAgent(&Config{
		Name:    "parent",
		Options: map[string]interface{}{AgentdefProjectRootOptionKey: projectRoot},
	}, nil)

	task := applyAgentdefTaskDefaults(parent, SubagentTask{AgentType: "reviewer"})
	require.True(t, task.ReadOnly, "read-only must inherit from the definition sandbox")
	require.Equal(t, "agentdef:reviewer", task.ReadOnlySource)
	require.ElementsMatch(t, []string{"view", "grep"}, task.ToolsWhitelist)
	require.Equal(t, "def-model", task.Model)
	require.Equal(t, "def-provider", task.Provider)
	require.Equal(t, "high", task.ReasoningEffort)
	require.Equal(t, 7, task.MaxTurns)
	require.Equal(t, "complete_task", task.CompletionRequirement)
}

func TestApplyAgentdefTaskDefaultsExplicitFieldsWin(t *testing.T) {
	projectRoot := t.TempDir()
	writeAgentBridgeTestFile(t, filepath.Join(projectRoot, ".agents", "agents"), "reviewer.md", `---
name: reviewer
description: Reviews diffs
tools: ["view", "grep"]
model: def-model
maxTurns: 7
sandbox: read-only
---
Review.
`)
	parent := NewAgent(&Config{
		Name:    "parent",
		Options: map[string]interface{}{AgentdefProjectRootOptionKey: projectRoot},
	}, nil)

	task := applyAgentdefTaskDefaults(parent, SubagentTask{
		AgentType:       "reviewer",
		Model:           "explicit-model",
		MaxTurns:        3,
		ToolsWhitelist:  []string{"view"},
		RouteWarnings:   []string{"keep-me"},
		ReasoningEffort: "low",
	})
	require.Equal(t, "explicit-model", task.Model)
	require.Equal(t, 3, task.MaxTurns)
	require.Equal(t, []string{"view"}, task.ToolsWhitelist)
	require.Equal(t, "low", task.ReasoningEffort)
	require.True(t, task.ReadOnly)
}

func TestApplyAgentdefTaskDefaultsUnknownTypeWarns(t *testing.T) {
	parent := NewAgent(&Config{
		Name:    "parent",
		Options: map[string]interface{}{AgentdefProjectRootOptionKey: t.TempDir()},
	}, nil)
	task := applyAgentdefTaskDefaults(parent, SubagentTask{AgentType: "does-not-exist"})
	require.False(t, task.ReadOnly)
	require.Contains(t, task.RouteWarnings, "agent_type_not_found:does-not-exist")
}

func TestDecodeSubagentTasksAcceptsAgentTypeAndMaxTurns(t *testing.T) {
	tasks, err := decodeSubagentTasks(map[string]interface{}{
		"agents": []interface{}{
			map[string]interface{}{
				"goal":       "review the diff",
				"agent_type": "reviewer",
				"max_turns":  float64(5),
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "reviewer", tasks[0].AgentType)
	require.Equal(t, 5, tasks[0].MaxTurns)
}

func TestSpawnSubagentsSchemaAdvertisesAgentTypeAndMaxTurns(t *testing.T) {
	definition := spawnSubagentsToolDefinition(true, "")
	properties, ok := definition.Parameters["properties"].(map[string]interface{})
	require.True(t, ok)
	agents, ok := properties["agents"].(map[string]interface{})
	require.True(t, ok)
	items, ok := agents["items"].(map[string]interface{})
	require.True(t, ok)
	itemProps, ok := items["properties"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, itemProps, "agent_type")
	require.Contains(t, itemProps, "max_turns")
	require.Contains(t, itemProps, "tools_whitelist")

	withCatalog := spawnSubagentsToolDefinition(true, "- explore: Read-only codebase explorer (builtin, read-only)")
	require.Contains(t, withCatalog.Description, "- explore: Read-only codebase explorer")
}
