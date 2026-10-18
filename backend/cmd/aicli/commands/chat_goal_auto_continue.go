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
	// Goal continuation can run either inside the foreground sendMessage or from
	// the deferred main-loop kick. In the kick path no StartWaiting precedes the
	// hidden actor turns, so establish/acquire the live clock here and settle it
	// on every exit path. If the foreground timer is already set, mark is a no-op
	// and the span covers the initial turn plus all continuations.
	markChatGoalStatusActiveTurnStarted(session)
	defer func() {
		goalElapsed := accountChatGoalElapsedInMemory(
			session,
			takeChatGoalStatusActiveTurnStartedAt(session),
			time.Now(),
		)
		persistChatGoalElapsed(session, goalElapsed)
		if goalElapsed != nil && session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
	}()
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
			// G3 终局收尾：本轮模型把 goal 置为终态后循环将退出；先执行一次
			// 收尾轮（向用户交代结果/验证/产物），再退出。预算收尾轮（已有
			// goalBudgetWrapUpPrompt 路径）不受影响。
			if wrapPrompt := consumeGoalTerminalWrapUp(session); wrapPrompt != "" {
				wrapCtx, wrapCancel := goalAutoContinuationAttemptContext(ctx, session)
				_, wrapErr := continuer.ContinueGoalWithPrompt(wrapCtx, session, wrapPrompt)
				wrapCancel()
				if wrapErr != nil {
					writeSessionDebugInfo(session, fmt.Sprintf("[goal] terminal wrap-up turn failed: %q", wrapErr.Error()), false)
				}
			}
			// 瞬时让位（queued_input：有排队输入，或排空中的过渡态）不是终局：
			// 转成一次性 kick 交给清道夫重估（主循环顶部 / 排空落地路径），否则
			// 主循环随后阻塞在读取上，没有任何事件再触发续跑——2026-10-18 真机：
			// 用户「继续」rearm 后收尾决策撞上 draining 瞬时态被跳过，续跑永久
			// 不启动。kick 的消费门（queued=0 且非 draining）保证不会抢在真实
			// 排队输入之前。
			if decision.Reason == "queued_input" && !session.goalKickPending.Load() {
				session.goalKickPending.Store(true)
				session.goalKickHoldNoted.Store(false)
				writeSessionDebugInfo(session, "[goal] continuation deferred reason=queued_input; kick re-armed", false)
			}
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
		// 本次续跑循环即将执行续跑轮本身，消费掉此前因瞬时让位而重排的 kick，
		// 避免同一段工作被 kick 再驱动一遍。
		session.goalKickPending.Store(false)
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
		// G2 轮上限（DSH 借鉴）：durable 轮数达到 MaxRounds 即置 blocked 终态
		//（round-limit），不再提交续跑轮。放在 attempt 提交前，保证"轮数"与
		// "实际提交的续跑轮"一一对应。
		if reached, roundErr := admitGoalRoundForSession(session); roundErr != nil {
			writeSessionDebugInfo(session, fmt.Sprintf("[goal] round accounting failed: %v", roundErr), false)
		} else if reached {
			markActiveGoalBlockedRoundLimit(session)
			reportGoalRoundLimitReached(session)
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

// chatGoalElapsedAccounting 是一次已写入内存、但尚未落库的 goal 时间增量。
// 状态行刷新需要先看到累计值，持久化则在释放 coordinator 锁之后执行，避免
// UI 临界区内做存储 I/O。
type chatGoalElapsedAccounting struct {
	Goal    runtimegoal.SessionGoal
	Seconds int64
}

// chatGoalElapsedSecondsForAccounting 计算当前 turn 应计入 goal.TimeUsedSeconds
// 的秒数。与状态栏的实时显示不同，这里以 activeTurnStartedAt 为基线，因为
// goal.UpdatedAt 会 token 记账/状态刷新推进，不能代表时间记账水位。
//
// 终态只计到其冻结时刻：complete 用 CompletedAt；paused/blocked/
// budget_limited 用 UpdatedAt。这样模型完成目标后的 wrap-up turn 不会被误计入
// goal 用时。goal 在 turn 中途创建时，从 CreatedAt 起算。
func chatGoalElapsedSecondsForAccounting(goal *runtimegoal.SessionGoal, activeTurnStartedAt, now time.Time) int64 {
	if goal == nil || activeTurnStartedAt.IsZero() {
		return 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	end := now
	switch goal.Status {
	case runtimegoal.StatusActive:
		// Whole turn remains accountable.
	case runtimegoal.StatusComplete:
		switch {
		case goal.CompletedAt != nil:
			end = *goal.CompletedAt
		case !goal.UpdatedAt.IsZero():
			end = goal.UpdatedAt
		}
	case runtimegoal.StatusPaused, runtimegoal.StatusBlocked, runtimegoal.StatusBudgetLimited:
		if !goal.UpdatedAt.IsZero() {
			end = goal.UpdatedAt
		}
	default:
		return 0
	}
	start := activeTurnStartedAt
	if goal.CreatedAt.After(start) {
		start = goal.CreatedAt
	}
	if !end.After(start) {
		return 0
	}
	return int64(end.Sub(start) / time.Second)
}

// accountChatGoalElapsedInMemory 把本 turn 的 goal 活跃时长累加进内存中的
// goal metadata，返回待持久化的快照。调用方随后必须调用
// persistChatGoalElapsed；不要把存储 I/O 放进 coordinator 的 UI 锁。
func accountChatGoalElapsedInMemory(session *ChatSession, activeTurnStartedAt, now time.Time) *chatGoalElapsedAccounting {
	if session == nil || session.RuntimeSession == nil || activeTurnStartedAt.IsZero() {
		return nil
	}
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil {
		return nil
	}
	delta := chatGoalElapsedSecondsForAccounting(goal, activeTurnStartedAt, now)
	if delta <= 0 {
		return nil
	}
	goal.TimeUsedSeconds += delta
	if err := runtimegoal.NewMetadataStore().Put(session.RuntimeSession, *goal); err != nil {
		writeSessionDebugInfo(session, "[goal] in-memory elapsed accounting failed: "+err.Error(), false)
		return nil
	}
	return &chatGoalElapsedAccounting{Goal: *goal, Seconds: delta}
}

// persistChatGoalElapsed 在释放 coordinator 锁后落库已累计的 goal 用时。
// 失败不回退内存值：保持当前界面正确，并留下诊断；后续正常 session sync 仍会
// 带上该内存快照。
func persistChatGoalElapsed(session *ChatSession, accounting *chatGoalElapsedAccounting) {
	if session == nil || accounting == nil || accounting.Seconds <= 0 ||
		session.SessionManager == nil || session.RuntimeSession == nil {
		return
	}
	_, err := runtimegoal.NewMetadataStore().PutPersistent(
		context.Background(),
		session.SessionManager.GetStorage(),
		currentRuntimeSessionID(session),
		accounting.Goal,
		runtimegoal.MutationSystem,
	)
	if err != nil {
		writeSessionDebugInfo(session, "[goal] elapsed accounting persist failed: "+err.Error(), false)
	}
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

// admitGoalRoundForSession 把本轮续跑记入 durable 轮数（G2，双宿主语义由
// internal/goal.AdmitGoalRound 单点承载）。返回是否已达轮上限。
func admitGoalRoundForSession(session *ChatSession) (bool, error) {
	if session == nil || session.SessionManager == nil || session.RuntimeSession == nil {
		return false, nil
	}
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil {
		return false, err
	}
	if !runtimegoal.AdmitGoalRound(goal) && goal.RoundsStarted == 0 {
		// 非 active goal：不落库（与记账窗口口径一致）。
		return false, nil
	}
	updated, err := runtimegoal.NewMetadataStore().PutPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), *goal, runtimegoal.MutationSystem)
	if err != nil {
		// 持久化失败回退内存增量，避免重复累计。
		goal.RoundsStarted--
		return false, err
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		return false, err
	}
	return runtimegoal.RoundLimitReached(goal), nil
}

// markActiveGoalBlockedRoundLimit 把 active goal 置为 blocked（系统写，
// BlockedCode=round-limit；/goal resume 可解除）。
func markActiveGoalBlockedRoundLimit(session *ChatSession) bool {
	if session == nil || session.SessionManager == nil || session.RuntimeSession == nil {
		return false
	}
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil || goal.Status != runtimegoal.StatusActive {
		return false
	}
	goal.Status = runtimegoal.StatusBlocked
	goal.BlockedCode = runtimegoal.BlockedCodeRoundLimit
	goal.BlockedReason = fmt.Sprintf("Goal reached its configured limit of %d rounds.", goal.MaxRounds)
	goal.UpdatedAt = time.Now()
	updated, err := runtimegoal.NewMetadataStore().PutPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), *goal, runtimegoal.MutationSystem)
	if err != nil {
		writeSessionDebugInfo(session, "[goal] mark round-limit blocked failed: "+err.Error(), false)
		return false
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		writeSessionDebugInfo(session, "[goal] round-limit blocked state restore failed: "+err.Error(), false)
		return false
	}
	writeSessionDebugInfo(session, "[goal] goal marked blocked (round-limit)", false)
	return true
}

