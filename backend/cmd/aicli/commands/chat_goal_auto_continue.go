package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	// goalAutoContinuationErrorStrikeLimit / goalAutoContinuationNoProgressStrikeLimit
	// 自 2026-10-15（gap 3）起下沉为 internal/goal 共享常量——CLI 循环与
	// runtime-server 驱动器必须同阈值，任何一侧改动都要先改共享层。此处保留
	// 包内别名，避免触碰 100+ 调用点。
	goalAutoContinuationErrorStrikeLimit      = runtimegoal.ContinuationErrorStrikeLimit
	goalAutoContinuationNoProgressStrikeLimit = runtimegoal.ContinuationNoProgressStrikeLimit
	// goalKickWatchInterval 是恢复态作答后就绪 watcher 的轮询间隔。
	goalKickWatchInterval = 3 * time.Second
	// goalMaxContinuationsEnv 是 runtime.yaml goal.maxConsecutiveContinuations
	// 未显式配置（0/缺省）时的环境变量覆盖；正整数生效、非法或非正值忽略
	// （对齐 turnAutoRetryLimitEnv 的既有先例，供 E2E/运维把"不限制"收敛有界）。
	goalMaxContinuationsEnv = "AICLI_GOAL_MAX_CONTINUATIONS"

	// goalAutoContinuationPrompt / goalBudgetWrapUpPrompt 自 gap 3 起下沉为
	// internal/goal 共享常量（字节级同文案，见 shared 层文件头的约束说明：
	// chat_skill_mentions 按整串相等识别系统生成的续跑提示）。CLI 保留别名。
	goalAutoContinuationPrompt  = runtimegoal.ContinuationPrompt
	goalContinuationMetadataKey = "aicli_goal_continuation"
	goalBudgetWrapUpPrompt      = runtimegoal.BudgetWrapUpPrompt
)

type goalAutoContinuationDecision struct {
	Continue   bool
	Reason     string
	GoalStatus runtimegoal.Status
}

