package chatcore

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestChatRequestClonePreservesCoreFields(t *testing.T) {
	req := NewChatRequest("analyze workspace")
	req.Provider = "CODEX_03"
	req.Model = "gpt-5.2-codex"
	req.ReasoningEffort = "high"
	budget := 8192
	req.Thinking = &types.ThinkingConfig{Type: "enabled", BudgetTokens: &budget}
	req.Profile = "planner"
	req.Agent = "lead"
	req.WorkspacePath = "E:\\projects\\ai\\ai-gateway"
	req.TeamID = "team-1"
	req.TaskID = "task-1"
	req.Stream = true
	req.History = append(req.History, *types.NewAssistantMessage("previous"))
	req.Context["workspace_path"] = req.WorkspacePath
	req.Options["planning_mode"] = "auto"
	req.Metadata.Set("source", "cli")

	cloned := req.Clone()
	if cloned == nil {
		t.Fatal("expected clone")
	}
	if cloned == req {
		t.Fatal("expected distinct clone instance")
	}
	if cloned.Provider != req.Provider || cloned.Model != req.Model {
		t.Fatalf("provider/model not preserved: %#v", cloned)
	}
	if cloned.ReasoningEffort != req.ReasoningEffort {
		t.Fatalf("reasoning effort not preserved: %#v", cloned)
	}
	if cloned.Thinking == nil || cloned.Thinking.Type != "enabled" {
		t.Fatalf("thinking not preserved: %#v", cloned.Thinking)
	}
	if cloned.Thinking == req.Thinking {
		t.Fatal("expected cloned thinking to be a deep copy")
	}
	if cloned.Profile != req.Profile || cloned.Agent != req.Agent {
		t.Fatalf("profile/agent not preserved: %#v", cloned)
	}
	if cloned.TeamID != req.TeamID || cloned.TaskID != req.TaskID {
		t.Fatalf("team/task not preserved: %#v", cloned)
	}
	if cloned.Context["workspace_path"] != req.WorkspacePath {
		t.Fatalf("context not preserved: %#v", cloned.Context)
	}
	if cloned.Options["planning_mode"] != "auto" {
		t.Fatalf("options not preserved: %#v", cloned.Options)
	}
	if len(cloned.History) != 1 || cloned.History[0].Content != "previous" {
		t.Fatalf("history not preserved: %#v", cloned.History)
	}
	if cloned.Metadata.GetString("source", "") != "cli" {
		t.Fatalf("metadata not preserved: %#v", cloned.Metadata)
	}
}

func TestChatResultSupportsOrchestrationAndPlanningPayloads(t *testing.T) {
	result := NewChatResult()
	result.Output = "final summary"
	result.TraceID = "trace-1"
	result.SessionID = "session-1"
	result.AgentID = "api-agent"
	result.Observations = []types.Observation{
		*types.NewObservation("step_1_tool_0", "spawn_team").MarkSuccess(),
	}
	result.Orchestration = &OrchestrationSummary{
		Source:        "agent_react",
		Success:       true,
		Steps:         2,
		ToolCallCount: 1,
	}
	result.Planning = &PlanningSummary{
		Attempted:         true,
		Source:            "planner",
		StepCount:         3,
		SubagentTaskCount: 2,
	}

	if result.Orchestration == nil || result.Orchestration.ToolCallCount != 1 {
		t.Fatalf("expected orchestration summary, got %#v", result.Orchestration)
	}
	if result.Planning == nil || result.Planning.SubagentTaskCount != 2 {
		t.Fatalf("expected planning summary, got %#v", result.Planning)
	}
	if len(result.Observations) != 1 || result.Observations[0].Tool != "spawn_team" {
		t.Fatalf("expected observations to be preserved: %#v", result.Observations)
	}
}

func TestChatEventTypesAreStable(t *testing.T) {
	events := []EventType{
		EventPlanning,
		EventSubagent,
		EventTool,
		EventResult,
		EventWarning,
	}

	expected := []string{"planning", "subagent", "tool", "result", "warning"}
	for i, eventType := range events {
		if string(eventType) != expected[i] {
			t.Fatalf("unexpected event type %d: got %q want %q", i, eventType, expected[i])
		}
	}
}
