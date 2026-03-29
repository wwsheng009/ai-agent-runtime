package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
)

func TestAgent_Orchestrate_RoutePreferredUsesSkill(t *testing.T) {
	mockProvider := llm.NewMockProvider("mock", 0)
	runtime := llm.NewLLMRuntime(nil)
	if err := runtime.RegisterProvider("mock", mockProvider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Model:            "mock",
		DefaultMaxTokens: 256,
	}, nil, runtime)

	if err := agent.RegisterSkill(&skill.Skill{
		Name:        "search_docs",
		Description: "Search docs",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"search", "docs"}, Weight: 1},
		},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			return types.NewResult(true, "SKILL_OK").WithSkill("search_docs"), nil
		}),
	}); err != nil {
		t.Fatalf("register skill failed: %v", err)
	}

	result, err := agent.Orchestrate(context.Background(), &OrchestrationRequest{
		Prompt: "search docs for me",
		Mode:   OrchestrationRoutePreferred,
	})
	if err != nil {
		t.Fatalf("orchestrate failed: %v", err)
	}
	if result.Source != "agent_route" {
		t.Fatalf("expected agent_route, got %s", result.Source)
	}
	if result.AgentResult == nil || result.AgentResult.Output != "SKILL_OK" {
		t.Fatalf("unexpected agent result: %+v", result.AgentResult)
	}
	if len(result.CapabilityCandidates) == 0 {
		t.Fatal("expected capability candidates")
	}
}

func TestAgent_Orchestrate_RoutePreferredFallsBackToLLM(t *testing.T) {
	mockProvider := llm.NewMockProvider("mock", 0)
	mockProvider.SetResponse("just chat", "LLM_OK")
	runtime := llm.NewLLMRuntime(nil)
	if err := runtime.RegisterProvider("mock", mockProvider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Model:            "mock",
		DefaultMaxTokens: 256,
		Temperature:      0.2,
	}, nil, runtime)

	result, err := agent.Orchestrate(context.Background(), &OrchestrationRequest{
		Prompt: "just chat",
		Mode:   OrchestrationRoutePreferred,
	})
	if err != nil {
		t.Fatalf("orchestrate failed: %v", err)
	}
	if result.Source != "llm_fallback" {
		t.Fatalf("expected llm_fallback, got %s", result.Source)
	}
	if result.LLMResponse == nil || result.LLMResponse.Content != "LLM_OK" {
		t.Fatalf("unexpected llm response: %+v", result.LLMResponse)
	}
}

func TestAgent_CapabilityDescriptors(t *testing.T) {
	agent := NewAgent(&Config{
		Name:             "test-agent",
		Model:            "mock",
		DefaultMaxTokens: 256,
	}, nil)

	if err := agent.RegisterSkill(&skill.Skill{
		Name:        "search_docs",
		Description: "Search docs",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"search"}, Weight: 1},
		},
	}); err != nil {
		t.Fatalf("register skill failed: %v", err)
	}

	descriptors := agent.CapabilityDescriptors()
	if len(descriptors) < 2 {
		t.Fatalf("expected agent + skill descriptors, got %d", len(descriptors))
	}
	if descriptors[0].Kind != "agent" {
		t.Fatalf("expected first descriptor to be agent, got %s", descriptors[0].Kind)
	}
}

func TestAgent_Orchestrate_PassesContextToSkillRequest(t *testing.T) {
	agent := NewAgent(&Config{
		Name:             "test-agent",
		Model:            "mock",
		DefaultMaxTokens: 256,
	}, nil)

	if err := agent.RegisterSkill(&skill.Skill{
		Name:        "search_docs",
		Description: "Search docs",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"search"}, Weight: 1},
		},
		Handler: skill.SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
			if req.Context["workspace_path"] != "/tmp/workspace" {
				return types.NewResult(false, "bad_context"), nil
			}
			return types.NewResult(true, "CTX_OK").WithSkill("search_docs"), nil
		}),
	}); err != nil {
		t.Fatalf("register skill failed: %v", err)
	}

	result, err := agent.Orchestrate(context.Background(), &OrchestrationRequest{
		Prompt: "search docs",
		Mode:   OrchestrationRoutePreferred,
		Context: map[string]interface{}{
			"workspace_path": "/tmp/workspace",
		},
	})
	if err != nil {
		t.Fatalf("orchestrate failed: %v", err)
	}
	if result.AgentResult == nil || result.AgentResult.Output != "CTX_OK" {
		t.Fatalf("unexpected agent result: %+v", result.AgentResult)
	}
}

