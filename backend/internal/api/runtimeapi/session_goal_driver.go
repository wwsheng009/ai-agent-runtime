// session_goal_driver.go — runtime-server 进程内的 goal 自动续跑驱动器（gap 3）。
//
// 2026-10-15：续跑决策循环此前只在 CLI 进程（chat_goal_auto_continue.go）。
// 纯 web / connector / schedule 会话的 turn 终态发生在 SessionActor 与 web
// 直连路径上，但没有进程在该事件后继续驱动 goal —— 控制台能改状态，resume
// 后却没有下一轮。本驱动器在 turn 终态后：
//
//  1. 门控：配置开关（goal.serverAutoContinuation，缺省 false = 行为零变化）、
//     基础会话（子代理不驱动父会话目标，与 gap-1 注入口径一致）、
//     CLI 冷却窗（观测到 CLI 来源 turn 上报后让位，避免双驱动）；
//  2. 决策：goal active + 无审批/工具挂起 → 提交续跑轮（internal/goal 共享
//     prompt，与 CLI 逐字节同文案）；
//  3. 记账：以终态 usage 载荷按轮记账，超预算置 budget_limited 并跑一次
//     收尾轮（对齐 CLI 的 budget wrap-up）；
//  4. 容忍：连续错误 ×3 / 连续空转 ×3 → 停止并置 blocked（共享层阈值）。
//
// 与 CLI 循环的分工：CLI 在场时（冷却窗内）服务端让位——CLI 决策循环本身
// 完整（记账/strike/安全阀），服务端只补"CLI 不在场"的空档。CLI 在场的直接
// 证据是命令通道 submit_prompt/continue 入口（CLI 远程模式每轮必经）；CLI
// 关闭后窗口过期，服务端接管。
//
// 终态观测有两条路径，汇聚到同一评估：
//   - actor 路径（connectors/schedule/CLI 远程/团队）：订阅总线 `session_end`
//     （载荷含 success/error/usage_total_tokens/steps/cancel_source）；
//   - web 直连路径（/api/agent/chat 的 SSE 终态不进总线）：由 handler 的
//     成功/失败终态点显式调用 observeWebTerminal。
//
// 空转近似登记：CLI 口径是"无文本产出且无工具执行"；服务端终态载荷没有
// 工具执行计数，以 steps<=1 && output=="" 近似（单步纯空响应；工具执行会
// 推高 steps）。该近似只影响 no-progress strike 的灵敏度，不影响正确性。
package runtimeapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimehost "github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// goalDriverRunSource 是驱动器提交续跑轮的 run_meta.source 标记（观测归因）。
const goalDriverRunSource = "driver:goal"

// goalDriverCooldown 是 CLI 让位窗口（internal/goal.ContinuationCLICooldownWindow）。
const goalDriverCooldown = goal.ContinuationCLICooldownWindow

// goalDriverState 是驱动器在 Handler 上的进程内状态：CLI 在场标记与每会话
// strike 计数（连续错误/连续空转，成功且非空转一轮即重置），以及升级尝试标记
// （P2-2' 入口 2：同一轮空转序列内 IterateEscalationPrompt 至多注入一次）。
type goalDriverState struct {
	mu                  sync.Mutex
	cliLastSeen         map[string]time.Time // sessionID → 最近一次 CLI 来源 turn 上报
	cliRecheck          map[string]*time.Timer
	errorStrikes        map[string]int
	noProgStrikes       map[string]int
	escalationAttempted map[string]struct{}
	// doomLoopAdvised 是 doom_loop 告警的上浮标记（工具循环反复回放同一语义批）：
	// 由总线订阅写入，升级注入时消费；有进展（resetStrikes）不自动清除——告警
	// 多数发生在「有输出但重复工具批」的成功轮，否则信号会在评估时被误清。
	doomLoopAdvised map[string]struct{}
	// continuations counts rounds this driver submitted for the current user
	// intent. It enforces goal.maxConsecutiveContinuations; an external
	// successful turn or a set/resume kick starts a fresh budget.
	continuations map[string]int
	// deciding serializes one per-session decision. Actor supersede semantics
	// would cancel a live run if two session_end/kick paths submitted at once.
	deciding map[string]struct{}
	// wrapUps deduplicates terminal wrap-up prompts by goal_id|status|updated_at.
	wrapUps map[string]struct{}
	// disarmed records the user's explicit stop/intent gate. It mirrors the CLI
	// goalContinuationDisarmed flag: a user interrupt freezes autonomous
	// continuation until /goal resume or an explicit continue message re-arms.
	disarmed map[string]string
}

func newGoalDriverState() *goalDriverState {
	return &goalDriverState{
		cliLastSeen:         map[string]time.Time{},
		cliRecheck:          map[string]*time.Timer{},
		errorStrikes:        map[string]int{},
		noProgStrikes:       map[string]int{},
		escalationAttempted: map[string]struct{}{},
		doomLoopAdvised:     map[string]struct{}{},
		continuations:       map[string]int{},
		deciding:            map[string]struct{}{},
		wrapUps:             map[string]struct{}{},
		disarmed:            map[string]string{},
	}
}