func maybeAutoContinueActiveGoal(ctx context.Context, session *ChatSession, executor aicliChatExecutor) error {
	if session == nil || executor == nil {
		return nil
	}
	// 状态栏 attempt 计数只在续跑循环存活期间有效：所有退出路径都归零。
	defer session.goalContinuationAttempt.Store(0)
	// A canceled turn context must not be answered with a fresh continuation
	// run: remote cancels may not have set the session's interrupted flag
	// (plan doc P3-9).
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		writeSessionDebugInfo(session, "[goal] auto continuation skipped reason=context_done", false)
		return nil
	}
	continuer, ok := goalAutoContinuationExecutor(executor)
	if !ok {
		writeSessionDebugInfo(session, "[goal] auto continuation skipped reason=executor_without_continue_goal", false)
		return nil
	}
	// 对齐 Codex 的 continue_if_idle：goal 为 active 且无让位条件时持续续跑，
	// 没有固定次数上限；goal.maxConsecutiveContinuations（0=不限制，默认）只
	// 作为成本安全阀。停止规则：状态变化/让位条件/安全阀到顶/连续错误达阈值。
	// 见 docs/plan/aicli-goal-continuation-codex-alignment-plan-20261014.md。
	// 入口先记账（上一次 turn 的 usage）：恢复已超预算的 goal 时立即收敛。
	accountGoalTurnUsageBestEffort(session)
	limit := goalAutoContinuationLimit(session)
	attempts := 0
	errorStrikes := 0
	noProgressStrikes := 0
	// 升级路径（P2-2' 入口 2）：连续无进展达到共享阈值、或收到 doom_loop
	// 告警（同一语义工具批反复回放）且部署启用了 iterate_goal 时，下一次续跑
	// 改用 IterateEscalationPrompt（恰好一次）。
	escalationAttempted := false
	iterationAvailable := goalIterationEscalationAvailable(session)
	// doom_loop 信号上浮：本会话循环期间收到 tool_loop.doom_loop_warning 即
	// 标记升级；升级注入时消费该标记（一次性）。事件处理器可能在其他 goroutine
	// 派发，因此用 atomic 传递。
	var doomWarned atomic.Bool
	if session.LocalRuntimeHost != nil && session.LocalRuntimeHost.EventBus != nil {
		runtimeSessionID := strings.TrimSpace(currentRuntimeSessionID(session))
		unsubscribe := session.LocalRuntimeHost.EventBus.SubscribeCancelable(agent.EventDoomLoopWarning, func(event runtimeevents.Event) {
			if runtimeSessionID != "" && strings.TrimSpace(event.SessionID) == runtimeSessionID {
				doomWarned.Store(true)
			}
		})
		defer unsubscribe()
	}
	for {
		if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation stopped reason=context_done attempts_completed=%d", attempts), false)
			return nil
		}
		decision, err := shouldAutoContinueActiveGoalDecision(session)
		if err != nil {
			return err
		}
		if !decision.Continue {
			if decision.Reason == "update_goal_unavailable" {
				reportGoalAutoContinuationUnavailableOnce(session)
			}
			state := "stopped"
			if attempts == 0 {
				state = "skipped"
			}
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation %s reason=%s attempts_completed=%d", state, decision.Reason, attempts), false)
			return nil
		}
		if goalBudgetExhausted(session) {
			if markActiveGoalBudgetLimited(session) {
				reportGoalBudgetLimited(session)
			} else {
				writeSessionDebugInfo(session, "[goal] token budget exhausted but mark budget_limited failed", false)
			}
			return nil
		}
		if limit > 0 && attempts >= limit {
			reportGoalAutoContinuationLimitReached(session, limit)
			return nil
		}
		attempts++
		writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation attempt=%d limit=%d status=%s", attempts, limit, decision.GoalStatus), false)
		session.goalContinuationAttempt.Store(int32(attempts))
		attemptCtx, cancel := goalAutoContinuationAttemptContext(ctx, session)
		toolsBefore := chatTurnToolExecutions(session)
		output, continueErr := goalContinuationAttemptWithEscalation(
			attemptCtx, continuer, session, noProgressStrikes, &doomWarned, &escalationAttempted, iterationAvailable)
		cancel()
		toolsDelta := chatTurnToolExecutions(session) - toolsBefore
		if continueErr != nil {
			// 真实取消（用户 ESC / 远程 cancel）：立即停止，不计入错误容忍。
			if errors.Is(continueErr, context.Canceled) {
				writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation stopped reason=context_done attempts_completed=%d error=%q", attempts, continueErr.Error()), false)
				return nil
			}
			errorStrikes++
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation retry_after_error attempt=%d strikes=%d/%d error=%q", attempts, errorStrikes, goalAutoContinuationErrorStrikeLimit, continueErr.Error()), false)
			if errorStrikes >= goalAutoContinuationErrorStrikeLimit {
				reportGoalAutoContinuationStoppedForErrors(session, errorStrikes)
				return nil
			}
			continue
		}
		errorStrikes = 0
		if exceeded, accErr := accountActiveGoalTurnUsage(session); accErr != nil {
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] goal usage accounting failed: %v", accErr), false)
		} else if exceeded {
			// 预算耗尽：置 budget_limited，并执行一次收尾轮（对齐 budget_limit
			// steering：不再开新工作，只收尾并给出下一步）。
			markActiveGoalBudgetLimited(session)
			wrapCtx, wrapCancel := goalAutoContinuationAttemptContext(ctx, session)
			_, wrapErr := continuer.ContinueGoalWithPrompt(wrapCtx, session, goalBudgetWrapUpPrompt)
			wrapCancel()
			if wrapErr != nil {
				writeSessionDebugInfo(session, fmt.Sprintf("[goal] budget wrap-up turn failed: %q", wrapErr.Error()), false)
			}
			reportGoalBudgetLimited(session)
			return nil
		}
		// 空响应/无进展判定（对齐 Codex 的 empty-response ×3）：本轮既无文本
		// 产出、也没有工具执行才算空转；连续达到阈值即停止并置 blocked。
		// 注：工具计数覆盖 CLI/shared 执行路径；计数不可得时按"有文本即非空转"
		// 保守处理（不误伤）。
		if strings.TrimSpace(output) == "" && toolsDelta <= 0 {
			noProgressStrikes++
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation no_progress strike=%d/%d attempt=%d", noProgressStrikes, goalAutoContinuationNoProgressStrikeLimit, attempts), false)
			if noProgressStrikes >= goalAutoContinuationNoProgressStrikeLimit {
				reportGoalAutoContinuationStoppedForNoProgress(session, noProgressStrikes)
				return nil
			}
			continue
		}
		noProgressStrikes = 0
		// 有实质进展即重置升级状态：后续若再次连续空转，允许再来一次升级。
		escalationAttempted = false
	}
}

