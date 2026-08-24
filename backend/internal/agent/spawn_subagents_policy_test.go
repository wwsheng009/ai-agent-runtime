package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
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

func TestDisabledDelegationSchedulerHidesToolAndFailsBeforeChildCreation(t *testing.T) {
	apiAgent := NewAgent(&Config{Name: "nested-child"}, nil)
	apiAgent.SetSubagentScheduler(NewSubagentScheduler(apiAgent, SubagentSchedulerConfig{
		DelegationPolicy: DelegationPolicyDisabled,
	}))

	if shouldExposeSpawnSubagents(apiAgent, nil) {
		t.Fatal("disabled inherited delegation boundary must hide spawn_subagents")
	}
	reports, err := apiAgent.GetSubagentScheduler().RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, []SubagentTask{{ID: "grandchild", Goal: "inspect"}})
	require.Error(t, err)
	require.Empty(t, reports)
	require.True(t, runtimeerrors.Is(err, runtimeerrors.ErrAgentNestedDelegation))
	require.Contains(t, err.Error(), "before child creation")
}

func TestChildSchedulerRequiresExplicitNestedDelegationOptIn(t *testing.T) {
	root := NewAgent(&Config{Name: "root"}, nil)
	implicit := NewSubagentScheduler(root, SubagentSchedulerConfig{})
	implicitChild := childSubagentSchedulerConfig(implicit, SubagentSchedulerConfig{})
	require.Equal(t, DelegationPolicyDisabled, implicitChild.DelegationPolicy)

	explicit := NewSubagentScheduler(root, SubagentSchedulerConfig{DelegationPolicy: DelegationPolicyEnabled})
	explicitChild := childSubagentSchedulerConfig(explicit, SubagentSchedulerConfig{DelegationPolicy: DelegationPolicyEnabled})
	require.Equal(t, DelegationPolicyEnabled, explicitChild.DelegationPolicy)
}

func TestSubagentSchedulerOpensCircuitAfterConsecutiveFailedBatches(t *testing.T) {
	apiAgent := NewAgent(&Config{Name: "circuit"}, nil)
	bus := runtimeevents.NewBus()
	var circuitEvents int
	bus.Subscribe("subagent.batch.circuit_open", func(runtimeevents.Event) { circuitEvents++ })
	apiAgent.SetEventBus(bus)
	scheduler := NewSubagentScheduler(apiAgent, SubagentSchedulerConfig{MaxConsecutiveFailures: 2})

	for i := 0; i < 2; i++ {
		reports, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{ParentSessionID: "parent"}, []SubagentTask{{ID: "invalid", Goal: ""}})
		require.NoError(t, err)
		require.Len(t, reports, 1)
		require.False(t, reports[0].Success)
	}
	require.Equal(t, 1, circuitEvents, "circuit terminal event must be emitted exactly once")

	reports, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{}, []SubagentTask{{ID: "never-started", Goal: "inspect"}})
	require.Error(t, err)
	require.Empty(t, reports)
	require.True(t, runtimeerrors.Is(err, runtimeerrors.ErrAgentSubagentCircuitOpen))
}
