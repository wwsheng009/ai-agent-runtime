package runtimehost

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestApplyAgentServicesAttachesSharedServiceGraph(t *testing.T) {
	apiAgent := agent.NewAgent(&agent.Config{Name: "services-agent"}, nil)
	bus := runtimeevents.NewBus()
	scheduler := agent.NewSubagentScheduler(apiAgent, agent.SubagentSchedulerConfig{})
	registry := skill.NewRegistry(nil)

	ApplyAgentServices(apiAgent, AgentServiceSpec{
		Scheduler:     scheduler,
		SkillRegistry: registry,
		EventBus:      bus,
	})

	if apiAgent.GetEventBus() != bus {
		t.Fatal("shared event bus was not attached")
	}
	if apiAgent.GetSubagentScheduler() != scheduler {
		t.Fatal("shared scheduler was not attached")
	}
}

func TestBuildLoopReActConfigSharedDefaults(t *testing.T) {
	config := BuildLoopReActConfig(LoopConfigSpec{})
	if config.MaxSteps != 0 ||
		!config.EnableThought ||
		!config.EnableToolCalls ||
		!config.EnableParallelTools ||
		config.MaxParallelToolCalls != 4 ||
		config.Temperature != 0.7 {
		t.Fatalf("unexpected loop defaults: %+v", config)
	}
	if config.TurnBudgetTokens != 0 || config.ReasoningEffort != "" {
		t.Fatalf("zero-value spec must not synthesize budget/reasoning: %+v", config)
	}
}

func TestBuildLoopReActConfigAppliesRuntimeAndReasoningPrecedence(t *testing.T) {
	runtimeConfig := &runtimecfg.RuntimeConfig{
		Agent: runtimecfg.AgentConfig{
			MaxMaxSteps:          11,
			MaxToolCalls:         12,
			MaxExplorationSteps:  13,
			MaxRepeatedToolCalls: 14,
			MaxRepeatedPollCalls: 15,
			EnableParallelTools:  false,
			MaxParallelToolCalls: 2,
			Timeout:              time.Minute,
		},
		Context: runtimecfg.ContextConfig{AccountingMode: "estimate_only"},
	}
	config := BuildLoopReActConfig(LoopConfigSpec{
		RuntimeConfig:            runtimeConfig,
		RequestedReasoningEffort: " high ",
		FallbackReasoningEffort:  "low",
		TurnBudgetTokens:         4200,
	})
	if config.MaxSteps != 11 ||
		config.MaxToolCalls != 12 ||
		config.MaxRunDuration != time.Minute ||
		config.MaxExplorationSteps != 13 ||
		config.MaxRepeatedToolCalls != 14 ||
		config.MaxRepeatedPollCalls != 15 ||
		config.EnableParallelTools ||
		config.MaxParallelToolCalls != 2 {
		t.Fatalf("runtime agent limits not applied: %+v", config)
	}
	if config.ContextAccountingMode != "estimate_only" {
		t.Fatalf("ContextAccountingMode = %q, want estimate_only", config.ContextAccountingMode)
	}
	if config.TurnBudgetTokens != 4200 {
		t.Fatalf("TurnBudgetTokens = %d, want 4200", config.TurnBudgetTokens)
	}
	if config.ReasoningEffort != "high" {
		t.Fatalf("requested reasoning must win: %q", config.ReasoningEffort)
	}

	fallback := BuildLoopReActConfig(LoopConfigSpec{FallbackReasoningEffort: "low"})
	if fallback.ReasoningEffort != "low" {
		t.Fatalf("fallback reasoning = %q, want low", fallback.ReasoningEffort)
	}
}

func TestAgentConfigAssemblySharesRuntimeLimitsAndRequestOptions(t *testing.T) {
	config := BuildAgentConfig(AgentConfigSpec{
		Name:             "child",
		Provider:         "provider",
		Model:            "model",
		SystemPrompt:     "prompt",
		DefaultMaxTokens: 512,
		RuntimeConfig: &runtimecfg.RuntimeConfig{Agent: runtimecfg.AgentConfig{
			MaxMaxSteps:          7,
			MaxToolCalls:         8,
			MaxExplorationSteps:  9,
			MaxRepeatedToolCalls: 10,
			MaxRepeatedPollCalls: 11,
			Timeout:              time.Minute,
		}},
	})
	if config.Name != "child" || config.Provider != "provider" || config.Model != "model" ||
		config.SystemPrompt != "prompt" || config.DefaultMaxTokens != 512 {
		t.Fatalf("base fields lost: %+v", config)
	}
	if config.MaxSteps != 7 || config.MaxToolCalls != 8 || config.MaxRunDuration != time.Minute ||
		config.MaxExplorationSteps != 9 || config.MaxRepeatedToolCalls != 10 || config.MaxRepeatedPollCalls != 11 {
		t.Fatalf("runtime limits not applied: %+v", config)
	}

	ApplyAgentRequestOptions(config, true, " high ")
	if config.Options["stream"] != true || config.Options["reasoning_effort"] != "high" {
		t.Fatalf("request options not applied: %+v", config.Options)
	}
}