func TestAgent_Orchestrate_UsesWorkspaceSummaryInLLMMode(t *testing.T) {
	mockProvider := llm.NewMockProvider("mock", 0)
	mockProvider.SetResponse("hello", "LLM_OK")
	runtime := llm.NewLLMRuntime(nil)
	if err := runtime.RegisterProvider("mock", mockProvider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Model:            "mock",
		DefaultMaxTokens: 256,
	}, nil, runtime)

	result, err := agent.Orchestrate(context.Background(), &OrchestrationRequest{
		Prompt: "hello",
		Mode:   OrchestrationLLMOnly,
		Workspace: &workspace.WorkspaceContext{
			Query:   "hello",
			Summary: `query="hello" files=1 symbols=1 chunks=1 top_symbols=Hello`,
		},
	})
	if err != nil {
		t.Fatalf("orchestrate failed: %v", err)
	}
	if result.LLMResponse == nil || result.LLMResponse.Content != "LLM_OK" {
		t.Fatalf("unexpected llm response: %+v", result.LLMResponse)
	}
}

func TestAgent_Orchestrate_BuildsWorkspaceContextExternally(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(file, []byte(`package demo
func SearchDocs() {}
`), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	scanner := workspace.NewScanner(nil)
	scan, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	ctx := workspace.NewContextBuilder(scan, nil).Build("search docs")
	if ctx == nil || ctx.Summary == "" {
		t.Fatalf("expected workspace context summary, got %+v", ctx)
	}
}

func TestBuildSubagentTasksFromPlan_WriterVerifierGraph(t *testing.T) {
	plan := &Plan{
		Goal: "Implement feature and verify it",
		Steps: []PlanStep{
			{
				ID:          "step_write",
				Description: "Write the implementation changes",
				Tool:        "write_file",
				Priority:    1,
			},
			{
				ID:          "step_verify",
				Description: "Run tests to verify the implementation",
				Tool:        "run_tests",
				DependsOn:   []string{"step_write"},
				Priority:    2,
			},
		},
	}

	tasks := BuildSubagentTasksFromPlan(plan)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 subagent tasks, got %d", len(tasks))
	}
	if tasks[0].Role != "writer" {
		t.Fatalf("expected first task to be writer, got %s", tasks[0].Role)
	}
	if tasks[1].Role != "verifier" {
		t.Fatalf("expected second task to be verifier, got %s", tasks[1].Role)
	}
	if len(tasks[1].DependsOn) != 1 || tasks[1].DependsOn[0] != "step_write" {
		t.Fatalf("expected verifier to depend on writer, got %+v", tasks[1].DependsOn)
	}
}

