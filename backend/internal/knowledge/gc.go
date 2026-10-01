package knowledge

import (
	"context"
	"time"
)

// 本文件是 Phase 5 交付 4（GC）：软删除行的物理清理。
//
// 分工：`store_sqlite_gc.go` 负责 SQL（显式删 symbols 以触发 FTS 同步、再删
// files 让外键级联清掉 refs / symbol_versions / symbol_aliases）；本文件负责
// 策略——保留期、触发条件（DB 软上限 + 冷却窗口）与状态面摘要。

// DefaultGCRetentionDays 是软删除行的默认保留期（04 §5 Phase 5 交付 4：30 天）。
//
// 保留期存在的意义：软删除行是"文件曾经存在过"的唯一记录，重命名/移动后的
// 引用修复、以及"这个符号去哪了"的追问都要靠它；过期才允许物理清理。
const DefaultGCRetentionDays = 30

// DefaultGCInterval 是自动 GC 的最小间隔（冷却窗口）。
//
// 超过 `max_db_size_mb` 时也不是每次判定都跑：清理要删行 + 走 FTS 触发器，
// 不该在热路径上反复付成本；且清理是维护动作，晚几分钟无害。
const DefaultGCInterval = 10 * time.Minute

// bytesPerMiB 是 MB→字节的换算口径（配置项以 MB 计）。
const bytesPerMiB int64 = 1 << 20

// GCReport 是一次 GC 的结果。
type GCReport struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Cutoff 是保留期分界：deleted_at <= Cutoff 的行被物理清理。
	Cutoff time.Time `json:"cutoff"`
	RanAt  time.Time `json:"ran_at"`
	// Files / Symbols / Refs 是本次清理的行数（refs 为级联删除的计数）。
	Files   int `json:"files"`
	Symbols int `json:"symbols"`
	Refs    int `json:"refs"`
	// BytesBefore / BytesAfter 是主库文件大小（不含 -wal/-shm）；SQLite 不会
	// 因为删行自动收缩文件，这里的差值只反映"顺带发生的页回收"。
	BytesBefore int64 `json:"bytes_before"`
	BytesAfter  int64 `json:"bytes_after"`
}

// GCStatus 是状态面的 GC 摘要（本进程视角）。
type GCStatus struct {
	// LastRunAt 是最近一次 GC **尝试**（含失败）的时间；零值 = 本进程还没跑过。
	LastRunAt time.Time `json:"last_run_at,omitempty"`
	// Runs 是成功完成的 GC 次数。
	Runs int `json:"runs,omitempty"`
	// Last 是最近一次成功的报告；失败尝试不覆盖它（状态面不展示半截数据）。
	Last *GCReport `json:"last,omitempty"`
	// LastError 是最近一次失败的摘要（成功后被清空）。
	LastError string `json:"last_error,omitempty"`
}

// RunGC 显式执行一次物理清理（owner 专属）。
//
// reader / off / 无 store：返回空报告且不报错——与状态面同口径，"没有可写的
// 库"不是错误，调用方（CLI/运维）看报告里的零值即可。
func (l *Layer) RunGC(ctx context.Context) (GCReport, error) {
	if l == nil || l.store == nil {
		return GCReport{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if l.role != RoleOwner {
		return GCReport{}, nil
	}
	wsID, err := l.planWorkspaceID(ctx)
	if err != nil {
		return GCReport{}, err
	}
	now := time.Now()
	report, err := l.store.GCDeleted(ctx, wsID, now.Add(-l.cfg.GCRetention()))
	if err != nil {
		l.recordGCAttempt(now, GCReport{}, err)
		return report, err
	}
	l.recordGC(report)
	return report, nil
}

// GCStats 返回本进程的 GC 摘要（状态面与测试用）。
func (l *Layer) GCStats() GCStatus {
	if l == nil {
		return GCStatus{}
	}
	l.gcMu.Lock()
	defer l.gcMu.Unlock()
	status := GCStatus{LastRunAt: l.lastGCAttemptAt, Runs: l.gcRuns, LastError: l.lastGCError}
	if l.lastGCReport.RanAt != (time.Time{}) {
		report := l.lastGCReport
		status.Last = &report
	}
	return status
}

// maybeAutoGC 在判定点顺带检查：DB 超过软上限且冷却期已过时清理一次。
//
// 失败只记录、不向调用方传播：GC 是维护动作，不是"这条记忆能不能复用"的
// 正确性前提——判定点绝不能因为清理失败而变形。
func (l *Layer) maybeAutoGC(ctx context.Context, workspaceID string) {
	if l == nil || l.store == nil || l.role != RoleOwner || workspaceID == "" {
		return
	}
	now := time.Now()
	l.gcMu.Lock()
	lastAttempt := l.lastGCAttemptAt
	l.gcMu.Unlock()
	if !shouldAutoGC(l.gcSizeBytes(), l.cfg.MaxDBSizeMB, lastAttempt, now, DefaultGCInterval) {
		return
	}
	report, err := l.store.GCDeleted(ctx, workspaceID, now.Add(-l.cfg.GCRetention()))
	if err != nil {
		// 失败也记录时间：否则每次判定都会重试同一个注定失败的清理。
		l.recordGCAttempt(now, GCReport{}, err)
		return
	}
	l.recordGC(report)
}

// shouldAutoGC 判断是否应触发自动 GC（纯函数，便于直接钉住触发口径）。
//
// 口径：maxDBSizeMB <= 0 视为未启用；从未跑过时只看大小；跑过之后还要过冷却期。
func shouldAutoGC(sizeBytes, maxDBSizeMB int64, lastAttempt, now time.Time, interval time.Duration) bool {
	if maxDBSizeMB <= 0 || sizeBytes <= 0 {
		return false
	}
	if sizeBytes <= maxDBSizeMB*bytesPerMiB {
		return false
	}
	if lastAttempt.IsZero() {
		return true
	}
	return now.Sub(lastAttempt) >= interval
}

// gcSizeBytes 返回用于触发判定的库大小。
//
// 默认走 store 的主库文件大小；gcSizeFn 是测试注入口（否则只能靠真的把库撑到
// max_db_size_mb 才能验证触发接线）。
func (l *Layer) gcSizeBytes() int64 {
	if l.gcSizeFn != nil {
		return l.gcSizeFn()
	}
	if s, ok := l.store.(*sqliteStore); ok {
		return s.fileSizeBytes()
	}
	return 0
}

func (l *Layer) recordGC(report GCReport) {
	l.gcMu.Lock()
	defer l.gcMu.Unlock()
	l.gcRuns++
	l.lastGCAttemptAt = report.RanAt
	l.lastGCReport = report
	l.lastGCError = ""
}

func (l *Layer) recordGCAttempt(at time.Time, report GCReport, err error) {
	l.gcMu.Lock()
	defer l.gcMu.Unlock()
	l.lastGCAttemptAt = at
	if err != nil {
		l.lastGCError = err.Error()
		return
	}
	l.gcRuns++
	l.lastGCReport = report
	l.lastGCError = ""
}
