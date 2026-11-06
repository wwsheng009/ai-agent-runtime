package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionpurge"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

const storagePruneSessionsDefaultTimeout = 30 * time.Minute

type storagePruneSessionsOptions struct {
	out              io.Writer
	errOut           io.Writer
	json             bool
	apply            bool
	fast             bool
	profile          bool
	compactAfter     bool
	compactForce     bool
	skipIntegrity    bool
	includeOrphans   bool
	includeTeam      bool
	requireExclusive bool
	olderThan        time.Duration
	timeout          time.Duration
	paths            *sessionpurge.Paths
}

// newStoragePruneSessionsCommand 创建 `aicli storage prune-sessions`。
func newStoragePruneSessionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune-sessions",
		Short: "按最后活动时间清理会话及其关联数据（默认 dry-run）",
		Long: `按会话最后活动时间，跨 history/runtime/analytics/artifacts/agent-control/
background/schedule 清理过期会话。默认只预演，必须显式加 --apply 才删除。

已经验证过删除语义且不需要预演时，可以直接执行：
  aicli storage prune-sessions --older-than 7d --apply --fast --compact-after --json
--fast 不做 orphan 全表扫描和逐表预计行数统计；--compact-after 在删除成功后
紧接着 VACUUM 回收空间，避免为同一批数据再次启动命令并重新扫描。

保护规则（候选会话满足任一即跳过）：
  - runtime 状态 running / waiting_approval / waiting_input / rewinding
  - 未过期的 session_actor_leases
  - 未终结的 background job
  - status=active 的 agent-control agent（并保护其父/根会话）
  - active/paused 的 schedule task（并保护创建者会话）
  - 可选：active team（--include-team 时）

默认清理范围不包含 team store；使用 --include-team 显式纳入。
删除完成后，建议运行 aicli storage compact --target all --require-exclusive 回收文件页。`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			olderThan, err := parsePruneSessionsAge(stringFlag(cmd, "older-than"))
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "参数错误：%v\n", err)
				statsExit(statsExitUsage)
				return
			}
			statsExit(runStoragePruneSessions(storagePruneSessionsOptions{
				out:              cmd.OutOrStdout(),
				errOut:           cmd.ErrOrStderr(),
				json:             boolFlag(cmd, "json"),
				apply:            boolFlag(cmd, "apply"),
				fast:             boolFlag(cmd, "fast"),
				profile:          boolFlag(cmd, "profile"),
				compactAfter:     boolFlag(cmd, "compact-after"),
				compactForce:     boolFlag(cmd, "compact-force"),
				skipIntegrity:    boolFlag(cmd, "compact-skip-integrity-check"),
				includeOrphans:   boolFlag(cmd, "include-orphans"),
				includeTeam:      boolFlag(cmd, "include-team"),
				requireExclusive: boolFlag(cmd, "require-exclusive"),
				olderThan:        olderThan,
				timeout:          time.Duration(intFlag(cmd, "timeout")) * time.Second,
			}))
		},
	}
	cmd.Flags().String("older-than", "30d", "最后活动时间阈值，例如 30d、2w、72h")
	cmd.Flags().Bool("apply", false, "实际删除；缺省只 dry-run")
	cmd.Flags().Bool("fast", false, "跳过 orphan 全表扫描和逐表预计行数统计；需与 --apply 同用")
	cmd.Flags().Bool("profile", false, "输出候选/保护/各库阶段耗时")
	cmd.Flags().Bool("compact-after", false, "apply 成功后接着执行离线 VACUUM；无需再次预演")
	cmd.Flags().Bool("compact-force", false, "VACUUM 时即使 freelist=0 也重建；仅 compact-after 生效")
	cmd.Flags().Bool("compact-skip-integrity-check", false, "VACUUM 时跳过 quick_check；仅 compact-after 生效")
	cmd.Flags().Bool("include-orphans", true, "包含 history 已不存在、但其它库仍有的 orphan 会话")
	cmd.Flags().Bool("include-team", false, "纳入 team store（teams/teammates 及团队历史）")
	cmd.Flags().Bool("require-exclusive", false, "Windows 句柄级独占探测；任一库被其它进程打开时立即失败")
	cmd.Flags().Int("timeout", int(storagePruneSessionsDefaultTimeout/time.Second), "总超时（秒）")
	cmd.Flags().Bool("json", false, "以 JSON 输出")
	return cmd
}

