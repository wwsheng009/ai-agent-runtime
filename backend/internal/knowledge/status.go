package knowledge

import (
	"context"
	"fmt"
	"os"
	"time"
)

// IndexJob 是 index_jobs 表的一行：一次索引运行的账本（06 §4 Phase 1 交付 5）。
//
// 状态面用它回答"最近一次索引什么时候跑的、跑完没有、扫了多少文件"；
// 时间戳统一为 unix 毫秒（0 = 未发生）。
type IndexJob struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Kind 目前只有 "light"（Phase 1 的 regex_builtin 通道）。
	Kind string `json:"kind"`
	// Status ∈ running|done|failed。
	Status string `json:"status"`
	// FilesTotal / FilesDone 是本次运行的文件进度。
	FilesTotal int `json:"files_total"`
	FilesDone  int `json:"files_done"`
	// Error 是失败原因摘要（failed 时非空，截断到 512 字节）。
	Error string `json:"error,omitempty"`
	// StartedAt / FinishedAt / DurationMS 是运行时长（FinishedAt=0 表示仍在跑）。
	StartedAt  int64 `json:"started_at,omitempty"`
	FinishedAt int64 `json:"finished_at,omitempty"`
	DurationMS int64 `json:"duration_ms,omitempty"`
}

// index_jobs.status 的取值（DDL 注释：queued|running|done|failed|cancelled）。
const (
	IndexJobStatusQueued    = "queued"
	IndexJobStatusRunning   = "running"
	IndexJobStatusDone      = "done"
	IndexJobStatusFailed    = "failed"
	IndexJobStatusCancelled = "cancelled"
)

// IndexJobKindLight 是 Phase 1 唯一在跑的索引通道（regex_builtin 解析器）。
const IndexJobKindLight = "light"

// IndexJobKindIncremental 是定向增量通道（IndexPaths，Phase 5 交付 1/2）：
// 只处理调用方标记的少数文件，不遍历工作区。单独成 kind 是为了让状态面
// 能区分"全量/扫描运行"与"编辑触发的增量运行"（也是单文件增量 p95 的测量口径）。
const IndexJobKindIncremental = "incremental"

// maxIndexJobErrorBytes 限制落库的错误摘要长度，避免一次坏运行的完整堆栈把行撑爆。
const maxIndexJobErrorBytes = 512

