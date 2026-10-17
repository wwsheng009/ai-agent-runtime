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
	errorStrikes        map[string]int
	noProgStrikes       map[string]int
	escalationAttempted map[string]struct{}
	// doomLoopAdvised 是 doom_loop 告警的上浮标记（工具循环反复回放同一语义批）：
	// 由总线订阅写入，升级注入时消费；有进展（resetStrikes）不自动清除——告警
	// 多数发生在「有输出但重复工具批」的成功轮，否则信号会在评估时被误清。
	doomLoopAdvised map[string]struct{}
}

func newGoalDriverState() *goalDriverState {
	return &goalDriverState{
		cliLastSeen:         map[string]time.Time{},
		errorStrikes:        map[string]int{},
		noProgStrikes:       map[string]int{},
		escalationAttempted: map[string]struct{}{},
		doomLoopAdvised:     map[string]struct{}{},
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

// goalDriverTurnOutcome 是一次 turn 终态的驱动器观测输入。
type goalDriverTurnOutcome struct {
	SessionID string
	Success   bool
	// ErrMessage / CancelSource 区分真失败与用户中断（中断不计 strike）。
	ErrMessage   string
	CancelSource string
	Output       string
	// Steps 用于空转近似（<=1 且 Output 为空视为无进展，见文件头登记）。
	Steps       int
	UsageTokens int
}

// goalDriverNoteCLI 是命令通道入口的 CLI 在场上报（CLI 远程模式每轮必经）。
// 无论驱动器开关状态都执行（开关关闭时只是无害的簿记）。
func (h *Handler) goalDriverNoteCLI(sessionID string) {
	if h == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	state := h.getGoalDriverState()
	state.markCLISeen(sessionID, time.Now())
	state.resetStrikes(sessionID)
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
			SessionID:    event.SessionID,
			Success:      boolFromAny(payload["success"]),
			ErrMessage:   goalDriverStringFromAny(payload["error"]),
			CancelSource: goalDriverStringFromAny(payload["cancel_source"]),
			Output:       goalDriverStringFromAny(payload["partial_summary"]),
			Steps:        intFromAny(payload["steps"]),
			UsageTokens:  intFromAny(payload["usage_total_tokens"]),
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
	if state.cliActive(sessionID, time.Now()) {
		// CLI 在场：resume 后的续跑由 CLI 的 requestGoalContinuationKick 负责。
		return
	}
	if !h.goalDriverBaseSession(sessionID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if h.goalDriverHasPendingApproval(sessionID) {
			return
		}
		if err := h.goalDriverSubmitContinuation(ctx, sessionID); err != nil {
			logger.Warnf("goal driver: kick continuation %s: %s", sessionID, err)
		}
	}()
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
	// CLI 冷却窗内完全让位（必须在 strike 记账之前）：CLI 在场时 goal 推进、
	// 记账与 strike 全部由 CLI 循环负责，服务端计数不得被 CLI 轮污染。
	if state.cliActive(sessionID, time.Now()) {
		return
	}
	// 子代理会话不驱动（与 gap-1 指引注入同口径）。
	if !h.goalDriverBaseSession(sessionID) {
		return
	}

	// strike 记账（与 CLI 同阈值）：用户中断不是失败；成功且非空转重置。
	switch {
	case outcome.CancelSource != "":
		// 用户中断/停止：不记账，等用户输入驱动下一轮。
	case !outcome.Success:
		if strikes := state.noteError(sessionID); strikes >= goal.ContinuationErrorStrikeLimit {
			go func(strikes int) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				h.goalDriverMarkBlocked(ctx, sessionID, "consecutive_errors", strikes)
			}(strikes)
			return
		}
	case outcome.Output == "" && outcome.Steps <= 1:
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

// sessionGoalDriverDecide 在独立 goroutine 中完成记账、goal 态判定与提交。
func (h *Handler) sessionGoalDriverDecide(outcome goalDriverTurnOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sessionID := outcome.SessionID

	// 1) 记账：本次 turn usage 入 goal；超预算置 budget_limited 并收尾。
	if outcome.UsageTokens > 0 {
		if h.goalDriverAccountUsage(ctx, sessionID, outcome.UsageTokens) {
			h.goalDriverBudgetWrapUp(ctx, sessionID)
			h.getGoalDriverState().resetStrikes(sessionID)
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
		h.getGoalDriverState().resetStrikes(sessionID)
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
			if wrapPrompt != "" {
				if err := h.goalDriverSubmitContinuationWithPrompt(ctx, sessionID, wrapPrompt); err != nil {
					logger.Warnf("goal driver: terminal wrap-up submit %s: %s", sessionID, err)
				} else {
					logger.Infof("goal driver: terminal wrap-up submitted session=%s status=%s", sessionID, current.Status)
				}
			}
		}
		return
	}

	// G2 轮上限（DSH 借鉴）：durable 轮数达到 MaxRounds 即置 blocked 终态
	//（round-limit），不再提交续跑轮——与 CLI 驱动器同语义（单点
	// internal/goal.AdmitGoalRound）。
	if goal.AdmitGoalRound(current) {
		current.Status = goal.StatusBlocked
		current.BlockedCode = goal.BlockedCodeRoundLimit
		current.BlockedReason = fmt.Sprintf("Goal reached its configured limit of %d rounds.", current.MaxRounds)
		current.UpdatedAt = time.Now()
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

	// 3) 挂起审批/工具：等用户决断，不续跑。
	if h.goalDriverHasPendingApproval(sessionID) {
		return
	}

	// 4) 提交续跑轮（共享 prompt，与 CLI 逐字节同文案）；连续无进展达到共享
	// 升级阈值、或收到 doom_loop 告警，且部署启用了 iterate_goal 时，本轮改用
	// 升级提示词（P2-2' 入口 2，同一空转序列至多一次，doom 标记随之消费）。
	state := h.getGoalDriverState()
	prompt := goal.ContinuationPrompt
	doomAdvised := state.doomLoopAdvisedFor(sessionID)
	if goal.DecideIterationEscalation(state.noProgressStrikes(sessionID), doomAdvised, state.escalationTried(sessionID), h.goalDriverIterationEscalationAvailable()) {
		state.markEscalationAttempted(sessionID)
		prompt = goal.IterateEscalationPrompt
		logger.Infof("goal driver: escalating to fresh-context iteration session=%s strikes=%d doom_loop=%v", sessionID, state.noProgressStrikes(sessionID), doomAdvised)
	}
	if err := h.goalDriverSubmitContinuationWithPrompt(ctx, sessionID, prompt); err != nil {
		logger.Warnf("goal driver: submit continuation %s: %s", sessionID, err)
	}
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

// goalDriverSubmitContinuation 通过 hub 取（或建）actor 并异步提交默认续跑轮。
func (h *Handler) goalDriverSubmitContinuation(ctx context.Context, sessionID string) error {
	return h.goalDriverSubmitContinuationWithPrompt(ctx, sessionID, goal.ContinuationPrompt)
}

// goalDriverSubmitContinuationWithPrompt 通过 hub 取（或建）actor 并异步提交
// 指定提示词的续跑轮。run_meta.source 标记 driver 来源（观测归因）；turn 的
// 指引注入、工具面与持久化由既有 actor 路径承担；终态事件会再次进入本驱动器
// 形成闭环。
func (h *Handler) goalDriverSubmitContinuationWithPrompt(ctx context.Context, sessionID, prompt string) error {
	if h == nil || h.sessionManager == nil {
		return fmt.Errorf("goal driver: handler/session manager not configured")
	}
	hub := h.getSessionHub()
	if hub == nil {
		return fmt.Errorf("goal driver: session hub not configured")
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		return err
	}
	return actor.SubmitPromptAsync(ctx, prompt, &team.RunMeta{Source: goalDriverRunSource})
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
	if err := actor.SubmitPromptAsync(ctx, goal.BudgetWrapUpPrompt, &team.RunMeta{Source: goalDriverRunSource}); err != nil {
		logger.Warnf("goal driver: wrap-up submit %s: %s", sessionID, err)
	}
}

// goalDriverMarkBlocked 把 active goal 置为 blocked（系统写；对齐 CLI 停止语义）。
func (h *Handler) goalDriverMarkBlocked(ctx context.Context, sessionID, reason string, strikes int) bool {
	session, ok, err := h.goalDriverLoadSession(ctx, sessionID)
	if err != nil || !ok || session == nil {
		return false
	}
	store := goal.NewMetadataStore()
	current, hasGoal, err := store.Get(session)
	if err != nil || !hasGoal || current == nil || current.Status != goal.StatusActive {
		return false
	}
	if err := goal.ValidateStatusTransition(current.Status, goal.StatusBlocked); err != nil {
		return false
	}
	current.Status = goal.StatusBlocked
	current.BlockedReason = goal.SystemBlockedReason(reason)
	current.UpdatedAt = time.Now()
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
	if current.Status != goal.StatusActive && current.Status != goal.StatusBudgetLimited {
		return false
	}
	current.TokensUsed += usageTokens
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
	if err != nil || !hasGoal || current == nil || current.Status != goal.StatusActive {
		return false
	}
	if err := goal.ValidateStatusTransition(current.Status, goal.StatusBudgetLimited); err != nil {
		return false
	}
	current.Status = goal.StatusBudgetLimited
	current.UpdatedAt = time.Now()
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

// goalDriverEnabled 读配置开关（缺省 false = 行为零变化）。
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

// goalDriverHasPendingApproval 报告会话 actor 是否有挂起审批/工具/提问
// （有则不续跑，与 CLI 的 queued-input 让位同语义——等用户先决断）。
func (h *Handler) goalDriverHasPendingApproval(sessionID string) bool {
	hub := h.getSessionHub()
	if hub == nil {
		return false
	}
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		return false
	}
	state := actor.State()
	if state == nil {
		return false
	}
	return state.PendingApproval != nil || state.PendingTool != nil || state.PendingQuestion != nil
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
