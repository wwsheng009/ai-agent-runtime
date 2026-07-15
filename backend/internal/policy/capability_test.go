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

func TestDefaultCapabilityResolverSeparatesAgentManagementFromObservation(t *testing.T) {
	resolver := DefaultCapabilityResolver{}
	for _, toolName := range []string{"spawn_agent", "send_message", "followup_task", "send_input", "close_agent", "resume_agent"} {
		assert.Equal(t, []Capability{CapReadOnly, CapAgentManagement}, resolver.Resolve(EvalRequest{ToolName: toolName}))
	}
	for _, toolName := range []string{"list_agents", "wait_agent", "read_agent_events"} {
		assert.Equal(t, []Capability{CapReadOnly}, resolver.Resolve(EvalRequest{ToolName: toolName}))
	}
}