// reportGoalRoundLimitReached 输出轮上限到达的可见提示。
func reportGoalRoundLimitReached(session *ChatSession) {
	rounds, maxRounds := 0, 0
	if goal, ok, err := currentSessionGoal(session); err == nil && ok && goal != nil {
		rounds, maxRounds = goal.RoundsStarted, goal.MaxRounds
	}
	reportGoalAutoContinuationSupplement(session, fmt.Sprintf(
		"[goal] 已达轮数上限（%d/%d），目标已置为 blocked（round-limit）；/goal resume 可解除并继续。", rounds, maxRounds))
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
	if !session.setGoalContinuationDisarmed("restore") {
		return
	}
	writeSessionDebugInfo(session, "[goal] continuation disarmed after restore; /goal resume or an explicit continue message re-arms", false)
}

// rearmGoalContinuation 清除 rearm 闸（/goal resume 与用户显式继续意图入口）。
// reason 只用于 debug 归因（如 "goal_resume_command" / "user_continue_intent"）；
// 闸门确实置位时才写一行日志，armed 状态下的重复调用保持静默。
func rearmGoalContinuation(session *ChatSession, reason string) {
	if session == nil {
		return
	}
	if !session.goalContinuationDisarmed.Swap(false) {
		return
	}
	session.clearGoalContinuationDisarmReason()
	if strings.TrimSpace(reason) == "" {
		reason = "explicit"
	}
	writeSessionDebugInfo(session, "[goal] continuation rearmed reason="+reason, false)
	// Phase 1（方案 §4.8）驱动接力修复：rearm 是驱动许可的状态变更，必须立即
	// 请求一次驱动检查——历史实现把接力赌在"后续某轮正常收尾自然接上"上，
	// 2026-10-18 事故中该假设在"rearm 后首轮被中断"组合下断裂，goal 悬置为
	// active 且无人驱动。回退开关（AICLI_GOAL_DRIVER_DISABLED）设置时维持旧行为（零变化）。
	if session.goalDriverEnabled {
		requestGoalDriverKick(session)
	}
}

