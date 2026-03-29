package agent

import (
	"context"
	"fmt"
	"strings"

	runtimecheckpoint "github.com/ai-gateway/ai-agent-runtime/internal/checkpoint"
	runtimehooks "github.com/ai-gateway/ai-agent-runtime/internal/hooks"
	"github.com/ai-gateway/ai-agent-runtime/internal/output"
	runtimepolicy "github.com/ai-gateway/ai-agent-runtime/internal/policy"
	"github.com/ai-gateway/ai-agent-runtime/internal/types"
	"github.com/google/uuid"
)

// ExecuteApprovedToolCall executes a previously approved tool call without re-running approval checks.
func (a *Agent) ExecuteApprovedToolCall(ctx context.Context, sessionID string, call types.ToolCall, history []types.Message) (*types.Message, error) {
	if a == nil {
		return nil, fmt.Errorf("agent is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	traceID := "trace_" + uuid.NewString()
	gateway := a.GetOutputGateway()
	result := toolExecutionResult{Call: call}
	metadata := map[string]interface{}{
		"step":            0,
		"trace_id":        traceID,
		"approved_resume": true,
	}
	a.emitRuntimeEvent("tool.requested", sessionID, call.Name, map[string]interface{}{
		"tool_call_id": call.ID,
		"trace_id":     traceID,
		"approved":     true,
	})

	finalize := func() *types.Message {
		envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
			SessionID:  sessionID,
			ToolName:   call.Name,
			ToolCallID: call.ID,
			Step:       0,
			Content:    result.Output,
			Error:      result.Error,
			Metadata:   metadata,
		})
		if gatewayErr != nil && envelope != nil {
			envelope.Metadata["gateway_error"] = gatewayErr.Error()
		}
		result.Envelope = envelope
		a.emitRuntimeEvent("tool.completed", sessionID, call.Name, map[string]interface{}{
			"tool_call_id": call.ID,
			"error":        result.Error,
			"trace_id":     traceID,
			"approved":     true,
		})
		a.runPostToolUseHooks(ctx, sessionID, result)
		message := types.NewToolMessage(call.ID, "")
		if envelope != nil {
			message.Content = envelope.Render()
			if len(envelope.Metadata) > 0 {
				message.Metadata = types.NewMetadata()
				for key, value := range envelope.Metadata {
					message.Metadata[key] = value
				}
			}
		}
		if strings.TrimSpace(message.Content) == "" && strings.TrimSpace(result.Error) != "" {
			message.Content = "Tool execution failed: " + strings.TrimSpace(result.Error)
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
		rawOutput, rawMeta, callErr := broker.ExecuteToolCall(ctx, sessionID, call)
		if callErr != nil {
			result.Error = callErr.Error()
		} else {
			result.Output = rawOutput
			if len(rawMeta) > 0 {
				metadata["tool_metadata"] = cloneInterfaceMap(rawMeta)
			}
		}
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

	var pending *runtimecheckpoint.PendingCheckpoint
	checkpointMgr := a.GetCheckpointManager()
	if checkpointMgr != nil && (runtimepolicy.IsWriteLikeToolName(call.Name) || runtimepolicy.IsShellLikeToolName(call.Name) || runtimepolicy.HasMutationHints(call.Args)) {
		pending, _ = checkpointMgr.BeforeMutation(ctx, sessionID, call.Name, call.ID, call.Args)
		if pending != nil {
			pending.MessageCount = len(history)
			pending.Conversation = cloneMessages(history)
		}
	}

	var rawMeta map[string]interface{}
	if caller, ok := a.mcpManager.(richToolCaller); ok {
		result.Output, rawMeta, err = caller.CallToolWithMeta(ctx, toolInfo.MCPName, call.Name, call.Args)
	} else {
		result.Output, err = a.mcpManager.CallTool(ctx, toolInfo.MCPName, call.Name, call.Args)
	}
	if err != nil {
		result.Error = err.Error()
	} else if len(rawMeta) > 0 {
		metadata["tool_metadata"] = cloneInterfaceMap(rawMeta)
	}

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
