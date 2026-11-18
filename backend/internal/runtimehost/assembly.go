package runtimehost

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// AgentConfigSpec is the host-neutral subset of agent construction inputs.
//
// Hosts still own profile/session prompt composition and tool-surface
// construction; this type only centralizes the runtime limits that must be
// identical for runtime-server and aicli.
type AgentConfigSpec struct {
	Name             string
	Provider         string
	Model            string
	MaxSteps         int
	SystemPrompt     string
	DefaultMaxTokens int
	RuntimeConfig    *runtimecfg.RuntimeConfig
}

// BuildAgentConfig builds the common base agent config and applies runtime
// limits in one place. Host-specific fields (prompt freezing, tool surface,
// runtime services) remain outside this function.
func BuildAgentConfig(spec AgentConfigSpec) *agent.Config {
	config := &agent.Config{
		Name:             spec.Name,
		Provider:         spec.Provider,
		Model:            spec.Model,
		MaxSteps:         spec.MaxSteps,
		SystemPrompt:     spec.SystemPrompt,
		DefaultMaxTokens: spec.DefaultMaxTokens,
	}
	ApplyAgentConfigLimits(config, spec.RuntimeConfig)
	return config
}

// ApplyAgentConfigLimits mirrors the runtime.agent limits onto an existing
// config. Negative MaxSteps is a request sentinel, not a literal limit.
func ApplyAgentConfigLimits(config *agent.Config, runtimeConfig *runtimecfg.RuntimeConfig) {
	if config == nil || runtimeConfig == nil {
		return
	}
	if config.MaxSteps < 0 {
		config.MaxSteps = 0
	} else if config.MaxSteps == 0 {
		config.MaxSteps = agent.NormalizeMaxSteps(runtimeConfig.Agent.MaxMaxSteps)
	}
	config.MaxToolCalls = runtimeConfig.Agent.MaxToolCalls
	config.MaxRunDuration = runtimeConfig.Agent.Timeout
	config.MaxExplorationSteps = runtimeConfig.Agent.MaxExplorationSteps
	config.MaxRepeatedToolCalls = runtimeConfig.Agent.MaxRepeatedToolCalls
	config.MaxRepeatedPollCalls = runtimeConfig.Agent.MaxRepeatedPollCalls
}

// ApplyAgentRequestOptions applies the per-turn stream/reasoning preferences
// shared by both hosts. The reasoning value is normalized by the same helper
// used by the loop assembler.
func ApplyAgentRequestOptions(config *agent.Config, stream bool, requestedReasoningEffort string) {
	if config == nil {
		return
	}
	if !stream && strings.TrimSpace(requestedReasoningEffort) == "" {
		return
	}
	if config.Options == nil {
		config.Options = make(map[string]interface{})
	}
	if stream {
		config.Options["stream"] = true
	}
	if reasoningEffort := runtimetypes.NormalizeReasoningEffort(requestedReasoningEffort); reasoningEffort != "" {
		config.Options["reasoning_effort"] = reasoningEffort
	}
}

// ApplyToolBasePathOption anchors relative tool arguments on the same root the
// toolkit was registered with. It is separate from workspace_path: workspace
// context scanning may be disabled while path validation still needs an anchor.
func ApplyToolBasePathOption(config *agent.Config, path string) {
	if config == nil || strings.TrimSpace(path) == "" {
		return
	}
	if config.Options == nil {
		config.Options = make(map[string]interface{})
	}
	config.Options["tool_base_path"] = strings.TrimSpace(path)
}

// ApplyShellRuntimeOption publishes the workspace/session shell selection to
// the agent option surface. The agent loop copies it into every tool-call
// context, so concurrent sessions in different workspaces stay isolated.
func ApplyShellRuntimeOption(config *agent.Config, runtime *runtimeexecutor.ShellRuntime) {
	if config == nil || runtime == nil {
		return
	}
	if config.Options == nil {
		config.Options = make(map[string]interface{})
	}
	config.Options["shell_runtime"] = runtime
}

