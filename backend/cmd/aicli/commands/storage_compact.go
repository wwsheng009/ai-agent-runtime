package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ============================================================================
// `aicli storage`：本地 SQLite 存储维护（离线压缩）。
//
// 背景：2026-09-26 的 WAL 损坏事故后，在线页回收被整体移除——
//   - `PRAGMA auto_vacuum=INCREMENTAL` + `PRAGMA incremental_vacuum(N)` 会移动页
//     并重写 ptrmap，是异常退出/并发写后 ptrmap、freelist 错乱的高风险来源；
//   - `wal_checkpoint(TRUNCATE)` 在并发下放大 Windows 驱动 wal-index 缺陷。
//
// 代价是删除产生的空闲页（freelist）不再在线归还给操作系统，库文件只涨不缩。
// 这里提供唯一被认可的回收途径：**独占访问时的离线 VACUUM**：
//   - 打开前后各做一次 quick_check；
//   - 短 busy_timeout：库里还有活跃写事务/快照读者时立刻 BUSY 退出（退出码 2），
//     不排队、不长时间持锁；
//   - 重建时顺手把历史库的 auto_vacuum=INCREMENTAL 转成 NONE（VACUUM 生效），
//     与“在线自回收已移除”的决策保持一致。
//
// 退出码沿用 stats 约定：0=成功（含 absent/skipped）；1=参数错误；
// 2=确定性错误（库被占用、损坏、不可读）。
// ============================================================================

const (
	storageCompactDefaultBusyTimeout = 3 * time.Second
	storageCompactDefaultTimeout     = 30 * time.Minute
)

// storageCompactTarget 是一个待压缩的库文件。
type storageCompactTarget struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// storageCompactReport 是单个目标的结果（JSON 字段稳定，供脚本消费）。
type storageCompactReport struct {
	Target           string `json:"target"`
	Path             string `json:"path"`
	Status           string `json:"status"` // compacted|planned|skipped|absent|in_use|failed
	Reason           string `json:"reason,omitempty"`
	IntegrityBefore  string `json:"integrity_before,omitempty"`
	IntegrityAfter   string `json:"integrity_after,omitempty"`
	PageSize         int64  `json:"page_size,omitempty"`
	PageCountBefore  int64  `json:"page_count_before,omitempty"`
	PageCountAfter   int64  `json:"page_count_after,omitempty"`
	FreePagesBefore  int64  `json:"free_pages_before,omitempty"`
	AutoVacuumBefore int64  `json:"auto_vacuum_before,omitempty"`
	AutoVacuumAfter  int64  `json:"auto_vacuum_after,omitempty"`
	BytesBefore      int64  `json:"bytes_before,omitempty"`
	BytesAfter       int64  `json:"bytes_after,omitempty"`
	BytesReclaimed   int64  `json:"bytes_reclaimed,omitempty"`
	DurationMs       int64  `json:"duration_ms,omitempty"`
	Error            string `json:"error,omitempty"`
}

type storageCompactOptions struct {
	out           io.Writer
	errOut        io.Writer
	json          bool
	dbPath        string
	target        string
	dryRun        bool
	force         bool
	skipIntegrity bool
	busyTimeout   time.Duration
	timeout       time.Duration
}

// NewStorageCommand 创建 `aicli storage`：本地 SQLite 库的离线维护入口。
func NewStorageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "本地 SQLite 存储维护（离线压缩）",
		Long: `维护本地 SQLite 库（会话运行库/会话历史/制品库/分析库等）。

子命令：
  storage compact   离线压缩：独占访问时 VACUUM，回收 freelist 空间

说明：在线页回收（auto_vacuum/incremental_vacuum）已在 2026-09 的损坏事故后
整体移除。文件空间回收只能通过本命令的离线 VACUUM 完成，请先退出正在使用这些
库的 aicli / runtime-server 进程；命令检测到占用会立即失败（退出码 2）。

退出码：
  0 = 成功（含 absent/skipped：库不存在或没有空闲页）
  1 = 参数错误
  2 = 确定性错误（库被占用、损坏、不可读）`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newStorageCompactCommand())
	return cmd
}

func newStorageCompactCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compact",
		Short: "离线压缩本地 SQLite 库（独占访问时 VACUUM）",
		Long: `对本地 SQLite 库执行离线 VACUUM：重建数据库文件、回收 freelist 空闲页，
并把历史库残留的 auto_vacuum=INCREMENTAL 转成 NONE。

安全约束：
  - 仅做 VACUUM（不触碰 auto_vacuum/incremental_vacuum/wal_checkpoint(TRUNCATE)）；
  - 打开前后各做一次 PRAGMA quick_check，损坏库只报告不重建；
  - busy_timeout 很短：库里还有活跃写事务或未释放的快照读者时立刻 BUSY 退出，
    不会排队等待、也不会长时间持锁。

建议先退出所有正在使用这些库的进程（aicli / runtime-server）再执行。

示例：
  aicli storage compact
  aicli storage compact --target runtime
  aicli storage compact --db ~/.aicli/sessions/runtime/session_runtime.sqlite --json
  aicli storage compact --dry-run`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			statsExit(runStorageCompact(storageCompactOptions{
				out:           cmd.OutOrStdout(),
				errOut:        cmd.ErrOrStderr(),
				json:          boolFlag(cmd, "json"),
				dbPath:        strings.TrimSpace(stringFlag(cmd, "db")),
				target:        strings.TrimSpace(stringFlag(cmd, "target")),
				dryRun:        boolFlag(cmd, "dry-run"),
				force:         boolFlag(cmd, "force"),
				skipIntegrity: boolFlag(cmd, "no-integrity-check"),
				busyTimeout:   time.Duration(intFlag(cmd, "busy-timeout")) * time.Second,
				timeout:       time.Duration(intFlag(cmd, "timeout")) * time.Second,
			}))
		},
	}
	cmd.Flags().String("target", "all", "目标：all|runtime|history|artifacts|analytics|team|agent-control|background")
	cmd.Flags().String("db", "", "显式指定单个库文件（与 --target 互斥）")
	cmd.Flags().Bool("dry-run", false, "只体检与预估，不执行 VACUUM")
	cmd.Flags().Bool("force", false, "即使没有空闲页也执行 VACUUM")
	cmd.Flags().Bool("no-integrity-check", false, "跳过 quick_check 体检（更快，但可能对损坏库执行 VACUUM）")
	cmd.Flags().Int("busy-timeout", int(storageCompactDefaultBusyTimeout/time.Second), "锁等待上限（秒）：超时即判定库被占用")
	cmd.Flags().Int("timeout", int(storageCompactDefaultTimeout/time.Second), "单库总超时（秒）")
	cmd.Flags().Bool("json", false, "以 JSON 输出")
	return cmd
}

func runStorageCompact(opts storageCompactOptions) int {
	if opts.out == nil {
		opts.out = os.Stdout
	}
	if opts.errOut == nil {
		opts.errOut = os.Stderr
	}
	if opts.busyTimeout <= 0 {
		opts.busyTimeout = storageCompactDefaultBusyTimeout
	}
	if opts.timeout <= 0 {
		opts.timeout = storageCompactDefaultTimeout
	}
	targets, err := storageResolveTargets(opts)
	if err != nil {
		fmt.Fprintf(opts.errOut, "参数错误：%v\n", err)
		return statsExitUsage
	}
	reports := make([]storageCompactReport, 0, len(targets))
	exit := statsExitOK
	for _, target := range targets {
		report := storageCompactOne(target, opts)
		reports = append(reports, report)
		if report.Status == "failed" || report.Status == "in_use" {
			exit = statsExitDeterministic
		}
	}
	if opts.json {
		encoder := json.NewEncoder(opts.out)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(map[string]interface{}{
			"reports": reports,
			"summary": storageCompactSummary(reports),
		})
		return exit
	}
	storageRenderCompactText(opts.out, reports)
	return exit
}

func storageCompactSummary(reports []storageCompactReport) map[string]int {
	summary := map[string]int{"total": len(reports)}
	for _, report := range reports {
		summary[report.Status]++
	}
	return summary
}