func (s *goalDriverState) markCLISeen(sessionID string, now time.Time) {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cliLastSeen[sessionID] = now
}

func (s *goalDriverState) cliActive(sessionID string, now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.cliLastSeen[sessionID]
	if !ok {
		return false
	}
	return now.Sub(last) < goalDriverCooldown
}

// cliRemaining reports how long the CLI presence window still has to run.
func (s *goalDriverState) cliRemaining(sessionID string, now time.Time) (time.Duration, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.cliLastSeen[sessionID]
	if !ok {
		return 0, false
	}
	remaining := goalDriverCooldown - now.Sub(last)
	if remaining <= 0 {
		return 0, false
	}
	return remaining, true
}

func (s *goalDriverState) disarm(sessionID, reason string) bool {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, already := s.disarmed[sessionID]; already {
		return false
	}
	if s.disarmed == nil {
		s.disarmed = map[string]string{}
	}
	s.disarmed[sessionID] = strings.TrimSpace(reason)
	return true
}

func (s *goalDriverState) rearm(sessionID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.disarmed[sessionID]; !ok {
		return false
	}
	delete(s.disarmed, sessionID)
	return true
}

func (s *goalDriverState) disarmedReason(sessionID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.disarmed[sessionID])
}

// scheduleCLIRecheck arms one delayed takeover check per session. A newer call
// replaces the previous timer; the callback re-checks CLI presence, so an
// extended CLI window never causes an early duplicate submission.
func (s *goalDriverState) scheduleCLIRecheck(sessionID string, delay time.Duration, fn func()) {
	if s == nil || strings.TrimSpace(sessionID) == "" || fn == nil {
		return
	}
	if delay < 0 {
		delay = 0
	}
	s.mu.Lock()
	if existing := s.cliRecheck[sessionID]; existing != nil {
		existing.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if s.cliRecheck[sessionID] == timer {
			delete(s.cliRecheck, sessionID)
		}
		s.mu.Unlock()
		fn()
	})
	s.cliRecheck[sessionID] = timer
	s.mu.Unlock()
}

func (s *goalDriverState) noteError(sessionID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorStrikes[sessionID]++
	return s.errorStrikes[sessionID]
}

func (s *goalDriverState) noteNoProgress(sessionID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noProgStrikes[sessionID]++
	return s.noProgStrikes[sessionID]
}

// noProgressStrikes 返回当前连续无进展计数（升级决策的只读输入）。
func (s *goalDriverState) noProgressStrikes(sessionID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noProgStrikes[sessionID]
}

// escalationTried 报告本轮空转序列内是否已注入过升级提示。
func (s *goalDriverState) escalationTried(sessionID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.escalationAttempted[sessionID]
	return ok
}

// markEscalationAttempted 记录升级提示已注入（同一序列不再重复），并消费
// doom_loop 上浮标记（一次性信号）。
func (s *goalDriverState) markEscalationAttempted(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.escalationAttempted[sessionID] = struct{}{}
	delete(s.doomLoopAdvised, sessionID)
}

// noteDoomLoopWarning 记录一次 doom_loop 告警（升级判定的 OR 条件）。
func (s *goalDriverState) noteDoomLoopWarning(sessionID string) {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doomLoopAdvised[sessionID] = struct{}{}
}

// doomLoopAdvisedFor 报告本会话是否持有未消费的 doom_loop 告警。
func (s *goalDriverState) doomLoopAdvisedFor(sessionID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.doomLoopAdvised[sessionID]
	return ok
}

func (s *goalDriverState) resetStrikes(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.errorStrikes, sessionID)
	delete(s.noProgStrikes, sessionID)
	// 有实质进展（或 CLI 接管）时重置升级标记：后续再次连续空转允许再升级一次。
	delete(s.escalationAttempted, sessionID)
}

func (s *goalDriverState) beginDecision(sessionID string) bool {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.deciding[sessionID]; busy {
		return false
	}
	s.deciding[sessionID] = struct{}{}
	return true
}

func (s *goalDriverState) endDecision(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deciding, sessionID)
}

func (s *goalDriverState) continuationCount(sessionID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.continuations[sessionID]
}

func (s *goalDriverState) noteContinuation(sessionID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.continuations[sessionID]++
	return s.continuations[sessionID]
}

func (s *goalDriverState) resetContinuations(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.continuations, sessionID)
}

func (s *goalDriverState) wrapUpSubmitted(key string) bool {
	if s == nil || strings.TrimSpace(key) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.wrapUps[key]
	return ok
}

func (s *goalDriverState) markWrapUpSubmitted(key string) {
	if s == nil || strings.TrimSpace(key) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrapUps[key] = struct{}{}
}