// ApplySessionRootOptions publishes the durable external-directory grants and
// read-only exempt roots to the agent option surface. Empty sets are a no-op;
// revocation/live updates remain owned by SessionActor admission.
func ApplySessionRootOptions(config *agent.Config, allowedRoots, readOnlyRoots []string) {
	if config == nil {
		return
	}
	if len(allowedRoots) == 0 && len(readOnlyRoots) == 0 {
		return
	}
	if config.Options == nil {
		config.Options = make(map[string]interface{})
	}
	if len(allowedRoots) > 0 {
		config.Options["allowed_roots"] = append([]string(nil), allowedRoots...)
	}
	if len(readOnlyRoots) > 0 {
		config.Options["read_only_roots"] = append([]string(nil), readOnlyRoots...)
	}
}

// ApplyDisableToolsOption keeps the request catalog visible while disabling
// execution and forcing tool_choice=none in the model request.
func ApplyDisableToolsOption(config *agent.Config, disabled bool) {
	if config == nil || !disabled {
		return
	}
	if config.Options == nil {
		config.Options = make(map[string]interface{})
	}
	config.Options[llm.MetadataKeyDisableTools] = true
}

// AgentServiceSpec is the host-neutral service graph attached after an agent is
// constructed. Hosts may still build the scheduler/coordinator themselves
// because their admission hooks differ; the skill catalog, embedding router,
// event bus and team context wiring is identical and lives here.
type AgentServiceSpec struct {
	Scheduler        *agent.SubagentScheduler
	BatchCoordinator *agent.SubagentBatchCoordinator
	SkillRegistry    *skill.Registry
	EmbeddingRouter  *skill.SemanticEmbeddingRouter
	EventBus         *runtimeevents.Bus
	TeamStore        team.Store
}

// ApplyAgentServices attaches the shared service graph. Nil fields are no-ops,
// preserving hosts/tests that intentionally run with a partial dependency set.
func ApplyAgentServices(apiAgent *agent.Agent, spec AgentServiceSpec) {
	if apiAgent == nil {
		return
	}
	if spec.Scheduler != nil {
		apiAgent.SetSubagentScheduler(spec.Scheduler)
	}
	if spec.BatchCoordinator != nil {
		apiAgent.SetSubagentBatchCoordinator(spec.BatchCoordinator)
	}
	if spec.SkillRegistry != nil {
		for _, summary := range spec.SkillRegistry.ListSummaries() {
			if summary == nil {
				continue
			}
			_ = apiAgent.RegisterSkill(summary.ToSkillStub())
		}
	}
	if spec.EmbeddingRouter != nil {
		if cloned, err := spec.EmbeddingRouter.CloneForRegistry(apiAgent.GetSkillRouter().Registry()); err == nil {
			apiAgent.GetSkillRouter().SetEmbeddingRouter(cloned)
		}
	}
	if spec.EventBus != nil {
		apiAgent.SetEventBus(spec.EventBus)
	}
	if spec.TeamStore != nil {
		if ctxMgr := apiAgent.GetContextManager(); ctxMgr != nil {
			ctxMgr.TeamContext = team.NewContextBuilder(spec.TeamStore)
		}
	}
}

// LoopConfigSpec is the host-neutral loop assembly input.
//
// RequestedReasoningEffort is the per-turn override; FallbackReasoningEffort
// is the session-level value used when the override is empty. TurnBudgetTokens
// is zero for runtime-server actors and set from the TUI ChatSession.
type LoopConfigSpec struct {
	RuntimeConfig            *runtimecfg.RuntimeConfig
	RequestedReasoningEffort string
	FallbackReasoningEffort  string
	TurnBudgetTokens         int
}

