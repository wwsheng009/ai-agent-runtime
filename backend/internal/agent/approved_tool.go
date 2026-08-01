package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	runtimecheckpoint "github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ExecuteToolCall executes one tool call using the normal permission/broker/runtime path.
// batch should contain the full assistant tool batch so recovery can persist crash-safe state.
func (a *Agent) ExecuteToolCall(ctx context.Context, sessionID string, call types.ToolCall, history []types.Message, batch []types.ToolCall) (*types.Message, error) {
	if a == nil {
		return nil, fmt.Errorf("agent is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(batch) == 0 {
		batch = []types.ToolCall{call}
	}
	loop := NewReActLoop(a, a.llmRuntime, nil)
	callCtx := WithToolBatchContext(ctx, batch, call.ID, completedBatchToolMessages(history, batch, call.ID))
	results, err := loop.act(callCtx, "trace_"+uuid.NewString(), sessionID, 0, 0, cloneMessages(history), []types.ToolCall{call}, nil)
	if err != nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, fmt.Errorf("unexpected tool execution result count: %d", len(results))
	}
	if message := toolExecutionResultMessage(results[0]); message != nil {
		return message, nil
	}
	return types.NewToolMessage(call.ID, ""), nil
}

// ExecuteApprovedToolCall executes a previously approved tool call without re-running approval checks.
func (a *Agent) ExecuteApprovedToolCall(ctx context.Context, sessionID string, call types.ToolCall, history []types.Message) (*types.Message, error) {
	if a == nil {
		return nil, fmt.Errorf("agent is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	call = NewReActLoop(a, a.llmRuntime, nil).bindFreeformToolCall(ctx, call)
	traceID := "trace_" + uuid.NewString()
	gateway := a.GetOutputGateway()
	result := toolExecutionResult{Call: call}
	metadata := map[string]interface{}{
		"step":            0,
		"trace_id":        traceID,
		"approved_resume": true,
	}
	if source := resolveToolSourceForRequest(a, call.Name); source != "" {
		metadata[toolresult.SourceKey] = source
	}
	requestedExtra := map[string]interface{}{
		"approved": true,
	}
	if source := resolveToolSourceForRequest(a, call.Name); source != "" {
		requestedExtra[toolresult.SourceKey] = source
	}
	a.emitRuntimeEvent("tool.requested", sessionID, call.Name, runtimeEventPayloadWithTurnID(
		ctx,
		toolRequestedEventPayload(call, 0, traceID, requestedExtra),
	))
	ctx = a.withToolProgressReporter(ctx, sessionID, traceID, call)

	finalize := func() *types.Message {
		envelope, gatewayErr := gateway.Process(ctx, newRawToolResult(sessionID, call, 0, result.Output, result.Error, metadata))
		if gatewayErr != nil && envelope != nil {
			envelope.Metadata["gateway_error"] = gatewayErr.Error()
		}
		result.Envelope = envelope
		a.emitRuntimeEvent("tool.completed", sessionID, call.Name, runtimeEventPayloadWithTurnID(
			ctx,
			toolCompletedEventPayload(result, 0, traceID, map[string]interface{}{
				"approved":       true,
				"awaiting_model": true,
			}),
		))
		a.runPostToolUseHooks(ctx, sessionID, result)
		message := types.NewToolMessage(call.ID, "")
		message.Content = output.RenderToolResultContentForModel(result.Output, result.Error, envelope)
		if envelope != nil {
			if len(envelope.Metadata) > 0 {
				message.Metadata = types.NewMetadata()
				for key, value := range envelope.Metadata {
					message.Metadata[key] = value
				}
			}
		}
		if strings.TrimSpace(result.Error) != "" {
			if message.Metadata == nil {
				message.Metadata = types.NewMetadata()
			}
			message.Metadata["tool_error"] = result.Error
		}
		return message
	}

	if err := a.runPreToolUseHooks(ctx, sessionID, call); err != nil {
		result.Error = err.Error()
		return finalize(), nil
	}

	if hookManager := a.GetHookManager(); hookManager != nil {
		decision, hookErr := hookManager.Dispatch(ctx, runtimehooks.EventPreToolUse, map[string]interface{}{
			"tool_name":  call.Name,
			"tool_call":  call.ID,
			"session_id": sessionID,
			"trace_id":   traceID,
			"args":       call.Args,
		})
		if hookErr != nil {
			result.Error = hookErr.Error()
			return finalize(), nil
		}
		if decision.Action == runtimehooks.DecisionBlock {
			result.Error = strings.TrimSpace(decision.Message)
			if result.Error == "" {
				result.Error = "hook blocked tool"
			}
			return finalize(), nil
		}
		if decision.Action == runtimehooks.DecisionModify && len(decision.PatchedPayload) > 0 {
			patched, patchErr := runtimepolicy.ApplyPatchedArgs(call.Args, decision.PatchedPayload)
			if patchErr != nil {
				result.Error = patchErr.Error()
				return finalize(), nil
			}
			call.Args = patched
			result.Call.Args = patched
		}
		mergeHookMetadata(metadata, decision.Message, decision.ExtraContext)
	}

	if broker := a.GetToolBroker(); broker != nil && broker.IsBrokerTool(call.Name) {
		loop := NewReActLoop(a, a.llmRuntime, nil)
		preflightInfo := loop.lookupToolInfoForPreflight(ctx, call.Name, nil)
		var deny string
		if call, deny = a.enforceApprovedToolHardConstraints(ctx, sessionID, traceID, call, preflightInfo); deny != "" {
			result.Error = deny
			result.Call.Args = call.Args
			loop.finishToolExecutionOutcome(metadata, call.Name, "", result.Error)
			return finalize(), nil
		}
		result.Call.Args = call.Args
		decision := loop.prepareToolExecution(metadata, call.Name, call.ID, call.Args, preflightInfo)
		if !decision.Allow {
			if decision.SoftEmpty {
				applySoftEmptyPreflightResult(&result, metadata, decision)
				loop.finishToolExecutionOutcome(metadata, call.Name, decision.Digest, "")
				return finalize(), nil
			}
			result.Error = decision.Error
			loop.finishToolExecutionOutcome(metadata, call.Name, decision.Digest, result.Error)
			return finalize(), nil
		}
		rawOutput, rawMeta, callErr := broker.ExecuteToolCall(ctx, sessionID, call)
		recordToolExecutionOutcome(&result, metadata, rawOutput, rawMeta, callErr)
		loop.finishToolExecutionOutcome(metadata, call.Name, decision.Digest, result.Error)
		return finalize(), nil
	}

	if a.mcpManager == nil {
		result.Error = "mcp manager is nil"
		return finalize(), nil
	}

	toolInfo, err := a.mcpManager.FindTool(call.Name)
	if err != nil {
		result.Error = fmt.Sprintf("tool not found: %s", call.Name)
		return finalize(), nil
	}
	metadata["mcp_name"] = toolInfo.MCPName
	metadata["trust_level"] = toolInfo.MCPTrustLevel
	metadata["execution_mode"] = toolInfo.ExecutionMode

	loop := NewReActLoop(a, a.llmRuntime, nil)
	preflightInfo := loop.lookupToolInfoForPreflight(ctx, call.Name, &toolInfo)
	var deny string
	if call, deny = a.enforceApprovedToolHardConstraints(ctx, sessionID, traceID, call, &toolInfo); deny != "" {
		result.Error = deny
		result.Call.Args = call.Args
		loop.finishToolExecutionOutcome(metadata, call.Name, "", result.Error)
		return finalize(), nil
	}
	result.Call.Args = call.Args
	decision := loop.prepareToolExecution(metadata, call.Name, call.ID, call.Args, preflightInfo)
	if !decision.Allow {
		if decision.SoftEmpty {
			applySoftEmptyPreflightResult(&result, metadata, decision)
			loop.finishToolExecutionOutcome(metadata, call.Name, decision.Digest, "")
			return finalize(), nil
		}
		result.Error = decision.Error
		loop.finishToolExecutionOutcome(metadata, call.Name, decision.Digest, result.Error)
		return finalize(), nil
	}

	var pending *runtimecheckpoint.PendingCheckpoint
	checkpointMgr := a.GetCheckpointManager()
	if checkpointMgr != nil && (runtimepolicy.IsWriteLikeToolName(call.Name) || runtimepolicy.IsShellLikeToolName(call.Name) || runtimepolicy.HasMutationHints(call.Args)) {
		pending, _ = checkpointMgr.BeforeMutation(ctx, sessionID, call.Name, call.ID, call.Args)
		if pending != nil {
			pending.MessageCount = len(history)
			if checkpointMgr.ConversationSnapshot {
				pending.Conversation = cloneMessages(history)
			}
		}
	}

	var rawMeta map[string]interface{}
	if caller, ok := a.mcpManager.(richToolCaller); ok {
		result.Output, rawMeta, err = caller.CallToolWithMeta(ctx, toolInfo.MCPName, call.Name, call.Args)
	} else {
		result.Output, err = a.mcpManager.CallTool(ctx, toolInfo.MCPName, call.Name, call.Args)
	}
	recordToolExecutionOutcome(&result, metadata, result.Output, rawMeta, err)
	loop.finishToolExecutionOutcome(metadata, call.Name, decision.Digest, result.Error)

	message := finalize()
	if pending != nil {
		meta := map[string]interface{}{}
		for key, value := range metadata {
			meta[key] = value
		}
		if result.Envelope != nil && len(result.Envelope.Metadata) > 0 {
			for key, value := range result.Envelope.Metadata {
				meta[key] = value
			}
		}
		checkpointID, checkpointErr := checkpointMgr.AfterMutation(ctx, pending, meta, result.Error)
		if checkpointID != "" {
			if hookMgr := a.GetHookManager(); hookMgr != nil {
				payload := map[string]interface{}{
					"session_id":    sessionID,
					"tool_name":     call.Name,
					"tool_call_id":  call.ID,
					"checkpoint_id": checkpointID,
					"trace_id":      traceID,
				}
				if checkpointErr != nil {
					payload["error"] = checkpointErr.Error()
				}
				hookMgr.DispatchAsync(ctx, runtimehooks.EventCheckpointCreated, payload)
			}
		}
	}
	return message, nil
}

// enforceApprovedToolHardConstraints re-validates an already-approved tool call
// against non-negotiable policy (permission-hook hard block/modify, capability
// scope, tool allow/deny, sandbox path/URL/command checks, and hard deny rules)
// without re-prompting for approval. It returns the possibly hook-patched call
// and a non-empty deny reason when execution must be refused. This closes the
// gap where approval/hook-patched arguments could otherwise be executed on
// replay without passing hard boundaries.
func (a *Agent) enforceApprovedToolHardConstraints(ctx context.Context, sessionID, traceID string, call types.ToolCall, info *runtimeskill.ToolInfo) (types.ToolCall, string) {
	if engine := a.GetPermissionEngine(); engine != nil {
		decision, err := engine.ValidateHardConstraints(ctx, runtimepolicy.EvalRequest{
			SessionID:  sessionID,
			TraceID:    traceID,
			ToolCallID: call.ID,
			ToolName:   call.Name,
			ToolInfo:   info,
			Args:       call.Args,
			Mode:       permissionModeFromContext(ctx),
		})
		if err != nil {
			return call, err.Error()
		}
		if decision.Type == runtimepolicy.DecisionDeny {
			reason := strings.TrimSpace(decision.Reason)
			if reason == "" {
				reason = "denied by execution policy"
			}
			return call, reason
		}
		if len(decision.PatchedArgs) > 0 {
			patched, patchErr := runtimepolicy.ApplyPatchedArgs(call.Args, decision.PatchedArgs)
			if patchErr != nil {
				return call, patchErr.Error()
			}
			call.Args = patched
		}
		return call, ""
	}
	// No permission engine wired: fall back to the static tool policy so an
	// approved replay still cannot escape capability/tool/sandbox constraints.
	if policy := a.GetToolExecutionPolicy(); policy != nil {
		if info != nil {
			if err := policy.AllowToolCall(*info, call.Args); err != nil {
				return call, err.Error()
			}
		} else if err := policy.AllowTool(call.Name); err != nil {
			return call, err.Error()
		}
	}
	return call, ""
}

func completedBatchToolMessages(history []types.Message, batch []types.ToolCall, currentToolCallID string) []types.Message {
	if len(history) == 0 || len(batch) == 0 {
		return nil
	}
	messages := make([]types.Message, 0, len(batch))
	for _, batchCall := range batch {
		callID := strings.TrimSpace(batchCall.ID)
		if callID == "" || callID == strings.TrimSpace(currentToolCallID) {
			break
		}
		if message := findToolResultMessage(history, callID); message != nil {
			messages = append(messages, *message.Clone())
		}
	}
	return messages
}

func findToolResultMessage(history []types.Message, toolCallID string) *types.Message {
	if len(history) == 0 || strings.TrimSpace(toolCallID) == "" {
		return nil
	}
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role != "tool" || strings.TrimSpace(history[index].ToolCallID) != strings.TrimSpace(toolCallID) {
			continue
		}
		return &history[index]
	}
	return nil
}
