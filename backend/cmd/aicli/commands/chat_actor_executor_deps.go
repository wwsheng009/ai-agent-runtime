package commands

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// deriveLocalSessionContext 从 **runtime session** 派生执行上下文（E3.4a-1）。
//
// 与 runtimeapi 的 `runtimehost.ExtractSessionContext`（来源 `chat.Session.Metadata.Context`）
// **同构但来源不同**——aicli 的会话上下文存在 runtime session store 里，经
// `GetContext` / `agentcontrol.ContextString` 读取（见 aicli-executor-reuse-design.md §2 D1）。
//
// 语义与抽取前 `buildSessionActor` 的内联块逐条一致（行为零变化）：
//   - `agent_type` / `completion requirement` 对基础会话与子会话都读取；
//   - worktree/workspace 覆盖、provider/model/reasoning/read_only/depth **仅子会话**读取；
//   - 存储不可用 / 会话不存在时返回零值上下文（不报错）。
//
// 返回覆盖后的 workspaceRoot（子会话可能被 worktree 或 workspace 覆盖）。
func deriveLocalSessionContext(
	sessionStore runtimechat.SessionStorage,
	sessionID string,
	isBaseSession bool,
	workspaceRoot string,
) (runtimehost.SessionContext, string) {
	sessionCtx := runtimehost.SessionContext{}
	if sessionStore == nil {
		return sessionCtx, workspaceRoot
	}
	runtimeSession, err := sessionStore.Load(context.Background(), sessionID)
	if err != nil || runtimeSession == nil {
		return sessionCtx, workspaceRoot
	}
	if value, ok := runtimeSession.GetContext(toolbroker.AgentSessionContextAgentType); ok {
		if text, ok := value.(string); ok {
			sessionCtx.ChildAgentType = strings.TrimSpace(text)
		}
	}
	sessionCtx.ChildCompletionRequirement = agentcontrol.ContextString(runtimeSession, toolbroker.AgentSessionContextCompletionRequirement)
	if !isBaseSession {
		// Prefer per-child worktree path so tools and system prompt cwd stay isolated.
		if path := agentcontrol.ContextString(runtimeSession, toolbroker.AgentSessionContextWorktreePath); path != "" {
			workspaceRoot = path
		} else if path := agentcontrol.ContextString(runtimeSession, sessionmeta.WorkspacePath); path != "" {
			workspaceRoot = path
		}
		sessionCtx.RequestedProvider = agentcontrol.ContextString(runtimeSession, sessionmeta.ProviderName)
		sessionCtx.RequestedModel = agentcontrol.ContextString(runtimeSession, toolbroker.AgentSessionContextRequestedModel)
		if sessionCtx.RequestedModel == "" {
			sessionCtx.RequestedModel = agentcontrol.ContextString(runtimeSession, sessionmeta.Model)
		}
		sessionCtx.RequestedReasoningEffort = agentcontrol.ContextString(runtimeSession, sessionmeta.ReasoningEffort)
		if value, ok := runtimeSessionContextBool(runtimeSession, toolbroker.AgentSessionContextReadOnly); ok {
			sessionCtx.ChildReadOnly = value
		}
		sessionCtx.ChildDepth = localAgentSessionDepth(runtimeSession)
	}
	return sessionCtx, workspaceRoot
}
