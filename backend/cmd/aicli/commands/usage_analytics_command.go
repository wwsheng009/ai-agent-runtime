package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ============================================================================
// `aicli usage-analytics`：分析库维护命令树。
//
// 方案：docs/plan/usage-analytics-query-performance-optimization-plan-20260917.md
// §6.3 对账与修复。rebuild-stats 从 usage_requests 全量重算会话预聚合计数
// 与 turn 去重键（usage_session_turn_keys），用于修复已知/人为漂移。
//
// 退出码沿用 stats 约定：0=成功；1=参数错误；2=确定性错误（库不可打开/未启用）。
// ============================================================================

// NewUsageAnalyticsCommand 创建 `aicli usage-analytics` 命令树。
func NewUsageAnalyticsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "usage-analytics",
		Short: "用量分析库维护（重建预聚合统计）",
		Long: `维护统一用量分析库（usage_analytics.sqlite）。

子命令：
  usage-analytics rebuild-stats   从原始请求表重建会话预聚合统计（对账/修复漂移）
  usage-analytics compact-records 批量瘦身历史工具 record_json 大载荷
  usage-analytics prune           按保留期清理原始明细

退出码：
  0 = 成功
  1 = 参数错误
  2 = 库不可打开/未启用预聚合统计等确定性错误

库路径优先级：--db > AICLI_USAGE_ANALYTICS_DB > 默认
（~/.aicli/sessions/runtime/usage_analytics.sqlite）。
排错文档：docs/plan/session-analytics-runbook.md`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().String("db", "", "分析库路径（默认取 AICLI_USAGE_ANALYTICS_DB，未设置时为 ~/.aicli/sessions/runtime/usage_analytics.sqlite）")
	cmd.AddCommand(newUsageAnalyticsRebuildStatsCommand())
	cmd.AddCommand(newUsageAnalyticsCompactRecordsCommand())
	cmd.AddCommand(newUsageAnalyticsPruneCommand())
	return cmd
}

type usageAnalyticsRebuildOptions struct {
	dbPath    string
	sessionID string
	all       bool
	json      bool
	out       io.Writer
	errOut    io.Writer
}

func newUsageAnalyticsRebuildStatsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rebuild-stats",
		Short: "从原始请求表重建会话预聚合统计（对账/修复漂移）",
		Long: `从 usage_requests 全量重算 usage_sessions 的预聚合计数列与 turn 去重键。

使用场景：
  - 健康快照（/api/runtime/status → usage_analytics.stats_drift）报告计数漂移；
  - 逃生开关回退旧读路径期间产生的漂移需要显式修复；
  - 手工/外部工具直接改写过分析库。

--session 与 --all 必须二选一。命令以可写方式打开分析库（可能触发 v3 迁移）。

示例：
  aicli usage-analytics rebuild-stats --session 0f3c...
  aicli usage-analytics rebuild-stats --all --json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			statsExit(runUsageAnalyticsRebuildStats(usageAnalyticsRebuildOptions{
				dbPath:    stringFlag(cmd, "db"),
				sessionID: strings.TrimSpace(stringFlag(cmd, "session")),
				all:       boolFlag(cmd, "all"),
				json:      boolFlag(cmd, "json"),
				out:       cmd.OutOrStdout(),
				errOut:    cmd.ErrOrStderr(),
			}))
		},
	}
	cmd.Flags().String("session", "", "只重建指定会话（与 --all 二选一）")
	cmd.Flags().Bool("all", false, "重建全库所有会话")
	cmd.Flags().Bool("json", false, "以 JSON 输出结果")
	return cmd
}

func runUsageAnalyticsRebuildStats(opts usageAnalyticsRebuildOptions) int {
	out := opts.out
	if out == nil {
		out = io.Discard
	}
	errOut := opts.errOut
	if errOut == nil {
		errOut = io.Discard
	}
	hasSession := opts.sessionID != ""
	if hasSession == opts.all {
		fmt.Fprintln(errOut, "参数错误：--session 与 --all 必须二选一")
		return statsExitUsage
	}

	path := strings.TrimSpace(opts.dbPath)
	if path == "" {
		// DefaultDBPath 已包含 AICLI_USAGE_ANALYTICS_DB 覆盖优先级。
		path = usageanalytics.DefaultDBPath()
	}
	path = filepath.Clean(path)

	store, err := usageanalytics.Open(usageanalytics.Config{Path: path})
	if err != nil {
		fmt.Fprintf(errOut, "打开分析库失败（%s）：%v\n", path, err)
		return statsExitDeterministic
	}
	defer func() { _ = store.Close() }()
	if !store.StatsSchemaReady() {
		fmt.Fprintf(errOut, "分析库未启用预聚合统计（schema 版本不足或命中 %s）：%s\n",
			usageanalytics.EnvDisableStats, path)
		return statsExitDeterministic
	}

	scope := "all"
	if hasSession {
		scope = "session"
		if err := store.RebuildSessionStats(opts.sessionID); err != nil {
			fmt.Fprintf(errOut, "重建会话统计失败（%s）：%v\n", opts.sessionID, err)
			return statsExitDeterministic
		}
	} else if err := store.RebuildAllSessionStats(); err != nil {
		fmt.Fprintf(errOut, "重建全库会话统计失败：%v\n", err)
		return statsExitDeterministic
	}

	if opts.json {
		payload := map[string]interface{}{
			"db_path":    path,
			"scope":      scope,
			"session_id": opts.sessionID,
			"rebuilt":    true,
		}
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(errOut, "序列化输出失败：%v\n", err)
			return statsExitDeterministic
		}
		fmt.Fprintln(out, string(encoded))
		return statsExitOK
	}
	if hasSession {
		fmt.Fprintf(out, "重建完成：%s（会话 %s）\n", path, opts.sessionID)
	} else {
		fmt.Fprintf(out, "重建完成：%s（全部会话）\n", path)
	}
	return statsExitOK
}

type usageAnalyticsPruneOptions struct {
	dbPath string
	before string
	ctx    context.Context
	// vacuum 仅用于解析已停用的 --vacuum flag：命中即刻以用法错误拒绝，
	// 保证不存在任何在线文件级压缩路径。
	vacuum bool
	json   bool
	out    io.Writer
	errOut io.Writer
}

func newUsageAnalyticsPruneCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "按保留期清理全部原始明细并重建统计",
		Long: `删除 --before 之前的请求、工具、回合、子代理、路由、LSP 与围栏原始明细，
