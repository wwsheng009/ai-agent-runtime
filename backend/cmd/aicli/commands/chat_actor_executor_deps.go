package commands

import (
	"context"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// deriveLocalSessionContext 从 runtime session 派生执行上下文。
//
// The extraction itself is host-neutral: `runtimehost.ExtractSessionContext`
// owns the metadata-key mapping so aicli and runtime-server cannot drift on
// profile / routing / observation / stream / disable-tools / child fields.
// `isBaseSession` remains a policy decision at the call sites; it must not make
// the same durable row project to a different SessionContext shape.
//
// 存储不可用 / 会话不存在时返回零值上下文（不报错）。返回覆盖后的
// workspaceRoot（worktree 或 session workspace 优先）。
func deriveLocalSessionContext(
	sessionStore runtimechat.SessionStorage,
	sessionID string,
	_ bool,
	workspaceRoot string,
) (runtimehost.SessionContext, string) {
	if sessionStore == nil {
		return runtimehost.SessionContext{}, workspaceRoot
	}
	runtimeSession, err := sessionStore.Load(context.Background(), sessionID)
	if err != nil || runtimeSession == nil {
		return runtimehost.SessionContext{}, workspaceRoot
	}
	sessionCtx := runtimehost.ExtractSessionContext(runtimeSession)
	if strings.TrimSpace(sessionCtx.WorkspacePath) != "" {
		workspaceRoot = sessionCtx.WorkspacePath
	}
	return sessionCtx, workspaceRoot
}

// localSessionRouteContext keeps child-only route overrides out of base
// sessions. ExtractSessionContext is intentionally role-agnostic, so the
// caller must apply the base/child policy decision before passing route
// fields to the agent builder.
func localSessionRouteContext(sessionCtx runtimehost.SessionContext, isBaseSession bool) (provider, model, reasoningEffort string) {
	if isBaseSession {
		return "", "", ""
	}
	return sessionCtx.RequestedProvider, sessionCtx.RequestedModel, sessionCtx.RequestedReasoningEffort
}

func applyLocalSessionContextRoots(apiAgent *agent.Agent, sessionCtx runtimehost.SessionContext) {
	if apiAgent == nil {
		return
	}
	runtimehost.ApplySessionRootOptions(apiAgent.GetConfig(), sessionCtx.AllowedRoots, sessionCtx.ReadOnlyRoots)
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
// 说明：`buildSessionActor` 默认调用本函数（E3.4b 已切换）；仅当
// AICLI_EXECUTOR_REUSE 显式关闭时回退下方 legacy 装配路径。两条路径的
// 差异只允许是逃生门本身的租约/钩子实现，不得再新增业务语义分叉。
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
	isBaseSessionByID := baseSessionID != "" && strings.EqualFold(strings.TrimSpace(sessionID), baseSessionID)
	sessionCtx, effectiveWorkspace := deriveLocalSessionContext(sessionStore, sessionID, isBaseSessionByID, workspaceRoot)
	isBaseSession := runtimehost.IsBaseSession(sessionID, baseSessionID, sessionCtx)
	requestedProvider, requestedModel, requestedReasoning := localSessionRouteContext(sessionCtx, isBaseSession)

	// agent 装配（与 buildSessionActor 同序：agent → 工具策略 → loop）。
	apiAgent := buildLocalChatAgent(baseSession, h, runtimeConfig, effectiveWorkspace,
		sessionCtx.ChildAgentType, requestedModel, requestedProvider, requestedReasoning)
	applyLocalSessionContextRoots(apiAgent, sessionCtx)
	runtimehost.ApplySessionPermissionMode(apiAgent, sessionCtx.PermissionMode)
	if !isBaseSession {
		clearLocalChildAdmissionSnapshot(apiAgent)
	}
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
	applyLocalChildShellPolicy(apiAgent, sessionCtx.ChildShellDisabled)
	applyLocalChildDepthPolicy(apiAgent, sessionCtx.ChildDepth, localAgentDepthCeiling(runtimeConfig, h))

	loopConfig := buildLocalChatLoopConfig(runtimeConfig, baseSession, requestedReasoning)
	loopConfig.AgentSessionObligations = h.agentSessionObligationResolver()
	loopConfig.TeamObligations = h.teamObligationResolver()
	loopConfig.BackgroundJobObligations = h.backgroundJobObligationResolver()
	apiAgent.SetObligationResolvers(h.obligationResolvers())
	applyLocalChatMainAgentRouting(loopConfig, baseSession, isBaseSession)
	observationSubagent := !isBaseSession || strings.TrimSpace(sessionCtx.ChildAgentType) != "" || sessionCtx.ChildDepth > 0 || sessionCtx.ChildReadOnly
	applyLocalChatToolObservation(loopConfig, baseSession, h,
		knowledge.ObservationSourceFor(observationSubagent, sessionCtx.ChildReadOnly), localChatObservationTaskID(baseSession))
	applyLocalChatCompletionRequirement(loopConfig, baseSession, sessionCtx.ChildAgentType, sessionCtx.ChildCompletionRequirement, effectiveWorkspace)

	// All tool/permission/depth policies above are final before a detached
	// parked-child resume is allowed to start.
	h.startLocalParkedBatchResumer(apiAgent, sessionID, isBaseSession)

	acquire := func(ctx context.Context, sid string) (*runtimechat.SessionLeaseHandle, error) {
		store := h.LeaseStore
		if store == nil {
			if typed, ok := h.RuntimeStore.(runtimechat.SessionLeaseStore); ok {
				store = typed
			}
		}
		return acquireLocalChatSessionLease(ctx, store, sid)
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
		// OnRunPrepare runs **after** the default lease acquisition. Do not use
		// PrepareRun here: it would replace the default ensureLease path, so a
		// run that follows an OnRunStalled lease release could execute without
		// a session lease. The hook itself only refreshes prompt/tool policy.
		OnRunPrepare: localChatPrepareRunHook(apiAgent, baseSession, effectiveWorkspace, isBaseSession, h.ToolSurface),
	}
}

// aicliHostLLMRuntime 返回宿主的 LLM runtime；Bootstrap 未装配时为 nil（便于单测/降级）。
func aicliHostLLMRuntime(h *localChatRuntimeHost) *llm.LLMRuntime {
	if h == nil || h.Bootstrap == nil {
		return nil
	}
	return h.Bootstrap.LLMRuntime()
}

// aicliExecutorReuseEnabled 报告是否启用统一执行核（灰度第 3 步：**默认开启**）。
//
// buildSessionActor 默认走 runtimehost.BuildSessionActor + aicliRuntimeDeps；
// AICLI_EXECUTOR_REUSE=0|false|off|no 显式回退旧装配路径（逃生门，无需改码）。
// 依据：opt-in 灰度已通过 ConPTY A/B 实测（余项①/复测）与新旧路径对比契约测试
// （余项②）；旧路径的最终删除（E3.4c）仍待观察期后执行。
func aicliExecutorReuseEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_EXECUTOR_REUSE"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}