// BuildLoopReActConfig builds the shared LoopReActConfig defaults and applies
// runtime limits, context accounting, budget, and reasoning precedence.
func BuildLoopReActConfig(spec LoopConfigSpec) *agent.LoopReActConfig {
	config := &agent.LoopReActConfig{
		MaxSteps:             0,
		EnableThought:        true,
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 4,
		Temperature:          0.7,
	}
	if spec.RuntimeConfig != nil {
		config.MaxSteps = agent.NormalizeMaxSteps(spec.RuntimeConfig.Agent.MaxMaxSteps)
		config.MaxToolCalls = spec.RuntimeConfig.Agent.MaxToolCalls
		config.MaxRunDuration = spec.RuntimeConfig.Agent.Timeout
		config.MaxExplorationSteps = spec.RuntimeConfig.Agent.MaxExplorationSteps
		config.MaxRepeatedToolCalls = spec.RuntimeConfig.Agent.MaxRepeatedToolCalls
		config.MaxRepeatedPollCalls = spec.RuntimeConfig.Agent.MaxRepeatedPollCalls
		config.EnableParallelTools = spec.RuntimeConfig.Agent.EnableParallelTools
		if spec.RuntimeConfig.Agent.MaxParallelToolCalls > 0 {
			config.MaxParallelToolCalls = spec.RuntimeConfig.Agent.MaxParallelToolCalls
		}
		// Empty keeps the loop fallback (provider_first); an explicit
		// estimate_only override must survive both host paths.
		if mode := strings.TrimSpace(spec.RuntimeConfig.Context.AccountingMode); mode != "" {
			config.ContextAccountingMode = mode
		}
	}
	if spec.TurnBudgetTokens > 0 {
		config.TurnBudgetTokens = spec.TurnBudgetTokens
	}
	if reasoningEffort := runtimetypes.NormalizeReasoningEffort(spec.RequestedReasoningEffort); reasoningEffort != "" {
		config.ReasoningEffort = reasoningEffort
	} else if reasoningEffort := runtimetypes.NormalizeReasoningEffort(spec.FallbackReasoningEffort); reasoningEffort != "" {
		config.ReasoningEffort = reasoningEffort
	}
	return config
}

// AgentDepthCeiling returns the normalized child-depth ceiling for a runtime
// config. Zero means unset and must fall back to the default, never "unlimited".
func AgentDepthCeiling(runtimeConfig *runtimecfg.RuntimeConfig) int {
	if runtimeConfig == nil {
		return runtimecfg.NormalizeAgentsConfig(runtimecfg.AgentsConfig{}).MaxDepth
	}
	return runtimecfg.NormalizeAgentsConfig(runtimeConfig.Agents).MaxDepth
}

// ApplyChildReadOnlyPolicy narrows a child to the read-only capability floor.
func ApplyChildReadOnlyPolicy(apiAgent *agent.Agent, readOnly bool) {
	if apiAgent == nil || !readOnly {
		return
	}
	toolPolicy := apiAgent.GetToolExecutionPolicy()
	if toolPolicy == nil {
		toolPolicy = agent.NewToolExecutionPolicy(nil, true)
	} else {
		toolPolicy = toolPolicy.Clone()
		toolPolicy.ReadOnly = true
	}
	toolPolicy.SetCapabilityScope(toolPolicy.IntersectAllowedCapabilities(runtimepolicy.ReadOnlyChildCapabilities()))
	apiAgent.SetToolExecutionPolicy(toolPolicy)
}

// ApplyChildShellPolicy applies the explicit shell=false child flag. False
// keeps the inherited behavior.
func ApplyChildShellPolicy(apiAgent *agent.Agent, shellDisabled bool) {
	if apiAgent == nil || !shellDisabled {
		return
	}
	toolPolicy := apiAgent.GetToolExecutionPolicy()
	if toolPolicy == nil {
		toolPolicy = agent.NewToolExecutionPolicy(nil, false)
	} else {
		toolPolicy = toolPolicy.Clone()
	}
	toolPolicy.SetShellDisabled(true)
	apiAgent.SetToolExecutionPolicy(toolPolicy)
}

