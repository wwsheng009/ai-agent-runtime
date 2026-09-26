package chat

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync/atomic"
)

// P1.6：AppendEvent 热路径优化（RETURNING 单语句 + prune 拆分）。
//
// 设计基线见 docs/plan/runtime-store-append-hotpath-optimization-plan-20260918.md
// §7 决策记录：
//   - D1 INSERT ... SELECT ... RETURNING seq + sqlite_version() 能力探测/回退；
//   - D3 DELETE 留在写事务内；
//   - D5 统计用「计数 + 均值 + Max」。
//
// 历史变更（2026-09-26，SQLite 损坏事故后的根治）：
//   - 删除 D4 的后台页回收任务（原 runtime_store_maintenance.go：单飞 + 节流 +
//     有界 deadline 的 `PRAGMA incremental_vacuum(N)` 循环）。
//   - 原因：在线 incremental_vacuum 会移动页并重写 ptrmap，是 bad ptr map
//     entry / freelist 错乱这一损坏形态的高风险来源；它没有任何功能价值，只
//     回收文件空间，改为离线 compaction（独占访问时手动执行 VACUUM）。
//   - 配套删除：store 的 auto_vacuum=INCREMENTAL 初始化 PRAGMA、Close 时的
//     wal_checkpoint(TRUNCATE)。参见 session_runtime_store.go 的 init/Close 注释。
const (
	runtimeReturningMinVersion = "3.35.0"
)

// runtimeAppendSQLHook 是测试专用探针：报告 AppendEvent 走了哪条 SQL 路径
// （"returning" / "legacy-select" / "legacy-insert"），生产路径恒为 nil。
var runtimeAppendSQLHook func(path string)

type runtimeAppendCounters struct {
	appends         atomic.Int64
	totalNs         atomic.Int64
	maxNs           atomic.Int64
	pruneRuns       atomic.Int64
	returningUsed   atomic.Int64
	legacyUsed      atomic.Int64
	lockHoldNs      atomic.Int64
	batches         atomic.Int64
	batchedEvents   atomic.Int64
	batchTotalNs    atomic.Int64
	batchLockHoldNs atomic.Int64
}

// AppendTimingStats 是 append 热路径的统计快照（P1.6/D5）。
type AppendTimingStats struct {
	Appends             int64  `json:"appends"`
	TotalNs             int64  `json:"total_ns"`
	MaxNs               int64  `json:"max_ns"`
	PruneRuns           int64  `json:"prune_runs"`
	ReturningUsed       int64  `json:"returning_used"`
	LegacyUsed          int64  `json:"legacy_used"`
	LockHoldNs          int64  `json:"lock_hold_ns"`
	Batches             int64  `json:"batches"`
	BatchedEvents       int64  `json:"batched_events"`
	BatchTotalNs        int64  `json:"batch_total_ns"`
	BatchLockHoldNs     int64  `json:"batch_lock_hold_ns"`
	BusyRetries         int64  `json:"busy_retries"`
	BusySnapshotRetries int64  `json:"busy_snapshot_retries"`
	SQLiteVersion       string `json:"sqlite_version,omitempty"`
	SupportsReturning   bool   `json:"supports_returning"`
}

// AppendTimingStats 返回当前统计快照。
func (s *SQLiteRuntimeStore) AppendTimingStats() AppendTimingStats {
	if s == nil {
		return AppendTimingStats{}
	}
	return AppendTimingStats{
		Appends:             s.appendCounters.appends.Load(),
		TotalNs:             s.appendCounters.totalNs.Load(),
		MaxNs:               s.appendCounters.maxNs.Load(),
		PruneRuns:           s.appendCounters.pruneRuns.Load(),
		ReturningUsed:       s.appendCounters.returningUsed.Load(),
		LegacyUsed:          s.appendCounters.legacyUsed.Load(),
		LockHoldNs:          s.appendCounters.lockHoldNs.Load(),
		Batches:             s.appendCounters.batches.Load(),
		BatchedEvents:       s.appendCounters.batchedEvents.Load(),
		BatchTotalNs:        s.appendCounters.batchTotalNs.Load(),
		BatchLockHoldNs:     s.appendCounters.batchLockHoldNs.Load(),
		BusyRetries:         s.contention.busyRetries.Load(),
		BusySnapshotRetries: s.contention.busySnapshotErrors.Load(),
		SQLiteVersion:       s.sqliteVersion,
		SupportsReturning:   s.supportsReturning,
	}
}

// SQLiteVersion 返回打开时探测到的 SQLite 版本（未打开时为空串）。
func (s *SQLiteRuntimeStore) SQLiteVersion() string {
	if s == nil {
		return ""
	}
	return s.sqliteVersion
}

// SupportsReturning 报告当前 store 是否走 INSERT ... RETURNING 路径。
func (s *SQLiteRuntimeStore) SupportsReturning() bool {
	if s == nil {
		return false
	}
	return s.supportsReturning && !s.cfg.DisableSQLiteReturning
}

func (s *SQLiteRuntimeStore) useReturningPath() bool {
	return s != nil && s.supportsReturning && !s.cfg.DisableSQLiteReturning
}

// probeSQLiteReturningSupport 探测 SQLite 版本并判定 RETURNING 可用性；
// 探测失败按「不支持」处理（自动回退 legacy 路径，不影响可用性）。
func probeSQLiteReturningSupport(ctx context.Context, db *sql.DB) (string, bool) {
	if db == nil {
		return "", false
	}
	var version string
	if err := db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err != nil {
		return "", false
	}
	return version, sqliteVersionAtLeast(version, runtimeReturningMinVersion)
}

// sqliteVersionAtLeast 比较 semver 前缀（忽略非数字后缀，如 3.51.3-dev）。
func sqliteVersionAtLeast(version, minimum string) bool {
	current, ok := parseSQLiteVersion(version)
	if !ok {
		return false
	}
	required, ok := parseSQLiteVersion(minimum)
	if !ok {
		return false
	}
	for index := range required {
		if current[index] != required[index] {
			return current[index] > required[index]
		}
	}
	return true
}

func parseSQLiteVersion(version string) ([3]int, bool) {
	var result [3]int
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) == 0 {
		return result, false
	}
	for index := 0; index < 3 && index < len(parts); index++ {
		digits := strings.Builder{}
		for _, char := range parts[index] {
			if char < '0' || char > '9' {
				break
			}
			digits.WriteRune(char)
		}
		if digits.Len() == 0 {
			return result, index > 0
		}
		value, err := strconv.Atoi(digits.String())
		if err != nil {
			return result, false
		}
		result[index] = value
	}
	return result, true
}