// goalContinuationAttemptWithEscalation 选择本次续跑注入的提示词：连续无进展
// 达到共享升级阈值、或本会话已收到 doom_loop 告警（doomWarned），且部署启用了
// iterate_goal（iterationAvailable）且尚未尝试过升级时，改用
// IterateEscalationPrompt（恰好一次，doom 标记随之消费）。其余情况保持默认续跑提示。
func goalContinuationAttemptWithEscalation(ctx context.Context, continuer aicliGoalContinuationExecutor, session *ChatSession, noProgressStrikes int, doomWarned *atomic.Bool, escalationAttempted *bool, iterationAvailable bool) (string, error) {
	doomAdvised := doomWarned != nil && doomWarned.Load()
	if runtimegoal.DecideIterationEscalation(noProgressStrikes, doomAdvised, escalationAttempted != nil && *escalationAttempted, iterationAvailable) {
		if escalationAttempted != nil {
			*escalationAttempted = true
		}
		if doomWarned != nil {
			// 消费一次性信号：升级轮若仍无措，下一次同类告警才可再次升级。
			doomWarned.Store(false)
		}
		writeSessionDebugInfo(session, fmt.Sprintf(
			"[goal] auto continuation escalating to fresh-context iteration strikes=%d/%d doom_loop=%v",
			noProgressStrikes, goalAutoContinuationNoProgressStrikeLimit, doomAdvised), false)
		return continuer.ContinueGoalWithPrompt(ctx, session, runtimegoal.IterateEscalationPrompt)
	}
	return continuer.ContinueGoal(ctx, session)
}

// goalIterationEscalationAvailable 报告本会话部署是否启用了 iterate_goal 工具
// （agents.iterateGoalEnabled）。未启用时驱动器不做升级注入——避免向模型指示
// 一个不存在的工具；托管宿主未挂载（如远端 server 模式）时保守返回 false，
// 由服务端驱动器在各自宿主上独立判定。
func goalIterationEscalationAvailable(session *ChatSession) bool {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.RuntimeConfig == nil {
		return false
	}
	return runtimecfg.NormalizeAgentsConfig(session.LocalRuntimeHost.RuntimeConfig.Agents).IterateGoalEnabled
}

