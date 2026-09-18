package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// P1.6：AppendEvent 热路径优化（RETURNING 单语句 + prune/vacuum 拆分）。
//
// 设计基线见 docs/plan/runtime-store-append-hotpath-optimization-plan-20260918.md
// §7 决策记录：
//   - D1 INSERT ... SELECT ... RETURNING seq + sqlite_version() 能力探测/回退；
//   - D3 DELETE 留在写事务内，只把 incremental_vacuum 移出；
//   - D4 后台维护 single-flight、最小间隔 5s、deadline min(busyTimeout*3,15s)；
//   - D5 统计用「计数 + 均值 + Max」；
//   - D7 维护任务不持 s.mu，空闲门槛 200ms + 16 页小步 + BUSY 退避（审查 R2）。
const (
	runtimeReturningMinVersion      = "3.35.0"
	runtimeMaintenanceMinInterval   = 5 * time.Second
	runtimeMaintenanceIdleGap       = 200 * time.Millisecond
	runtimeMaintenanceVacuumPages   = 16
	runtimeMaintenanceMaxSteps      = 64
	runtimeMaintenanceStepTimeout   = 2 * time.Second
	runtimeMaintenanceBackoffFirst  = 500 * time.Millisecond
	runtimeMaintenanceBackoffSecond = 2 * time.Second
	runtimeMaintenanceMaxDeadline   = 15 * time.Second
	runtimeMaintenanceStopTimeout   = time.Second
)

// runtimeAppendSQLHook / runtimeMaintenanceBeforeVacuumHook 是测试专用探针，
// 生产路径恒为 nil（用于 golden 语句序列与慢真空注入）。
var (
	runtimeAppendSQLHook               func(path string)
	runtimeMaintenanceBeforeVacuumHook func(ctx context.Context) error
)

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

// AppendTimingStats 是 append 热路径与维护任务的统计快照（P1.6/D5）。
type AppendTimingStats struct {
	Appends             int64  `json:"appends"`
	TotalNs             int64  `json:"total_ns"`
	MaxNs               int64  `json:"max_ns"`
	PruneRuns           int64  `json:"prune_runs"`
	VacuumRuns          int64  `json:"vacuum_runs"`
	VacuumTotalNs       int64  `json:"vacuum_total_ns"`
	ReturningUsed       int64  `json:"returning_used"`
	LegacyUsed          int64  `json:"legacy_used"`
	LockHoldNs          int64  `json:"lock_hold_ns"`
	Batches             int64  `json:"batches"`
	BatchedEvents       int64  `json:"batched_events"`
	BatchTotalNs        int64  `json:"batch_total_ns"`
	BatchLockHoldNs     int64  `json:"batch_lock_hold_ns"`
	BusyRetries         int64  `json:"busy_retries"`
	BusySnapshotRetries int64  `json:"busy_snapshot_retries"`
	MaintenanceRuns     int64  `json:"maintenance_runs"`
	MaintenanceSkipped  int64  `json:"maintenance_skipped"`
	MaintenanceBusy     int64  `json:"maintenance_skipped_busy"`
	MaintenanceFailures int64  `json:"maintenance_failures"`
	MaintenancePending  bool   `json:"maintenance_pending"`
	SQLiteVersion       string `json:"sqlite_version,omitempty"`
	SupportsReturning   bool   `json:"supports_returning"`
}

// AppendTimingStats 返回当前统计快照。
func (s *SQLiteRuntimeStore) AppendTimingStats() AppendTimingStats {
	if s == nil {
		return AppendTimingStats{}
	}
	stats := AppendTimingStats{
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
	if m := s.maintenance; m != nil {
		stats.VacuumRuns = m.vacuumRuns.Load()
		stats.VacuumTotalNs = m.vacuumTotalNs.Load()
		stats.MaintenanceRuns = m.runs.Load()
		stats.MaintenanceSkipped = m.skippedIdle.Load() + m.skippedThrottle.Load()
		stats.MaintenanceBusy = m.skippedBusy.Load()
		stats.MaintenanceFailures = m.failures.Load()
		stats.MaintenancePending = m.pendingVacuum.Load()
	}
	return stats
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

// runtimeStoreMaintenance 是后台页回收任务（单飞 + 节流 + 有界）。
type runtimeStoreMaintenance struct {
	pendingVacuum atomic.Bool
	running       atomic.Bool
	started       atomic.Bool
	stopOnce      sync.Once
	stopCh        chan struct{}
	wakeCh        chan struct{}
	doneCh        chan struct{}

	lastRunMu sync.Mutex
	lastRunAt time.Time
	// lastWriteAt 是最近一次 append/邮箱写的 unix nano，用于空闲门槛（D7）。
	lastWriteAt atomic.Int64

	runs            atomic.Int64
	skippedBusy     atomic.Int64
	skippedIdle     atomic.Int64
	skippedThrottle atomic.Int64
	failures        atomic.Int64
	vacuumRuns      atomic.Int64
	vacuumTotalNs   atomic.Int64

	// testDeadline 覆盖单轮 deadline（测试注入；0=默认）。
	testDeadline time.Duration
}

func newRuntimeStoreMaintenance() *runtimeStoreMaintenance {
	return &runtimeStoreMaintenance{
		stopCh: make(chan struct{}),
		wakeCh: make(chan struct{}, 1),
		doneCh: make(chan struct{}),
	}
}

func (m *runtimeStoreMaintenance) touchWrite() {
	if m == nil {
		return
	}
	m.lastWriteAt.Store(time.Now().UnixNano())
}

// signal 非阻塞唤醒维护 goroutine（合并多次触发）。
func (m *runtimeStoreMaintenance) signal() {
	if m == nil {
		return
	}
	select {
	case m.wakeCh <- struct{}{}:
	default:
	}
}

func (m *runtimeStoreMaintenance) markPending() {
	if m == nil {
		return
	}
	m.pendingVacuum.Store(true)
	m.signal()
}

func (m *runtimeStoreMaintenance) start(s *SQLiteRuntimeStore) {
	if m == nil || s == nil {
		return
	}
	if !m.started.CompareAndSwap(false, true) {
		return
	}
	go m.loop(s)
}

func (m *runtimeStoreMaintenance) stop(timeout time.Duration) {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() { close(m.stopCh) })
	if !m.started.Load() {
		return
	}
	if timeout <= 0 {
		timeout = runtimeMaintenanceStopTimeout
	}
	select {
	case <-m.doneCh:
	case <-time.After(timeout):
	}
}

func (m *runtimeStoreMaintenance) loop(s *SQLiteRuntimeStore) {
	defer close(m.doneCh)
	ticker := time.NewTicker(runtimeMaintenanceIdleGap)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-m.wakeCh:
		case <-ticker.C:
		}
		if !m.pendingVacuum.Load() {
			continue
		}
		m.runOnce(s, context.Background())
	}
}

