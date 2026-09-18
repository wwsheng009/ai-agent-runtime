package chat

import (
	"sync/atomic"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// sqliteContentionCounters 记录 SQLite BUSY 争用（P0.5 / 审查 R1）。
//
// 背景：runtime store 与 runtime-server/aicli 的另一进程共享同一个
// session_runtime.sqlite。写事务若以 deferred 模式「先读后写」，另一进程
// 提交后提升写锁会失败（SQLITE_BUSY_SNAPSHOT，ext 517）。修复方式是
// BEGIN IMMEDIATE（sqliteutil.WriteTxOptions）+ BUSY 家族的新事务重试；
// 这里记录重试与耗尽次数，供状态端点与排障使用。
type sqliteContentionCounters struct {
	busyRetries        atomic.Int64
	busySnapshotErrors atomic.Int64
	busyExhausted      atomic.Int64
}

// SQLiteContentionStats 是争用计数快照。
type SQLiteContentionStats struct {
	// BusyRetries 是 RetryWriteTx 实际发起的重试次数。
	BusyRetries int64 `json:"busy_retries"`
	// BusySnapshotErrors 是分类为 SQLITE_BUSY_SNAPSHOT(517) 的失败次数
	//（修复后应恒为 0；非 0 表示仍存在 deferred 读→写路径或回归）。
	BusySnapshotErrors int64 `json:"busy_snapshot_errors"`
	// BusyExhausted 是重试耗尽后仍以 BUSY 家族错误返回给调用方的次数。
	BusyExhausted int64 `json:"busy_exhausted"`
}

// ContentionStats 返回当前争用计数快照。
func (s *SQLiteRuntimeStore) ContentionStats() SQLiteContentionStats {
	if s == nil {
		return SQLiteContentionStats{}
	}
	return SQLiteContentionStats{
		BusyRetries:        s.contention.busyRetries.Load(),
		BusySnapshotErrors: s.contention.busySnapshotErrors.Load(),
		BusyExhausted:      s.contention.busyExhausted.Load(),
	}
}

// trackWriteRetry 由 sqliteutil.RetryWriteTx 在每次重试前调用。
func (s *SQLiteRuntimeStore) trackWriteRetry(err error, _ int) {
	if s == nil || err == nil {
		return
	}
	if sqliteutil.IsBusySnapshotError(err) {
		s.contention.busySnapshotErrors.Add(1)
	}
	s.contention.busyRetries.Add(1)
}

// trackWriteError 在重试耗尽（或首轮即不可重试失败）后调用。
func (s *SQLiteRuntimeStore) trackWriteError(err error) {
	if s == nil || err == nil {
		return
	}
	if sqliteutil.IsBusyError(err) {
		s.contention.busyExhausted.Add(1)
	}
}
