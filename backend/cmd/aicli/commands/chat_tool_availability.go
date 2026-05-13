package commands

import (
	"context"
	"strings"
	"time"
)

// chatToolAvailable reports whether the current executor path has the
// capability to expose toolName. It is a path-level exposure capability check:
// it does not prove the current provider request includes the tool, and it is
// not authorization for a concrete tool call.
func chatToolAvailable(session *ChatSession, toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	if session == nil || toolName == "" || session.DisableTools {
		return false
	}
	if !chatToolAllowedByPolicy(session, toolName) {
		return false
	}
	switch session.ChatExecutor.(type) {
	case *aicliActorChatExecutor:
		return chatActorToolAvailable(session, toolName)
	case *aicliRuntimeServerChatExecutor:
		return chatRuntimeServerToolAvailable(session, toolName)
	default:
		return chatSharedToolAvailable(session, toolName)
	}
}

func chatSharedToolAvailable(session *ChatSession, toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	if session == nil || toolName == "" || session.DisableTools {
		return false
	}
	if !chatToolAllowedByPolicy(session, toolName) {
		return false
	}
	catalog := session.FunctionCatalog
	if catalog == nil || catalog.Registry() == nil {
		return false
	}
	_, ok := catalog.Registry().Get(toolName)
	return ok
}

func chatActorToolAvailable(session *ChatSession, toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	if session == nil || toolName == "" || session.DisableTools {
		return false
	}
	if !chatToolAllowedByPolicy(session, toolName) {
		return false
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ToolSurface == nil {
		return false
	}
	_, err := session.LocalRuntimeHost.ToolSurface.FindTool(toolName)
	return err == nil
}

func chatRuntimeServerToolAvailable(session *ChatSession, toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	if session == nil || toolName == "" || session.DisableTools {
		return false
	}
	if !chatToolAllowedByPolicy(session, toolName) {
		return false
	}
	executor, ok := session.ChatExecutor.(*aicliRuntimeServerChatExecutor)
	if !ok || executor == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return executor.toolAvailable(ctx, session, toolName)
}

func chatToolAllowedByPolicy(session *ChatSession, toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	if session == nil || toolName == "" {
		return false
	}
	if session.ToolPolicy == nil {
		return true
	}
	return session.ToolPolicy.AllowsDefinition(toolName)
}
