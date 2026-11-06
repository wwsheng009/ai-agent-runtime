package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

const (
	storagePruneRuntimeDefaultBusyTimeout = 3 * time.Second
	storagePruneRuntimeDefaultTimeout     = 30 * time.Minute
)

type storagePruneRuntimeOptions struct {
	out                     io.Writer
	errOut                  io.Writer
	json                    bool
	dbPath                  string
	toolReceiptRows         int
	toolReceiptBytes        int64
	cacheRequestRows        int
	eventRows               int
	streamingEventRetention time.Duration
	dryRun                  bool
	requireExclusive        bool
	busyTimeout             time.Duration
	timeout                 time.Duration
}

func durationFlag(cmd *cobra.Command, name string) time.Duration {
	value, _ := cmd.Flags().GetDuration(name)
	return value
}

type storagePruneRuntimeReport struct {
	Target   string                            `json:"target"`
	Path     string                            `json:"path"`
	Status   string                            `json:"status"`
	Stats    runtimechat.RuntimeRetentionStats `json:"stats"`
	Duration int64                             `json:"duration_ms"`
	Error    string                            `json:"error,omitempty"`
}

// newStoragePruneRuntimeCommand 创建 `aicli storage prune-runtime`：
// 对 session_runtime.sqlite 执行一次离线保留裁剪。
func newStoragePruneRuntimeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune-runtime",
		Short: "离线裁剪 session_runtime.sqlite 的历史回执、缓存明细和旧事件",
		Long: `按会话保留预算裁剪 runtime 库，清理策略生效前已写入或长期不再写入的历史行。

默认只 dry-run；必须显式加 --apply 才执行删除。

默认预算：
  - session_tool_receipts: 每会话 2048 条 / 8 MiB
  - cache_requests:       每会话 4096 条
  - session_events:       每会话 2048 条
  - assistant 流式事件:   默认只保留最近 48h（--streaming-event-retention 可调；0 关闭）

同时把历史内联 stable_tool_surface_json 迁移到内容寻址表。

安全约束：
  - 建议先退出所有使用该库的 aicli / runtime-server 进程；
  - --require-exclusive 在 Windows 上做句柄级独占探测；
  - 本命令只删除数据，不执行 VACUUM；删除后需运行
    aicli storage compact --target runtime --require-exclusive 才能把
    空闲页归还给操作系统；
  - 删除不可恢复，执行前请备份数据库。`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			apply := boolFlag(cmd, "apply")
			dryRun := !apply
			if apply && boolFlag(cmd, "dry-run") {
				fmt.Fprintln(cmd.ErrOrStderr(), "参数错误：--apply 与 --dry-run 不能同时使用")
				statsExit(statsExitUsage)
				return
			}
			statsExit(runStoragePruneRuntime(storagePruneRuntimeOptions{
				out:                     cmd.OutOrStdout(),
				errOut:                  cmd.ErrOrStderr(),
				json:                    boolFlag(cmd, "json"),
				dbPath:                  strings.TrimSpace(stringFlag(cmd, "db")),
				toolReceiptRows:         intFlag(cmd, "tool-receipt-rows"),
				toolReceiptBytes:        int64(intFlag(cmd, "tool-receipt-bytes")),
				cacheRequestRows:        intFlag(cmd, "cache-request-rows"),
				eventRows:               intFlag(cmd, "event-rows"),
				streamingEventRetention: durationFlag(cmd, "streaming-event-retention"),
				dryRun:                  dryRun,
				requireExclusive:        boolFlag(cmd, "require-exclusive"),
				busyTimeout:             time.Duration(intFlag(cmd, "busy-timeout")) * time.Second,
				timeout:                 time.Duration(intFlag(cmd, "timeout")) * time.Second,
			}))
		},
	}
	cmd.Flags().String("db", "", "显式指定 runtime 库路径（缺省按 runtime 配置解析）")
	cmd.Flags().Int("tool-receipt-rows", 2048, "单会话工具回执保留条数")
	cmd.Flags().Int("tool-receipt-bytes", 8<<20, "单会话工具回执保留总字节")
	cmd.Flags().Int("cache-request-rows", 4096, "单会话缓存请求明细保留条数")
	cmd.Flags().Int("event-rows", 2048, "单会话事件保留条数")
	cmd.Flags().Duration("streaming-event-retention", 48*time.Hour, "assistant 流式事件按龄保留时长；0 关闭")
	cmd.Flags().Bool("apply", false, "实际执行删除；缺省只 dry-run")
	cmd.Flags().Bool("dry-run", false, "显式预演（默认行为；不能与 --apply 同时使用）")
	cmd.Flags().Bool("require-exclusive", false, "Windows 句柄级独占探测；库仍被其它进程打开时立即失败")
	cmd.Flags().Int("busy-timeout", int(storagePruneRuntimeDefaultBusyTimeout/time.Second), "锁等待上限（秒）")
	cmd.Flags().Int("timeout", int(storagePruneRuntimeDefaultTimeout/time.Second), "总超时（秒）")
	cmd.Flags().Bool("json", false, "以 JSON 输出")
	return cmd
}

