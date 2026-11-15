package goal

import (
	"strconv"
	"strings"
	"time"
)

// ApplyUserStatusUpdate 规范化一次**用户发起**的状态迁移：写入状态与更新时间，
// 完成时填充 CompletedAt/CompletedBy/CompletionSummary，离开完成态时清空这三项；
// 除 blocked 外清空 BlockedReason。
//
// 调用方负责先做转换校验（ValidateStatusTransition）与持久化
// （MetadataStore.PutPersistent，actor=MutationUser）。CLI `/goal pause|resume|
// complete` 与 runtime-server 的 goal 管理 HTTP 面共用本函数，避免两条路径的
// 字段清理规则漂移。
func ApplyUserStatusUpdate(g *SessionGoal, status Status, completedBy, completionSummary string, now time.Time) {
	if g == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	g.Status = status
	g.UpdatedAt = now
	if status == StatusComplete {
		g.CompletedAt = &now
		g.CompletedBy = strings.TrimSpace(completedBy)
		g.CompletionSummary = strings.TrimSpace(completionSummary)
	} else {
		g.CompletedAt = nil
		g.CompletedBy = ""
		g.CompletionSummary = ""
	}
	if status != StatusBlocked {
		g.BlockedReason = ""
	}
}

// ApplySystemBlocked marks an active goal blocked for a system-observed stop
// reason. Persistence and logging remain host-owned.
func ApplySystemBlocked(g *SessionGoal, reason string, now time.Time) error {
	if g == nil {
		return nil
	}
	if err := ValidateStatusTransition(g.Status, StatusBlocked); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	g.Status = StatusBlocked
	g.BlockedReason = SystemBlockedReason(reason)
	g.UpdatedAt = now
	return nil
}

// ApplySystemRoundLimit marks an active goal blocked at its configured round
// ceiling with the canonical round-limit blocked code.
func ApplySystemRoundLimit(g *SessionGoal, now time.Time) error {
	if g == nil {
		return nil
	}
	if err := ValidateStatusTransition(g.Status, StatusBlocked); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	g.Status = StatusBlocked
	g.BlockedCode = BlockedCodeRoundLimit
	g.BlockedReason = "Goal reached its configured round limit."
	g.UpdatedAt = now
	if g.MaxRounds > 0 {
		g.BlockedReason = "Goal reached its configured limit of " + strconv.Itoa(g.MaxRounds) + " rounds."
	}
	return nil
}

// ApplySystemBudgetLimited marks an active goal budget_limited. Raising the
// budget and resuming remains a user action.
func ApplySystemBudgetLimited(g *SessionGoal, now time.Time) error {
	if g == nil {
		return nil
	}
	if err := ValidateStatusTransition(g.Status, StatusBudgetLimited); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	g.Status = StatusBudgetLimited
	g.UpdatedAt = now
	return nil
}

// AddTokens accumulates provider usage on an active/budget_limited goal.
// Returns false when the goal is absent, tokens is non-positive, or the status
// is not usage-accountable; callers must then skip persistence.
func AddTokens(g *SessionGoal, tokens int) bool {
	if g == nil || tokens <= 0 {
		return false
	}
	if g.Status != StatusActive && g.Status != StatusBudgetLimited {
		return false
	}
	g.TokensUsed += tokens
	return true
}