// ApplyChildDepthPolicy hides delegation tools when the child has reached the
// configured depth ceiling.
func ApplyChildDepthPolicy(apiAgent *agent.Agent, depth, maxDepth int) {
	if apiAgent == nil || maxDepth <= 0 || depth < maxDepth {
		return
	}
	toolPolicy := apiAgent.GetToolExecutionPolicy()
	if toolPolicy == nil {
		toolPolicy = agent.NewToolExecutionPolicy(nil, false)
	} else {
		toolPolicy = toolPolicy.Clone()
	}
	if toolPolicy.DeniedTools == nil {
		toolPolicy.DeniedTools = map[string]bool{}
	}
	for _, toolName := range toolregistry.CallableNames("subagent_spawn", agent.SpawnSubagentsToolName, "team_spawn") {
		toolPolicy.DeniedTools[toolName] = true
	}
	apiAgent.SetToolExecutionPolicy(toolPolicy)
}

// ApplyChildAgentdefToolPolicy applies a resolved agent definition's
// allow/deny/read-only/sandbox defaults to a child agent.
//
// Resolution stays host-specific because aicli applies folder trust while the
// server uses server-side discovery. This function starts from the resolved
// binding so both hosts apply the same narrowing rules. Sandbox warnings and
// errors are returned for the caller's host logger; the policy is applied even
// when sandbox materialization reports a warning, matching prior behavior.
func ApplyChildAgentdefToolPolicy(apiAgent *agent.Agent, binding *agentdef.Binding, role, workspaceRoot, pathAnchorRoot string) ([]string, error) {
	if apiAgent == nil || binding == nil {
		return nil, nil
	}
	hasSandbox := len(binding.Sandbox) > 0
	if len(binding.ToolAllowlist) == 0 && len(binding.ToolDenylist) == 0 && (binding.ReadOnly == nil || !*binding.ReadOnly) && !hasSandbox {
		return nil, nil
	}

	readOnly := binding.ReadOnly != nil && *binding.ReadOnly
	var allowlist []string
	if len(binding.ToolAllowlist) > 0 {
		allowlist = append([]string(nil), binding.ToolAllowlist...)
	}
	toolPolicy := apiAgent.GetToolExecutionPolicy()
	if toolPolicy == nil {
		toolPolicy = agent.NewToolExecutionPolicy(allowlist, readOnly)
	} else {
		// The role is required: an empty role drops exec_shell from the
		// capability floor, which breaks inherited builtin agent surfaces.
		toolPolicy = toolPolicy.DeriveChildForTask(allowlist, readOnly, role, nil)
	}
	if strings.TrimSpace(toolPolicy.PathAnchorRoot) == "" {
		toolPolicy.SetPathAnchorRoot(pathAnchorRoot)
	}
	if len(binding.ToolDenylist) > 0 {
		if toolPolicy.DeniedTools == nil {
			toolPolicy.DeniedTools = map[string]bool{}
		}
		for _, name := range binding.ToolDenylist {
			name = strings.TrimSpace(name)
			if name != "" {
				toolPolicy.DeniedTools[name] = true
			}
		}
	}
	var (
		warnings   []string
		sandboxErr error
	)
	if hasSandbox {
		warnings, sandboxErr = runtimeprofileinput.MaterializeSandboxForWorkspace(toolPolicy, binding.Sandbox, workspaceRoot)
	}
	apiAgent.SetToolExecutionPolicy(toolPolicy)

	// An agentdef plan mode is only a default; the resolved session approval
	// mode remains authoritative. Read-only is enforced independently above.
	if binding.PermissionMode == runtimepolicy.ModePlan {
		engine := apiAgent.GetPermissionEngine()
		if engine == nil {
			engine = agent.NewPermissionEngine()
			engine.Mode = binding.PermissionMode
			apiAgent.SetPermissionEngine(engine)
		}
		if engine.Mode == "" {
			engine.Mode = binding.PermissionMode
		}
		if engine.Mode == runtimepolicy.ModePlan {
			runtimepolicy.EnsurePlanWriteAllowPaths(engine)
		}
	}
	return warnings, sandboxErr
}