// goalAutoContinuationLimit 返回生效的自动续跑安全阀：0（缺省）与负值都表示
// 不限制（对齐 Codex 的"active 即续"）；正整数为每条用户消息的轮数上限。
func goalAutoContinuationLimit(session *ChatSession) int {
	if session != nil && session.goalMaxContinuations > 0 {
		return session.goalMaxContinuations
	}
	if raw := strings.TrimSpace(os.Getenv(goalMaxContinuationsEnv)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

// accountGoalTurnUsageBestEffort 记账并只记 debug 日志（入口等无返回值场景）。
func accountGoalTurnUsageBestEffort(session *ChatSession) {
	if _, err := accountActiveGoalTurnUsage(session); err != nil {
		writeSessionDebugInfo(session, fmt.Sprintf("[goal] goal usage accounting failed: %v", err), false)
	}
}

// resetGoalAccountingBaseline 按当前 goal 重置记账基线（新建 goal 后调用，
// 使该 goal 的第一个 turn 也计入 TokensUsed）。
func resetGoalAccountingBaseline(session *ChatSession) {
	if session == nil {
		return
	}
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil {
		return
	}
	session.goalAccountingGoalID = goal.GoalID
	session.goalAccountedTokens = session.TokenCount
}

// goalBudgetExhausted 报告当前 goal 是否已耗尽 token 预算（仅 active 判定）。
func goalBudgetExhausted(session *ChatSession) bool {
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil {
		return false
	}
	return goal.Status == runtimegoal.StatusActive && goal.TokenBudget > 0 && goal.TokensUsed >= goal.TokenBudget
}

// accountActiveGoalTurnUsage 把自上次记账以来的会话 token 增量记入当前 goal
// （对齐 Codex 的每轮记账），返回是否已耗尽预算。基线按 goal id 维护；仅在
// goal 处于 active / budget_limited（预算相关窗口）时累计。
func accountActiveGoalTurnUsage(session *ChatSession) (bool, error) {
	if session == nil || session.SessionManager == nil || session.RuntimeSession == nil {
		return false, nil
	}
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil {
		return false, err
	}
	relevant := goal.Status == runtimegoal.StatusActive || goal.Status == runtimegoal.StatusBudgetLimited
	if !relevant {
		return false, nil
	}
	if session.goalAccountingGoalID != goal.GoalID {
		// 换 goal：只重建基线，不把旧 usage 计入新 goal。
		session.goalAccountingGoalID = goal.GoalID
		session.goalAccountedTokens = session.TokenCount
	} else {
		delta := session.TokenCount - session.goalAccountedTokens
		if delta < 0 {
			// 会话 token 计数被重置（例如 /clear 类操作）：重置基线避免负增量。
			delta = 0
			session.goalAccountedTokens = session.TokenCount
		}
		if delta > 0 {
			session.goalAccountedTokens = session.TokenCount
			goal.TokensUsed += delta
			updated, err := runtimegoal.NewMetadataStore().PutPersistent(
				context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), *goal, runtimegoal.MutationSystem)
			if err != nil {
				// 持久化失败：回退内存增量，避免下次重复累计。
				goal.TokensUsed -= delta
				return false, err
			}
			if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
				return false, err
			}
			// restore 会用持久化副本覆盖 CLI 侧 token 计数（存储可能滞后于
			// 本轮增量）：按恢复后的值重建基线，避免下一轮因负增量丢记账。
			session.goalAccountedTokens = session.TokenCount
		}
	}
	return goal.TokenBudget > 0 && goal.TokensUsed >= goal.TokenBudget, nil
}

// markActiveGoalBudgetLimited 把 active 的持久化 goal 置为 budget_limited
// （系统写；对齐 Codex 的预算终态：提高预算后经 /goal resume 恢复）。
func markActiveGoalBudgetLimited(session *ChatSession) bool {
	if session == nil || session.RuntimeSession == nil || session.SessionManager == nil {
		return false
	}
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil || goal.Status != runtimegoal.StatusActive {
		return false
	}
	if err := runtimegoal.ValidateStatusTransition(goal.Status, runtimegoal.StatusBudgetLimited); err != nil {
		return false
	}
	goal.Status = runtimegoal.StatusBudgetLimited
	goal.UpdatedAt = time.Now()
	updated, err := runtimegoal.NewMetadataStore().PutPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), *goal, runtimegoal.MutationSystem)
	if err != nil {
		writeSessionDebugInfo(session, "[goal] mark budget_limited failed: "+err.Error(), false)
		return false
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		writeSessionDebugInfo(session, "[goal] budget_limited state restore failed: "+err.Error(), false)
		return false
	}
	writeSessionDebugInfo(session, "[goal] goal marked budget_limited", false)
	return true
}

// reportGoalBudgetLimited 输出预算耗尽的可见提示。
func reportGoalBudgetLimited(session *ChatSession) {
	used, budget := 0, 0
	if goal, ok, err := currentSessionGoal(session); err == nil && ok && goal != nil {
		used, budget = goal.TokensUsed, goal.TokenBudget
	}
	reportGoalAutoContinuationSupplement(session, fmt.Sprintf(
		"[goal] token 预算已用完（%d/%d），目标已置为 budget_limited；/goal budget <n> 提高预算后 /goal resume 可继续。", used, budget))
}

