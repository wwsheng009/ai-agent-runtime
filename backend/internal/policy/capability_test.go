package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultCapabilityResolverTreatsReportTaskOutcomeAsReadOnly(t *testing.T) {
	resolver := DefaultCapabilityResolver{}

	caps := resolver.Resolve(EvalRequest{
		ToolName: "report_task_outcome",
	})

	assert.Equal(t, []Capability{CapReadOnly}, caps)
}

func TestNormalizeToolNameMapsReportTaskOutcomeAliases(t *testing.T) {
	assert.Equal(t, "report_task_outcome", normalizeToolName("reporttaskoutcome"))
	assert.Equal(t, "report_task_outcome", normalizeToolName("report-task-outcome"))
	assert.Equal(t, "spawn_agent", normalizeToolName("spawnagent"))
	assert.Equal(t, "list_agents", normalizeToolName("listagents"))
	assert.Equal(t, "send_message", normalizeToolName("sendmessage"))
	assert.Equal(t, "followup_task", normalizeToolName("followuptask"))
	assert.Equal(t, "wait_agent", normalizeToolName("wait-agent"))
	assert.Equal(t, "read_agent_events", normalizeToolName("readagentevents"))
}

func TestDefaultCapabilityResolverTreatsLightAgentToolsAsReadOnly(t *testing.T) {
	resolver := DefaultCapabilityResolver{}
	for _, toolName := range []string{"spawn_agent", "list_agents", "send_message", "followup_task", "send_input", "wait_agent", "read_agent_events", "close_agent", "resume_agent"} {
		assert.Equal(t, []Capability{CapReadOnly}, resolver.Resolve(EvalRequest{ToolName: toolName}))
	}
}