func parsePruneSessionsAge(raw string) (time.Duration, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return 30 * 24 * time.Hour, nil
	}
	multiplier := time.Duration(0)
	number := value
	switch {
	case strings.HasSuffix(value, "d"):
		multiplier = 24 * time.Hour
		number = strings.TrimSuffix(value, "d")
	case strings.HasSuffix(value, "w"):
		multiplier = 7 * 24 * time.Hour
		number = strings.TrimSuffix(value, "w")
	case strings.HasSuffix(value, "h"), strings.HasSuffix(value, "m"), strings.HasSuffix(value, "s"):
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("invalid --older-than %q", raw)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("invalid --older-than %q (use e.g. 30d, 2w, 72h)", raw)
	}
	amount, err := strconv.ParseFloat(number, 64)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("invalid --older-than %q", raw)
	}
	return time.Duration(amount * float64(multiplier)), nil
}

func runStoragePruneSessions(opts storagePruneSessionsOptions) int {
	if opts.out == nil {
		opts.out = os.Stdout
	}
	if opts.errOut == nil {
		opts.errOut = os.Stderr
	}
	if opts.olderThan <= 0 {
		opts.olderThan = 30 * 24 * time.Hour
	}
	if opts.timeout <= 0 {
		opts.timeout = storagePruneSessionsDefaultTimeout
	}
	if opts.fast && !opts.apply {
		fmt.Fprintln(opts.errOut, "参数错误：--fast 只用于直接执行的 --apply；预演请去掉 --fast。")
		return statsExitUsage
	}
	if opts.compactAfter && !opts.apply {
		fmt.Fprintln(opts.errOut, "参数错误：--compact-after 只在 --apply 成功后执行 VACUUM。")
		return statsExitUsage
	}
	paths := sessionpurge.Paths{}
	if opts.paths != nil {
		paths = *opts.paths
	} else {
		resolved, err := resolvePruneSessionsPaths()
		if err != nil {
			fmt.Fprintf(opts.errOut, "参数错误：%v\n", err)
			return statsExitUsage
		}
		paths = resolved
	}
	if opts.requireExclusive {
		for _, path := range []string{paths.History, paths.Cells, paths.Runtime, paths.UsageAnalytics, paths.Artifacts, paths.AgentControl, paths.Background, paths.Schedule, paths.Team} {
			if strings.TrimSpace(path) == "" {
				continue
			}
			if _, err := os.Stat(path); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				fmt.Fprintf(opts.errOut, "读取库文件失败：%v\n", err)
				return statsExitDeterministic
			}
			if err := storageExclusiveProbe(path); err != nil {
				fmt.Fprintf(opts.errOut, "库仍被占用或无法探测：%s：%v\n", path, err)
				return statsExitDeterministic
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	report, err := sessionpurge.Run(ctx, paths, sessionpurge.Options{
		OlderThan:      opts.olderThan,
		IncludeOrphans: opts.includeOrphans,
		IncludeTeam:    opts.includeTeam,
		DryRun:         !opts.apply,
		FastCandidates: opts.fast,
		Profile:        opts.profile,
	})
	var compactExit int
	if opts.json {
		encoder := json.NewEncoder(opts.out)
		encoder.SetIndent("", "  ")
		var payload interface{} = report
		if opts.compactAfter {
			compactReports, exitCode := runStorageCompactCaptured(storageCompactOptions{
				out:              io.Discard,
				errOut:           opts.errOut,
				json:             true,
				target:           "all",
				force:            opts.compactForce,
				requireExclusive: opts.requireExclusive,
				skipIntegrity:    opts.skipIntegrity,
				busyTimeout:      storageCompactDefaultBusyTimeout,
				timeout:          opts.timeout,
			})
			compactExit = exitCode
			payload = struct {
				Prune       sessionpurge.Report `json:"prune"`
				Compact     interface{}         `json:"compact"`
				CompactExit int                 `json:"compact_exit"`
			}{
				Prune: report,
				Compact: map[string]interface{}{
					"reports": compactReports,
					"summary": storageCompactSummary(compactReports),
				},
				CompactExit: compactExit,
			}
		}
		if encodeErr := encoder.Encode(payload); encodeErr != nil {
			fmt.Fprintf(opts.errOut, "编码 JSON 失败：%v\n", encodeErr)
			return statsExitDeterministic
		}
	} else {
		renderPruneSessionsReport(opts.out, report)
	}
	if err != nil {
		fmt.Fprintf(opts.errOut, "清理失败：%v\n", err)
		return statsExitDeterministic
	}
	if opts.compactAfter && !opts.json {
		compactExit := runStorageCompact(storageCompactOptions{
			out:              opts.out,
			errOut:           opts.errOut,
			json:             opts.json,
			target:           "all",
			force:            opts.compactForce,
			requireExclusive: opts.requireExclusive,
			skipIntegrity:    opts.skipIntegrity,
			busyTimeout:      storageCompactDefaultBusyTimeout,
			timeout:          opts.timeout,
		})
		if compactExit != statsExitOK {
			fmt.Fprintln(opts.errOut, "数据已清理，但压缩阶段失败；修复占用/完整性后可单独执行 storage compact。")
			return compactExit
		}
	}
	if !opts.apply && !opts.json {
		fmt.Fprintln(opts.out, "dry-run：未修改任何数据；确认后加 --apply 执行。")
	}
	if opts.compactAfter && opts.json && compactExit != statsExitOK {
		fmt.Fprintln(opts.errOut, "数据已清理，但压缩阶段失败；修复占用/完整性后可单独执行 storage compact。")
		return compactExit
	}
	return statsExitOK
}

func resolvePruneSessionsPaths() (sessionpurge.Paths, error) {
	config, configFile := storageLoadRuntimeConfig()
	resolved := sessionruntime.ResolvePaths(sessionruntime.ResolveOptions{
		Config:     config,
		ConfigFile: configFile,
		Mode:       sessionruntime.ModeCLILocal,
	})
	paths := sessionpurge.Paths{
		History:        storageHistoryStorePath(config, configFile, resolved),
		Cells:          filepath.Join(resolved.SessionDir, "session_cells.sqlite"),
		Runtime:        resolved.SessionRuntimeStorePath,
		UsageAnalytics: usageanalytics.DefaultDBPath(),
		Artifacts:      resolved.ArtifactStorePath,
		AgentControl:   resolved.AgentControlStorePath,
		Background:     resolved.BackgroundStorePath,
		Team:           resolved.TeamStorePath,
	}
	paths.Schedule = resolvePruneSessionsSchedulePath(config, configFile)
	return paths, nil
}

func resolvePruneSessionsSchedulePath(config *runtimecfg.RuntimeConfig, configFile string) string {
	if config != nil {
		if path := strings.TrimSpace(config.Schedule.StorePath); path != "" {
			if filepath.IsAbs(path) {
				return path
			}
			if strings.TrimSpace(configFile) != "" {
				return filepath.Join(filepath.Dir(configFile), path)
			}
			if abs, err := filepath.Abs(path); err == nil {
				return abs
			}
		}
	}
	base := filepath.Dir(agentconfig.DefaultAuthStorePath())
	if base == "" || base == "." {
		return ""
	}
	return filepath.Join(base, "data", "schedule", "schedule.sqlite")
}

func renderPruneSessionsReport(out io.Writer, report sessionpurge.Report) {
	mode := "DRY-RUN"
	if report.Status == "pruned" || report.Status == "partial" {
		mode = "APPLY"
	}
	fmt.Fprintf(out, "[%s] cutoff=%s older_than=%s\n", mode, report.Cutoff.Format(time.RFC3339), report.OlderThan)
	fmt.Fprintf(out, "history_sessions=%d known_candidates=%d orphan_candidates=%d protected=%d candidates=%d\n",
		report.HistorySessions, report.KnownCandidates, report.OrphanCandidates,
		report.ProtectedCandidates, report.CandidateCount)
	if len(report.CandidateSamples) > 0 {
		fmt.Fprintf(out, "candidate_sample=%s\n", strings.Join(report.CandidateSamples, ","))
	}
	for _, protected := range report.ProtectedSamples {
		fmt.Fprintf(out, "  protected %s reasons=%s\n", protected.SessionID, strings.Join(protected.Reasons, ","))
	}
	for _, store := range report.Stores {
		fmt.Fprintf(out, "store=%s path=%s status=%s", store.Name, store.Path, store.Status)
		if store.Error != "" {
			fmt.Fprintf(out, " error=%s", store.Error)
		}
		if store.ArtifactDirs > 0 {
			fmt.Fprintf(out, " artifact_dirs=%d", store.ArtifactDirs)
		}
		fmt.Fprintln(out)
		for _, table := range store.Tables {
			if table.Rows == 0 {
				continue
			}
			fmt.Fprintf(out, "  %-42s rows=%d", table.Table, table.Rows)
			if table.Detail != "" {
				fmt.Fprintf(out, " (%s)", table.Detail)
			}
			fmt.Fprintln(out)
		}
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(out, "warning: %s\n", warning)
	}
	for name, elapsed := range report.TimingsMS {
		fmt.Fprintf(out, "timing: %-42s %dms\n", name, elapsed)
	}
	fmt.Fprintf(out, "duration=%s\n", time.Duration(report.DurationMS)*time.Millisecond)
}