func shouldAutoContinueAfterGoalTurnError(session *ChatSession, err error) bool {
	if err == nil {
		return false
	}
	// Only real cancellation (user interrupt / external cancel) must stop goal
	// recovery; a deadline-exceeded initial error stays recoverable, which the
	// continuation path intentionally supports.
	if errors.Is(err, context.Canceled) {
		writeSessionDebugInfo(session, fmt.Sprintf("[goal] error recovery skipped reason=context_done turn_error=%q", err.Error()), false)
		return false
	}
	decision, decisionErr := shouldAutoContinueActiveGoalDecision(session)
	if decisionErr != nil || !decision.Continue {
		if decisionErr != nil {
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] error recovery decision failed error=%q turn_error=%q", decisionErr.Error(), err.Error()), false)
		} else {
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] error recovery skipped reason=%s turn_error=%q", decision.Reason, err.Error()), false)
		}
		return false
	}
	writeSessionDebugInfo(session, fmt.Sprintf("[goal] error recovery eligible reason=%s turn_error=%q", decision.Reason, err.Error()), false)
	return true
}

func goalAutoContinuationAttemptContext(ctx context.Context, session *ChatSession) (context.Context, context.CancelFunc) {
	base := context.Background()
	switch {
	case session != nil && session.cancelCtx != nil && session.cancelCtx.Err() == nil:
		base = session.cancelCtx
	case ctx != nil && ctx.Err() == nil:
		base = ctx
	case ctx != nil && errors.Is(ctx.Err(), context.Canceled):
		// The turn context was explicitly canceled (user interrupt / external
		// cancel): never fall back to a fresh background context, or the
		// interrupt would resurrect the work it just stopped.
		return context.WithCancel(ctx)
	case session != nil && session.cancelCtx != nil && errors.Is(session.cancelCtx.Err(), context.Canceled):
		return context.WithCancel(session.cancelCtx)
	}
	if session != nil && session.RequestTimeout > 0 {
		return runtimeexecution.WithTimeoutSource(base, session.RequestTimeout, runtimeexecution.TimeoutSourceChatTurnDeadline)
	}
	return base, func() {}
}

func goalAutoContinuationExecutor(executor aicliChatExecutor) (aicliGoalContinuationExecutor, bool) {
	continuer, ok := executor.(aicliGoalContinuationExecutor)
	return continuer, ok
}

func shouldAutoContinueActiveGoal(session *ChatSession) (bool, error) {
	decision, err := shouldAutoContinueActiveGoalDecision(session)
	if err != nil {
		return false, err
	}
	return decision.Continue, nil
}

func shouldAutoContinueActiveGoalDecision(session *ChatSession) (goalAutoContinuationDecision, error) {
	if session == nil {
		return goalAutoContinuationDecision{Reason: "session_nil"}, nil
	}
	if session.NoInteractive {
		return goalAutoContinuationDecision{Reason: "no_interactive"}, nil
	}
	if session.DisableTools {
		return goalAutoContinuationDecision{Reason: "tools_disabled"}, nil
	}
	if session.JSONOutput {
		return goalAutoContinuationDecision{Reason: "json_output"}, nil
	}
	if session.IsInterrupted() {
		return goalAutoContinuationDecision{Reason: "interrupted"}, nil
	}
	if session.goalContinuationDisarmed.Load() {
		// G4 rearm 闸：恢复会话后默认不自动续跑（对齐 DSH goal 的 armed/
		// disarmed 语义）；/goal resume 或带继续意图的用户消息重新武装。
		return goalAutoContinuationDecision{Reason: "goal_disarmed"}, nil
	}
	if session.RuntimeSession == nil {
		return goalAutoContinuationDecision{Reason: "runtime_session_missing"}, nil
	}
	if session.SessionManager == nil {
		return goalAutoContinuationDecision{Reason: "session_manager_missing"}, nil
	}
	if !canCurrentChatPathUpdateGoal(session) {
		return goalAutoContinuationDecision{Reason: "update_goal_unavailable"}, nil
	}
	if queuedCount, draining := queuedInteractiveInputState(session); queuedCount > 0 || draining {
		return goalAutoContinuationDecision{Reason: "queued_input"}, nil
	}
	goal, ok, err := currentSessionGoal(session)
	if err != nil {
		return goalAutoContinuationDecision{}, err
	}
	if !ok || goal == nil {
		return goalAutoContinuationDecision{Reason: "goal_missing"}, nil
	}
	if goal.Status != runtimegoal.StatusActive {
		return goalAutoContinuationDecision{Reason: "goal_" + string(goal.Status), GoalStatus: goal.Status}, nil
	}
	return goalAutoContinuationDecision{Continue: true, Reason: "goal_active", GoalStatus: goal.Status}, nil
}

