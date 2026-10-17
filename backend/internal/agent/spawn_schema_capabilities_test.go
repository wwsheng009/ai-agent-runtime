package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSpawnSubagentsSchemaFollowsSchedulerCapabilities 锁定 P1-1' 三层一致的
// 第三层：模型可见 schema 必须与调度器装配的部署能力位同源。历史缺陷是
// loop/tool_list 两处 schema 构造传静态 DefaultCapabilities()，部署通过
// agents.forkContextEnabled / outputSchemaEnabled 授予能力后 context /
// report_schema 字段仍永远不可见（真实终端验证 E 的现场缺陷）。
func TestSpawnSubagentsSchemaFollowsSchedulerCapabilities(t *testing.T) {
	agentItemProperties := func(t *testing.T, schedulerCaps *SubagentCapabilities) map[string]interface{} {
		t.Helper()
		apiAgent := NewAgent(&Config{Name: "schema-caps"}, nil)
		apiAgent.SetSubagentScheduler(NewSubagentScheduler(apiAgent, SubagentSchedulerConfig{Capabilities: schedulerCaps}))
		loop := NewReActLoop(apiAgent, nil, &LoopReActConfig{})
		tools, _, err := loop.computeAvailableTools(context.Background(), "delegate research", nil, false)
		require.NoError(t, err)
		for _, tool := range tools {
			if tool.Name != SpawnSubagentsToolName {
				continue
			}
			properties, _ := tool.Parameters["properties"].(map[string]interface{})
			agents, _ := properties["agents"].(map[string]interface{})
			items, _ := agents["items"].(map[string]interface{})
			itemProps, _ := items["properties"].(map[string]interface{})
			require.NotEmpty(t, itemProps, "subagent_spawn_batch agents item schema must have properties")
			return itemProps
		}
		t.Fatalf("%s must be exposed when no restrictive policy is set", SpawnSubagentsToolName)
		return nil
	}

	t.Run("default caps keep the new fields out of the schema", func(t *testing.T) {
		props := agentItemProperties(t, nil)
		if _, ok := props["context"]; ok {
			t.Fatal("context field must stay out of the schema until fork_context is granted")
		}
		if _, ok := props["report_schema"]; ok {
			t.Fatal("report_schema must stay out of the schema until output schema is granted")
		}
	})

	t.Run("granted caps surface context and report_schema", func(t *testing.T) {
		caps := DefaultCapabilities()
		caps.ForkContext = true
		caps.OutputSchema = true
		props := agentItemProperties(t, &caps)

		contextField, ok := props["context"].(map[string]interface{})
		require.True(t, ok, "granted fork_context must surface the context field")
		enum, _ := contextField["enum"].([]string)
		require.Contains(t, enum, SubagentContextFresh)
		require.Contains(t, enum, SubagentContextFork)

		if _, ok := props["report_schema"]; !ok {
			t.Fatal("granted output schema must surface the report_schema field")
		}
	})
}
