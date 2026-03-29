package agent

import (
	"context"

	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
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

func (a *Agent) runPostToolUseHooks(ctx context.Context, sessionID string, result toolExecutionResult) {
	if a == nil {
		return
	}
	if hookMgr := a.GetHookManager(); hookMgr != nil {
		payload := map[string]interface{}{
			"session_id":   sessionID,
			"tool_name":    result.Call.Name,
			"tool_call_id": result.Call.ID,
			"error":        result.Error,
		}
		if result.Envelope != nil && len(result.Envelope.Metadata) > 0 {
			for key, value := range result.Envelope.Metadata {
				payload[key] = value
			}
		}
		hookMgr.DispatchAsync(ctx, runtimehooks.EventPostToolUse, payload)
	}
	a.mu.RLock()
	hooks := make([]func(context.Context, string, toolExecutionResult), len(a.toolHooks.PostToolUse))
	copy(hooks, a.toolHooks.PostToolUse)
	a.mu.RUnlock()
	for _, hook := range hooks {
		hook(ctx, sessionID, result)
	}
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