func reportGoalAutoContinuationWarning(session *ChatSession, err error) {
	if err == nil {
		return
	}
	message := fmt.Sprintf("[goal] 自动审计未完成: %v", err)
	writeSessionDebugInfo(session, message, false)
	if session == nil || !shouldRenderInteractiveOutput(session) {
		return
	}
	if session.Interaction != nil {
		session.Interaction.RenderLocalSupplement(message)
		return
	}
	fmt.Println(message)
}

func reportGoalAutoContinuationLimitReached(session *ChatSession, limit int) {
	if limit <= 0 {
		return
	}
	reportGoalAutoContinuationSupplement(session, fmt.Sprintf(
		"[goal] 自动续跑已达配置上限(%d)，目标仍为 active；发送任意消息可继续，或调高 goal.maxConsecutiveContinuations 取消限制。",
		limit))
}

// reportGoalAutoContinuationStoppedForErrors 在连续错误达到容忍阈值时停止，
// 并（对齐 Codex）把目标置为 blocked；置 blocked 失败时退化为"保持 active"
// 的可见提示。
func reportGoalAutoContinuationStoppedForErrors(session *ChatSession, strikes int) {
	if markActiveGoalBlocked(session, "consecutive_errors") {
		reportGoalAutoContinuationSupplement(session, fmt.Sprintf(
			"[goal] 连续 %d 轮续跑失败，目标已置为 blocked；/goal resume 可解除并继续。", strikes))
		return
	}
	reportGoalAutoContinuationSupplement(session, fmt.Sprintf(
		"[goal] 连续 %d 轮续跑失败，已停止自动续跑；目标保持 active，发送任意消息可重试。",
		strikes))
}

// reportGoalAutoContinuationStoppedForNoProgress 在连续空响应/无进展达到阈值
// 时停止并置 blocked（对齐 Codex 空响应 ×3 语义）。
func reportGoalAutoContinuationStoppedForNoProgress(session *ChatSession, strikes int) {
	if markActiveGoalBlocked(session, "no_progress") {
		reportGoalAutoContinuationSupplement(session, fmt.Sprintf(
			"[goal] 连续 %d 轮无实质进展（无工具执行且空响应），目标已置为 blocked；/goal resume 可解除并继续。", strikes))
		return
	}
	reportGoalAutoContinuationSupplement(session, fmt.Sprintf(
		"[goal] 连续 %d 轮无实质进展，已停止自动续跑；目标保持 active，发送任意消息可重试。", strikes))
}

// markActiveGoalBlocked 把 active 的持久化 goal 置为 blocked（系统写；对齐
// Codex 的停止语义：连续失败/无进展不再空烧 token，等待用户 /goal resume）。
func markActiveGoalBlocked(session *ChatSession, reason string) bool {
	if session == nil || session.RuntimeSession == nil || session.SessionManager == nil {
		return false
	}
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil || goal.Status != runtimegoal.StatusActive {
		return false
	}
	if err := runtimegoal.ValidateStatusTransition(goal.Status, runtimegoal.StatusBlocked); err != nil {
		return false
	}
	goal.Status = runtimegoal.StatusBlocked
	goal.BlockedReason = goalBlockedReasonText(reason)
	goal.UpdatedAt = time.Now()
	updated, err := runtimegoal.NewMetadataStore().PutPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), *goal, runtimegoal.MutationSystem)
	if err != nil {
		writeSessionDebugInfo(session, fmt.Sprintf("[goal] mark blocked failed reason=%s error=%q", reason, err.Error()), false)
		return false
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		writeSessionDebugInfo(session, fmt.Sprintf("[goal] blocked state restore failed reason=%s error=%q", reason, err.Error()), false)
		return false
	}
	writeSessionDebugInfo(session, "[goal] goal marked blocked reason="+reason, false)
	return true
}

// goalBlockedReasonText 把内部停止原因翻译成用户可读的 blocked 说明。
func goalBlockedReasonText(reason string) string {
	switch reason {
	case "consecutive_errors":
		return "连续续跑失败（系统判定）"
	case "no_progress":
		return "连续无实质进展（系统判定）"
	default:
		return reason
	}
}

