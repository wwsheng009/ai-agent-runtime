// chat_goal_driver.go — CLI 侧 goal round-driver（Phase 1 统一挂载点）。
//
// 2026-10-18 事故（session_20261018191622_xGGs4QZJ）根因：goal 续跑评估散落
// 在前台交互路径上，当"恢复→rearm→首轮被中断→supervision 内部轮接管"组合
// 出现时，没有任何组件再评估 goal（active 悬置，UI 仍显示 Pursuing）。本文件
// 把评估挂到所有 run 终态的统一事件（chat.EventSessionEnd）上：
//
//  1. 订阅：宿主总线按 base session 过滤；回退开关（AICLI_GOAL_DRIVER_DISABLED）
//     设置时整个文件零行为变化；
//  2. 评估：goal 已武装 + 无在途 kick → 请求一次性 kick（复用"请求→主循环
//     消费"两段式：直接起轮会与渲染/输入竞争，见 kick 机制的历史教训）；
//  3. 冻结：user_interrupt 终态 → disarm{user_interrupt} + 作废在途 kick。
//     中断是 turn 级动作，但必须同时显式冻结 goal 驱动——否则残留 kick 或
//     后续外部轮会把目标"自动拉起来"（停不住）；用户显式继续（/goal resume
//     或继续意图）rearm 时立即驱动（见 rearmGoalContinuation 的 Phase 1 修复）；
//  4. 去重：同一 run 终态只评估一次（同一 turn 的既有内联评估与订阅评估互斥）。
//
// 设计文档：docs/plan/aicli-goal-driver-unification-plan-20261018.md §4/§5（Phase 1）。
package commands

import (
	"os"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// goalDriverTerminalSeenLimit 是 run 终态去重表的容量（FIFO 修剪；远大于
// 单会话在途轮数，防止极端场景下无界增长）。
const goalDriverTerminalSeenLimit = 128

// goalDriverDisabledEnv 是 Phase 1 driver 的应急回退开关（隐藏，不进入公开配置
// 面）：设置为 1/true/yes/on 时退回旧行为（续跑仅由前台交互路径驱动）。
// 默认启用——“防静默悬置”是正确性修复，不以 opt-in 形态发布；退役条件见
// docs/plan/aicli-goal-driver-unification-plan-20261018.md §9.4（Phase 2 单源化
// 落地 + 观察期后移除本开关）。
const goalDriverDisabledEnv = "AICLI_GOAL_DRIVER_DISABLED"

// goalDriverEnabledFromEnv 报告 CLI goal driver 是否启用（默认 true）。
func goalDriverEnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(goalDriverDisabledEnv))) {
	case "1", "true", "yes", "on":
		return false
	default:
		return true
	}
}

// wireLocalGoalDriver 在宿主装配完成后订阅 run 终态事件；回退开关设置时 no-op。
// 调用点在 newLocalChatRuntimeHost 的装配序列（与 supervision wake consumer 并列）。
func (h *localChatRuntimeHost) wireLocalGoalDriver() {
	if h == nil || h.EventBus == nil || h.BaseSession == nil {
		return
	}
	session := h.BaseSession
	if !session.goalDriverEnabled {
		return
	}
	runtimeSessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if runtimeSessionID == "" {
		return
	}
	unsubscribe := h.EventBus.SubscribeCancelable(runtimechat.EventSessionEnd, func(event runtimeevents.Event) {
		if event.Type != runtimechat.EventSessionEnd {
			return
		}
		if !strings.EqualFold(strings.TrimSpace(event.SessionID), runtimeSessionID) {
			return
		}
		observeGoalDriverTerminal(session, event)
	})
	if unsubscribe != nil {
		h.cleanupFns = append(h.cleanupFns, unsubscribe)
	}
	writeSessionDebugInfo(session, "[goal] driver subscribed run terminal events (default-on; AICLI_GOAL_DRIVER_DISABLED to opt out)", false)
}

// observeGoalDriverTerminal 是 driver 的单一评估入口：任何来源的 run 终态
// （前台轮 / supervision wake 内部轮 / 远程提交 / 错误恢复轮）都经此重新评估。
// 此处只读 atomics 与会话基本字段（不读 goal 元数据）；实际的目标态判定在 kick
// 消费后的决策循环内完成——回调线程与主循环的并发接触面因此保持最小。
func observeGoalDriverTerminal(session *ChatSession, event runtimeevents.Event) {
	if session == nil || !session.goalDriverEnabled {
		return
	}
	if key := goalDriverTerminalKey(event); key != "" && !session.markGoalDriverTerminalSeen(key) {
		return
	}
	if cancelSource := goalDriverPayloadString(event.Payload["cancel_source"]); cancelSource == "user_interrupt" {
		freezeGoalContinuationForUserInterrupt(session)
		return
	}
	if session.goalContinuationDisarmed.Load() {
		writeSessionDebugInfo(session, "[goal] driver defer reason=goal_disarmed (run terminal)", false)
		return
	}
	if session.goalKickPending.Load() {
		// 已有一笔待消费的驱动请求（含续跑循环自身 arm 的 queued_input kick）：
		// 幂等返回，避免每个终态重复请求。
		return
	}
	if session.NoInteractive || session.JSONOutput {
		// 非交互路径的续跑由既有脚本/headless 语义负责，维持零变化。
		return
	}
	if requestGoalDriverKick(session) {
		writeSessionDebugInfo(session, "[goal] driver requested continuation after run terminal", false)
	}
}

