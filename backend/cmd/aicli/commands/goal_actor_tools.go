package commands

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclitools"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

const goalActorMCPName = "aicli_goal"

func wrapGoalToolSurface(session *ChatSession, next runtimeskill.MCPManager) runtimeskill.MCPManager {
	return &aiclitools.CapabilityMCPManager{
		Registry: goalCapabilityRegistry(),
		Next:     next,
		ContextFactory: func(ctx context.Context) aiclitools.ToolSessionContext {
			return newChatToolSessionContext(session, aiclitools.ExposureActor)
		},
		Enabled: func() bool {
			return session != nil && !session.DisableTools && hasRuntimeSessionGoalPersistence(session)
		},
		Path:    aiclitools.ExposureActor,
		MCPName: goalActorMCPName,
	}
}

func isGoalActorTool(name string) bool {
	return name == getGoalFunctionName || name == updateGoalFunctionName
}

func hasRuntimeSessionGoalPersistence(session *ChatSession) bool {
	return session != nil && session.RuntimeSession != nil && session.SessionManager != nil
}