// reportGoalAutoContinuationUnavailableOnce 让"当前模式无法调用 update_goal，
// 自动续跑被禁用"这一静默停机原因对用户可见（每会话仅提示一次，避免刷屏）。
func reportGoalAutoContinuationUnavailableOnce(session *ChatSession) {
	if session == nil || !session.goalContinuationUnavailableWarned.CompareAndSwap(false, true) {
		return
	}
	reportGoalAutoContinuationSupplement(session,
		"[goal] 自动续跑已禁用：当前模式无法调用 update_goal（goal 无法被自动完成）。检查工具面/tool policy，或手动 /goal complete。")
}

// reportGoalAutoContinuationSupplement 输出 goal 续跑的用户可见补充信息
// （debug 日志 + 交互渲染；与 reportGoalAutoContinuationWarning 同口径）。
func reportGoalAutoContinuationSupplement(session *ChatSession, message string) {
	if session == nil || strings.TrimSpace(message) == "" {
		return
	}
	writeSessionDebugInfo(session, message, false)
	if !shouldRenderInteractiveOutput(session) {
		return
	}
	if session.Interaction != nil {
		session.Interaction.RenderLocalSupplement(message)
		return
	}
	fmt.Println(message)
}

// sessionHasActivePersistedGoal 报告会话当前是否有 active 的持久化 goal。
func sessionHasActivePersistedGoal(session *ChatSession) bool {
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil {
		return false
	}
	return goal.Status == runtimegoal.StatusActive
}

// goalDisarmedStatusHint 是恢复态下 active goal 的状态提示：自动续跑已被
// rearm 闸挡住，需要 /goal resume（或用户在消息中明示继续）才恢复（G4，
// docs/analysis/dsh-goal-domain-vs-aar-goal-20261017.md §2.3/G4）。
const goalDisarmedStatusHint = "提示: 已恢复的历史会话默认不自动续跑；/goal resume 可重新启动该目标。"

// disarmGoalContinuationAfterRestore 在会话恢复（--resume / /resume / 会话切换
// 加载）完成后调用：目标仍为 active 时置 rearm 闸——用户随后的任意消息不再
// 触发自动续跑，直到 /goal resume 或用户在消息中明示继续。无 goal 或 goal
// 非 active 的会话不受影响（普通历史会话行为零变化）。进程内标记，不落库。
func disarmGoalContinuationAfterRestore(session *ChatSession) {
	if session == nil {
		return
	}
	if !sessionHasActivePersistedGoal(session) {
		return
	}
	session.goalContinuationDisarmed.Store(true)
}

// rearmGoalContinuation 清除 rearm 闸（/goal resume 与用户显式继续意图入口）。
func rearmGoalContinuation(session *ChatSession) {
	if session == nil {
		return
	}
	session.goalContinuationDisarmed.Store(false)
}

// requestGoalContinuationKick 请求一次隐藏续跑：/goal resume（以及其它把 goal
// 置回 active 的入口）成功后调用。执行在 maybeConsumeDeferredGoalKick（主循环
// 上下文），避免在命令处理中直接启动 turn 与渲染竞争；返回 false 表示当前
// 模式不支持续跑（非交互/工具禁用/JSON 输出/已中断/无 active goal）。
func requestGoalContinuationKick(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.DisableTools || session.JSONOutput {
		return false
	}
	if session.IsInterrupted() {
		return false
	}
	if !sessionHasActivePersistedGoal(session) {
		return false
	}
	session.goalKickPending.Store(true)
	return true
}

// requestGoalContinuationKickAfterResumedAnswer 在恢复态提问/审批作答后请求
// 续跑：作答会复活被挂起的 turn，续跑必须等它结束后再执行，因此只落 flag；
// 就绪 watcher 在 actor 空闲后唤醒主循环消费（见 startGoalKickReadinessWatcher）。
func requestGoalContinuationKickAfterResumedAnswer(session *ChatSession) {
	if !requestGoalContinuationKick(session) {
		return
	}
	startGoalKickReadinessWatcher(session)
}

