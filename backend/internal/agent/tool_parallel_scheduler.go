package agent

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type parallelToolCallPlan struct {
	index    int
	call     types.ToolCall
	toolInfo runtimeskill.ToolInfo
}

type parallelToolBatchPlan struct {
	calls       []parallelToolCallPlan
	globalLimit int
}

func (loop *ReActLoop) buildParallelToolBatchPlan(toolCalls []types.ToolCall, toolWhitelist []string) *parallelToolBatchPlan {
	if parallelToolsDisabledByEnv() {
		return nil
	}
	if loop == nil || loop.config == nil || !loop.config.EnableParallelTools || loop.config.MaxParallelToolCalls <= 1 {
		return nil
	}
	if len(toolCalls) < 2 {
		return nil
	}
	if loop.agent == nil || loop.agent.mcpManager == nil {
		return nil
	}
	if loop.agent.GetHookManager() != nil {
		return nil
	}
	if hooks := loop.agent.snapshotToolHooks(); len(hooks.PreToolUse) > 0 || len(hooks.PostToolUse) > 0 {
		return nil
	}
	if !isParallelPermissionEngineSafe(loop.agent.GetPermissionEngine()) {
		return nil
	}

	allowedTools := whitelistSet(toolWhitelist)
	policy := loop.agent.GetToolExecutionPolicy()
	plan := &parallelToolBatchPlan{
		calls:       make([]parallelToolCallPlan, 0, len(toolCalls)),
		globalLimit: loop.config.MaxParallelToolCalls,
	}

	for index, tc := range toolCalls {
		name := strings.TrimSpace(tc.Name)
		if name == "" {
			return nil
		}
		if isParallelBrokerToolCall(name) || runtimepolicy.IsWriteLikeToolName(name) || runtimepolicy.IsShellLikeToolName(name) || runtimepolicy.HasMutationHints(tc.Args) {
			return nil
		}
		if name == "spawn_subagents" {
			return nil
		}
		if len(allowedTools) > 0 && !allowedTools[name] {
			return nil
		}

		toolInfo, err := loop.agent.mcpManager.FindTool(name)
		if err != nil {
			return nil
		}
		if !toolInfo.Enabled {
			return nil
		}
		if supportsParallel, declared := toolInfo.ParallelSupport(); declared {
			if !supportsParallel {
				return nil
			}
		} else if !isParallelReadOnlyTool(name, toolInfo, tc.Args) {
			return nil
		}
		if policy != nil {
			if err := policy.AllowToolCall(toolInfo, tc.Args); err != nil {
				return nil
			}
		}
		if toolInfo.MCPName != "" && toolInfo.MaxParallelCalls <= 1 {
			return nil
		}

		plan.calls = append(plan.calls, parallelToolCallPlan{
			index:    index,
			call:     tc,
			toolInfo: toolInfo,
		})
	}

	if len(plan.calls) < 2 {
		return nil
	}
	if plan.globalLimit > len(plan.calls) {
		plan.globalLimit = len(plan.calls)
	}
	if plan.globalLimit <= 1 {
		return nil
	}

	return plan
}

func (loop *ReActLoop) runParallelToolBatch(ctx context.Context, traceID, sessionID string, step int, toolCalls []types.ToolCall, plan *parallelToolBatchPlan) []toolExecutionResult {
	results := make([]toolExecutionResult, len(toolCalls))
	if loop == nil || plan == nil || len(plan.calls) == 0 {
		return results
	}

	gateway := loop.agent.GetOutputGateway()
	globalSem := make(chan struct{}, plan.globalLimit)
	mcpSems := buildParallelMCPGates(plan)

	var wg sync.WaitGroup
	wg.Add(len(plan.calls))
	for _, item := range plan.calls {
		item := item
		go func() {
			defer wg.Done()
			results[item.index] = loop.executeParallelToolCall(ctx, gateway, traceID, sessionID, step, toolCalls, globalSem, mcpSems[item.toolInfo.MCPName], item)
		}()
	}
	wg.Wait()
	return results
}

func buildParallelMCPGates(plan *parallelToolBatchPlan) map[string]chan struct{} {
	if plan == nil || len(plan.calls) == 0 {
		return nil
	}
	gates := make(map[string]chan struct{})
	for _, item := range plan.calls {
		mcpName := strings.TrimSpace(item.toolInfo.MCPName)
		if mcpName == "" {
			continue
		}
		limit := item.toolInfo.MaxParallelCalls
		if limit <= 0 {
			limit = 1
		}
		if limit > plan.globalLimit {
			limit = plan.globalLimit
		}
		if existing, ok := gates[mcpName]; ok {
			if cap(existing) > limit {
				gates[mcpName] = make(chan struct{}, limit)
			}
			continue
		}
		gates[mcpName] = make(chan struct{}, limit)
	}
	return gates
}