// goalContinueIntentPhrases 是「用户在消息中明示继续」的保守匹配表（G4 的第二
// 个 rearm 入口；第一个是 /goal resume）。只认短促、无歧义的显式继续指令——
// 去除首尾空白与常见句末标点后整串相等才命中；「看一下当前状态」这类查询必须
// 不命中，否则 rearm 后 turn 结束立即恢复自主续跑，正是 G4 要消除的幽灵续跑。
// 2026-10-18 现场事故修复：此前注释/设计文档承诺该入口，但代码只有 /goal resume。
var goalContinueIntentPhrases = map[string]struct{}{
	"继续": {}, "继续吧": {}, "继续执行": {}, "继续干活": {}, "继续工作": {}, "请继续": {},
	"接着": {}, "接着来": {}, "接着做": {}, "接着干": {}, "往下做": {}, "往下干": {},
	"恢复": {}, "恢复吧": {}, "恢复执行": {}, "干吧": {},
	"continue": {}, "continue please": {}, "please continue": {}, "go on": {},
	"keep going": {}, "carry on": {}, "proceed": {}, "resume": {}, "keep working": {},
}

// isGoalContinueIntentMessage 判定一条用户消息是否属于显式继续意图。
// 归一化：去首尾空白 → 去句末标点/省略号 → 小写（ASCII）后整串比对。
func isGoalContinueIntentMessage(message string) bool {
	normalized := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(message), "。．.!！?？~～… "))
	if normalized == "" {
		return false
	}
	normalized = strings.ToLower(normalized)
	_, ok := goalContinueIntentPhrases[normalized]
	return ok
}