// goalDriverTurnOutcome 是一次 turn 终态的驱动器观测输入。
type goalDriverTurnOutcome struct {
	SessionID string
	// RunSource 区分本轮是否由本驱动器提交（driver:goal）。容忍计数只应
	// 统计续跑轮，不能把用户首轮失败/空响应记到 goal 上；CLI 循环同样只
	// 统计 ContinueGoal* 的失败与空转。
	RunSource string
	Success   bool
	// ErrMessage / CancelSource 区分真失败与用户中断（中断不计 strike）。
	ErrMessage   string
	CancelSource string
	Output       string
	// OutputPresent is the actor's trimmed-output signal. partial_summary is
	// only emitted on failed/canceled runs, so using Output alone would treat
	// every successful text answer as a no-progress turn.
	OutputPresent bool
	// Steps 用于空转近似（<=1 且 Output 为空视为无进展，见文件头登记）。
	Steps       int
	UsageTokens int
	// ResetContinuations marks an explicit user kick (set/resume/budget) that
	// starts a fresh maxConsecutiveContinuations budget. It is applied after
	// beginDecision so a concurrent terminal decision's budget is not changed
	// underneath it.
	ResetContinuations bool
}

// goalDriverNoteCLI 是命令通道入口的 CLI 在场上报（CLI 远程模式每轮必经）。
// 无论驱动器开关状态都执行（开关关闭时只是无害的簿记）。
//
// A CLI-driven hidden continuation is also an explicit re-arm signal: the CLI
// goal loop only submits it while its local goal gate is armed. A user
// "continue" message is the other shared re-arm signal. Both must clear a
// server-side disarm that was set by an earlier server-owned user_interrupt;
// otherwise a CLI re-arm stays invisible here and the server driver remains
// frozen after the CLI leaves (R6c).
func (h *Handler) goalDriverNoteCLI(sessionID, message string, drivingContinuation bool) {
	if h == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	state := h.getGoalDriverState()
	state.markCLISeen(sessionID, time.Now())
	state.resetStrikes(sessionID)
	state.resetContinuations(sessionID)
	if drivingContinuation {
		h.goalDriverRearm(sessionID, "cli_continuation")
		return
	}
	if goal.IsContinueIntentMessage(message) {
		h.goalDriverMaybeRearmForUserIntentByID(sessionID, message)
	}
}

// goalDriverRearm clears the user-intent gate and starts a fresh continuation
// budget. It is used by explicit /goal mutations and by a conservative
// continue-intent user message, mirroring the CLI rearm entries.
func (h *Handler) goalDriverRearm(sessionID, reason string) {
	if h == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	state := h.getGoalDriverState()
	if !state.rearm(sessionID) {
		return
	}
	state.resetStrikes(sessionID)
	state.resetContinuations(sessionID)
	logger.Infof("goal driver: rearmed session=%s reason=%s", sessionID, strings.TrimSpace(reason))
}

// goalDriverMaybeRearmForUserIntent applies the shared continue-intent matcher
// to a Web user turn. Only an active persisted goal can be re-armed.
func (h *Handler) goalDriverMaybeRearmForUserIntent(session *chat.Session, message string) {
	if h == nil || session == nil || !goal.IsContinueIntentMessage(message) {
		return
	}
	current, hasGoal, err := goal.NewMetadataStore().Get(session)
	if err != nil || !hasGoal || current == nil || current.Status != goal.StatusActive {
		return
	}
	h.goalDriverRearm(session.ID, "user_continue_intent")
}

// goalDriverMaybeRearmForUserIntentByID is the command-channel counterpart of
// goalDriverMaybeRearmForUserIntent: the CLI has already supplied the message
// but the handler has not loaded the session yet.
func (h *Handler) goalDriverMaybeRearmForUserIntentByID(sessionID, message string) {
	if h == nil || strings.TrimSpace(sessionID) == "" || !goal.IsContinueIntentMessage(message) {
		return
	}
	session, ok, err := h.goalDriverLoadSession(context.Background(), sessionID)
	if err != nil || !ok || session == nil {
		return
	}
	current, hasGoal, err := goal.NewMetadataStore().Get(session)
	if err != nil || !hasGoal || current == nil || current.Status != goal.StatusActive {
		return
	}
	h.goalDriverRearm(sessionID, "cli_user_continue_intent")
}

