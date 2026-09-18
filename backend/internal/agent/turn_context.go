package agent

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type turnSystemMessagesKey struct{}

type turnPinnedToolsKey struct{}

// WithTurnSystemMessages 为当前回合附加一次性 system 消息（如 skill 程序说明）。
// 这些消息随 ctx 传入 loop，只在本次 run 的请求历史里生效，不落会话持久历史。
func WithTurnSystemMessages(ctx context.Context, messages []types.Message) context.Context {
	if len(messages) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnSystemMessagesKey{}, append([]types.Message(nil), messages...))
}

// turnSystemMessagesFromContext 返回当前回合附加的 system 消息副本。
func turnSystemMessagesFromContext(ctx context.Context) []types.Message {
	if ctx == nil {
		return nil
	}
	messages, _ := ctx.Value(turnSystemMessagesKey{}).([]types.Message)
	if len(messages) == 0 {
		return nil
	}
	return append([]types.Message(nil), messages...)
}

// WithTurnPinnedTools 为当前回合叠加额外工具定义（如 /skill 注入的 skill 函数与
// 声明的程序）。pin 只在本次 run 的内存工具面上生效，不写回会话级稳定工具面。
func WithTurnPinnedTools(ctx context.Context, tools []types.ToolDefinition) context.Context {
	if len(tools) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnPinnedToolsKey{}, cloneToolDefinitions(tools))
}

// turnPinnedToolsFromContext 返回当前回合叠加的工具定义副本。
func turnPinnedToolsFromContext(ctx context.Context) []types.ToolDefinition {
	if ctx == nil {
		return nil
	}
	tools, _ := ctx.Value(turnPinnedToolsKey{}).([]types.ToolDefinition)
	if len(tools) == 0 {
		return nil
	}
	return cloneToolDefinitions(tools)
}

// overlayTurnPinnedTools 把本回合 pin 的工具叠加到基础工具面上：同名 pin 覆盖基础
// 定义（保证 pin 的 schema 生效），不同名按基础顺序在前、pin 追加在后。返回值是新
// 切片，调用方不得据此覆盖会话级稳定工具面缓存。
func overlayTurnPinnedTools(base []types.ToolDefinition, pinned []types.ToolDefinition) []types.ToolDefinition {
	if len(pinned) == 0 {
		return base
	}
	merged := make([]types.ToolDefinition, 0, len(base)+len(pinned))
	for _, item := range base {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		replaced := false
		for _, pin := range pinned {
			if strings.EqualFold(strings.TrimSpace(pin.Name), name) {
				merged = append(merged, pin)
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, item)
		}
	}
	seen := make(map[string]struct{}, len(merged))
	for _, item := range merged {
		seen[strings.ToLower(strings.TrimSpace(item.Name))] = struct{}{}
	}
	for _, pin := range pinned {
		name := strings.TrimSpace(pin.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, pin)
	}
	return merged
}

// ---------------------------------------------------------------------------
// 既有 turn-id 契约（供 runtime event payload / 延迟事件隔离使用，勿删）
// ---------------------------------------------------------------------------

type turnIDContextKey struct{}

// WithTurnID annotates an agent run with the durable chat actor turn identity.
// The ReAct trace ID is intentionally separate and must not be used to isolate
// delayed runtime events across chat turns.
func WithTurnID(ctx context.Context, turnID string) context.Context {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnIDContextKey{}, turnID)
}

// TurnIDFromContext returns the durable chat actor turn identity, when present.
func TurnIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	turnID, _ := ctx.Value(turnIDContextKey{}).(string)
	return strings.TrimSpace(turnID)
}

func runtimeEventPayloadWithTurnID(ctx context.Context, payload map[string]interface{}) map[string]interface{} {
	turnID := TurnIDFromContext(ctx)
	if turnID == "" {
		return payload
	}
	if payload == nil {
		payload = make(map[string]interface{})
	}
	payload["turn_id"] = turnID
	return payload
}
