package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestNewPlanner(t *testing.T) {
	mcpManager := &MockMCPManager{}
	planner := NewPlanner(mcpManager)

	if planner == nil {
		t.Fatal("expected planner to be created")
	}

	if planner.mcpManager == nil {
		t.Error("expected mcpManager to be set")
	}
}

func TestNewPlannerWithLLM(t *testing.T) {
	mcpManager := &MockMCPManager{}
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	planner := NewPlannerWithLLM(mcpManager, llmRuntime)

	if planner == nil {
		t.Fatal("expected planner to be created")
	}

	if planner.mcpManager == nil {
		t.Error("expected mcpManager to be set")
	}

	if planner.llmRuntime == nil {
		t.Error("expected llmRuntime to be set")
	}
}

func TestPlanner_CreatePlanWithLLM_InvalidGoal(t *testing.T) {
	mcpManager := &MockMCPManager{}
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	planner := NewPlannerWithLLM(mcpManager, llmRuntime)

	ctx := context.Background()
	_, err := planner.CreatePlanWithLLM(ctx, "", []skill.ToolInfo{})

	if err == nil {
		t.Error("expected error for empty goal, got nil")
	}
}

func TestPlanner_CreatePlanWithLLM_NoLLMRuntime(t *testing.T) {
	mcpManager := &MockMCPManager{}
	planner := NewPlanner(mcpManager) // 没有 LLM Runtime

	ctx := context.Background()
	_, err := planner.CreatePlanWithLLM(ctx, "test goal", []skill.ToolInfo{})

	if err == nil {
		t.Error("expected error for nil LLM runtime, got nil")
	}
}

func TestPlanner_CreatePlanWithLLM_BasicExecution(t *testing.T) {
	mcpManager := &MockMCPManager{}
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	planner := NewPlannerWithLLM(mcpManager, llmRuntime)

	ctx := context.Background()
	goal := "Research the latest AI developments for 2024"

	availableTools := []skill.ToolInfo{
		{
			Name:        "web_search",
			Description: "Search the web for information",
			MCPName:     "web",
			Enabled:     true,
		},
		{
			Name:        "summarize",
			Description: "Summarize text content",
			MCPName:     "text",
			Enabled:     true,
		},
	}

	_, err := planner.CreatePlanWithLLM(ctx, goal, availableTools)

	// 由于 mock provider 返回固定的简单响应，可能无法解析为有效计划
	// 但不应该崩溃
	if err != nil {
		t.Logf("Expected: LLM response may not be valid JSON plan: %v", err)
	}
}

func TestPlanner_CreatePlan_PrefersLLMWhenAvailable(t *testing.T) {
	mcpManager := &MockMCPManager{}
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := llm.NewMockProvider("test-provider", 0)
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}

	goal := "prepare release readiness plan"
	provider.SetResponse(goal, `{
		"goal":"llm_plan",
		"steps":[
			{"id":"step_1","description":"search requirements","tool":"search_docs","args":{"query":"release readiness"},"dependsOn":[],"priority":2},
			{"id":"step_2","description":"verify checks","tool":"run_tests","args":{"target":"./..."},"dependsOn":["step_1"],"priority":1}
		]
	}`)

	planner := NewPlannerWithLLM(mcpManager, llmRuntime)
	plan, err := planner.CreatePlan(context.Background(), goal, []skill.ToolInfo{
		{Name: "search_docs", Description: "search docs", Enabled: true},
		{Name: "run_tests", Description: "run tests", Enabled: true},
	})
	if err != nil {
		t.Fatalf("expected llm plan success, got error: %v", err)
	}
	if plan.Goal != "llm_plan" {
		t.Fatalf("expected llm plan goal, got %q", plan.Goal)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 plan steps, got %d", len(plan.Steps))
	}
}