func TestApplyChildAgentdefToolPolicySharesNarrowingRules(t *testing.T) {
	apiAgent := agent.NewAgent(&agent.Config{Name: "child", Provider: "test", Model: "test"}, nil)
	parent := agent.NewToolExecutionPolicy([]string{"view", "shell", "write"}, false)
	parent.SetCapabilityScope([]runtimepolicy.Capability{
		runtimepolicy.CapReadOnly,
		runtimepolicy.CapWriteFS,
		runtimepolicy.CapExecShell,
		runtimepolicy.CapNetwork,
	})
	apiAgent.SetToolExecutionPolicy(parent)

	readOnly := true
	binding := &agentdef.Binding{
		AgentID:        "reviewer",
		ToolAllowlist:  []string{"view", "shell"},
		ToolDenylist:   []string{"team_spawn"},
		ReadOnly:       &readOnly,
		PermissionMode: runtimepolicy.ModePlan,
	}
	if _, err := ApplyChildAgentdefToolPolicy(apiAgent, binding, "reviewer", t.TempDir(), t.TempDir()); err != nil {
		t.Fatalf("ApplyChildAgentdefToolPolicy: %v", err)
	}
	child := apiAgent.GetToolExecutionPolicy()
	if child == nil || !child.ReadOnly || !child.DeniedTools["team_spawn"] {
		t.Fatalf("agentdef narrowing not applied: %+v", child)
	}
	if err := child.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapExecShell}); err != nil {
		t.Fatalf("reviewer shell allowlist must retain exec_shell: %v", err)
	}
	if err := child.AllowCapabilities([]runtimepolicy.Capability{runtimepolicy.CapNetwork}); err == nil {
		t.Fatal("read-only reviewer must not gain network")
	}
	engine := apiAgent.GetPermissionEngine()
	if engine == nil || engine.Mode != runtimepolicy.ModePlan {
		t.Fatalf("agentdef plan default not applied: %+v", engine)
	}
}

func TestAgentDepthCeilingNormalizesZero(t *testing.T) {
	defaultDepth := runtimecfg.NormalizeAgentsConfig(runtimecfg.AgentsConfig{}).MaxDepth
	if got := AgentDepthCeiling(nil); got != defaultDepth {
		t.Fatalf("nil config depth = %d, want default %d", got, defaultDepth)
	}
	if got := AgentDepthCeiling(&runtimecfg.RuntimeConfig{}); got != defaultDepth {
		t.Fatalf("zero agents block depth = %d, want default %d", got, defaultDepth)
	}
	if got := AgentDepthCeiling(&runtimecfg.RuntimeConfig{Agent: runtimecfg.AgentConfig{}}); got != defaultDepth {
		t.Fatalf("zero-value config depth = %d, want default %d", got, defaultDepth)
	}
	if got := AgentDepthCeiling(&runtimecfg.RuntimeConfig{Agents: runtimecfg.AgentsConfig{MaxDepth: 2}}); got != 2 {
		t.Fatalf("explicit depth = %d, want 2", got)
	}
}

func TestBuildContextOptionsKeepsSparseBoundsAndArtifactFields(t *testing.T) {
	config := &runtimecfg.RuntimeConfig{
		Context:  runtimecfg.ContextConfig{MaxImageAttachmentTokens: 2048},
		Artifact: runtimecfg.ArtifactConfig{StorePath: " artifacts ", StoreDSN: " dsn "},
	}
	options := BuildContextOptions(config, nil)
	if options == nil {
		t.Fatal("sparse context/artifact settings must not be dropped")
	}
	if options["context_max_image_attachment_tokens"] != 2048 {
		t.Fatalf("image attachment bound missing: %+v", options)
	}
	if options["artifact_store_path"] != "artifacts" || options["artifact_store_dsn"] != "dsn" {
		t.Fatalf("artifact settings missing/untrimmed: %+v", options)
	}
	if _, ok := options["memory_mode"]; ok {
		t.Fatalf("mode=off must not publish memory options: %+v", options)
	}

	agentConfig := &agent.Config{Options: map[string]interface{}{"stream": true}}
	ApplyContextOptions(agentConfig, config, nil)
	if agentConfig.Options["stream"] != true {
		t.Fatal("shared context projection must merge, not replace host options")
	}
	if agentConfig.Options["context_max_image_attachment_tokens"] != 2048 {
		t.Fatalf("merged options lost image bound: %+v", agentConfig.Options)
	}
}
