package agent

import (
	"testing"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestShouldExposeSpawnSubagentsRequiresWhitelistAndExecutionPolicy(t *testing.T) {
	apiAgent := NewAgent(&Config{Name: "spawn-policy"}, nil)
	apiAgent.SetSubagentScheduler(NewSubagentScheduler(apiAgent, SubagentSchedulerConfig{}))

	if !shouldExposeSpawnSubagents(apiAgent, nil) {
		t.Fatal("expected scheduler tool without restrictive policy to be exposed")
	}
	if shouldExposeSpawnSubagents(apiAgent, map[string]bool{"view": true}) {
		t.Fatal("expected call whitelist without spawn_subagents to hide it")
	}

	apiAgent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false))
	if shouldExposeSpawnSubagents(apiAgent, nil) {
		t.Fatal("expected execution allowlist without spawn_subagents to hide it")
	}

	policy := runtimepolicy.NewToolExecutionPolicy([]string{"view", SpawnSubagentsToolName}, false)
	apiAgent.SetToolExecutionPolicy(policy)
	if !shouldExposeSpawnSubagents(apiAgent, map[string]bool{SpawnSubagentsToolName: true}) {
		t.Fatal("expected aligned whitelist and execution policy to expose it")
	}

	policy.DeniedTools = map[string]bool{SpawnSubagentsToolName: true}
	if shouldExposeSpawnSubagents(apiAgent, map[string]bool{SpawnSubagentsToolName: true}) {
		t.Fatal("expected explicit deny to hide spawn_subagents")
	}
}

func TestComputeAvailableToolsDoesNotExposePolicyDeniedSpawnSubagents(t *testing.T) {
	apiAgent := NewAgent(&Config{Name: "spawn-surface"}, nil)
	apiAgent.SetSubagentScheduler(NewSubagentScheduler(apiAgent, SubagentSchedulerConfig{}))
	apiAgent.SetToolExecutionPolicy(runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false))

	loop := NewReActLoop(apiAgent, nil, &LoopReActConfig{})
	tools, err := loop.computeAvailableTools(t.Context(), "delegate research", nil, false)
	if err != nil {
		t.Fatalf("computeAvailableTools: %v", err)
	}
	for _, tool := range tools {
		if tool.Name == SpawnSubagentsToolName {
			t.Fatal("policy-denied spawn_subagents must not be model-visible")
		}
	}
}
