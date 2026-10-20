package agent

import (
	"context"
	"strings"

	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ToolHooks 提供工具调用前后的可拦截扩展点。
type ToolHooks struct {
	PreToolUse  []func(context.Context, string, types.ToolCall) error
	PostToolUse []func(context.Context, string, toolExecutionResult)
}

// AddPreToolUse 注册一个 PreToolUse hook。
func (a *Agent) AddPreToolUse(hook func(context.Context, string, types.ToolCall) error) {
	if a == nil || hook == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toolHooks.PreToolUse = append(a.toolHooks.PreToolUse, hook)
}

// AddPostToolUse 注册一个 PostToolUse hook。
func (a *Agent) AddPostToolUse(hook func(context.Context, string, toolExecutionResult)) {
	if a == nil || hook == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toolHooks.PostToolUse = append(a.toolHooks.PostToolUse, hook)
}

func (a *Agent) runPreToolUseHooks(ctx context.Context, sessionID string, call types.ToolCall) error {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	hooks := make([]func(context.Context, string, types.ToolCall) error, len(a.toolHooks.PreToolUse))
	copy(hooks, a.toolHooks.PreToolUse)
	a.mu.RUnlock()
	for _, hook := range hooks {
		if err := hook(ctx, sessionID, call); err != nil {
			return err
		}
	}
	return nil
}

// hookBasePayload builds the Codex-compatible common input fields shared by
// tool hooks. Existing native fields (args/tool_call) are preserved.
func (a *Agent) hookBasePayload(ctx context.Context, sessionID, traceID, toolName, toolCallID string, args map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{
		"session_id":      sessionID,
		"turn_id":         TurnIDFromContext(ctx),
		"trace_id":        traceID,
		"tool_name":       toolName,
		"tool_input":      args,
		"tool_use_id":     toolCallID,
		"hook_event_name": "",
	}
	if payload["turn_id"] == "" {
		delete(payload, "turn_id")
	}
	if payload["trace_id"] == "" {
		delete(payload, "trace_id")
	}
	if a != nil {
		if cfg := a.GetConfig(); cfg != nil {
			payload["model"] = cfg.Model
		}
	}
	if mode := permissionModeFromContext(ctx); mode != "" {
		payload["permission_mode"] = mode
	}
	if root := toolctx.WorkspaceRoot(ctx); root != "" {
		payload["cwd"] = root
	}
	return payload
}

func (a *Agent) runPostToolUseHooks(ctx context.Context, sessionID string, result toolExecutionResult) {
	if a == nil {
		return
	}
	if hookMgr := a.GetHookManager(); hookMgr != nil && strings.TrimSpace(result.Error) != "" {
		// EventPostToolUse itself is dispatched synchronously by the loop
		// (dispatchPostToolUseHook) so its additionalContext/feedback reaches
		// the next model step. Only the failure notification stays async here.
		payload := a.hookBasePayload(ctx, sessionID, "", result.Call.Name, result.Call.ID, result.Call.Args)
		payload["tool_call_id"] = result.Call.ID
		payload["error"] = result.Error
		payload["hook_event_name"] = "PostToolUseFailure"
		payload["tool_response"] = toolHookResponse(result)
		if result.Envelope != nil && len(result.Envelope.Metadata) > 0 {
			for key, value := range result.Envelope.Metadata {
				payload[key] = value
			}
		}
		hookMgr.DispatchAsync(ctx, runtimehooks.EventPostToolUseFailure, payload)
	}
	a.mu.RLock()
	hooks := make([]func(context.Context, string, toolExecutionResult), len(a.toolHooks.PostToolUse))
	copy(hooks, a.toolHooks.PostToolUse)
	a.mu.RUnlock()
	for _, hook := range hooks {
		hook(ctx, sessionID, result)
	}
}

// dispatchPostToolUseHook runs synchronous PostToolUse hooks and returns the
// combined decision. The tool has already executed; a block decision is
// feedback for the next model step, not a retroactive cancellation.
func (a *Agent) dispatchPostToolUseHook(ctx context.Context, sessionID string, result toolExecutionResult) runtimehooks.Decision {
	if a == nil {
		return runtimehooks.Decision{Action: runtimehooks.DecisionContinue}
	}
	hookMgr := a.GetHookManager()
	if hookMgr == nil {
		return runtimehooks.Decision{Action: runtimehooks.DecisionContinue}
	}
	payload := a.hookBasePayload(ctx, sessionID, "", result.Call.Name, result.Call.ID, result.Call.Args)
	payload["tool_call_id"] = result.Call.ID
	payload["error"] = result.Error
	payload["hook_event_name"] = "PostToolUse"
	payload["tool_response"] = toolHookResponse(result)
	decision, err := hookMgr.DispatchSync(ctx, runtimehooks.EventPostToolUse, payload)
	if err != nil {
		return runtimehooks.Decision{Action: runtimehooks.DecisionContinue}
	}
	return decision
}

func toolHookResponse(result toolExecutionResult) map[string]interface{} {
	response := map[string]interface{}{}
	if result.Envelope != nil {
		response["summary"] = result.Envelope.Summary
		response["ok"] = result.Envelope.OK
		response["error_code"] = result.Envelope.ErrorCode
	} else if result.Output != nil {
		response["output"] = result.Output
	}
	if strings.TrimSpace(result.Error) != "" {
		response["error"] = result.Error
	}
	return response
}

func (a *Agent) snapshotToolHooks() ToolHooks {
	if a == nil {
		return ToolHooks{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()

	snapshot := ToolHooks{
		PreToolUse:  make([]func(context.Context, string, types.ToolCall) error, len(a.toolHooks.PreToolUse)),
		PostToolUse: make([]func(context.Context, string, toolExecutionResult), len(a.toolHooks.PostToolUse)),
	}
	copy(snapshot.PreToolUse, a.toolHooks.PreToolUse)
	copy(snapshot.PostToolUse, a.toolHooks.PostToolUse)
	return snapshot
}

func (a *Agent) inheritToolHooksFrom(parent *Agent) {
	if a == nil || parent == nil {
		return
	}
	snapshot := parent.snapshotToolHooks()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toolHooks = snapshot
}