// StatusReport 是 `knowledge.status` CLI / HTTP 面的统一载荷（06 §4 Phase 1 交付 5）。
//
// 它是**只读快照**：所有字段都来自当前进程的层句柄与 store 的一次查询，
// 不触发索引、不写库（owner 与 reader 行为一致）。
type StatusReport struct {
	Mode    Mode `json:"mode"`
	Enabled bool `json:"enabled"`
	// Role 是本进程相对 store 的角色：owner（可写）| reader（只读降级）。
	Role Role `json:"role"`
	// OwnerPID 是持有写锁的进程 pid（本进程是 owner 时即自身）。
	OwnerPID  int    `json:"owner_pid,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	DBPath    string `json:"db_path,omitempty"`
	// DBSizeBytes 是 knowledge.db 主文件大小（不含 -wal/-shm 副文件）。
	DBSizeBytes   int64 `json:"db_size_bytes"`
	SchemaVersion int   `json:"schema_version"`
	// Files / Symbols / Refs 是行数汇总（当前 workspace）。
	Files   int64 `json:"files"`
	Symbols int64 `json:"symbols"`
	Refs    int64 `json:"refs"`
	// IndexedAt 是最近一次成功写事务的 unix 毫秒；0 表示尚无索引。
	IndexedAt int64 `json:"indexed_at,omitempty"`
	// StalenessMS 是 (now - IndexedAt)，供状态栏直接展示"索引有多旧"。
	StalenessMS int64 `json:"staleness_ms,omitempty"`
	// IndexRunning 表示本进程的后台首次索引仍在跑（Activation 级事实）。
	IndexRunning bool `json:"index_running"`
	// LastJob 是最近一次索引运行（index_jobs 的最新行）；nil = 从未跑过。
	LastJob *IndexJob `json:"last_job,omitempty"`
	// LockWait 是本进程写路径的锁等待采样（见 LockWaitStats 的口径说明）。
	LockWait LockWaitStats `json:"lock_wait"`
	// GC 是软删除行物理清理的摘要（04 §5 Phase 5 交付 4，本进程视角）。
	GC GCStatus `json:"gc,omitempty"`
	// DegradedReason 解释"为什么状态不完整"（未索引 / reader 降级 / 上次索引失败）。
	// 空串 = 无降级。它存在的原因：状态面最怕静默——零值必须能被解释。
	DegradedReason string `json:"degraded_reason,omitempty"`
	GeneratedAt    int64  `json:"generated_at"`
}

// OwnerPID 返回当前持有 store 写锁的进程 pid；未知时为 0。
func (l *Layer) OwnerPID() int {
	if l == nil || l.owner == nil {
		return 0
	}
	if l.owner.state == RoleOwner {
		return os.Getpid()
	}
	return l.owner.holder
}

// lookupWorkspace 只读地解析 workspace 行 id；ok=false 表示尚未登记。
//
// 与 ensureWorkspace 的区别：**任何角色都不写库**。状态面（reader 也会调用）
// 因此不会因"看一眼状态"而拿到 ErrReadOnlyStore，也不会在 owner 上产生多余写。
func (l *Layer) lookupWorkspace(ctx context.Context) (string, bool, error) {
	if l == nil || l.store == nil {
		return "", false, nil
	}
	if l.workspaceID != "" {
		return l.workspaceID, true, nil
	}
	id, ok, err := l.store.FindWorkspace(ctx, l.cfg.Workspace)
	if err != nil {
		return "", false, err
	}
	if ok {
		l.workspaceID = id
	}
	return id, ok, nil
}

// Status 返回知识层状态快照（只读；nil Layer 返回 mode=off 的最小载荷）。
func (l *Layer) Status(ctx context.Context) (StatusReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := StatusReport{
		Mode:        ModeOff,
		Role:        RoleNone,
		GeneratedAt: time.Now().UnixMilli(),
	}
	if l == nil {
		return report, nil
	}
	report.Mode = l.Mode()
	report.Enabled = l.Enabled()
	report.Role = l.Role()
	report.OwnerPID = l.OwnerPID()
	report.Workspace = l.Workspace()
	report.DBPath = l.DBPath()
	if report.DBPath != "" {
		if info, err := os.Stat(report.DBPath); err == nil {
			report.DBSizeBytes = info.Size()
		}
	}
	if s, ok := l.store.(*sqliteStore); ok {
		report.LockWait = s.lockWait.snapshot()
	}
	report.GC = l.GCStats()
	if l.store == nil {
		return report, nil
	}

	version, err := l.store.SchemaVersion(ctx)
	if err != nil {
		return report, err
	}
	report.SchemaVersion = version

	wsID, ok, err := l.lookupWorkspace(ctx)
	if err != nil {
		return report, err
	}
	if !ok {
		// 未索引不是错误：owner 首次索引前的正常状态，也是 reader 挂上来的常见初始态。
		report.DegradedReason = "workspace is not indexed yet"
	} else {
		stats, err := l.store.Stats(ctx, wsID)
		if err != nil {
			return report, err
		}
		report.Files, report.Symbols, report.Refs = stats.Files, stats.Symbols, stats.Refs
		report.IndexedAt = stats.IndexedAt
		if stats.IndexedAt > 0 {
			if stale := report.GeneratedAt - stats.IndexedAt; stale > 0 {
				report.StalenessMS = stale
			}
		}
		job, err := l.store.LatestIndexJob(ctx, wsID)
		if err != nil {
			return report, err
		}
		report.LastJob = job
	}

	if report.DegradedReason == "" && l.Role() == RoleReader {
		if report.OwnerPID > 0 {
			report.DegradedReason = fmt.Sprintf("read-only: store is owned by pid %d", report.OwnerPID)
		} else {
			report.DegradedReason = "read-only: store is owned by another process"
		}
	}
	return report, nil
}
