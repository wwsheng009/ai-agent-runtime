package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestDefaultCapabilityResolverTreatsReportTaskOutcomeAsReadOnly(t *testing.T) {
	resolver := DefaultCapabilityResolver{}

	caps := resolver.Resolve(EvalRequest{
		ToolName: "report_task_outcome",
	})

	assert.Equal(t, []Capability{CapReadOnly}, caps)
}

// 规范名一律对齐**命名契约**（broker 暴露面口径 <domain>_<verb>）：legacy 拼写
// 必须归一到契约名；policy 内部的旧口径（spawn_agent/list_agents/... ）已退役。
func TestNormalizeToolNameMapsReportTaskOutcomeAliases(t *testing.T) {
	assert.Equal(t, "team_task_report", normalizeToolName("reporttaskoutcome"))
	assert.Equal(t, "team_task_report", normalizeToolName("report-task-outcome"))
	assert.Equal(t, "subagent_spawn", normalizeToolName("spawnagent"))
	assert.Equal(t, "subagent_list", normalizeToolName("listagents"))
	assert.Equal(t, "subagent_send", normalizeToolName("sendmessage"))
	assert.Equal(t, "subagent_followup", normalizeToolName("followuptask"))
	assert.Equal(t, "subagent_wait", normalizeToolName("wait-agent"))
	assert.Equal(t, "subagent_events", normalizeToolName("readagentevents"))
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

func TestReadOnlyChildCapabilitiesAllowsControlPlaneNotMutations(t *testing.T) {
	policy := NewCapabilityScopedToolExecutionPolicy(nil, ReadOnlyChildCapabilities())
	policy.ReadOnly = true

	for _, toolName := range []string{"view", "shell", "bash", "spawn_agent", "spawn_team", "ask_user_question", "enter_plan_mode", "send_message"} {
		if err := policy.AllowTool(toolName); err != nil {
			t.Fatalf("expected read-only child capabilities to allow %s: %v", toolName, err)
		}
	}
	for _, toolName := range []string{"write", "edit", "background_task"} {
		if err := policy.AllowTool(toolName); err == nil {
			t.Fatalf("expected read-only child capabilities to block %s", toolName)
		}
	}

	if err := policy.AllowToolCall(skill.ToolInfo{Name: "shell"}, map[string]interface{}{"command": "git status"}); err != nil {
		t.Fatalf("expected read-only shell git status to be allowed: %v", err)
	}
	if err := policy.AllowToolCall(skill.ToolInfo{Name: "shell"}, map[string]interface{}{"command": "rm -rf /"}); err == nil {
		t.Fatal("expected read-only shell mutating command to be blocked")
	}
	if err := policy.AllowToolCall(skill.ToolInfo{Name: "shell"}, map[string]interface{}{
		"command": "git status; git diff",
	}); err != nil {
		t.Fatalf("expected every-read-only compound to be allowed, got %v", err)
	}
	if err := policy.AllowToolCall(skill.ToolInfo{Name: "shell"}, map[string]interface{}{
		"command": "git status; rm -rf build",
	}); err == nil {
		t.Fatal("expected a compound with a mutating segment to be blocked")
	}
	if err := policy.AllowToolCall(skill.ToolInfo{Name: "shell"}, map[string]interface{}{
		"commands": []interface{}{
			map[string]interface{}{"command": "git status"},
			map[string]interface{}{"command": "git diff --stat"},
		},
	}); err != nil {
		t.Fatalf("expected structured read-only command batch to be allowed: %v", err)
	}
}