func TestValidatePlannedSubagentExecution_BlocksMultipleWriters(t *testing.T) {
	err := ValidatePlannedSubagentExecution([]SubagentTask{
		{ID: "writer_1", Role: "writer", ReadOnly: false, ToolsWhitelist: []string{"write_file"}},
		{ID: "writer_2", Role: "writer", ReadOnly: false, ToolsWhitelist: []string{"edit_file"}},
	}, nil, true)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "only one writer") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePlannedSubagentExecution_BlocksWriterWithoutApproval(t *testing.T) {
	err := ValidatePlannedSubagentExecution([]SubagentTask{
		{ID: "writer_1", Role: "writer", ReadOnly: false, ToolsWhitelist: []string{"write_file"}},
		{ID: "verifier_1", Role: "verifier", ReadOnly: true, ToolsWhitelist: []string{"run_tests"}, DependsOn: []string{"writer_1"}},
	}, nil, false)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "requires explicit write approval") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAgent_Orchestrate_PlannerPreferredIncludesSubagentTasks(t *testing.T) {
	agent := NewAgent(&Config{
		Name:             "test-agent",
		Model:            "mock",
		DefaultMaxTokens: 256,
	}, &workflowPlanningMCPManager{})

	if err := agent.RegisterSkill(&skill.Skill{
		Name:        "planned-skill",
		Description: "workflow with write and verify",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"plan", "verify"}, Weight: 1},
		},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify with tests", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}); err != nil {
		t.Fatalf("register skill failed: %v", err)
	}

	result, err := agent.Orchestrate(context.Background(), &OrchestrationRequest{
		Prompt: "plan verify work",
		Mode:   OrchestrationPlannerPreferred,
	})
	if err != nil {
		t.Fatalf("orchestrate failed: %v", err)
	}
	if result.Plan == nil {
		t.Fatal("expected plan")
	}
	if len(result.SubagentTasks) != 2 {
		t.Fatalf("expected 2 subagent tasks, got %d", len(result.SubagentTasks))
	}
	if result.SubagentTasks[0].Role != "writer" {
		t.Fatalf("expected writer task, got %s", result.SubagentTasks[0].Role)
	}
	if result.SubagentTasks[1].Role != "verifier" {
		t.Fatalf("expected verifier task, got %s", result.SubagentTasks[1].Role)
	}
}

func TestAgent_Orchestrate_PlannerPreferredCanExecuteSubagents(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Run verifier.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "run_tests", Args: map[string]interface{}{"target": "./..."}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Model:            "test-provider",
		DefaultMaxTokens: 256,
		MaxSteps:         3,
	}, &workflowPlanningMCPManager{}, llmRuntime)

	if err := agent.RegisterSkill(&skill.Skill{
		Name:        "planned-skill",
		Description: "workflow with write and verify",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"plan", "verify"}, Weight: 1},
		},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify with tests", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}); err != nil {
		t.Fatalf("register skill failed: %v", err)
	}

	result, err := agent.Orchestrate(context.Background(), &OrchestrationRequest{
		Prompt:                     "plan verify work",
		Mode:                       OrchestrationPlannerPreferred,
		ExecutePlannedSubagents:    true,
		AllowWritePlannedSubagents: true,
	})
	if err != nil {
		t.Fatalf("orchestrate failed: %v", err)
	}
	if result.Source != "agent_planned_subagents" {
		t.Fatalf("expected agent_planned_subagents, got %s", result.Source)
	}
	if !result.SubagentExecutionAttempted {
		t.Fatal("expected subagent execution attempt")
	}
	if len(result.SubagentResults) != 2 {
		t.Fatalf("expected 2 subagent results, got %d", len(result.SubagentResults))
	}
	if result.PatchDecision != "approved" {
		t.Fatalf("expected patch decision approved, got %q", result.PatchDecision)
	}
	if !result.PatchDecisionRequired {
		t.Fatal("expected patch decision to be required")
	}
	if result.AgentResult == nil || result.AgentResult.Output == "" {
		t.Fatalf("expected synthetic agent result, got %+v", result.AgentResult)
	}
	if result.AgentResult.TraceID == "" {
		t.Fatal("expected trace id on synthetic agent result")
	}
}

