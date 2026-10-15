package goal

import (
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