// maybeConsumeDeferredGoalKick 在主循环上下文消费待执行的续跑 kick。仅当会话
// 完全就绪（无挂起弹层、无排队输入、actor 可接单、目标仍为 active）时真正执行；
// 否则保留 flag，等待下一次循环迭代或就绪 watcher 唤醒后再尝试。
func maybeConsumeDeferredGoalKick(session *ChatSession) {
	if session == nil || !session.goalKickPending.Load() {
		return
	}
	if session.NoInteractive || session.IsInterrupted() {
		session.goalKickPending.Store(false)
		return
	}
	if currentRestoredPendingPrompt(session) != nil {
		return
	}
	if queuedCount, draining := queuedInteractiveInputState(session); queuedCount > 0 || draining {
		return
	}
	if !chatActorAcceptingWork(session) {
		return
	}
	if !sessionHasActivePersistedGoal(session) {
		session.goalKickPending.Store(false)
		return
	}
	session.goalKickPending.Store(false)
	runGoalContinuationKick(session)
}

// runGoalContinuationKick 在主循环上下文执行一次续跑 pass（可能连续多轮，停止
// 规则与 turn 收尾的续跑一致）；期间启用与前台 turn 一致的忙时输入捕获，用户
// 输入仍可入队/作答，不因续跑执行而丢失。
func runGoalContinuationKick(session *ChatSession) {
	executor, err := ensureChatExecutor(session)
	if err != nil {
		reportGoalAutoContinuationWarning(session, err)
		return
	}
	ctx := session.cancelCtx
	if ctx == nil {
		ctx = context.Background()
	}
	stopBusyInputCapture := startBusyQueuedInputCapture(session)
	defer stopBusyInputCapture()
	if err := maybeAutoContinueActiveGoal(ctx, session, executor); err != nil {
		reportGoalAutoContinuationWarning(session, err)
	}
}

// chatActorAcceptingWork 非阻塞地报告会话 actor 是否可接受下一次操作。就绪
// 判定与 waitForAICLIActorReady 一致（无 actor / 无忙碌轮 ⇒ 可接单；挂起的
// turn 经 AcceptsResume 视为可接单）。非 actor 会话（shared/legacy）恒为 true。
func chatActorAcceptingWork(session *ChatSession) bool {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil || session.RuntimeSession == nil {
		return true
	}
	actor, ok := session.LocalRuntimeHost.SessionHub.Get(session.RuntimeSession.ID)
	if !ok || actor == nil {
		return true
	}
	state, stateOK := actor.StateSummary()
	if !stateOK || state.AcceptsResume() {
		return true
	}
	return false
}

// startGoalKickReadinessWatcher 在恢复态作答后看守 kick：轮询 actor 就绪，
// 就绪即唤醒阻塞的 composer 读取，让主循环消费 flag。仅交互行编辑器模式
// 启用（其余读取路径在 ctx 取消时可能走退出分支，不能无条件唤醒）。
func startGoalKickReadinessWatcher(session *ChatSession) {
	if session == nil || !shouldUseInteractiveLineEditor(session) {
		return
	}
	if !session.goalKickWatchActive.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer session.goalKickWatchActive.Store(false)
		ticker := time.NewTicker(goalKickWatchInterval)
		defer ticker.Stop()
		for {
			if !session.goalKickPending.Load() {
				return
			}
			if session.cancelCtx != nil {
				select {
				case <-session.cancelCtx.Done():
					return
				default:
				}
			}
			if session.IsInterrupted() {
				return
			}
			if chatActorAcceptingWork(session) {
				session.wakeComposerRead()
				return
			}
			<-ticker.C
		}
	}()
}

func goalContinuationInstructionMessage(prompt string) runtimetypes.Message {
	message := *runtimetypes.NewUserMessage(prompt)
	message.Metadata.Set(goalContinuationMetadataKey, true)
	return message
}

func stripGoalContinuationInstructionMessages(messages []runtimetypes.Message) []runtimetypes.Message {
	if len(messages) == 0 {
		return nil
	}
	stripped := make([]runtimetypes.Message, 0, len(messages))
	for _, message := range messages {
		if message.Metadata != nil {
			if value, ok := message.Metadata.Get(goalContinuationMetadataKey); ok {
				if hidden, _ := value.(bool); hidden {
					continue
				}
			}
		}
		stripped = append(stripped, message)
	}
	return stripped
}