// subscribeGoalDriver 把 actor 终态事件绑定到驱动器（幂等，Once 内调用）。
// bus 由调用方注入：本函数在 attachRuntimeEventBridge 的 Once 内运行，若再经
// getRuntimeEventBus() 反拉总线会重入同一 sync.Once 自锁——首次取总线即永久
// 挂死（2026-10-15 回归：整个测试包只能等 10 分钟超时），故总线一律由外部传入。
func (h *Handler) subscribeGoalDriver(bus *runtimeevents.Bus) {
	if h == nil || bus == nil {
		return
	}
	bus.Subscribe(chat.EventSessionEnd, func(event runtimeevents.Event) {
		if strings.TrimSpace(event.SessionID) == "" {
			return
		}
		payload := event.Payload
		h.sessionGoalDriverEvaluate(goalDriverTurnOutcome{
			SessionID:     event.SessionID,
			RunSource:     goalDriverStringFromAny(payload["run_source"]),
			Success:       boolFromAny(payload["success"]),
			ErrMessage:    goalDriverStringFromAny(payload["error"]),
			CancelSource:  goalDriverStringFromAny(payload["cancel_source"]),
			Output:        goalDriverStringFromAny(payload["partial_summary"]),
			OutputPresent: goalDriverOutputPresent(payload),
			Steps:         intFromAny(payload["steps"]),
			UsageTokens:   intFromAny(payload["usage_total_tokens"]),
		})
	})
	// doom_loop 告警上浮（P2-2' 入口 2 的 OR 条件）：agent 循环在同一个语义
	// 工具批反复回放时发 tool_loop.doom_loop_warning（会话级）；驱动器记下
	// 该标记，下一次续跑升级注入（消费一次）。订阅与 session_end 同一条总线。
	bus.Subscribe(agent.EventDoomLoopWarning, func(event runtimeevents.Event) {
		h.getGoalDriverState().noteDoomLoopWarning(strings.TrimSpace(event.SessionID))
	})
}

// observeWebTerminal 是 web 直连路径终态的显式驱动器入口（SSE 终态不进总线）。
func (h *Handler) observeWebTerminal(outcome goalDriverTurnOutcome) {
	h.sessionGoalDriverEvaluate(outcome)
}

// goalDriverKick 是 set/resume/budget-raise 成功后的立即续跑入口（对齐 Codex
// main 的 set/resume 触发语义：目标就绪即开工，不等下一个 turn 终态——空闲
// web 会话没有任何终态事件，kick 是它唯一的唤醒路径）。
func (h *Handler) goalDriverKick(sessionID string) {
	if h == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if !h.goalDriverEnabled() {
		return
	}
	state := h.getGoalDriverState()
	if reason := state.disarmedReason(sessionID); reason != "" {
		logger.Debugf("goal driver: defer session=%s reason=disarmed:%s", sessionID, reason)
		return
	}
	if state.cliActive(sessionID, time.Now()) {
		// CLI 在场：续跑先由 CLI 负责；窗口结束后服务端再接管，而不是永久
		// 丢弃这次 kick。
		h.scheduleGoalDriverCLITakeover(sessionID)
		return
	}
	if !h.goalDriverBaseSession(sessionID) {
		return
	}
	// A kick is a request to evaluate, not an unconditional submit. The
	// delayed CLI-takeover callback can fire long after the mutation that armed
	// it; the goal may have been paused/completed/budget-limited or hit a
	// continuation/round limit in the meantime. Going through the same decision
	// path as run terminals prevents a stale kick from starting a ghost
	// continuation turn.
	go h.sessionGoalDriverDecide(goalDriverTurnOutcome{
		SessionID:          sessionID,
		Success:            true,
		ResetContinuations: true,
	})
}

// sessionGoalDriverEvaluate 是驱动器的单一评估路径：门控 + strike 记账 + 决策。
// 全程失败只记日志，绝不影响原 turn 的终态语义。
func (h *Handler) sessionGoalDriverEvaluate(outcome goalDriverTurnOutcome) {
	if h == nil || strings.TrimSpace(outcome.SessionID) == "" {
		return
	}
	state := h.getGoalDriverState()
	sessionID := outcome.SessionID

	if !h.goalDriverEnabled() {
		return
	}
	if reason := state.disarmedReason(sessionID); reason != "" {
		logger.Debugf("goal driver: defer session=%s reason=disarmed:%s", sessionID, reason)
		return
	}
	// CLI-owned runs handle their own disarm/rearm gate. Only a server-owned
	// user interrupt freezes the server driver; otherwise a CLI re-arm would be
	// invisible to this process and the server would stay frozen forever.
	if state.cliActive(sessionID, time.Now()) {
		h.scheduleGoalDriverCLITakeover(sessionID)
		return
	}
	if strings.EqualFold(strings.TrimSpace(outcome.CancelSource), "user_interrupt") {
		if state.disarm(sessionID, "user_interrupt") {
			logger.Infof("goal driver: disarmed session=%s reason=user_interrupt", sessionID)
		}
		return
	}
	// 子代理会话不驱动（与 gap-1 指引注入同口径）。
	if !h.goalDriverBaseSession(sessionID) {
		return
	}

	// strike 记账（与 CLI 同阈值）：只统计本驱动器提交的续跑轮。用户首轮
	// 失败、CLI 轮或其它来源的 run 不能被记成 goal 续跑失败；成功的外部轮
	// 仍重置计数（用户已把会话拉回可推进状态）。用户中断不是失败。
	switch {
	case !goalDriverContinuationRun(outcome):
		if outcome.Success && strings.TrimSpace(outcome.CancelSource) == "" {
			state.resetStrikes(sessionID)
			state.resetContinuations(sessionID)
		}
	case outcome.CancelSource != "":
		// user_interrupt was handled above and returned. Any other cancel
		// source (actor stop, runtime refresh, execution context) is a system
		// cancellation: it must not count as a goal failure, but it also must
		// not strand an armed goal. Fall through to the decision path, which
		// matches the CLI driver's non-user terminal handling (D1-a).
		logger.Debugf("goal driver: re-evaluate after system cancellation session=%s source=%s", sessionID, strings.TrimSpace(outcome.CancelSource))
	case !outcome.Success:
		if strikes := state.noteError(sessionID); strikes >= goal.ContinuationErrorStrikeLimit {
			go func(strikes int) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				h.goalDriverMarkBlocked(ctx, sessionID, "consecutive_errors", strikes)
			}(strikes)
			return
		}
	case !outcome.OutputPresent && outcome.Steps <= 1:
		if strikes := state.noteNoProgress(sessionID); strikes >= goal.ContinuationNoProgressStrikeLimit {
			go func(strikes int) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				h.goalDriverMarkBlocked(ctx, sessionID, "no_progress", strikes)
			}(strikes)
			return
		}
	default:
		state.resetStrikes(sessionID)
	}
	go h.sessionGoalDriverDecide(outcome)
}

