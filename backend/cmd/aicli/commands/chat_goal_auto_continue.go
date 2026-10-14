package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	// goalAutoContinuationErrorStrikeLimit 是连续续跑失败的容忍阈值：达到即
	// 停止自动续跑（进入下一轮循环前等待用户输入），防止"无上限 + 持续错误 =
	// 无限重试"（对齐 Codex main 的 3 连门槛；成功一轮即重置）。M2 的空响应/
	// 无进展 3 连判定沿用同一常量。
	goalAutoContinuationErrorStrikeLimit = 3
	// goalKickWatchInterval 是恢复态作答后就绪 watcher 的轮询间隔。
	goalKickWatchInterval = 3 * time.Second

	goalAutoContinuationPrompt  = "Continue working toward the active persistent goal. First audit whether the objective is already fully achieved; if it is, call update_goal with status=complete and a concise evidence-based summary. If required work remains, do the next necessary step now. Do not merely say that you will inspect files, run commands, or continue later: either call the necessary tool, call update_goal when complete, or state the concrete blocker."
	goalContinuationMetadataKey = "aicli_goal_continuation"
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
	limit := goalAutoContinuationLimit(session)
	attempts := 0
	errorStrikes := 0
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
		if limit > 0 && attempts >= limit {
			reportGoalAutoContinuationLimitReached(session, limit)
			return nil
		}
		attempts++
		writeSessionDebugInfo(session, fmt.Sprintf("[goal] auto continuation attempt=%d limit=%d status=%s", attempts, limit, decision.GoalStatus), false)
		session.goalContinuationAttempt.Store(int32(attempts))
		attemptCtx, cancel := goalAutoContinuationAttemptContext(ctx, session)
		_, continueErr := continuer.ContinueGoal(attemptCtx, session)
		cancel()
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
	}
}

// goalAutoContinuationLimit 返回生效的自动续跑安全阀：0（缺省）与负值都表示
// 不限制（对齐 Codex 的"active 即续"）；正整数为每条用户消息的轮数上限。
func goalAutoContinuationLimit(session *ChatSession) int {
	if session == nil || session.goalMaxContinuations < 0 {
		return 0
	}
	return session.goalMaxContinuations
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

// reportGoalAutoContinuationStoppedForErrors 在连续错误达到容忍阈值时给出
// 用户可见的停止原因（M2 对齐 Codex 后改为置 blocked + /goal resume 恢复）。
func reportGoalAutoContinuationStoppedForErrors(session *ChatSession, strikes int) {
	reportGoalAutoContinuationSupplement(session, fmt.Sprintf(
		"[goal] 连续 %d 轮续跑失败，已停止自动续跑；目标保持 active，发送任意消息可重试。",
		strikes))
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