// maybeRearmGoalContinuationForUserIntent 在用户消息进入 turn 前判定显式继续
// 意图并 rearm（G4 入口 2，与 /goal resume 同语义）。只处理「目标 active +
// 闸门已置位」的恢复态会话；普通会话与已 armed 会话零行为变化。这里不请求
// kick——本条消息本身就是一次前台 turn，rearm 后 turn 收尾的既有续跑决策会
// 自然接上（kick 仅用于没有前台 turn 驱动的 /goal resume）。
func maybeRearmGoalContinuationForUserIntent(session *ChatSession, message string) {
	if session == nil || !session.goalContinuationDisarmed.Load() {
		return
	}
	if !isGoalContinueIntentMessage(message) {
		return
	}
	if !sessionHasActivePersistedGoal(session) {
		return
	}
	rearmGoalContinuation(session, "user_continue_intent")
}

// requestGoalContinuationKick 请求一次隐藏续跑：/goal resume（以及其它把 goal
// 置回 active 的入口）成功后调用。执行在 maybeConsumeDeferredGoalKick（主循环
// 上下文），避免在命令处理中直接启动 turn 与渲染竞争；返回 false 表示当前
// 模式不支持续跑（非交互/工具禁用/JSON 输出/已中断/无 active goal）。
func requestGoalContinuationKick(session *ChatSession) bool {
	if session == nil {
		return false
	}
	// 拒绝路径给出 debug 归因：kick 是"请求→延迟消费"两段式，静默拒绝会让
	// 上游（/goal resume 等）看起来"没有任何作用"（2026-10-18 现场事故）。
	refuse := func(reason string) bool {
		writeSessionDebugInfo(session, "[goal] continuation kick refused reason="+reason, false)
		return false
	}
	switch {
	case session.NoInteractive:
		return refuse("no_interactive")
	case session.DisableTools:
		return refuse("tools_disabled")
	case session.JSONOutput:
		return refuse("json_output")
	case session.IsInterrupted():
		return refuse("interrupted")
	case !sessionHasActivePersistedGoal(session):
		return refuse("goal_missing")
	}
	session.goalKickPending.Store(true)
	session.goalKickHoldNoted.Store(false)
	writeSessionDebugInfo(session, "[goal] continuation kick requested", false)
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
		reason := "interrupted"
		if session.NoInteractive {
			reason = "no_interactive"
		}
		writeSessionDebugInfo(session, "[goal] continuation kick dropped reason="+reason, false)
		session.goalKickPending.Store(false)
		session.goalKickHoldNoted.Store(false)
		return
	}
	if session.goalContinuationDisarmed.Load() {
		// 未武装（恢复态 / 用户中断冻结）：kick 是给已武装目标的一次性驱动，
		// 冻结期间直接作废（中断冻结已清 flag；此处兜底消费与冻结的竞态窗口）。
		// rearm 路径（rearmGoalContinuation）会重新请求。
		writeSessionDebugInfo(session, "[goal] continuation kick dropped reason=goal_disarmed", false)
		session.goalKickPending.Store(false)
		session.goalKickHoldNoted.Store(false)
		return
	}
	// 被挡时记一次归因日志（每笔 kick 只记一条，避免就绪 watcher 轮询刷屏）：
	// 此前 hold 完全静默，是"kick 请求了却没有后续"的不可观测面。
	hold := func(reason string) {
		if session.goalKickHoldNoted.CompareAndSwap(false, true) {
			writeSessionDebugInfo(session, "[goal] continuation kick held reason="+reason, false)
		}
	}
	if currentRestoredPendingPrompt(session) != nil {
		hold("restored_pending_prompt")
		return
	}
	if queuedCount, draining := queuedInteractiveInputState(session); queuedCount > 0 || draining {
		hold(fmt.Sprintf("queued_input queued=%d draining=%t", queuedCount, draining))
		return
	}
	if !chatActorAcceptingWork(session) {
		hold("actor_not_ready")
		// actor 未就绪同样是"清道夫缺失"的挂起条件：安排就绪 watcher，actor 可
		// 接单后唤醒主循环重跑顶部检查，避免 kick 停在 pending 无人重估。
		startGoalKickReadinessWatcher(session)
		return
	}
	if !sessionHasActivePersistedGoal(session) {
		writeSessionDebugInfo(session, "[goal] continuation kick dropped reason=goal_missing", false)
		session.goalKickPending.Store(false)
		session.goalKickHoldNoted.Store(false)
		return
	}
	session.goalKickPending.Store(false)
	session.goalKickHoldNoted.Store(false)
	writeSessionDebugInfo(session, "[goal] continuation kick consumed", false)
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
