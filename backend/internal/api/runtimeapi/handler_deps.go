package runtimeapi

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// SessionDeps 组装执行核的显式依赖（E3.1b-1）。
//
// 它用带截止时间的查询加载会话并派生 SessionContext（与 buildSessionActor 原
// 内联块同源），存储/事件 getter 也是既有实现。**行为零变化**——本函数只做装配。
//
// 会话存储（session_history.sqlite）可能与并发 aicli CLI 进程共享、被写锁阻塞，
// 因此查询带截止时间；失败时降级为默认配置，而不是让请求无限挂起。
func (h *Handler) SessionDeps(sessionID string) runtimehost.Deps {
	if h == nil {
		return runtimehost.Deps{}
	}
	deps := runtimehost.Deps{
		RuntimeStore: h.getSessionRuntimeStore(),
		EventStore:   h.getSessionEventStore(),
		EventBus:     h.getRuntimeEventBus(),
		LLMRuntime:   h.llmRuntime,
	}
	// 会话执行租约：与宿主同源（owner kind = sessionActorLeaseOwnerKind，scope = sessionID）。
	deps.AcquireSessionLeaseNoWait = func(ctx context.Context, sid string) (*chat.SessionLeaseHandle, error) {
		return h.acquireSessionLeaseNoWait(ctx, sid, sessionActorLeaseOwnerKind, sid)
	}
	deps.AcquireSessionLease = func(ctx context.Context, sid string) (*chat.SessionLeaseHandle, error) {
		return h.acquireSessionLease(ctx, sid, sessionActorLeaseOwnerKind, sid)
	}
	// agent 装配与 loop 接线：把 profile 相关逻辑封装在宿主侧，profile 状态经
	// 不透明 handle 在两次回调间传递（不二次解析）。
	// preparedAgent 供 run 前钩子（OnRunPrepare）刷新 turn 级 goal 指引：
	// PrepareAgent 只在 actor 构建期调用一次，run 级刷新必须走 OnRunPrepare。
	var preparedAgent *agent.Agent
	deps.PrepareAgent = func(ctx context.Context, sid string, sessionCtx runtimehost.SessionContext) (*agent.Agent, *runtimecfg.RuntimeConfig, any) {
		profileState := h.resolveProfileStateForSessionContext(sessionCtx)
		apiAgent, selectedConfig := h.buildSessionAgentForContext(sid, sessionCtx, profileState, h.resolveRuntimeConfig(UsageScope{}))
		preparedAgent = apiAgent
		return apiAgent, selectedConfig, profileState
	}
	deps.BuildLoopConfig = func(sid string, sessionCtx runtimehost.SessionContext, selectedConfig *runtimecfg.RuntimeConfig, profileHandle any) *agent.LoopReActConfig {
		profileState, _ := profileHandle.(*profileRuntimeState)
		return h.buildSessionLoopConfigFor(sid, sessionCtx, selectedConfig, profileState)
	}
	// run 级进度 / supervision / 挂起放弃 / 持久化钩子（工厂尾部触点）。
	deps.OnProgress = h.progressRecorderForSession(sessionID)
	deps.OnBudgetProgress = h.budgetRecorderForSession(sessionID)
	deps.ApprovalTerminalGuard = h.supervisionConfig.ApprovalTerminalGuard
	deps.TriggerTurnDrain = h.supervisionConfig.TriggerTurnDrainEnabled()
	deps.TriggerTurnRunMeta = func(ctx context.Context, _ *chat.Session) *team.RunMeta {
		return h.apiSessionRunMeta(ctx, sessionID)
	}
	deps.SuspendedTurnAbandon = chat.SuspendedTurnAbandonHooks{
		CancelAgentSession: h.cancelSuspendedTurnAgentSession,
		CancelTeam:         h.cancelSuspendedTurnTeam,
	}
	deps.PersistHook = h.runtimeServerGoalPersistHook
	// run 级 goal 指引（gap 修复）：runtime-server 的每次 turn 之前按实时会话刷新
	// active_goal_guidance。租约仍由 runtimehost 默认 PrepareRun 负责，本钩子只做
	// agent 刷新（见 runtimehost.Deps.OnRunPrepare 的顺序契约）。
	deps.OnRunPrepare = func(_ context.Context, session *chat.Session, _ bool) error {
		h.applyRuntimeServerGoalGuidance(preparedAgent, session)
		return nil
	}
	if h.sessionManager == nil {
		return deps
	}
	deps.SessionStorage = h.sessionManager.GetStorage()
	getCtx, cancel := context.WithTimeout(context.Background(), sessionStoreQueryTimeout)
	defer cancel()
	if session, err := h.sessionManager.Get(getCtx, sessionID); err == nil && session != nil {
		deps.SessionContext = runtimehost.ExtractSessionContext(session)
	}
	return deps
}