// scheduleGoalDriverCLITakeover arms a delayed re-evaluation for the end of the
// CLI cooldown. The callback goes through the normal kick path, which re-reads
// goal state and never assumes the CLI left work unfinished.
func (h *Handler) scheduleGoalDriverCLITakeover(sessionID string) {
	if h == nil || !h.goalDriverEnabled() {
		return
	}
	state := h.getGoalDriverState()
	remaining, ok := state.cliRemaining(sessionID, time.Now())
	if !ok {
		return
	}
	state.scheduleCLIRecheck(sessionID, remaining+10*time.Millisecond, func() {
		if state.cliActive(sessionID, time.Now()) {
			h.scheduleGoalDriverCLITakeover(sessionID)
			return
		}
		h.goalDriverKick(sessionID)
	})
}

// sessionGoalDriverDecide 在独立 goroutine 中完成记账、goal 态判定与提交。
func (h *Handler) sessionGoalDriverDecide(outcome goalDriverTurnOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sessionID := outcome.SessionID
	state := h.getGoalDriverState()
	if !h.goalDriverEnabled() {
		return
	}
	if reason := state.disarmedReason(sessionID); reason != "" {
		logger.Debugf("goal driver: defer decision session=%s reason=disarmed:%s", sessionID, reason)
		return
	}
	if !state.beginDecision(sessionID) {
		return
	}
	defer state.endDecision(sessionID)
	if outcome.ResetContinuations {
		state.resetContinuations(sessionID)
	}

	// 1) 记账：本次 turn usage 入 goal；超预算置 budget_limited 并收尾。
	if outcome.UsageTokens > 0 {
		if h.goalDriverAccountUsage(ctx, sessionID, outcome.UsageTokens) {
			// A parked approval/question owns the session. Do not start a
			// budget wrap-up turn into it: mark the budget only after the
			// user's decision releases the run, matching the CLI decision
			// order (pending interaction precedes budget wrap-up).
			if h.goalDriverHasPendingApproval(ctx, sessionID) {
				logger.Debugf("goal driver: defer budget wrap-up session=%s reason=pending_interaction", sessionID)
				return
			}
			h.goalDriverBudgetWrapUp(ctx, sessionID)
			state.resetStrikes(sessionID)
			return
		}
	}

	// 2) goal 态判定（active 即续，对齐 Codex continue_if_idle）。
	session, ok, err := h.goalDriverLoadSession(ctx, sessionID)
	if err != nil {
		logger.Warnf("goal driver: load session %s: %s", sessionID, err)
		return
	}
	if !ok || session == nil {
		return
	}
	store := goal.NewMetadataStore()
	current, hasGoal, err := store.Get(session)
	if err != nil {
		logger.Warnf("goal driver: read goal %s: %s", sessionID, err)
		return
	}
	decision := goal.EvaluateGoalContinuation(current, hasGoal)
	if !decision.Continue {
		state.resetStrikes(sessionID)
		// G3 终局收尾（DSH wrapup 借鉴）：模型刚把 goal 置为终态时，提交一次
		// 收尾轮（向用户交代结果/验证/产物），让会话在自主结束前给用户一个
		// 交代。仅模型终态（completed_by=model 或系统 blocked）触发；用户命令
		// 终态有自己的确认输出，不需要收尾轮。
		if current != nil && current.CompletedBy == "model" {
			var wrapPrompt string
			switch current.Status {
			case goal.StatusComplete:
				wrapPrompt = goal.CompleteWrapUpPrompt
			case goal.StatusBlocked:
				wrapPrompt = goal.BlockedWrapUpPrompt
			}
			wrapKey := goalDriverTerminalWrapUpKey(current)
			if wrapPrompt != "" && !state.wrapUpSubmitted(wrapKey) {
				if err := h.goalDriverSubmitContinuationWithPrompt(ctx, sessionID, wrapPrompt); err != nil {
					logger.Warnf("goal driver: terminal wrap-up submit %s: %s", sessionID, err)
				} else {
					state.markWrapUpSubmitted(wrapKey)
					logger.Infof("goal driver: terminal wrap-up submitted session=%s status=%s", sessionID, current.Status)
				}
			}
		}
		return
	}

	// 3) 挂起审批/工具：等用户决断，不续跑，也不消耗 round/limit 配额。
	if h.goalDriverHasPendingApproval(ctx, sessionID) {
		return
	}
	if limit := h.goalDriverContinuationLimit(); limit > 0 {
		if state.continuationCount(sessionID) >= limit {
			logger.Infof("goal driver: continuation limit reached session=%s limit=%d", sessionID, limit)
			return
		}
	}

	// G2 轮上限（DSH 借鉴）：durable 轮数达到 MaxRounds 即置 blocked 终态
	//（round-limit），不再提交续跑轮——与 CLI 驱动器同语义（单点
	// internal/goal.AdmitGoalRound）。
	if goal.AdmitGoalRound(current) {
		if err := goal.ApplySystemRoundLimit(current, time.Now()); err != nil {
			logger.Warnf("goal driver: apply round-limit blocked %s: %s", sessionID, err)
			return
		}
		if _, err := store.PutPersistent(ctx, h.sessionManager.GetStorage(), sessionID, *current, goal.MutationSystem); err != nil {
			logger.Warnf("goal driver: mark round-limit blocked %s: %s", sessionID, err)
			return
		}
		logger.Infof("goal driver: goal blocked session=%s reason=round-limit rounds=%d/%d", sessionID, current.RoundsStarted, current.MaxRounds)
		return
	}
	if current.RoundsStarted > 0 || current.MaxRounds > 0 {
		if _, err := store.PutPersistent(ctx, h.sessionManager.GetStorage(), sessionID, *current, goal.MutationSystem); err != nil {
			logger.Warnf("goal driver: admit round %s: %s", sessionID, err)
			return
		}
	}

	// 4) 提交续跑轮（共享 prompt，与 CLI 逐字节同文案）；连续无进展达到共享
	// 升级阈值、或收到 doom_loop 告警，且部署启用了 iterate_goal 时，本轮改用
	// 升级提示词（P2-2' 入口 2，同一空转序列至多一次，doom 标记随之消费）。
	prompt := goal.ContinuationPrompt
	doomAdvised := state.doomLoopAdvisedFor(sessionID)
	if goal.DecideIterationEscalation(state.noProgressStrikes(sessionID), doomAdvised, state.escalationTried(sessionID), h.goalDriverIterationEscalationAvailable()) {
		state.markEscalationAttempted(sessionID)
		prompt = goal.IterateEscalationPrompt
		logger.Infof("goal driver: escalating to fresh-context iteration session=%s strikes=%d doom_loop=%v", sessionID, state.noProgressStrikes(sessionID), doomAdvised)
	}
	if err := h.goalDriverSubmitContinuationWithPrompt(ctx, sessionID, prompt); err != nil {
		logger.Warnf("goal driver: submit continuation %s: %s", sessionID, err)
		return
	}
	state.noteContinuation(sessionID)
}

