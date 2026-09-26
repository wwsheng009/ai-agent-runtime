package commands

import (
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
	vacuum bool
	json   bool
	out    io.Writer
	errOut io.Writer
}

func newUsageAnalyticsPruneCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "按保留期清理历史请求明细并重建统计",
		Long: `删除 --before 之前的 usage_requests 明细与不再引用的 turn 去重键，
并重建全库预聚合统计；会话元数据保留（列表仍可见历史会话）。

--before 按请求 started_at 比较（started_at=0 的行不清理）。
--vacuum 在清理后执行 VACUUM 回收文件空间（大库耗时较长，默认关闭）。

示例：
  aicli usage-analytics prune --before 2026-01-01
  aicli usage-analytics prune --before 2026-01-01T00:00:00+08:00 --vacuum --json`,
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
	cmd.Flags().Bool("vacuum", false, "清理后执行 VACUUM 回收文件空间")
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

	deleted, err := store.PruneBefore(cutoff)
	if err != nil {
		fmt.Fprintf(errOut, "清理历史请求失败：%v\n", err)
		return statsExitDeterministic
	}
	vacuumed := false
	if opts.vacuum {
		if err := store.Vacuum(); err != nil {
			fmt.Fprintf(errOut, "VACUUM 失败：%v\n", err)
			return statsExitDeterministic
		}
		vacuumed = true
	}

	if opts.json {
		payload := map[string]interface{}{
			"db_path":          path,
			"before":           cutoff.Format(time.RFC3339),
			"deleted_requests": deleted,
			"vacuumed":         vacuumed,
		}
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(errOut, "序列化输出失败：%v\n", err)
			return statsExitDeterministic
		}
		fmt.Fprintln(out, string(encoded))
		return statsExitOK
	}
	fmt.Fprintf(out, "清理完成：%s（删除 %d 条请求明细，before=%s，vacuum=%v）\n",
		path, deleted, cutoff.Format(time.RFC3339), vacuumed)
	return statsExitOK
}