func storageRenderCompactText(out io.Writer, reports []storageCompactReport) {
	if len(reports) == 0 {
		fmt.Fprintln(out, "没有需要处理的目标（库文件都不存在）")
		return
	}
	for _, report := range reports {
		switch report.Status {
		case "compacted":
			fmt.Fprintf(out, "[压缩] %s\n", report.Target)
			fmt.Fprintf(out, "  path=%s\n", report.Path)
			fmt.Fprintf(out, "  pages=%d→%d free=%d auto_vacuum=%d→%d\n",
				report.PageCountBefore, report.PageCountAfter, report.FreePagesBefore,
				report.AutoVacuumBefore, report.AutoVacuumAfter)
			fmt.Fprintf(out, "  bytes=%s→%s 回收=%s 耗时=%s\n",
				storageBytesText(report.BytesBefore), storageBytesText(report.BytesAfter),
				storageBytesText(report.BytesReclaimed), storageDurationText(report.DurationMs))
		case "planned":
			fmt.Fprintf(out, "[预演] %s path=%s free=%d auto_vacuum=%d bytes=%s（dry-run 未修改）\n",
				report.Target, report.Path, report.FreePagesBefore,
				report.AutoVacuumBefore, storageBytesText(report.BytesBefore))
		case "skipped":
			fmt.Fprintf(out, "[跳过] %s path=%s 原因=%s\n", report.Target, report.Path, report.Reason)
		case "absent":
			fmt.Fprintf(out, "[缺失] %s path=%s（跳过）\n", report.Target, report.Path)
		default:
			fmt.Fprintf(out, "[失败] %s path=%s 原因=%s\n", report.Target, report.Path, report.Error)
		}
	}
}