func TestAgent_Orchestrate_PlannerPreferredPatchDecisionBlockedWhenVerifierFails(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Try writable verifier action.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/verify.txt", "content": "should fail"}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Model:            "test-provider",
		DefaultMaxTokens: 256,
		MaxSteps:         3,
	}, &workflowPlanningMCPManager{}, llmRuntime)

	if err := agent.RegisterSkill(&skill.Skill{
		Name:        "planned-skill-blocked",
		Description: "workflow with write and verify",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"plan", "verify", "blocked"}, Weight: 1},
		},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify with tests", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}); err != nil {
		t.Fatalf("register skill failed: %v", err)
	}

	result, err := agent.Orchestrate(context.Background(), &OrchestrationRequest{
		Prompt:                     "plan verify blocked",
		Mode:                       OrchestrationPlannerPreferred,
		ExecutePlannedSubagents:    true,
		AllowWritePlannedSubagents: true,
	})
	if err != nil {
		t.Fatalf("orchestrate failed: %v", err)
	}
	if result.Source != "agent_planned_subagents" {
		t.Fatalf("expected agent_planned_subagents, got %s", result.Source)
	}
	if result.PatchDecision != "blocked" {
		t.Fatalf("expected patch decision blocked, got %q", result.PatchDecision)
	}
	if !result.PatchDecisionRequired {
		t.Fatal("expected patch decision to be required")
	}
	if !strings.Contains(result.SubagentExecutionBlockedReason, "requires manual review") {
		t.Fatalf("expected blocked reason to mention manual review, got %q", result.SubagentExecutionBlockedReason)
	}
	if result.AgentResult == nil {
		t.Fatal("expected agent result")
	}
	if result.AgentResult.Success {
		t.Fatal("expected agent result success=false when patch decision is blocked")
	}
	if !strings.Contains(result.AgentResult.Error, "requires manual review") {
		t.Fatalf("expected agent error to mention manual review, got %q", result.AgentResult.Error)
	}
}

func TestAgent_Orchestrate_PlannerPreferredPatchDecisionWarnPolicy(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Attempt writable verifier action.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/verify.txt", "content": "blocked"}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Model:            "test-provider",
		DefaultMaxTokens: 256,
		MaxSteps:         3,
	}, &workflowPlanningMCPManager{}, llmRuntime)

	if err := agent.RegisterSkill(&skill.Skill{
		Name:        "planned-skill-warn",
		Description: "workflow with write and verify",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"plan", "warn"}, Weight: 1},
		},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify with tests", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}); err != nil {
		t.Fatalf("register skill failed: %v", err)
	}

	result, err := agent.Orchestrate(context.Background(), &OrchestrationRequest{
		Prompt:                     "plan warn",
		Mode:                       OrchestrationPlannerPreferred,
		ExecutePlannedSubagents:    true,
		AllowWritePlannedSubagents: true,
		PatchDecisionPolicy:        PatchDecisionPolicyWarn,
	})
	if err != nil {
		t.Fatalf("orchestrate failed: %v", err)
	}
	if result.PatchDecision != "blocked" {
		t.Fatalf("expected patch decision blocked, got %q", result.PatchDecision)
	}
	if result.AgentResult == nil || !result.AgentResult.Success {
		t.Fatalf("expected warn policy to keep agent result successful, got %+v", result.AgentResult)
	}
	if result.SubagentExecutionBlockedReason != "" {
		t.Fatalf("expected no blocked reason under warn policy, got %q", result.SubagentExecutionBlockedReason)
	}
}

func TestAgent_Orchestrate_PlannerPreferredPatchDecisionManualOverrideObject(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Write implementation.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/out.txt", "content": "hello"}},
				},
			},
			{Content: "Writer done.", Model: "test-model"},
			{
				Content: "Attempt writable verifier action.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{Name: "write_file", Args: map[string]interface{}{"path": "workspace/verify.txt", "content": "blocked"}},
				},
			},
			{Content: "Verifier done.", Model: "test-model"},
		},
	}
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}

	agent := NewAgentWithLLM(&Config{
		Name:             "test-agent",
		Model:            "test-provider",
		DefaultMaxTokens: 256,
		MaxSteps:         3,
	}, &workflowPlanningMCPManager{}, llmRuntime)

	if err := agent.RegisterSkill(&skill.Skill{
		Name:        "planned-skill-override",
		Description: "workflow with write and verify",
		Triggers: []skill.Trigger{
			{Type: "keyword", Values: []string{"plan", "override"}, Weight: 1},
		},
		Workflow: &skill.Workflow{Steps: []skill.WorkflowStep{
			{ID: "step_write", Name: "write implementation", Tool: "write_file"},
			{ID: "step_verify", Name: "verify with tests", Tool: "run_tests", DependsOn: []string{"step_write"}},
		}},
	}); err != nil {
		t.Fatalf("register skill failed: %v", err)
	}

	result, err := agent.Orchestrate(context.Background(), &OrchestrationRequest{
		Prompt:                     "plan override",
		Mode:                       OrchestrationPlannerPreferred,
		ExecutePlannedSubagents:    true,
		AllowWritePlannedSubagents: true,
		PatchApproval: &PatchApproval{
			Approved: true,
			TicketID: "CAB-123",
			Approver: "ops-reviewer",
			Reason:   "human reviewed",
		},
	})
	if err != nil {
		t.Fatalf("orchestrate failed: %v", err)
	}
	if result.PatchDecision != "approved_override" {
		t.Fatalf("expected patch decision approved_override, got %q", result.PatchDecision)
	}
	if !result.PatchDecisionOverrideApplied {
		t.Fatal("expected override to be applied")
	}
	if result.PatchApproval == nil || result.PatchApproval.TicketID != "CAB-123" {
		t.Fatalf("expected patch approval ticket, got %+v", result.PatchApproval)
	}
	if result.AgentResult == nil || !result.AgentResult.Success {
		t.Fatalf("expected override to keep agent result successful, got %+v", result.AgentResult)
	}
}

