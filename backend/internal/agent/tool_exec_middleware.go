package agent

import (
	"context"
	"strings"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolexec"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

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
func (loop *ReActLoop) prepareToolExecution(metadata map[string]interface{}, toolName, toolCallID string, args map[string]interface{}, toolInfo *runtimeskill.ToolInfo) toolexec.PreflightDecision {
	var schema map[string]interface{}
	var toolMeta map[string]interface{}
	if toolInfo != nil {
		schema = toolInfo.InputSchema
		toolMeta = toolInfo.Metadata
	}
	decision := toolexec.ApplyPreflight(loop.ensureToolExecMemory(), toolexec.PreflightRequest{
		ToolName:    toolName,
		ToolCallID:  toolCallID,
		Args:        args,
		InputSchema: schema,
		Metadata:    toolMeta,
	})
	toolexec.AttachPreflightMetadata(metadata, decision)
	return decision
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