// storageResolveTargets 解析用户选定的库文件；--db 与 --target 互斥，
// 两者都缺省时压缩全部已知本地库（不存在的自动跳过）。
func storageResolveTargets(opts storageCompactOptions) ([]storageCompactTarget, error) {
	if opts.dbPath != "" {
		return []storageCompactTarget{{
			Name: "custom",
			Path: filepath.Clean(aiclipaths.ExpandUserPath(opts.dbPath)),
		}}, nil
	}
	target := strings.ToLower(strings.TrimSpace(opts.target))
	if target == "" {
		target = "all"
	}
	known := []string{"all", "runtime", "history", "artifacts", "analytics", "team", "agent-control", "background"}
	if !storageContainsString(known, target) {
		return nil, fmt.Errorf("未知 --target %q（可选：%s）", opts.target, strings.Join(known, "|"))
	}

	config, configFile := storageLoadRuntimeConfig()
	paths := sessionruntime.ResolvePaths(sessionruntime.ResolveOptions{
		Config:     config,
		ConfigFile: configFile,
		Mode:       sessionruntime.ModeCLILocal,
	})
	all := []storageCompactTarget{
		{Name: "runtime", Path: paths.SessionRuntimeStorePath},
		{Name: "history", Path: storageHistoryStorePath(config, configFile, paths)},
		{Name: "artifacts", Path: paths.ArtifactStorePath},
		{Name: "analytics", Path: usageanalytics.DefaultDBPath()},
		{Name: "team", Path: paths.TeamStorePath},
		{Name: "agent-control", Path: paths.AgentControlStorePath},
		{Name: "background", Path: paths.BackgroundStorePath},
	}
	selected := make([]storageCompactTarget, 0, len(all))
	seen := map[string]bool{}
	for _, candidate := range all {
		candidate.Path = strings.TrimSpace(candidate.Path)
		if candidate.Path == "" {
			continue
		}
		if target != "all" && candidate.Name != target {
			continue
		}
		// agent-control 的 mailbox/agent 库默认与主库同文件：按路径去重。
		key := strings.ToLower(filepath.Clean(candidate.Path))
		if seen[key] {
			continue
		}
		seen[key] = true
		selected = append(selected, candidate)
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return selected, nil
}

// storageHistoryStorePath 解析会话历史库：优先显式配置（Sessions.StorePath），
// 否则回落到 <sessionDir>/<DefaultSessionHistoryFileName>（与 CLI local 一致）。
func storageHistoryStorePath(config *runtimecfg.RuntimeConfig, configFile string, paths sessionruntime.ResolvedPaths) string {
	if config != nil {
		if path := sessionruntime.ResolvePath(configFile, config.Sessions.StorePath); path != "" {
			return path
		}
	}
	if strings.TrimSpace(paths.SessionDir) == "" {
		return ""
	}
	return filepath.Join(paths.SessionDir, aiclipaths.DefaultSessionHistoryFileName)
}

// storageLoadRuntimeConfig 尽力加载全局 runtime 配置（.aicli 层合并）；
// 失败时返回 nil，由 ResolvePaths 的 CLI-local 默认值兜底。
func storageLoadRuntimeConfig() (*runtimecfg.RuntimeConfig, string) {
	path := resolveGlobalRuntimeConfigPath(nil)
	if path == "" {
		return nil, ""
	}
	config, configFile, err := loadCachedRuntimeConfig(path)
	if err != nil || config == nil {
		return nil, ""
	}
	return config, configFile
}

// storageCompactOne 是单个库的压缩流程；任何失败都落在 report 里，
// 由调用方决定退出码。
func storageCompactOne(target storageCompactTarget, opts storageCompactOptions) storageCompactReport {
	report := storageCompactReport{Target: target.Name, Path: target.Path}
	info, err := os.Stat(target.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		report.Status = "absent"
		report.Reason = "库文件不存在"
		return report
	case err != nil:
		report.Status = "failed"
		report.Error = fmt.Sprintf("读取文件状态失败：%v", err)
		return report
	case info.IsDir():
		report.Status = "failed"
		report.Error = "路径是目录而不是文件"
		return report
	}
	report.BytesBefore = info.Size()
	started := time.Now()

	// 打开前对账 -wal/-shm：库可能被带外替换或上次异常退出留下陈旧 wal-index
	// （见 sqliteutil.ReconcileOrphanedSidecars）。残留 sidecar 被活跃会话映射时
	// 删除失败，这里不当作错误。
	_, _ = sqliteutil.ReconcileOrphanedSidecarsDSN(target.Path)

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	db, err := sql.Open("sqlite3", target.Path)
	if err != nil {
		report.Status = "failed"
		report.Error = fmt.Sprintf("打开失败：%v", err)
		return report
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	if err := storageExec(ctx, db, fmt.Sprintf("PRAGMA busy_timeout=%d", opts.busyTimeout.Milliseconds())); err != nil {
		return storageFail(report, "设置 busy_timeout 失败", err)
	}
	if !opts.skipIntegrity {
		integrity, err := storageQuickCheck(ctx, db)
		if err != nil {
			if storageIsBusyError(err) {
				return storageInUse(report, err)
			}
			return storageFail(report, "quick_check 未能完成（库可能损坏）", err)
		}
		report.IntegrityBefore = integrity
		if !strings.EqualFold(integrity, "ok") {
			clamped := storageClampIntegrity(integrity)
			report.IntegrityBefore = clamped
			return storageFail(report, "库完整性检查未通过，已跳过 VACUUM（请先按 docs 走修复流程）", errors.New(clamped))
		}
	}

	before, err := storageReadStats(ctx, db)
	if err != nil {
		if storageIsBusyError(err) {
			return storageInUse(report, err)
		}
		return storageFail(report, "读取库统计失败", err)
	}
	report.PageSize = before.pageSize
	report.PageCountBefore = before.pageCount
	report.FreePagesBefore = before.freePages
	report.AutoVacuumBefore = before.autoVacuum

	if opts.dryRun {
		report.Status = "planned"
		report.DurationMs = time.Since(started).Milliseconds()
		return report
	}
	if report.FreePagesBefore == 0 && !opts.force {
		report.Status = "skipped"
		report.Reason = "没有空闲页（freelist=0），无需压缩；可用 --force 强制重建"
		report.DurationMs = time.Since(started).Milliseconds()
		return report
	}

	// auto_vacuum=NONE：让历史库（auto_vacuum=INCREMENTAL）在本次重建后
	// 彻底退出在线自回收模式；该 PRAGMA 只有在 VACUUM 之后才落到文件头。
	if err := storageExec(ctx, db, "PRAGMA auto_vacuum=NONE"); err != nil {
		if storageIsBusyError(err) {
			return storageInUse(report, err)
		}
		return storageFail(report, "关闭 auto_vacuum 失败", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		if storageIsBusyError(err) {
			return storageInUse(report, err)
		}
		return storageFail(report, "VACUUM 失败", err)
	}

	after, err := storageReadStats(ctx, db)
	if err != nil {
		return storageFail(report, "读取压缩后统计失败", err)
	}
	report.PageCountAfter = after.pageCount
	report.AutoVacuumAfter = after.autoVacuum
	report.BytesAfter = after.bytes
	report.BytesReclaimed = report.BytesBefore - report.BytesAfter
	if report.BytesReclaimed < 0 {
		report.BytesReclaimed = 0
	}
	if !opts.skipIntegrity {
		integrity, err := storageQuickCheck(ctx, db)
		if err != nil {
			return storageFail(report, "压缩后 quick_check 未能完成", err)
		}
		report.IntegrityAfter = integrity
		if !strings.EqualFold(integrity, "ok") {
			clamped := storageClampIntegrity(integrity)
			report.IntegrityAfter = clamped
			return storageFail(report, "压缩后完整性检查未通过（请保留现场并走修复流程）", errors.New(clamped))
		}
	}
	report.Status = "compacted"
	report.DurationMs = time.Since(started).Milliseconds()
	return report
}

type storageSQLiteStats struct {
	pageSize   int64
	pageCount  int64
	freePages  int64
	autoVacuum int64
	bytes      int64
}

func storageReadStats(ctx context.Context, db *sql.DB) (storageSQLiteStats, error) {
	var stats storageSQLiteStats
	for _, item := range []struct {
		pragma string
		target *int64
	}{
		{"PRAGMA page_size", &stats.pageSize},
		{"PRAGMA page_count", &stats.pageCount},
		{"PRAGMA freelist_count", &stats.freePages},
		{"PRAGMA auto_vacuum", &stats.autoVacuum},
	} {
		if err := db.QueryRowContext(ctx, item.pragma).Scan(item.target); err != nil {
			return stats, fmt.Errorf("%s: %w", item.pragma, err)
		}
	}
	if stats.pageSize > 0 {
		stats.bytes = stats.pageSize * stats.pageCount
	}
	return stats, nil
}

func storageQuickCheck(ctx context.Context, db *sql.DB) (string, error) {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return "", err
	}
	return result, nil
}

func storageExec(ctx context.Context, db *sql.DB, statement string) error {
	_, err := db.ExecContext(ctx, statement)
	return err
}

func storageFail(report storageCompactReport, reason string, err error) storageCompactReport {
	report.Status = "failed"
	report.Error = fmt.Sprintf("%s：%v", reason, err)
	return report
}

func storageInUse(report storageCompactReport, err error) storageCompactReport {
	report.Status = "in_use"
	report.Error = fmt.Sprintf("库仍被其它进程/连接占用（%v）：请先退出正在使用它的 aicli/runtime-server 后重试", err)
	return report
}

// storageIsBusyError 识别 SQLITE_BUSY 形态（驱动错误文本在不同版本间略有差异）。
func storageIsBusyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, token := range []string{"database is locked", "database table is locked", "busy", "sqlite_busy", "locked"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// storageClampIntegrity 截断 quick_check 的明细：损坏库的原始输出可达上百行
// （每一行一个坏 ptrmap 条目），JSON/终端里保留头部与总行数即可定位形态。
func storageClampIntegrity(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return text
	}
	const (
		maxLines = 6
		maxRunes = 1200
	)
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("…（共 %d 行，已截断）", len(lines)))
	}
	result := strings.Join(lines, "\n")
	if runes := []rune(result); len(runes) > maxRunes {
		result = string(runes[:maxRunes]) + "…（已截断）"
	}
	return result
}

func storageContainsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func storageBytesText(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	value := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.2f%s", value, suffix)
		}
	}
	return fmt.Sprintf("%.2fPiB", value/unit)
}

func storageDurationText(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.2fs", float64(ms)/1000)
}
