package supervision

import "strings"

// turn.resumed 的 host-neutral 载荷与触发类型映射（方案 §6.8 / 审计缺口 G3）。
//
// 设计口径：`turn.resumed` 携带"resume 触发类型（terminal / progress / check-in）+
// digest 摘要"。supervision 包把 wake 原因收敛成触发类型并给出有界摘要，宿主把它
// 转成自己总线上的事件——supervision 不依赖任何事件总线实现，宿主可以选择只落
// 审计、只更新 UI，或两者都做。
const (
	// ResumeTriggerTerminal：终态类 wake（失败 / 超时 / 生命周期失败）——账本出现
	// 终局 obligation，这是 §16.4 join 判据的主要来源。
	ResumeTriggerTerminal = "terminal"
	// ResumeTriggerProgress：周期巡查（progress_check 家族；设计稿里与 check-in
	// 同属"非终态、例行确认"的恢复通道）。
	ResumeTriggerProgress = "progress"
	// ResumeTriggerApproval：审批 / 提问类（"blocked but healthy"，不是终态）。
	ResumeTriggerApproval = "approval"
	// ResumeTriggerOther：无原因或未知原因（保底：仍发事件，但不猜语义）。
	ResumeTriggerOther = "other"
)

// ResumeTriggerForReasons 把一批 wake 原因收敛成一个触发类型。优先级
// terminal > approval > progress > other：一批里同时有失败终态与周期巡查时，
// 恢复的真正内容是失败终态，UI 不该把这次恢复显示成"例行巡查"。
func ResumeTriggerForReasons(reasons []string) string {
	best := ""
	for _, reason := range reasons {
		trigger := resumeTriggerForReason(reason)
		if best == "" || resumeTriggerRank(trigger) > resumeTriggerRank(best) {
			best = trigger
		}
	}
	if best == "" {
		return ResumeTriggerOther
	}
	return best
}

func resumeTriggerForReason(reason string) string {
	switch WakeBudgetClassOf(reason) {
	case WakeBudgetClassFailure:
		return ResumeTriggerTerminal
	case WakeBudgetClassApproval:
		return ResumeTriggerApproval
	case WakeBudgetClassProgress:
		return ResumeTriggerProgress
	default:
		return ResumeTriggerOther
	}
}

func resumeTriggerRank(trigger string) int {
	switch trigger {
	case ResumeTriggerTerminal:
		return 3
	case ResumeTriggerApproval:
		return 2
	case ResumeTriggerProgress:
		return 1
	default:
		return 0
	}
}

// ResumeAnnouncement 是一次已投递 resume 的可观测快照。它由 WakeConsumer 在宿主
// 投递回调**成功返回**后构造并交给 Announce 钩子（投递失败 / 重新排队不发：UI
// 不该显示一次从未发生的恢复）。
type ResumeAnnouncement struct {
	// TurnID 是 resume 锚定的托管 turn（同一 turn_id，I3）。
	TurnID          string
	ParentSessionID string
	RootScopeID     string
	// Trigger 取 ResumeTrigger* 常量之一。
	Trigger string
	// WakeReasons / WakeIDs 是本次恢复消费掉的 wake 行（用于审计对账）。
	WakeReasons []string
	WakeIDs     []string
	// PendingCount / Status / Terminal 来自同一 turn 的 resume 上下文；未接线
	// 账本投影时保持 pending=-1 / status=unknown 的降级口径。
	PendingCount int
	Status       string
	Terminal     bool
	// Summary 是有界的 digest 摘要（生命周期摘要素描，受 resume 预算截断）。
	Summary string
}

// maxResumeAnnounceSummaryChars 限定播报摘要的字符数：事件是给 UI/审计看的
// 状态跃迁，不是第二个 digest 通道；完整内容仍在 ResumePrompt / 账本投影里。
const maxResumeAnnounceSummaryChars = 512

// EventPayload 把播报压成事件载荷（两宿主共用，避免 CLI/API 各写一份键名而漂移）。
// 键名即契约（前端与审计消费）；空字段不写入，避免"有键无值"的歧义。
func (a ResumeAnnouncement) EventPayload() map[string]interface{} {
	payload := map[string]interface{}{
		"session_id":    strings.TrimSpace(a.ParentSessionID),
		"root_scope_id": strings.TrimSpace(a.RootScopeID),
		"trigger":       strings.TrimSpace(a.Trigger),
		"pending_count": a.PendingCount,
		"status":        strings.TrimSpace(a.Status),
		"terminal":      a.Terminal,
	}
	if turnID := strings.TrimSpace(a.TurnID); turnID != "" {
		payload["turn_id"] = turnID
	}
	if len(a.WakeIDs) > 0 {
		payload["wake_ids"] = append([]string(nil), a.WakeIDs...)
	}
	if len(a.WakeReasons) > 0 {
		payload["wake_reasons"] = append([]string(nil), a.WakeReasons...)
	}
	if summary := strings.TrimSpace(a.Summary); summary != "" {
		payload["summary"] = summary
	}
	return payload
}

// boundedResumeSummary 按 rune 截断摘要，避免把一个超长 digest 塞进事件载荷。
func boundedResumeSummary(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= maxResumeAnnounceSummaryChars {
		return trimmed
	}
	return string(runes[:maxResumeAnnounceSummaryChars]) + "…"
}