// goalDriverIterationEscalationAvailable 报告服务端宿主部署是否启用了
// iterate_goal（agents.iterateGoalEnabled）。未启用时驱动器不做升级注入——
// 与 CLI 侧 goalIterationEscalationAvailable 同口径。未配置 runtimeConfig 的
// 宿主由 agentsConfig() 回落默认（false）。
func (h *Handler) goalDriverIterationEscalationAvailable() bool {
	if h == nil {
		return false
	}
	return (&sessionAgentController{handler: h}).agentsConfig().IterateGoalEnabled
}

// goalDriverSubmitContinuationWithPrompt 通过 hub 取（或建）actor 并异步提交
// 指定提示词的续跑轮。run_meta.source 标记 driver 来源（观测归因）；turn 的
// 指引注入、工具面与持久化由既有 actor 路径承担；终态事件会再次进入本驱动器
// 形成闭环。
func (h *Handler) goalDriverSubmitContinuationWithPrompt(ctx context.Context, sessionID, prompt string) error {
	if h == nil || h.sessionManager == nil {
		return fmt.Errorf("goal driver: handler/session manager not configured")
	}
	// The decision path can race a just-started external run. Retry within a
	// bounded window instead of dropping the continuation permanently; if the
	// run is still active after the window, that run's terminal event re-enters
	// the driver.
	deadline := time.Now().Add(5 * time.Second)
	for h.goalDriverActorBusy(sessionID) {
		if time.Now().After(deadline) {
			return fmt.Errorf("goal driver: session %s is busy", sessionID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	hub := h.getSessionHub()
	if hub == nil {
		return fmt.Errorf("goal driver: session hub not configured")
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		return err
	}
	// The decision context is short-lived and is canceled when this function
	// returns. ContinueAsync uses its context as the run context, so keep values
	// but detach cancellation; the actor's own run limits still bound execution.
	// The shared continuation metadata marks the synthetic prompt hidden and is
	// stripped before persistence, matching CLI goal continuations.
	return actor.ContinueAsync(context.WithoutCancel(ctx), &team.RunMeta{Source: goalDriverRunSource}, chat.ContinueOption{
		ContinuationPrompt: prompt,
		ContinuationMetadata: map[string]interface{}{
			goal.ContinuationMetadataKey: true,
		},
		StripMetadataKeys: []string{goal.ContinuationMetadataKey},
	})
}

// goalDriverBudgetWrapUp 预算耗尽：置 budget_limited（系统写）并跑一次收尾轮
// （对齐 CLI 的 budget wrap-up：不再开新工作，只收尾）。
func (h *Handler) goalDriverBudgetWrapUp(ctx context.Context, sessionID string) {
	if !h.goalDriverMarkBudgetLimited(ctx, sessionID) {
		return
	}
	hub := h.getSessionHub()
	if hub == nil {
		return
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		logger.Warnf("goal driver: wrap-up actor %s: %s", sessionID, err)
		return
	}
	if err := actor.ContinueAsync(context.WithoutCancel(ctx), &team.RunMeta{Source: goalDriverRunSource}, chat.ContinueOption{
		ContinuationPrompt: goal.BudgetWrapUpPrompt,
		ContinuationMetadata: map[string]interface{}{
			goal.ContinuationMetadataKey: true,
		},
		StripMetadataKeys: []string{goal.ContinuationMetadataKey},
	}); err != nil {
		logger.Warnf("goal driver: wrap-up submit %s: %s", sessionID, err)
	}
}

func (h *Handler) goalDriverContinuationLimit() int {
	if h == nil {
		return 0
	}
	cfg := h.resolveRuntimeConfig(UsageScope{})
	if cfg == nil || cfg.Goal.MaxConsecutiveContinuations <= 0 {
		return 0
	}
	return cfg.Goal.MaxConsecutiveContinuations
}

func (h *Handler) goalDriverActorBusy(sessionID string) bool {
	if h == nil {
		return false
	}
	hub := h.getSessionHub()
	if hub == nil {
		return false
	}
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		return false
	}
	state := actor.StateForInspection()
	if state == nil {
		return false
	}
	if state.HasPendingInteraction() {
		return true
	}
	switch state.Status {
	case chat.SessionRunning, chat.SessionWaitingApproval, chat.SessionWaitingInput, chat.SessionRewinding:
		return true
	default:
		return false
	}
}

func goalDriverTerminalWrapUpKey(current *goal.SessionGoal) string {
	if current == nil {
		return ""
	}
	return strings.TrimSpace(current.GoalID) + "|" + string(current.Status) + "|" +
		current.UpdatedAt.UTC().Format(time.RFC3339Nano)
}

// goalDriverMarkBlocked 把 active goal 置为 blocked（系统写；对齐 CLI 停止语义）。
func (h *Handler) goalDriverMarkBlocked(ctx context.Context, sessionID, reason string, strikes int) bool {
	session, ok, err := h.goalDriverLoadSession(ctx, sessionID)
	if err != nil || !ok || session == nil {
		return false
	}
	store := goal.NewMetadataStore()
	current, hasGoal, err := store.Get(session)
	if err != nil || !hasGoal || current == nil {
		return false
	}
	if err := goal.ApplySystemBlocked(current, reason, time.Now()); err != nil {
		return false
	}
	updated, err := store.PutPersistent(ctx, h.sessionManager.GetStorage(), sessionID, *current, goal.MutationSystem)
	if err != nil {
		logger.Warnf("goal driver: mark blocked %s: %s", sessionID, err)
		return false
	}
	logger.Infof("goal driver: goal blocked session=%s reason=%s strikes=%d", sessionID, reason, strikes)
	return updated != nil
}

// goalDriverAccountUsage 把 usage 增量记入当前 active/budget_limited goal
// （与 CLI accountActiveGoalTurnUsage 同口径），返回是否耗尽预算。
func (h *Handler) goalDriverAccountUsage(ctx context.Context, sessionID string, usageTokens int) bool {
	if usageTokens <= 0 {
		return false
	}
	session, ok, err := h.goalDriverLoadSession(ctx, sessionID)
	if err != nil || !ok || session == nil {
		return false
	}
	store := goal.NewMetadataStore()
	current, hasGoal, err := store.Get(session)
	if err != nil || !hasGoal || current == nil {
		return false
	}
	if !goal.AddTokens(current, usageTokens) {
		return false
	}
	current.UpdatedAt = time.Now()
	updatedGoal, err := store.PutPersistent(ctx, h.sessionManager.GetStorage(), sessionID, *current, goal.MutationSystem)
	if err != nil {
		logger.Warnf("goal driver: account usage %s: %s", sessionID, err)
		return false
	}
	if updatedGoal != nil {
		if refreshed, ok, getErr := store.Get(updatedGoal); getErr == nil && ok && refreshed != nil {
			return goal.BudgetExhausted(refreshed)
		}
	}
	return goal.BudgetExhausted(current)
}

// goalDriverMarkBudgetLimited 把 active goal 置为 budget_limited（系统写）。
func (h *Handler) goalDriverMarkBudgetLimited(ctx context.Context, sessionID string) bool {
	session, ok, err := h.goalDriverLoadSession(ctx, sessionID)
	if err != nil || !ok || session == nil {
		return false
	}
	store := goal.NewMetadataStore()
	current, hasGoal, err := store.Get(session)
	if err != nil || !hasGoal || current == nil {
		return false
	}
	if err := goal.ApplySystemBudgetLimited(current, time.Now()); err != nil {
		return false
	}
	updated, err := store.PutPersistent(ctx, h.sessionManager.GetStorage(), sessionID, *current, goal.MutationSystem)
	if err != nil {
		logger.Warnf("goal driver: mark budget_limited %s: %s", sessionID, err)
		return false
	}
	return updated != nil
}

// goalDriverLoadSession 带短超时读权威会话（存储可能与并发 CLI 进程共享，
// 与 SessionDeps 的查询同纪律；失败降级为不驱动）。
func (h *Handler) goalDriverLoadSession(ctx context.Context, sessionID string) (*chat.Session, bool, error) {
	if h == nil || h.sessionManager == nil {
		return nil, false, nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
	defer cancel()
	session, err := h.sessionManager.Get(queryCtx, sessionID)
	if err != nil {
		return nil, false, err
	}
	return session, session != nil, nil
}

// goalDriverEnabled 读配置开关（DefaultRuntimeConfig 缺省开启；显式 false
// 可在部署中关闭）。
func (h *Handler) goalDriverEnabled() bool {
	if h == nil {
		return false
	}
	cfg := h.resolveRuntimeConfig(UsageScope{})
	if cfg == nil {
		return false
	}
	return cfg.Goal.ServerAutoContinuation
}

// goalDriverBaseSession 判定是否基础会话（子代理不驱动；与 gap-1 注入同口径）。
func (h *Handler) goalDriverBaseSession(sessionID string) bool {
	if h == nil || h.sessionManager == nil {
		return false
	}
	queryCtx, cancel := context.WithTimeout(context.Background(), sessionStoreQueryTimeout)
	defer cancel()
	session, err := h.sessionManager.Get(queryCtx, sessionID)
	if err != nil || session == nil {
		return false
	}
	return isBaseSessionContext(runtimehost.ExtractSessionContext(session))
}

// goalDriverContinuationRun reports whether an outcome belongs to the loop
// this driver owns. Tolerance must never be polluted by an unrelated user turn.
func goalDriverContinuationRun(outcome goalDriverTurnOutcome) bool {
	return strings.TrimSpace(outcome.RunSource) == goalDriverRunSource
}

// goalDriverHasPendingApproval reports a parked approval/tool/question without
// requiring a live actor. After a server restart the actor may not exist yet,
// but the durable runtime state still owns the pending decision; submitting a
// continuation there would race the approval and turn it into a
// terminated-run decision. Prefer the live actor, then fall back to the same
// durable state read used by connector commands.
func (h *Handler) goalDriverHasPendingApproval(ctx context.Context, sessionID string) bool {
	if h == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	if hub := h.getSessionHub(); hub != nil {
		if actor, ok := hub.Get(sessionID); ok && actor != nil {
			if state := actor.StateForInspection(); state != nil {
				return goalDriverStateHasPendingWork(state)
			}
		}
	}
	if store := h.getSessionRuntimeStore(); store != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		queryCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
		defer cancel()
		if state, err := store.LoadState(queryCtx, sessionID); err == nil && state != nil {
			return goalDriverStateHasPendingWork(state)
		}
	}
	return false
}

func goalDriverStateHasPendingWork(state *chat.RuntimeState) bool {
	return state.HasPendingInteraction()
}

// getGoalDriverState 懒初始化驱动器状态（Handler 零值可用，与 activeTurns 同口径）。
func (h *Handler) getGoalDriverState() *goalDriverState {
	if h == nil {
		return nil
	}
	h.goalDriverOnce.Do(func() {
		h.goalDriver = newGoalDriverState()
	})
	return h.goalDriver
}

// usageTotalTokens 返回终态 usage 的记账口径：优先 TotalTokens，缺省时退化为
// prompt+completion（部分 provider 不回填 total），零值 usage 返回 0（不计账）。
func usageTotalTokens(usage *types.TokenUsage) int {
	if usage == nil {
		return 0
	}
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.PromptTokens + usage.CompletionTokens
}

// boolFromAny / goalDriverStringFromAny / intFromAny 是事件载荷的宽容取值
// helper（payload 来自 map[string]interface{}，类型宽容、缺省零值）。
func boolFromAny(v interface{}) bool {
	b, ok := v.(bool)
	return ok && b
}

func goalDriverStringFromAny(v interface{}) string {
	s, _ := v.(string)
	return s
}

func goalDriverOutputPresent(payload map[string]interface{}) bool {
	if value, ok := payload["output_present"]; ok {
		return boolFromAny(value)
	}
	return strings.TrimSpace(goalDriverStringFromAny(payload["partial_summary"])) != ""
}

func intFromAny(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}