并重建全库预聚合统计；会话元数据保留（列表仍可见历史会话）。

时间 0 的行不清理（无法判定年龄）。每批提交，避免长时间占用写锁。
空间回收不在本命令执行：在线压缩/页回收已在 2026-09 的数据库损坏事故后
整体移除。需要压缩分析库请先退出使用进程，再离线执行：
  aicli storage compact --target analytics

示例：
  aicli usage-analytics prune --before 2026-01-01
  aicli usage-analytics prune --before 2026-01-01T00:00:00+08:00 --json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			statsExit(runUsageAnalyticsPrune(usageAnalyticsPruneOptions{
				dbPath: stringFlag(cmd, "db"),
				before: stringFlag(cmd, "before"),
				vacuum: boolFlag(cmd, "vacuum"),
				json:   boolFlag(cmd, "json"),
				out:    cmd.OutOrStdout(),
				errOut: cmd.ErrOrStderr(),
			}))
		},
	}
	cmd.Flags().String("before", "", "保留边界：此时间之前的请求明细将被删除（RFC3339 或 2006-01-02）")
	cmd.Flags().Bool("json", false, "以 JSON 输出结果")
	// --vacuum 已停用：保留解析面以便给出明确迁移提示；MarkDeprecated 会隐藏该 flag。
	cmd.Flags().Bool("vacuum", false, "已停用：在线压缩已移除；请使用 aicli storage compact --target analytics")
	_ = cmd.Flags().MarkDeprecated("vacuum", "在线压缩已移除；请使用 aicli storage compact --target analytics")
	return cmd
}

func parseUsageAnalyticsCutoff(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("--before 必填")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", trimmed, time.Local); err == nil {
		return parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", trimmed, time.Local); err == nil {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("无法解析 --before 时间：%s", trimmed)
}

func runUsageAnalyticsPrune(opts usageAnalyticsPruneOptions) int {
	out := opts.out
	if out == nil {
		out = io.Discard
	}
	errOut := opts.errOut
	if errOut == nil {
		errOut = io.Discard
	}
	if opts.vacuum {
		fmt.Fprintln(errOut, "错误：prune --vacuum 已停用（2026-09 数据库损坏事故后禁止在线数据库文件维护）；请使用 aicli storage compact --target analytics 离线压缩")
		return statsExitUsage
	}
	cutoff, err := parseUsageAnalyticsCutoff(opts.before)
	if err != nil {
		fmt.Fprintf(errOut, "参数错误：%v\n", err)
		return statsExitUsage
	}

	path := strings.TrimSpace(opts.dbPath)
	if path == "" {
		path = usageanalytics.DefaultDBPath()
	}
	path = filepath.Clean(path)

	store, err := usageanalytics.Open(usageanalytics.Config{Path: path})
	if err != nil {
		fmt.Fprintf(errOut, "打开分析库失败（%s）：%v\n", path, err)
		return statsExitDeterministic
	}
	defer func() { _ = store.Close() }()
	if !store.StatsSchemaReady() {
		fmt.Fprintf(errOut, "分析库未启用预聚合统计（schema 版本不足）：%s\n", path)
		return statsExitDeterministic
	}

	stats, err := store.PruneDetailBefore(contextOrDefault(opts.ctx), usageanalytics.PruneOptions{
		Cutoff:    cutoff,
		BatchSize: 2000,
	})
	if err != nil {
		fmt.Fprintf(errOut, "清理历史请求失败：%v\n", err)
		return statsExitDeterministic
	}

	if opts.json {
		payload := map[string]interface{}{
			"db_path":          path,
			"before":           cutoff.Format(time.RFC3339),
			"deleted_requests": stats.Requests,
			"deleted_details":  stats,
		}
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(errOut, "序列化输出失败：%v\n", err)
			return statsExitDeterministic
		}
		fmt.Fprintln(out, string(encoded))
		return statsExitOK
	}
	fmt.Fprintf(out, "清理完成：%s（请求 %d，工具 %d，回合 %d，子代理 %d，路由 %d，LSP %d，围栏 %d；before=%s）\n",
		path, stats.Requests, stats.ToolCalls, stats.Turns, stats.Subagents,
		stats.Routes, stats.LSPRequests, stats.RenderFenceDrops,
		cutoff.Format(time.RFC3339))
	return statsExitOK
}