// freezeGoalContinuationForUserInterrupt 冻结驱动并作废在途 kick（用户中断）。
// 冻结以显式状态呈现（disarmed{user_interrupt}，状态栏显示
// "Goal idle (/goal resume)"）；用户显式继续时 rearm 并立即驱动。
// 对无 goal / 非 active goal 的会话无副作用：状态栏仅在 active goal 上渲染
// disarmed 提示，决策循环先判 goal 状态。
func freezeGoalContinuationForUserInterrupt(session *ChatSession) {
	if session == nil {
		return
	}
	if session.goalKickPending.Swap(false) {
		session.goalKickHoldNoted.Store(false)
	}
	session.setGoalContinuationDisarmed("user_interrupt")
	writeSessionDebugInfo(session, "[goal] continuation frozen reason=user_interrupt; /goal resume or an explicit continue message re-arms", false)
}

// requestGoalDriverKick 发出一次"请求→延迟消费"的驱动请求：设置 kick flag、
// 启动就绪 watcher（行编辑器模式）、唤醒可能阻塞的输入读取（与 web 注入同一
// 先例），使主循环在下一轮顶部消费 kick 并执行续跑决策。返回 true 表示请求已
// 受理（false = 被既有拒绝路径挡下，如 goal_missing / 非交互）。
func requestGoalDriverKick(session *ChatSession) bool {
	if session == nil {
		return false
	}
	if !requestGoalContinuationKick(session) {
		return false
	}
	startGoalKickReadinessWatcher(session)
	session.wakeComposerRead()
	return true
}

// setGoalContinuationDisarmed 置位 rearm 闸并记录首次原因；返回 true 表示本次
// 发生了 armed→disarmed 转换（已是 disarmed 时保留更早的原因并返回 false）。
func (s *ChatSession) setGoalContinuationDisarmed(reason string) bool {
	if s == nil {
		return false
	}
	if s.goalContinuationDisarmed.Swap(true) {
		return false
	}
	s.goalDisarmMu.Lock()
	s.goalContinuationDisarmReason = strings.TrimSpace(reason)
	s.goalDisarmMu.Unlock()
	return true
}

// clearGoalContinuationDisarmReason 在 rearm 时清空原因记录。
func (s *ChatSession) clearGoalContinuationDisarmReason() {
	if s == nil {
		return
	}
	s.goalDisarmMu.Lock()
	s.goalContinuationDisarmReason = ""
	s.goalDisarmMu.Unlock()
}

// goalContinuationDisarmReasonText 返回 disarmed 的原因（restore /
// user_interrupt；空串=未知），供审计与 UI 文案使用。
func (s *ChatSession) goalContinuationDisarmReasonText() string {
	if s == nil {
		return ""
	}
	s.goalDisarmMu.Lock()
	defer s.goalDisarmMu.Unlock()
	return s.goalContinuationDisarmReason
}

// markGoalDriverTerminalSeen 记录 run 终态去重键；返回 true 表示首次出现
// （应评估），false 表示重复（跳过）。key 为空时不去重（返回 true）。
func (s *ChatSession) markGoalDriverTerminalSeen(key string) bool {
	if s == nil {
		return true
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}
	s.goalDriverTerminalMu.Lock()
	defer s.goalDriverTerminalMu.Unlock()
	if s.goalDriverSeenTurns == nil {
		s.goalDriverSeenTurns = make(map[string]struct{})
	}
	if _, exists := s.goalDriverSeenTurns[key]; exists {
		return false
	}
	s.goalDriverSeenTurns[key] = struct{}{}
	s.goalDriverSeenOrder = append(s.goalDriverSeenOrder, key)
	if len(s.goalDriverSeenOrder) > goalDriverTerminalSeenLimit {
		oldest := s.goalDriverSeenOrder[0]
		s.goalDriverSeenOrder = s.goalDriverSeenOrder[1:]
		delete(s.goalDriverSeenTurns, oldest)
	}
	return true
}

// goalDriverTerminalKey 提取 run 终态去重键：turn_id 最精确（同一轮的内联评估
// 与订阅评估共用同一键），退化到 run_id / trace_id。
func goalDriverTerminalKey(event runtimeevents.Event) string {
	for _, name := range []string{"turn_id", "run_id", "trace_id"} {
		if value := goalDriverPayloadString(event.Payload[name]); value != "" {
			return value
		}
	}
	return strings.TrimSpace(event.TraceID)
}

func goalDriverPayloadString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return ""
	}
}