func runStoragePruneRuntime(opts storagePruneRuntimeOptions) int {
	if opts.out == nil {
		opts.out = os.Stdout
	}
	if opts.errOut == nil {
		opts.errOut = os.Stderr
	}
	if opts.busyTimeout <= 0 {
		opts.busyTimeout = storagePruneRuntimeDefaultBusyTimeout
	}
	if opts.timeout <= 0 {
		opts.timeout = storagePruneRuntimeDefaultTimeout
	}
	if opts.toolReceiptRows <= 0 {
		opts.toolReceiptRows = 2048
	}
	if opts.toolReceiptBytes <= 0 {
		opts.toolReceiptBytes = 8 << 20
	}
	if opts.cacheRequestRows <= 0 {
		opts.cacheRequestRows = 4096
	}
	if opts.eventRows <= 0 {
		opts.eventRows = 2048
	}

	targets, err := storageResolveTargets(storageCompactOptions{
		dbPath: opts.dbPath,
		target: "runtime",
	})
	if err != nil {
		fmt.Fprintf(opts.errOut, "参数错误：%v\n", err)
		return statsExitUsage
	}
	if len(targets) == 0 {
		fmt.Fprintln(opts.errOut, "没有找到 runtime 库路径")
		return statsExitUsage
	}
	target := targets[0]
	report := storagePruneRuntimeReport{Target: target.Name, Path: target.Path}
	started := time.Now()
	if _, err := os.Stat(target.Path); err != nil {
		if os.IsNotExist(err) {
			report.Status = "absent"
			report.Duration = time.Since(started).Milliseconds()
			writeStoragePruneRuntimeReport(opts, report)
			return statsExitOK
		}
		report.Status = "failed"
		report.Error = fmt.Sprintf("读取库文件失败：%v", err)
		report.Duration = time.Since(started).Milliseconds()
		writeStoragePruneRuntimeReport(opts, report)
		return statsExitDeterministic
	}

	if opts.requireExclusive {
		switch probeErr := storageExclusiveProbe(target.Path); {
		case probeErr == nil:
		case errors.Is(probeErr, errStorageExclusiveBusy):
			report.Status = "in_use"
			report.Error = "--require-exclusive：库文件仍被其它进程持有句柄（含空闲连接）：请先退出所有使用该库的 aicli/runtime-server 后重试"
		case errors.Is(probeErr, errStorageExclusiveUnsupported):
			report.Status = "failed"
			report.Error = "--require-exclusive：当前平台未实现句柄级独占探测；请退出所有使用进程后去掉该开关重试"
		default:
			report.Status = "failed"
			report.Error = fmt.Sprintf("--require-exclusive 独占探测失败：%v", probeErr)
		}
		if report.Status != "" {
			report.Duration = time.Since(started).Milliseconds()
			writeStoragePruneRuntimeReport(opts, report)
			return statsExitDeterministic
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	db, err := sql.Open("sqlite3", target.Path)
	if err != nil {
		report.Status = "failed"
		report.Error = fmt.Sprintf("打开失败：%v", err)
		report.Duration = time.Since(started).Milliseconds()
		writeStoragePruneRuntimeReport(opts, report)
		return statsExitDeterministic
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := storageExec(ctx, db, fmt.Sprintf("PRAGMA busy_timeout=%d", opts.busyTimeout.Milliseconds())); err != nil {
		report.Status = "failed"
		report.Error = fmt.Sprintf("设置 busy_timeout 失败：%v", err)
		report.Duration = time.Since(started).Milliseconds()
		writeStoragePruneRuntimeReport(opts, report)
		return statsExitDeterministic
	}
	if integrity, err := storageQuickCheck(ctx, db); err != nil {
		if storageIsBusyError(err) {
			report.Status = "in_use"
		} else {
			report.Status = "failed"
		}
		report.Error = fmt.Sprintf("quick_check 未能完成：%v", err)
		report.Duration = time.Since(started).Milliseconds()
		writeStoragePruneRuntimeReport(opts, report)
		return statsExitDeterministic
	} else if !strings.EqualFold(integrity, "ok") {
		report.Status = "failed"
		report.Error = "库完整性检查未通过：" + storageClampIntegrity(integrity)
		report.Duration = time.Since(started).Milliseconds()
		writeStoragePruneRuntimeReport(opts, report)
		return statsExitDeterministic
	}

	stats, err := runtimechat.PruneRuntimeRetention(ctx, db, runtimechat.RuntimeRetentionOptions{
		ToolReceiptRetention:    opts.toolReceiptRows,
		ToolReceiptMaxBytes:     opts.toolReceiptBytes,
		CacheRequestRetention:   opts.cacheRequestRows,
		EventRetention:          opts.eventRows,
		StreamingEventRetention: opts.streamingEventRetention,
		DryRun:                  opts.dryRun,
	})
	if err != nil {
		report.Status = "failed"
		report.Error = err.Error()
		report.Duration = time.Since(started).Milliseconds()
		writeStoragePruneRuntimeReport(opts, report)
		return statsExitDeterministic
	}
	report.Status = "pruned"
	if opts.dryRun {
		report.Status = "planned"
	}
	report.Stats = stats
	report.Duration = time.Since(started).Milliseconds()
	writeStoragePruneRuntimeReport(opts, report)
	return statsExitOK
}

func writeStoragePruneRuntimeReport(opts storagePruneRuntimeOptions, report storagePruneRuntimeReport) {
	if opts.json {
		encoder := json.NewEncoder(opts.out)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(report)
		return
	}
	switch report.Status {
	case "absent":
		fmt.Fprintf(opts.out, "[缺失] %s path=%s（跳过）\n", report.Target, report.Path)
	case "in_use":
		fmt.Fprintf(opts.out, "[占用] %s path=%s：%s\n", report.Target, report.Path, report.Error)
	case "pruned", "planned":
		label := "已裁剪"
		if report.Status == "planned" {
			label = "预演"
		}
		fmt.Fprintf(opts.out, "[%s] %s\n", label, report.Target)
		fmt.Fprintf(opts.out, "  path=%s\n", report.Path)
		fmt.Fprintf(opts.out, "  sessions=%d tool_receipts=%d (%s) cache_requests=%d events=%d (%s) streaming_events=%d (%s) legacy_surfaces=%d (%s) redundant_index_dropped=%t\n",
			report.Stats.SessionsScanned,
			report.Stats.ToolReceiptsDeleted, storageBytesText(report.Stats.ToolReceiptBytesDeleted),
			report.Stats.CacheRequestsDeleted,
			report.Stats.EventsDeleted, storageBytesText(report.Stats.EventPayloadBytesDeleted),
			report.Stats.StreamingEventsDeleted, storageBytesText(report.Stats.StreamingEventBytesDeleted),
			report.Stats.LegacyInlineSurfacesCompacted, storageBytesText(report.Stats.LegacyInlineSurfaceBytesFreed),
			report.Stats.RedundantSessionEventsIndexDropped)
		fmt.Fprintf(opts.out, "  耗时=%s；接下来运行 aicli storage compact --target runtime --require-exclusive 回收文件空间\n",
			storageDurationText(report.Duration))
	default:
		fmt.Fprintf(opts.out, "[失败] %s path=%s status=%s error=%s\n", report.Target, report.Path, report.Status, report.Error)
	}
}