func contextOrDefault(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

type usageAnalyticsCompactOptions struct {
	dbPath    string
	maxBytes  int
	batchSize int
	dryRun    bool
	ctx       context.Context
	json      bool
	out       io.Writer
	errOut    io.Writer
}

func newUsageAnalyticsCompactRecordsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compact-records",
		Short: "批量瘦身历史工具 record_json 大载荷",
		Long: `将 usage_tool_calls 中超过阈值的历史 record_json 改写为分析所需字段，
丢弃 output/render_output/protocol_result 等大载荷；保留 LSP 基线与诊断字段。

命令按 rowid 单向前进、分批提交，不会重复扫描已处理行。建议在低活跃时段运行；
删除/改写产生的空闲页由后续写入复用，缩小操作系统文件仍需退出进程后离线执行：
  aicli storage compact --target analytics --require-exclusive

示例：
  aicli usage-analytics compact-records --dry-run
  aicli usage-analytics compact-records --max-bytes 1024 --batch-size 200 --json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			statsExit(runUsageAnalyticsCompactRecords(usageAnalyticsCompactOptions{
				dbPath:    stringFlag(cmd, "db"),
				maxBytes:  intFlag(cmd, "max-bytes"),
				batchSize: intFlag(cmd, "batch-size"),
				dryRun:    boolFlag(cmd, "dry-run"),
				json:      boolFlag(cmd, "json"),
				out:       cmd.OutOrStdout(),
				errOut:    cmd.ErrOrStderr(),
			}))
		},
	}
	cmd.Flags().Int("max-bytes", 1024, "仅改写大于该字节数的 record_json")
	cmd.Flags().Int("batch-size", 200, "每批提交的行数")
	cmd.Flags().Bool("dry-run", false, "只统计可瘦身数据，不写库")
	cmd.Flags().Bool("json", false, "以 JSON 输出结果")
	return cmd
}

func runUsageAnalyticsCompactRecords(opts usageAnalyticsCompactOptions) int {
	out := opts.out
	if out == nil {
		out = io.Discard
	}
	errOut := opts.errOut
	if errOut == nil {
		errOut = io.Discard
	}
	if opts.maxBytes <= 0 || opts.batchSize <= 0 {
		fmt.Fprintln(errOut, "参数错误：--max-bytes 与 --batch-size 必须大于 0")
		return statsExitUsage
	}
	path := strings.TrimSpace(opts.dbPath)
	if path == "" {
		path = usageanalytics.DefaultDBPath()
	}
	path = filepath.Clean(path)
	store, err := usageanalytics.Open(usageanalytics.Config{Path: path, ReadOnly: opts.dryRun})
	if err != nil {
		fmt.Fprintf(errOut, "打开分析库失败（%s）：%v\n", path, err)
		return statsExitDeterministic
	}
	defer func() { _ = store.Close() }()
	if store.Empty() {
		fmt.Fprintf(errOut, "分析库不存在或为空：%s\n", path)
		return statsExitDeterministic
	}

	stats, err := store.CompactToolRecords(contextOrDefault(opts.ctx), usageanalytics.ToolRecordCompactOptions{
		MaxBytes:  opts.maxBytes,
		BatchSize: opts.batchSize,
		DryRun:    opts.dryRun,
	})
	if err != nil {
		fmt.Fprintf(errOut, "瘦身工具记录失败：%v\n", err)
		return statsExitDeterministic
	}
	if opts.json {
		payload := map[string]interface{}{
			"db_path": path,
			"stats":   stats,
		}
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(errOut, "序列化输出失败：%v\n", err)
			return statsExitDeterministic
		}
		fmt.Fprintln(out, string(encoded))
		return statsExitOK
	}
	mode := "瘦身完成"
	if opts.dryRun {
		mode = "预检完成（未写库）"
	}
	fmt.Fprintf(out, "%s：%s（扫描 %d，改写 %d，跳过 %d，%d -> %d bytes）\n",
		mode, path, stats.Scanned, stats.Rewritten, stats.Skipped,
		stats.BytesBefore, stats.BytesAfter)
	return statsExitOK
}