// runOnce 执行一轮有界页回收：不持 s.mu（D7/R2），只在每步短事务内占用池连接，
// 步与步之间释放连接，让业务写入可以插队。
func (m *runtimeStoreMaintenance) runOnce(s *SQLiteRuntimeStore, parent context.Context) {
	if m == nil || s == nil || s.db == nil {
		return
	}
	// 捕获 db 句柄：Close 可能在本轮执行期间把 s.db 置空，后续步骤必须继续
	// 使用已关闭的句柄并让 Exec 返回错误（而不是对 nil 解引用）。
	db := s.db
	// 先做 single-flight：与执行中的维护重叠的触发一律计为 busy，
	// 即使 pending 已被执行者清零（R2/D7 的可观测性要求）。
	if !m.running.CompareAndSwap(false, true) {
		m.skippedBusy.Add(1)
		return
	}
	defer m.running.Store(false)
	if !m.pendingVacuum.Load() {
		return
	}

	m.lastRunMu.Lock()
	lastRun := m.lastRunAt
	m.lastRunMu.Unlock()
	if !lastRun.IsZero() && time.Since(lastRun) < runtimeMaintenanceMinInterval {
		m.skippedThrottle.Add(1)
		return
	}
	if stamp := m.lastWriteAt.Load(); stamp != 0 && time.Since(time.Unix(0, stamp)) < runtimeMaintenanceIdleGap {
		m.skippedIdle.Add(1)
		return
	}

	m.pendingVacuum.Store(false)
	m.lastRunMu.Lock()
	m.lastRunAt = time.Now()
	m.lastRunMu.Unlock()

	deadline := m.testDeadline
	if deadline <= 0 {
		deadline = s.busyTimeout * 3
		if deadline > runtimeMaintenanceMaxDeadline {
			deadline = runtimeMaintenanceMaxDeadline
		}
	}
	ctx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()
	m.runs.Add(1)

	backoff := runtimeMaintenanceBackoffFirst
	for step := 0; step < runtimeMaintenanceMaxSteps && ctx.Err() == nil; step++ {
		if hook := runtimeMaintenanceBeforeVacuumHook; hook != nil {
			if err := hook(ctx); err != nil {
				m.failures.Add(1)
				return
			}
		}
		freeBefore, observed := m.freelistCount(ctx, db)
		if observed && freeBefore == 0 {
			return
		}
		stepStart := time.Now()
		stepCtx, stepCancel := context.WithTimeout(ctx, runtimeMaintenanceStepTimeout)
		_, err := db.ExecContext(stepCtx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", runtimeMaintenanceVacuumPages))
		stepCancel()
		if err != nil {
			if sqliteutil.IsBusyError(err) || errors.Is(err, context.DeadlineExceeded) {
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
				}
				if backoff < runtimeMaintenanceBackoffSecond {
					backoff = runtimeMaintenanceBackoffSecond
				}
				continue
			}
			m.failures.Add(1)
			logpkg.Warnf("[runtime-store] background vacuum failed: %v", err)
			return
		}
		m.vacuumRuns.Add(1)
		m.vacuumTotalNs.Add(time.Since(stepStart).Nanoseconds())
		if !observed {
			return
		}
		freeAfter, ok := m.freelistCount(ctx, db)
		if ok && freeAfter >= freeBefore {
			return
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
		}
	}
}

func (m *runtimeStoreMaintenance) freelistCount(ctx context.Context, db *sql.DB) (int64, bool) {
	var pages int64
	if err := db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&pages); err != nil {
		return 0, false
	}
	return pages, true
}

// runMaintenanceOnceForTest 同步执行一轮维护（测试用；生产由后台 goroutine 驱动）。
func (s *SQLiteRuntimeStore) runMaintenanceOnceForTest(ctx context.Context) {
	if s == nil || s.maintenance == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.maintenance.runOnce(s, ctx)
}