func TestNewDefaultContextManager_UsesProfileAndOverrides(t *testing.T) {
	manager := newDefaultContextManager(&Config{
		Options: map[string]interface{}{
			"context_profile":                 contextmgr.BudgetProfileCompact,
			"context_compaction_mode":         contextmgr.CompactionModeLedgerPreferred,
			"context_recall_mode":             contextmgr.RecallModeBroad,
			"context_observation_mode":        contextmgr.ObservationModeAll,
			"context_min_compaction_messages": 3,
			"context_min_recall_query_length": 15,
			"context_ledger_load_limit":       9,
			"context_max_messages":            18,
			"context_max_recall_results":      6,
			"context_max_observation_items":   7,
			"context_keep_recent_messages":    4,
			"context_max_prompt_tokens":       9000,
		},
	}, nil)
	if manager == nil {
		t.Fatal("expected context manager")
	}
	if manager.Budget.MaxPromptTokens != 9000 {
		t.Fatalf("expected max prompt tokens 9000, got %d", manager.Budget.MaxPromptTokens)
	}
	if manager.Budget.MaxMessages != 18 {
		t.Fatalf("expected max messages 18, got %d", manager.Budget.MaxMessages)
	}
	if manager.Budget.KeepRecentMessages != 4 {
		t.Fatalf("expected keep recent messages 4, got %d", manager.Budget.KeepRecentMessages)
	}
	if manager.Budget.MaxRecallResults != 6 {
		t.Fatalf("expected max recall results 6, got %d", manager.Budget.MaxRecallResults)
	}
	if manager.Budget.MaxObservationItems != 7 {
		t.Fatalf("expected max observation items 7, got %d", manager.Budget.MaxObservationItems)
	}
	if manager.Strategy.CompactionMode != contextmgr.CompactionModeLedgerPreferred {
		t.Fatalf("expected compaction mode override, got %q", manager.Strategy.CompactionMode)
	}
	if manager.Strategy.RecallMode != contextmgr.RecallModeBroad {
		t.Fatalf("expected recall mode override, got %q", manager.Strategy.RecallMode)
	}
	if manager.Strategy.ObservationMode != contextmgr.ObservationModeAll {
		t.Fatalf("expected observation mode override, got %q", manager.Strategy.ObservationMode)
	}
	if manager.Strategy.MinCompactionMessages != 3 || manager.Strategy.MinRecallQueryLength != 15 || manager.Strategy.LedgerLoadLimit != 9 {
		t.Fatalf("expected strategy threshold overrides, got %+v", manager.Strategy)
	}
}

type workflowPlanningMCPManager struct{}

func (m *workflowPlanningMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:        toolName,
		Description: "test workflow tool",
		MCPName:     "test-mcp",
		Enabled:     true,
	}, nil
}

func (m *workflowPlanningMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

func (m *workflowPlanningMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{
		{Name: "write_file", Description: "Write files", MCPName: "test-mcp", Enabled: true},
		{Name: "run_tests", Description: "Run tests", MCPName: "test-mcp", Enabled: true},
	}
}