func (loop *ReActLoop) executeParallelToolCall(ctx context.Context, gateway *output.Gateway, traceID, sessionID string, step int, toolCalls []types.ToolCall, globalSem, mcpSem chan struct{}, item parallelToolCallPlan) toolExecutionResult {
	result := toolExecutionResult{Call: item.call}
	metadata := map[string]interface{}{
		"step":     step,
		"trace_id": traceID,
	}
	if source := parallelToolSource(item); source != "" {
		metadata[toolresult.SourceKey] = source
	}
	if item.toolInfo.MCPName != "" {
		metadata["mcp_name"] = item.toolInfo.MCPName
		metadata["trust_level"] = item.toolInfo.MCPTrustLevel
		metadata["execution_mode"] = item.toolInfo.ExecutionMode
	}

	callCtx := promoteTeamRunContext(toolCallContext(ctx, toolCalls, item.call.ID, nil, loop.agent, sessionID), nil)
	loop.agent.emitRuntimeEvent("tool.requested", sessionID, item.call.Name, toolRequestedEventPayload(item.call, step, traceID, map[string]interface{}{
		"parallel":           true,
		"batch_index":        item.index,
		"batch_size":         len(toolCalls),
		toolresult.SourceKey: parallelToolSource(item),
	}))

	if err := acquireParallelSlot(ctx, globalSem); err != nil {
		result.Error = err.Error()
		return loop.finishParallelToolCall(ctx, gateway, sessionID, step, traceID, metadata, result, item, len(toolCalls))
	}
	defer releaseParallelSlot(globalSem)

	if err := acquireParallelSlot(ctx, mcpSem); err != nil {
		result.Error = err.Error()
		return loop.finishParallelToolCall(ctx, gateway, sessionID, step, traceID, metadata, result, item, len(toolCalls))
	}
	defer releaseParallelSlot(mcpSem)

	if err := ctx.Err(); err != nil {
		result.Error = err.Error()
		return loop.finishParallelToolCall(ctx, gateway, sessionID, step, traceID, metadata, result, item, len(toolCalls))
	}

	var (
		rawOutput interface{}
		rawMeta   map[string]interface{}
		err       error
	)
	if caller, ok := loop.agent.mcpManager.(richToolCaller); ok {
		rawOutput, rawMeta, err = caller.CallToolWithMeta(callCtx, item.toolInfo.MCPName, item.call.Name, item.call.Args)
	} else {
		rawOutput, err = loop.agent.mcpManager.CallTool(callCtx, item.toolInfo.MCPName, item.call.Name, item.call.Args)
	}
	recordToolExecutionOutcome(&result, metadata, rawOutput, rawMeta, err)

	return loop.finishParallelToolCall(ctx, gateway, sessionID, step, traceID, metadata, result, item, len(toolCalls))
}

func (loop *ReActLoop) finishParallelToolCall(ctx context.Context, gateway *output.Gateway, sessionID string, step int, traceID string, metadata map[string]interface{}, result toolExecutionResult, item parallelToolCallPlan, batchSize int) toolExecutionResult {
	envelope, gatewayErr := gateway.Process(ctx, output.RawToolResult{
		SessionID:  sessionID,
		ToolName:   result.Call.Name,
		ToolCallID: result.Call.ID,
		Step:       step,
		Content:    result.Output,
		Error:      result.Error,
		Metadata:   metadata,
	})
	if gatewayErr != nil && envelope != nil {
		envelope.Metadata["gateway_error"] = gatewayErr.Error()
	}
	result.Envelope = envelope

	loop.agent.emitRuntimeEvent("tool.completed", sessionID, result.Call.Name, toolCompletedEventPayload(result, step, traceID, map[string]interface{}{
		"awaiting_model": false,
		"parallel":       true,
		"batch_index":    item.index,
		"batch_size":     batchSize,
	}))
	loop.emitToolReduced(sessionID, result.Call, step, traceID, result, map[string]interface{}{
		"mcp_name":       item.toolInfo.MCPName,
		"execution_mode": item.toolInfo.ExecutionMode,
		"parallel":       true,
		"batch_index":    item.index,
		"batch_size":     batchSize,
	})
	loop.agent.runPostToolUseHooks(ctx, sessionID, result)
	return result
}

func acquireParallelSlot(ctx context.Context, sem chan struct{}) error {
	if sem == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseParallelSlot(sem chan struct{}) {
	if sem == nil {
		return
	}
	select {
	case <-sem:
	default:
	}
}

func parallelToolSource(item parallelToolCallPlan) string {
	name := strings.TrimSpace(item.call.Name)
	if name == "" {
		return ""
	}
	if name == "list_mcp_resources" {
		return toolresult.SourceMeta
	}
	if strings.TrimSpace(item.toolInfo.MCPName) != "" {
		return toolresult.SourceMCP
	}
	return toolresult.SourceToolkit
}

func parallelToolsDisabledByEnv() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_DISABLE_PARALLEL_TOOLS")))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isParallelBrokerToolCall(name string) bool {
	return (&toolbroker.Broker{}).IsBrokerTool(name)
}

func isParallelReadOnlyTool(name string, toolInfo runtimeskill.ToolInfo, args map[string]interface{}) bool {
	resolver := runtimepolicy.DefaultCapabilityResolver{}
	caps := resolver.Resolve(runtimepolicy.EvalRequest{
		ToolName: name,
		ToolInfo: &toolInfo,
		Args:     args,
	})
	if len(caps) == 0 {
		return false
	}
	for _, cap := range caps {
		if cap != runtimepolicy.CapReadOnly {
			return false
		}
	}
	return true
}

// v1 只支持默认形态的 permission engine；自定义 hooks、rules、callback
// 或 capability resolver 都可能改变 allow/deny 结果，必须回退串行。
func isParallelPermissionEngineSafe(engine *PermissionEngine) bool {
	if engine == nil {
		return true
	}
	if engine.Hooks != nil || len(engine.Rules) > 0 || engine.Callback != nil {
		return false
	}
	switch engine.CapabilityResolver.(type) {
	case nil, runtimepolicy.DefaultCapabilityResolver, *runtimepolicy.DefaultCapabilityResolver:
		return true
	default:
		return false
	}
}
