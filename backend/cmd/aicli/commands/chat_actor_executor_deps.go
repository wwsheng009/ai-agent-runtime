package commands

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
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

// aicliRuntimeDeps 为某个 sessionID 装配执行核依赖（E3.4a-2：**只装配、不切换调用点**）。
//
// 设计依据：`aicli-executor-reuse-design.md` §4（差异映射）。要点：
//   - **按 sessionID 派生**（hub 工厂以「基础会话闭包 + 变化的 sessionID」调用，§11 G2）；
//   - 会话上下文走 aicli 自己的 `deriveLocalSessionContext`（runtime session 来源，§2 D1）；
//   - agent / loop 装配复用 aicli 本地 helper（`PrepareAgent` / `BuildLoopConfig`，§4 D2–D4）；
//   - 租约为 **actor 生命周期**（`LeaseModeActorScoped`，§11 G1 / §7 O1）；
//   - `PrepareRun` 捕获 `apiAgent`（§4 D7），故这里**先装配 agent**，再交给 `PrepareRun`。
//
// 说明：本函数当前**没有调用方**（E3.4b 才切换 `buildSessionActor`），因此行为不可能改变；
// 其中的装配逻辑与 `buildSessionActor` 当前实现**暂时重复**，E3.4b 切换后删除旧路径。
func (h *localChatRuntimeHost) aicliRuntimeDeps(
	baseSession *ChatSession,
	sessionStore runtimechat.SessionStorage,
	runtimeConfig *runtimecfg.RuntimeConfig,
	workspaceRoot string,
	sessionID string,
) runtimehost.Deps {
	baseSessionID := ""
	if baseSession != nil && baseSession.RuntimeSession != nil {
		baseSessionID = strings.TrimSpace(baseSession.RuntimeSession.ID)
	}
	isBaseSession := baseSessionID != "" && strings.EqualFold(strings.TrimSpace(sessionID), baseSessionID)
	sessionCtx, effectiveWorkspace := deriveLocalSessionContext(sessionStore, sessionID, isBaseSession, workspaceRoot)

	// agent 装配（与 buildSessionActor 同序：agent → 工具策略 → loop）。
	apiAgent := buildLocalChatAgent(baseSession, h, runtimeConfig, effectiveWorkspace,
		sessionCtx.ChildAgentType, sessionCtx.RequestedModel, sessionCtx.RequestedProvider, sessionCtx.RequestedReasoningEffort)
	if !isBaseSession && strings.TrimSpace(sessionCtx.ChildAgentType) != "" {
		toolkitBasePath := ""
		switch {
		case runtimeConfig != nil:
			toolkitBasePath = runtimeConfig.Workspace.Root
		case h != nil && h.RuntimeConfig != nil:
			toolkitBasePath = h.RuntimeConfig.Workspace.Root
		}
		applyLocalChildAgentdefToolPolicy(apiAgent, sessionCtx.ChildAgentType, baseSession, effectiveWorkspace, toolkitBasePath)
	}
	applyLocalChildReadOnlyPolicy(apiAgent, sessionCtx.ChildReadOnly)
	applyLocalChildDepthPolicy(apiAgent, sessionCtx.ChildDepth, localAgentDepthCeiling(runtimeConfig, h))

	loopConfig := buildLocalChatLoopConfig(runtimeConfig, baseSession, sessionCtx.RequestedReasoningEffort)
	loopConfig.AgentSessionObligations = h.agentSessionObligationResolver()
	loopConfig.TeamObligations = h.teamObligationResolver()
	applyLocalChatMainAgentRouting(loopConfig, baseSession, isBaseSession)
	observationSubagent := !isBaseSession || strings.TrimSpace(sessionCtx.ChildAgentType) != "" || sessionCtx.ChildDepth > 0 || sessionCtx.ChildReadOnly
	applyLocalChatToolObservation(loopConfig, baseSession, h,
		knowledge.ObservationSourceFor(observationSubagent, sessionCtx.ChildReadOnly), localChatObservationTaskID(baseSession))
	applyLocalChatCompletionRequirement(loopConfig, baseSession, sessionCtx.ChildAgentType, sessionCtx.ChildCompletionRequirement, effectiveWorkspace)

	acquire := func(ctx context.Context, sid string) (*runtimechat.SessionLeaseHandle, error) {
		return acquireLocalChatSessionLease(ctx, h.RuntimeStore, sid)
	}
	return runtimehost.Deps{
		SessionContext:            sessionCtx,
		SessionStorage:            sessionStore,
		RuntimeStore:              h.RuntimeStore,
		EventStore:                h.EventStore,
		LLMRuntime:                aicliHostLLMRuntime(h),
		EventBus:                  h.EventBus,
		AcquireSessionLeaseNoWait: acquire,
		AcquireSessionLease:       acquire,
		// O1：aicli 在 actor 生命周期内持锁（仅 stalled/stop 释放）。
		LeaseMode: runtimehost.LeaseModeActorScoped,
		PrepareAgent: func(context.Context, string, runtimehost.SessionContext) (*agent.Agent, *runtimecfg.RuntimeConfig, any) {
			return apiAgent, runtimeConfig, nil
		},
		BuildLoopConfig: func(string, runtimehost.SessionContext, *runtimecfg.RuntimeConfig, any) *agent.LoopReActConfig {
			return loopConfig
		},
		OnProgress:            h.progressRecorderForSession(sessionID),
		OnBudgetProgress:      h.budgetRecorderForSession(sessionID),
		ApprovalTerminalGuard: h.supervisionConfig.ApprovalTerminalGuard,
		TriggerTurnDrain:      h.supervisionConfig.TriggerTurnDrainEnabled(),
		TriggerTurnRunMeta: func(_ context.Context, session *runtimechat.Session) *team.RunMeta {
			return toolbroker.SpawnAgentRunMetaFromContext(session)
		},
		SuspendedTurnAbandon: runtimechat.SuspendedTurnAbandonHooks{
			CancelAgentSession: h.cancelSuspendedTurnAgentSession,
			CancelTeam:         h.cancelSuspendedTurnTeam,
		},
		PersistHook:        localGoalPersistHook(sessionStore),
		EnsureSession:      localChatActorEnsureSession(baseSession, isBaseSession),
		CheckpointInterval: localChatSessionCheckpointIntervalFromEnv(),
		RunStallTimeout:    localChatRunStallTimeoutFromEnv(),
		// PrepareRun 捕获 apiAgent（§4 D7）；子会话时 helper 返回 nil → 留给 runtimehost 默认（actor-scoped 下为 no-op）。
		PrepareRun: localChatPrepareRunHook(apiAgent, baseSession, effectiveWorkspace, isBaseSession, h.ToolSurface),
	}
}

// aicliHostLLMRuntime 返回宿主的 LLM runtime；Bootstrap 未装配时为 nil（便于单测/降级）。
func aicliHostLLMRuntime(h *localChatRuntimeHost) *llm.LLMRuntime {
	if h == nil || h.Bootstrap == nil {
		return nil
	}
	return h.Bootstrap.LLMRuntime()
}
