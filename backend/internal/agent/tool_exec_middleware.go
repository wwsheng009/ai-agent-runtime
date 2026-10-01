package agent

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolexec"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// bindFreeformToolCall resolves a custom tool's raw input against its schema
// before hooks, permission checks, sandbox validation, and preflight run.
func (loop *ReActLoop) bindFreeformToolCall(ctx context.Context, call types.ToolCall) types.ToolCall {
	if !strings.EqualFold(strings.TrimSpace(call.Type), "custom_tool_call") {
		return call
	}
	info := loop.lookupToolInfoForPreflight(ctx, call.Name, nil)
	if info == nil {
		return call
	}
	call.Args = toolargs.BindFreeform(call.Args, info.InputSchema)
	return call
}

func (loop *ReActLoop) ensureToolExecMemory() *toolexec.Memory {
	if loop == nil {
		return nil
	}
	if loop.toolExecMemory == nil {
		loop.toolExecMemory = toolexec.NewMemory(toolexec.DefaultTerminalFailureThreshold)
	}
	return loop.toolExecMemory
}

// prepareToolExecution runs tool-agnostic preflight (schema required args,
// terminal failure circuit, optional read-path existence, empty soft negative
// cache) and attaches digest metadata.
func (loop *ReActLoop) prepareToolExecution(ctx context.Context, metadata map[string]interface{}, toolName, toolCallID string, args map[string]interface{}, toolInfo *runtimeskill.ToolInfo) toolexec.PreflightDecision {
	var schema map[string]interface{}
	var toolMeta map[string]interface{}
	if toolInfo != nil {
		schema = toolInfo.InputSchema
		toolMeta = toolInfo.Metadata
	}
	decision := toolexec.ApplyPreflight(loop.ensureToolExecMemory(), toolexec.PreflightRequest{
		ToolName:             toolName,
		ToolCallID:           toolCallID,
		Args:                 args,
		InputSchema:          schema,
		Metadata:             toolMeta,
		WorkspaceRoot:        loop.preflightWorkspaceRoot(),
		PathRewriteValidator: loop.pathRewriteValidator(ctx, toolInfo),
	})
	toolexec.AttachPreflightMetadata(metadata, decision)
	return decision
}

// pathRewriteValidator re-runs the policy/sandbox check on auto-healed
// arguments so a silent path rewrite can never bypass the validation the
// original call went through (P1-4 item 5). Returns nil when there is no tool
// info or policy to validate with, which leaves the historical behavior.
func (loop *ReActLoop) pathRewriteValidator(ctx context.Context, toolInfo *runtimeskill.ToolInfo) func(map[string]interface{}) error {
	if loop == nil || loop.agent == nil || toolInfo == nil {
		return nil
	}
	policy := loop.agent.GetToolExecutionPolicy()
	if policy == nil {
		return nil
	}
	info := *toolInfo
	return func(args map[string]interface{}) error {
		return policy.AllowToolCallWithContext(ctx, info, args)
	}
}

// toolWorkspaceRoot returns the filesystem base path used by toolkit tools
// (SetBasePath). Prefer options["tool_base_path"] so path resolution stays
// available even when workspace context injection is disabled. Fall back to
// workspace_path for older sessions / tests that only set the context key.
// Preflight must resolve relative paths against this same root so process CWD
// mismatches do not false-deny.
func (loop *ReActLoop) toolWorkspaceRoot() string {
	return toolWorkspaceRootForAgent(loop.agent)
}

// preflightWorkspaceRoot resolves the base for preflight path checks exactly the
// way the executor resolves the file it is about to touch: the session-bound
// root carried by the agent options, then the toolkit base path the builtin
// tools were registered with (ToolExecutionPolicy.PathAnchorRoot, i.e.
// SetBasePath(config.Workspace.Root)).
//
// Without the second step a run whose context carries no workspace root would
// have preflight test relative paths against the server process directory while
// the executor resolves them against the registered base path. The consequence
// is not only a false denial: the read-path auto-heal ranks siblings of the
// resolved path, so a CWD-anchored miss can rewrite the argument to a file in
// the wrong tree before the call is ever validated.
func (loop *ReActLoop) preflightWorkspaceRoot() string {
	if loop == nil {
		return ""
	}
	if root := loop.toolWorkspaceRoot(); root != "" {
		return root
	}
	if loop.agent == nil {
		return ""
	}
	if policy := loop.agent.GetToolExecutionPolicy(); policy != nil {
		return strings.TrimSpace(policy.PathAnchorRoot)
	}
	return ""
}

// toolWorkspaceRootForAgent is the agent-level variant of toolWorkspaceRoot so
// context builders that only carry the *Agent (e.g. toolCallContext) resolve
// the same session filesystem root.
func toolWorkspaceRootForAgent(agent *Agent) string {
	if agent == nil || agent.config == nil {
		return ""
	}
	if root := optionString(agent.config.Options, "tool_base_path"); root != "" {
		return root
	}
	if root := optionString(agent.config.Options, "workspace_path"); root != "" {
		return root
	}
	return ""
}