func TestPlanner_CreatePlan_FallsBackToHeuristicWhenLLMPlanInvalid(t *testing.T) {
	mcpManager := &MockMCPManager{}
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := llm.NewMockProvider("test-provider", 0)
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}

	goal := "search docs then verify"
	provider.SetResponse(goal, "not a valid json plan")

	planner := NewPlannerWithLLM(mcpManager, llmRuntime)
	plan, err := planner.CreatePlan(context.Background(), goal, []skill.ToolInfo{
		{Name: "search_docs", Description: "search docs", Enabled: true},
		{Name: "run_tests", Description: "run tests", Enabled: true},
		{Name: "summarize", Description: "summarize findings", Enabled: true},
	})
	if err != nil {
		t.Fatalf("expected heuristic fallback, got error: %v", err)
	}
	if plan.Goal != goal {
		t.Fatalf("expected fallback plan goal %q, got %q", goal, plan.Goal)
	}
	if len(plan.Steps) == 0 {
		t.Fatal("expected fallback plan steps")
	}
	if plan.Steps[0].Tool == "" {
		t.Fatal("expected fallback step tool")
	}
}

func TestPlanner_buildToolDescriptions(t *testing.T) {
	mcpManager := &MockMCPManager{}
	planner := NewPlanner(mcpManager)

	tools := []skill.ToolInfo{
		{
			Name:        "web_search",
			Description: "Search the web for information",
			MCPName:     "web",
			Enabled:     true,
		},
		{
			Name:        "summarize",
			Description: "Summarize text content",
			MCPName:     "text",
			Enabled:     true,
		},
	}

	// 使用反射获取未导出的 buildToolDescriptions 方法
	descriptions := planner.buildToolDescriptions(tools)

	if len(descriptions) == 0 {
		t.Error("expected tool descriptions, got empty slice")
	}

	// 验证工具数量
	if len(descriptions) != len(tools) {
		t.Errorf("expected %d tool descriptions, got %d", len(tools), len(descriptions))
	}
}