// knowledgeLayerForAgent returns the session knowledge layer handle stored under
// `context_knowledge_layer` (nil when the option is unset or holds another type).
// Used for Phase 5 change source 2: the turn-boundary external-change correction.
func knowledgeLayerForAgent(agent *Agent) *knowledge.Layer {
	if agent == nil || agent.config == nil {
		return nil
	}
	layer, _ := agent.config.Options["context_knowledge_layer"].(*knowledge.Layer)
	return layer
}

// knowledgeChangeNotifierForAgent returns the session knowledge layer's change
// sink (Phase 5 change source 1: the edit hook) so edit-class tools can report
// written files while the tool-call context is built.
//
// The assembly stores the handle under `context_knowledge_layer` (recommended
// value: `*knowledge.Layer`, which satisfies both knowledge.Planner and
// knowledge.ChangeNotifier). An unset option, a non-session agent, or a value
// that only implements Planner yields nil: tools then report nothing, which is
// exactly the mode=off baseline (zero side effects).
func knowledgeChangeNotifierForAgent(agent *Agent) knowledge.ChangeNotifier {
	if agent == nil || agent.config == nil {
		return nil
	}
	notifier, _ := agent.config.Options["context_knowledge_layer"].(knowledge.ChangeNotifier)
	return notifier
}

// toolAllowedRootsForAgent returns the session's admitted external roots
// (`/add-dir` / additionalDirectories, §4.5) stored on the agent options so
// every tool-call context carries them next to the workspace root.
func toolAllowedRootsForAgent(agent *Agent) []string {
	if agent == nil || agent.config == nil {
		return nil
	}
	return optionStringList(agent.config.Options, "allowed_roots", "additional_directories", "additionalDirectories")
}

// toolReadOnlyRootsForAgent returns the run's read-only exempt roots (F5):
// paths outside the workspace whose reads skip the external-directory gate
// (writes under them still ask). A worktree-isolated child names the main repo
// here so reading it is not an approval hotspot.
func toolReadOnlyRootsForAgent(agent *Agent) []string {
	if agent == nil || agent.config == nil {
		return nil
	}
	return optionStringList(agent.config.Options, "read_only_roots")
}

func (loop *ReActLoop) finishToolExecutionOutcome(metadata map[string]interface{}, toolName, digest, toolErr string) {
	if loop == nil {
		return
	}
	_ = toolexec.RecordOutcome(loop.ensureToolExecMemory(), toolName, digest, toolErr, metadata)
}

// applySoftEmptyPreflightResult stamps a successful empty disposition when
// preflight short-circuits an identical empty-success digest. Callers must
// skip tool execution and still run gateway reduction so the model sees
// outcome=empty + next_action without a hard error.
func applySoftEmptyPreflightResult(result *toolExecutionResult, metadata map[string]interface{}, decision toolexec.PreflightDecision) {
	if result == nil {
		return
	}
	result.Error = ""
	if strings.TrimSpace(decision.NextAction) != "" {
		result.Output = decision.NextAction
	} else {
		result.Output = "Identical tool call previously returned a successful empty result. Treat that as valid evidence; broaden/change inputs or proceed instead of retrying unchanged."
	}
	if metadata == nil {
		return
	}
	metadata[toolexec.MetadataEmptyReplayKey] = true
	metadata[toolresult.MetadataEmptyResultKey] = true
	metadata[toolresult.MetadataOutcomeKey] = toolresult.OutcomeEmpty
	metadata[toolresult.MetadataRetryableKey] = false
	if decision.NextAction != "" {
		metadata[toolresult.MetadataNextActionKey] = decision.NextAction
	}
	if compact := toolresult.CompactAttemptedArgs(decision.Args); len(compact) > 0 {
		if _, exists := metadata[toolresult.MetadataAttemptedArgsKey]; !exists {
			metadata[toolresult.MetadataAttemptedArgsKey] = compact
		}
	}
	// Ensure Diagnose/gateway keep empty success even if body text is non-empty.
	toolresult.MarkEmptySuccess(metadata)
}

// lookupToolInfoForPreflight resolves schema/metadata for preflight without
// hardcoding tool names. Prefer the provided ToolInfo; otherwise use MCP catalog
// or broker definitions (Parameters as InputSchema, Metadata as tool metadata).
func (loop *ReActLoop) lookupToolInfoForPreflight(ctx context.Context, toolName string, known *runtimeskill.ToolInfo) *runtimeskill.ToolInfo {
	if known != nil {
		return known
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" || loop == nil || loop.agent == nil {
		return nil
	}
	if loop.agent.mcpManager != nil {
		if info, err := loop.agent.mcpManager.FindTool(toolName); err == nil && strings.TrimSpace(info.Name) != "" {
			cloned := info
			return &cloned
		}
	}
	broker := loop.agent.GetToolBroker()
	if broker == nil || !broker.IsBrokerTool(toolName) {
		return nil
	}
	var definitions []types.ToolDefinition
	if ctx != nil {
		definitions = broker.DefinitionsForContext(ctx)
	} else {
		definitions = broker.Definitions()
	}
	for _, def := range definitions {
		if strings.EqualFold(strings.TrimSpace(def.Name), toolName) {
			return &runtimeskill.ToolInfo{
				Name:        def.Name,
				Description: def.Description,
				InputSchema: def.Parameters,
				Metadata:    def.Metadata,
			}
		}
	}
	return nil
}