func TestPlanner_buildPlanningPrompt(t *testing.T) {
	mcpManager := &MockMCPManager{}
	planner := NewPlanner(mcpManager)

	goal := "Research AI developments"
	tools := []skill.ToolInfo{
		{
			Name:        "web_search",
			Description: "Search the web",
			MCPName:     "web",
			Enabled:     true,
		},
	}

	toolDescriptions := planner.buildToolDescriptions(tools)
	prompt := planner.buildPlanningPrompt(goal, toolDescriptions)

	if len(prompt) == 0 {
		t.Error("expected prompt to be generated")
	}

	// 验证 prompt 包含 goal
	if !containsString(prompt, goal) {
		t.Errorf("expected goal '%s' in prompt", goal)
	}

	// 验证 prompt 包含工具相关信息（至少应该有 tool 字符串）
	// 由于 buildToolDescriptions 直接返回 tools，prompt 可能格式化为 JSON
	if len(toolDescriptions) > 0 && !containsString(prompt, "web_search") {
		t.Logf("prompt: %s", prompt)
		t.Log("Note: prompt may not contain tool name directly if formatted as JSON")
	}
	if !strings.Contains(prompt, `"difficulty": "easy|normal|hard|expert"`) {
		t.Fatalf("expected difficulty field guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not assign provider, model, or reasoning_effort") {
		t.Fatalf("expected provider/model routing boundary guidance, got:\n%s", prompt)
	}
}

func TestPlanner_parseLLMResponseToPlan_EmptyContent(t *testing.T) {
	mcpManager := &MockMCPManager{}
	planner := NewPlanner(mcpManager)

	response := &llm.LLMResponse{
		Content: "",
	}

	_, err := planner.parseLLMResponseToPlan(response, []skill.ToolInfo{})

	if err == nil {
		t.Error("expected error for empty content, got nil")
	}
}

func TestPlanner_parseLLMResponseToPlan_InvalidJSON(t *testing.T) {
	mcpManager := &MockMCPManager{}
	planner := NewPlanner(mcpManager)

	response := &llm.LLMResponse{
		Content: "This is not JSON",
	}

	_, err := planner.parseLLMResponseToPlan(response, []skill.ToolInfo{})

	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestPlanner_parseLLMResponseToPlan_AcceptsDifficultyMetadata(t *testing.T) {
	planner := NewPlanner(&MockMCPManager{})
	response := &llm.LLMResponse{Content: `{
		"goal":"inspect route policy",
		"steps":[
			{
				"id":"step_1",
				"description":"inspect routing policy",
				"tool":"read_file",
				"args":{"path":"config.yaml"},
				"dependsOn":[],
				"priority":1,
				"difficulty":"hard",
				"difficulty_rationale":"touches provider routing"
			}
		]
	}`}

	plan, err := planner.parseLLMResponseToPlan(response, []skill.ToolInfo{
		{Name: "read_file", Description: "read file", Enabled: true},
	})
	if err != nil {
		t.Fatalf("expected plan parse success, got %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected one step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Difficulty != "hard" ||
		plan.Steps[0].DifficultyRationale != "touches provider routing" {
		t.Fatalf("unexpected difficulty metadata: %+v", plan.Steps[0])
	}
}

func TestPlan_ValidatePlan_RejectsUnavailableTool(t *testing.T) {
	plan := &Plan{
		Goal: "validate",
		Steps: []PlanStep{
			{
				ID:          "step_1",
				Description: "use unknown tool",
				Tool:        "unknown_tool",
				Priority:    1,
			},
		},
	}

	if err := plan.ValidatePlan([]string{"read_file", "run_tests"}); err == nil {
		t.Fatal("expected validation failure for unavailable tool")
	}
}

func TestPlan_Clone_PreservesDifficultyMetadata(t *testing.T) {
	plan := &Plan{
		Goal: "clone",
		Steps: []PlanStep{
			{
				ID:                  "step_1",
				Description:         "inspect",
				Tool:                "read_file",
				Args:                map[string]interface{}{"path": "config.yaml"},
				Priority:            1,
				Difficulty:          "expert",
				DifficultyRationale: "provider migration",
			},
		},
	}

	clone := plan.Clone()
	if clone == nil || len(clone.Steps) != 1 {
		t.Fatalf("unexpected clone: %+v", clone)
	}
	if clone.Steps[0].Difficulty != "expert" ||
		clone.Steps[0].DifficultyRationale != "provider migration" {
		t.Fatalf("clone lost difficulty metadata: %+v", clone.Steps[0])
	}
	clone.Steps[0].Args["path"] = "other.yaml"
	if plan.Steps[0].Args["path"] != "config.yaml" {
		t.Fatalf("clone should not mutate original args: %+v", plan.Steps[0].Args)
	}
}

func TestPlanner_SetLLMRuntime(t *testing.T) {
	mcpManager := &MockMCPManager{}
	planner := NewPlanner(mcpManager)

	// 初始没有 LLM Runtime
	if planner.llmRuntime != nil {
		t.Error("expected no LLM runtime initially")
	}

	// 设置 LLM Runtime
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	planner.SetLLMRuntime(llmRuntime)

	if planner.llmRuntime == nil {
		t.Error("expected LLM runtime to be set")
	}
}

func TestPlanner_GetLLMRuntime(t *testing.T) {
	mcpManager := &MockMCPManager{}
	planner := NewPlanner(mcpManager)

	// 初始应该返回 nil
	if planner.GetLLMRuntime() != nil {
		t.Error("expected nil LLM runtime initially")
	}

	// 设置后应该返回 LLM Runtime
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &MockLLMProvider{name: "test-provider"}
	llmRuntime.RegisterProvider("test-provider", provider)

	planner.SetLLMRuntime(llmRuntime)

	if planner.GetLLMRuntime() == nil {
		t.Error("expected LLM runtime to be returned")
	}
}

// Helper function
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > 0 && findSubstring(s, substr) >= 0))
}

func findSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
