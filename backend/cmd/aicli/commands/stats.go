package commands

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// aicli stats 退出码约定（详见 docs/plan/session-analytics-runbook.md）：
//
//	0 = 查询成功（含空库/表缺失的降级输出「暂无数据」）
//	1 = 参数错误（非法 limit/top、缺少会话 ID、多余参数）
//	2 = 确定性错误（分析库损坏/不可读、路径是目录等）
//
// 采集进程侧的瞬时故障（写锁竞争、bus 未 attach）不在此列：只读查询要么
// 拿到数据、要么走 0 降级、要么命中 2。
const (
	statsExitOK            = 0
	statsExitUsage         = 1
	statsExitDeterministic = 2
)

// 查询参数边界（与 usageanalytics 内部上限对齐：maxListLimit=200、
// maxErrorPatternRows=50、maxSubagentStatsRows=200）。
const (
	statsDefaultSessionsLimit  = 20
	statsMaxSessionsLimit      = 200
	statsDefaultErrorsTop      = 10
	statsMaxErrorsTop          = 50
	statsDefaultSubagentsLimit = 50
	statsMaxSubagentsLimit     = 200
)

// statsExitHook 是退出钩子：生产路径为 os.Exit，测试注入以断言退出码。
var statsExitHook = os.Exit

func statsExit(code int) {
	if code == statsExitOK {
		return
	}
	runExitCleanup()
	statsExitHook(code)
}

// statsOutput 是子命令的统一输出出口（JSON 稳定性不受终端/主题影响）。
type statsOutput struct {
	json   bool
	out    io.Writer
	errOut io.Writer
}

func statsFlagOutput(cmd *cobra.Command) statsOutput {
	return statsOutput{
		json:   boolFlag(cmd, "json"),
		out:    cmd.OutOrStdout(),
		errOut: cmd.ErrOrStderr(),
	}
}

// NewStatsCommand 创建 `aicli stats`：无 runtime-server 进程时只读查看统一
// 用量分析库（usage_analytics.sqlite）的会话/失败/子代理/采集健康视图。
func NewStatsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "只读查看会话用量分析（sessions/session/errors/subagents/doctor）",
		Long: `只读查看 aicli 统一用量分析库（usage_analytics.sqlite）。

子命令：
  stats sessions   列出最近的会话用量汇总
  stats session    查看单个会话明细（轮次/工具/子代理/失败模式）
  stats errors     失败模式 Top-N（工具错误码 / 子代理失败分类 / LLM 错误分类）
  stats subagents  子代理完成情况（失败率、失败分类、重试）
  stats doctor     采集健康自检（分析库/会话库路径、表计数、最近事件时间）

退出码：
  0 = 查询成功（含空库/表缺失，输出「暂无数据」）
  1 = 参数错误
  2 = 库损坏等确定性错误

库路径优先级：--db > AICLI_USAGE_ANALYTICS_DB > 默认
（~/.aicli/sessions/runtime/usage_analytics.sqlite）。
查询一律只读打开，不会创建或修改分析库。

排错文档：docs/plan/session-analytics-runbook.md`,
		Example: `  aicli stats sessions --limit 10
  aicli stats sessions --limit 10 --json
  aicli stats session <session-id>
  aicli stats errors --top 20
  aicli stats subagents --failed-only
  aicli stats doctor --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().String("db", "", "分析库路径（默认取 AICLI_USAGE_ANALYTICS_DB，未设置时为 ~/.aicli/sessions/runtime/usage_analytics.sqlite）")
	cmd.PersistentFlags().Bool("json", false, "以 JSON 格式输出（字段名与 usageanalytics 结构体 JSON tag 一致）")

	cmd.AddCommand(newStatsSessionsCommand())
	cmd.AddCommand(newStatsSessionCommand())
	cmd.AddCommand(newStatsErrorsCommand())
	cmd.AddCommand(newStatsSubagentsCommand())
	cmd.AddCommand(newStatsDoctorCommand())
	return cmd
}

func newStatsSessionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "列出最近的会话用量汇总",
		Long: `列出分析库中最近的会话用量汇总（按最近活动时间倒序）。

字段来自 usage_requests/usage_sessions 聚合，并附带 schema v2 的工具与
子代理失败计数。空库或表缺失时输出「暂无数据」，退出码 0。

示例：
  aicli stats sessions --limit 10
  aicli stats sessions --limit 10 --json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			statsExit(runStatsSessions(statsSessionsOptions{
				statsOutput: statsFlagOutput(cmd),
				dbPath:      stringFlag(cmd, "db"),
				limit:       intFlag(cmd, "limit"),
			}))
		},
	}
	cmd.Flags().Int("limit", statsDefaultSessionsLimit, fmt.Sprintf("返回会话条数（1..%d）", statsMaxSessionsLimit))
	return cmd
}

func newStatsSessionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "session <session-id>",
		Short: "查看单个会话明细",
		Long: `查看单个会话的用量明细：会话汇总、轮次、工具、子代理、失败模式与诊断。

会话在分析库中不存在（或库为空）时输出「暂无数据」，退出码 0。

示例：
  aicli stats session 0f3c... --json`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			statsExit(runStatsSession(statsSessionOptions{
				statsOutput: statsFlagOutput(cmd),
				dbPath:      stringFlag(cmd, "db"),
				sessionID:   args[0],
			}))
		},
	}
}

func newStatsErrorsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "errors",
		Short: "失败模式 Top-N",
		Long: `失败模式 Top-N：汇总工具错误码、子代理失败分类与 LLM 请求错误分类。

示例：
  aicli stats errors --top 20
  aicli stats errors --top 20 --json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			statsExit(runStatsErrors(statsErrorsOptions{
				statsOutput: statsFlagOutput(cmd),
				dbPath:      stringFlag(cmd, "db"),
				top:         intFlag(cmd, "top"),
			}))
		},
	}
	cmd.Flags().Int("top", statsDefaultErrorsTop, fmt.Sprintf("返回失败模式条数（1..%d）", statsMaxErrorsTop))
	return cmd
}

func newStatsSubagentsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subagents",
		Short: "子代理完成情况",
		Long: `子代理完成情况：总数、成功/失败/未知、失败率、失败分类、来源与重试。

示例：
  aicli stats subagents
  aicli stats subagents --failed-only --json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			statsExit(runStatsSubagents(statsSubagentsOptions{
				statsOutput: statsFlagOutput(cmd),
				dbPath:      stringFlag(cmd, "db"),
				limit:       intFlag(cmd, "limit"),
				failedOnly:  boolFlag(cmd, "failed-only"),
			}))
		},
	}
	cmd.Flags().Bool("failed-only", false, "只显示失败的子代理")
	cmd.Flags().Int("limit", statsDefaultSubagentsLimit, fmt.Sprintf("返回子代理条数（1..%d）", statsMaxSubagentsLimit))
	return cmd
}

func newStatsDoctorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "采集健康自检（库路径/表计数/最近事件时间）",
		Long: `采集健康自检：分析库与会话库的路径、存在性、可读性、表计数、
最近事件时间，并给出方案 §0.3 已知断点（G1/G2/G3）的提示。

等价基线脚本（方案附录 A.1）的轻量版：只读打开，不写入任何库。

示例：
  aicli stats doctor
  aicli stats doctor --json
  aicli stats doctor --session-db ~/.aicli/sessions/runtime/session_runtime.sqlite`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			statsExit(runStatsDoctor(statsDoctorOptions{
				statsOutput: statsFlagOutput(cmd),
				dbPath:      stringFlag(cmd, "db"),
				sessionDB:   stringFlag(cmd, "session-db"),
			}))
		},
	}
	cmd.Flags().String("session-db", "", "会话库路径（默认 ~/.aicli/sessions/runtime/session_runtime.sqlite）")
	return cmd
}

// ============================================================================
// 只读打开与错误分级
// ============================================================================

// statsServiceHandle 一次只读分析库会话：service 提供 usageanalytics 查询
// 契约，path/exists/size 供降级与 doctor 输出使用。
type statsServiceHandle struct {
	service *usageanalytics.Service
	path    string
	exists  bool
	size    int64
}

func (h *statsServiceHandle) Close() {
	if h == nil || h.service == nil {
		return
	}
	h.service.Close()
}

// openStatsService 以只读方式打开分析库（一律 Open(Config{ReadOnly:true})）。
//
// 语义（方案批次 4）：
//   - 库/表缺失 → 空服务，查询返回空结果而非错误（调用方输出「暂无数据」）；
//   - 文件存在但不可读（损坏/非 SQLite）→ 返回确定性错误（退出码 2）。
func openStatsService(dbPath string) (*statsServiceHandle, error) {
	path := strings.TrimSpace(dbPath)
	if path == "" {
		// DefaultDBPath 已包含 AICLI_USAGE_ANALYTICS_DB 覆盖优先级。
		path = usageanalytics.DefaultDBPath()
	}
	path = filepath.Clean(path)

	handle := &statsServiceHandle{path: path}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("分析库路径是目录而不是文件：%s", path)
		}
		handle.exists = true
		handle.size = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取分析库状态失败（%s）：%w", path, err)
	}

	service, err := usageanalytics.Attach(nil, usageanalytics.Options{
		Config: usageanalytics.Config{Path: path, ReadOnly: true},
	})
	if err != nil {
		return nil, fmt.Errorf("打开分析库失败（%s）：%w", path, err)
	}
	handle.service = service

	// 只读 Open 对"文件存在但打不开"也走空库降级（store.go openReadOnly）。
	// 这里补一次独立探测，把「损坏/不是 SQLite」与「库尚未创建」区分开：
	// 前者是确定性错误（退出码 2），后者是正常降级（退出码 0）。
	if service.Store().Empty() && handle.exists && handle.size > 0 {
		if probeErr := statsProbeSQLite(path); probeErr != nil {
			service.Close()
			handle.service = nil
			return nil, fmt.Errorf("分析库不可读（可能损坏或不是 SQLite 数据库，%s）：%w", path, probeErr)
		}
	}
	return handle, nil
}

// statsProbeSQLite 独立验证文件是可读的 SQLite 库。
func statsProbeSQLite(path string) error {
	db, err := statsOpenReadOnlySQLite(path)
	if err != nil {
		return err
	}
	return db.Close()
}

// statsOpenReadOnlySQLite 建立只读连接并执行一次真实查询。
// 仅 Ping 不足以暴露损坏：sqlite 驱动是惰性打开的。
func statsOpenReadOnlySQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", statsReadOnlyDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	probe := func() error {
		var tables int
		return db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&tables)
	}
	if err := probe(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// statsReadOnlyDSN 与 usageanalytics store.go 的只读 DSN 保持同形。
func statsReadOnlyDSN(path string) string {
	slashed := filepath.ToSlash(path)
	if strings.HasPrefix(slashed, "file:") {
		return slashed
	}
	if !strings.HasPrefix(slashed, "/") && !(len(slashed) >= 3 && slashed[1] == ':') {
		slashed = "./" + slashed
	}
	return "file:" + slashed + "?mode=ro"
}

func statsFailUsage(out statsOutput, format string, args ...interface{}) int {
	fmt.Fprintf(out.errOut, "Error: 参数错误: %s\n", fmt.Sprintf(format, args...))
	return statsExitUsage
}

func statsFailDeterministic(out statsOutput, err error) int {
	fmt.Fprintf(out.errOut, "Error: %v\n", err)
	return statsExitDeterministic
}

func statsWriteJSON(out statsOutput, value interface{}) int {
	encoder := json.NewEncoder(out.out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return statsFailDeterministic(out, fmt.Errorf("编码 JSON 输出失败: %w", err))
	}
	return statsExitOK
}

// ============================================================================
// stats sessions / session / errors / subagents
// ============================================================================

type statsSessionsOptions struct {
	statsOutput
	dbPath string
	limit  int
}

func runStatsSessions(opts statsSessionsOptions) int {
	if opts.limit < 1 || opts.limit > statsMaxSessionsLimit {
		return statsFailUsage(opts.statsOutput, "--limit 必须在 1..%d 之间（当前 %d）", statsMaxSessionsLimit, opts.limit)
	}
	handle, err := openStatsService(opts.dbPath)
	if err != nil {
		return statsFailDeterministic(opts.statsOutput, err)
	}
	defer handle.Close()

	result, err := handle.service.ListSessions(usageanalytics.Query{Limit: opts.limit})
	if err != nil {
		return statsFailDeterministic(opts.statsOutput, fmt.Errorf("查询会话列表失败：%w", err))
	}
	if opts.json {
		return statsWriteJSON(opts.statsOutput, result)
	}
	if len(result.Sessions) == 0 {
		fmt.Fprintf(opts.out, "暂无数据：分析库中没有会话记录（db=%s）\n", handle.path)
		return statsExitOK
	}

	fmt.Fprintf(opts.out, "会话总数: %d，返回 %d 条（--limit 上限 %d）\n",
		result.Total, len(result.Sessions), statsMaxSessionsLimit)
	rows := make([][]string, 0, len(result.Sessions))
	for _, session := range result.Sessions {
		rows = append(rows, []string{
			statsTruncate(session.SessionID, 24),
			statsTruncate(session.Title, 24),
			statsTruncate(session.Model, 20),
			strconv.Itoa(session.TotalRequests),
			strconv.Itoa(session.TotalTokens),
			strconv.Itoa(session.ToolCallsObserved),
			strconv.Itoa(session.ToolFailures),
			strconv.Itoa(session.SubagentRuns),
			strconv.Itoa(session.SubagentFailures),
			statsDashIfEmpty(session.Status),
			statsFormatTime(session.LastObservedAt),
		})
	}
	statsWriteTable(opts.out, []string{
		"SESSION_ID", "TITLE", "MODEL", "REQUESTS", "TOKENS",
		"TOOL_CALLS", "TOOL_FAIL", "SUBAGENTS", "SUBAGENT_FAIL", "STATUS", "LAST_ACTIVE",
	}, rows)
	return statsExitOK
}

type statsSessionOptions struct {
	statsOutput
	dbPath    string
	sessionID string
}

func runStatsSession(opts statsSessionOptions) int {
	sessionID := strings.TrimSpace(opts.sessionID)
	if sessionID == "" {
		return statsFailUsage(opts.statsOutput, "session 需要非空的 <session-id>")
	}
	handle, err := openStatsService(opts.dbPath)
	if err != nil {
		return statsFailDeterministic(opts.statsOutput, err)
	}
	defer handle.Close()

	detail, err := handle.service.SessionUsage(sessionID)
	if err != nil {
		if usageanalytics.IsNotFound(err) {
			// 空库与会话不存在都是"查询成功但没有数据"。JSON 输出与真实
			// 明细同构（空数组而非 null），避免消费端分支。
			if opts.json {
				return statsWriteJSON(opts.statsOutput, statsEmptySessionDetail(sessionID))
			}
			fmt.Fprintf(opts.out, "暂无数据：会话 %s 没有分析记录（db=%s）\n", sessionID, handle.path)
			return statsExitOK
		}
		return statsFailDeterministic(opts.statsOutput, fmt.Errorf("查询会话明细失败：%w", err))
	}
	if opts.json {
		return statsWriteJSON(opts.statsOutput, detail)
	}
	statsRenderSessionDetail(opts.out, detail)
	return statsExitOK
}

// statsEmptySessionDetail 构造"未找到"时的同构空明细（数组非 null）。
func statsEmptySessionDetail(sessionID string) usageanalytics.SessionUsageDetail {
	return usageanalytics.SessionUsageDetail{
		SchemaVersion:   usageanalytics.SchemaVersion,
		GeneratedAt:     time.Now().UTC(),
		Session:         usageanalytics.SessionRollup{SessionID: sessionID},
		Steps:           []usageanalytics.StepUsage{},
		StepCount:       0,
		Turns:           []usageanalytics.TurnUsage{},
		Diagnostics:     []usageanalytics.Diagnostic{},
		ErrorCategories: map[string]int{},
		Tools:           []usageanalytics.ToolStat{},
		Subagents:       []usageanalytics.SubagentStat{},
		ErrorPatterns:   []usageanalytics.ErrorPattern{},
		PartialReasons:  []string{},
	}
}

type statsErrorsOptions struct {
	statsOutput
	dbPath string
	top    int
}

func runStatsErrors(opts statsErrorsOptions) int {
	if opts.top < 1 || opts.top > statsMaxErrorsTop {
		return statsFailUsage(opts.statsOutput, "--top 必须在 1..%d 之间（当前 %d）", statsMaxErrorsTop, opts.top)
	}
	handle, err := openStatsService(opts.dbPath)
	if err != nil {
		return statsFailDeterministic(opts.statsOutput, err)
	}
	defer handle.Close()

	result, err := handle.service.ErrorPatterns(usageanalytics.ErrorPatternsQuery{Top: opts.top})
	if err != nil {
		return statsFailDeterministic(opts.statsOutput, fmt.Errorf("查询失败模式失败：%w", err))
	}
	if opts.json {
		return statsWriteJSON(opts.statsOutput, result)
	}
	if len(result.Patterns) == 0 {
		fmt.Fprintf(opts.out, "暂无数据：没有失败模式记录（db=%s）\n", handle.path)
		return statsExitOK
	}
	rows := make([][]string, 0, len(result.Patterns))
	for index, pattern := range result.Patterns {
		rows = append(rows, []string{
			strconv.Itoa(index + 1),
			statsDashIfEmpty(pattern.Source),
			statsDashIfEmpty(pattern.ErrorCode),
			statsDashIfEmpty(pattern.FailureCategory),
			strconv.Itoa(pattern.Count),
		})
	}
	statsWriteTable(opts.out, []string{"#", "SOURCE", "ERROR_CODE", "FAILURE_CATEGORY", "COUNT"}, rows)
	return statsExitOK
}

type statsSubagentsOptions struct {
	statsOutput
	dbPath     string
	limit      int
	failedOnly bool
}

func runStatsSubagents(opts statsSubagentsOptions) int {
	if opts.limit < 1 || opts.limit > statsMaxSubagentsLimit {
		return statsFailUsage(opts.statsOutput, "--limit 必须在 1..%d 之间（当前 %d）", statsMaxSubagentsLimit, opts.limit)
	}
	handle, err := openStatsService(opts.dbPath)
	if err != nil {
		return statsFailDeterministic(opts.statsOutput, err)
	}
	defer handle.Close()

	result, err := handle.service.SubagentStats(usageanalytics.SubagentStatsQuery{
		FailedOnly: opts.failedOnly,
		Limit:      opts.limit,
	})
	if err != nil {
		return statsFailDeterministic(opts.statsOutput, fmt.Errorf("查询子代理统计失败：%w", err))
	}
	if opts.json {
		return statsWriteJSON(opts.statsOutput, result)
	}
	if len(result.Subagents) == 0 {
		fmt.Fprintf(opts.out, "暂无数据：没有子代理完成记录（db=%s）\n", handle.path)
		return statsExitOK
	}

	summary := result.Summary
	fmt.Fprintf(opts.out, "子代理: total=%d succeeded=%d failed=%d unknown=%d failure_rate=%.1f%% timeouts=%d retried=%d\n",
		summary.Total, summary.Succeeded, summary.Failed, summary.Unknown,
		summary.FailureRate*100, summary.Timeouts, summary.Retried)
	if len(summary.FailureCategories) > 0 {
		fmt.Fprintf(opts.out, "失败分类: %s\n", statsFormatCountMap(summary.FailureCategories))
	}
	if len(summary.Sources) > 0 {
		fmt.Fprintf(opts.out, "来源: %s\n", statsFormatCountMap(summary.Sources))
	}

	rows := make([][]string, 0, len(result.Subagents))
	for _, subagent := range result.Subagents {
		rows = append(rows, []string{
			statsTruncate(subagent.SubagentID, 24),
			statsTruncate(subagent.ParentSessionID, 20),
			statsFormatSuccess(subagent.Success),
			statsDashIfEmpty(subagent.CompletionReason),
			statsDashIfEmpty(subagent.FailureCategory),
			fmt.Sprintf("%d/%d", statsMaxInt(subagent.Attempt, 1), statsMaxInt(subagent.MaxAttempts, 1)),
			statsFormatDuration(subagent.DurationMS),
			statsFormatTime(subagent.CompletedAt),
		})
	}
	statsWriteTable(opts.out, []string{
		"SUBAGENT_ID", "PARENT_SESSION", "SUCCESS", "COMPLETION_REASON",
		"FAILURE_CATEGORY", "ATTEMPT", "DURATION", "COMPLETED_AT",
	}, rows)
	return statsExitOK
}

// statsRenderSessionDetail 输出单会话文本明细（明细数组过大时只报数量）。
func statsRenderSessionDetail(w io.Writer, detail usageanalytics.SessionUsageDetail) {
	session := detail.Session
	fmt.Fprintf(w, "会话: %s\n", statsDashIfEmpty(session.SessionID))
	if strings.TrimSpace(session.Title) != "" {
		fmt.Fprintf(w, "标题: %s\n", session.Title)
	}
	fmt.Fprintf(w, "模型: %s / %s\n", statsDashIfEmpty(session.Provider), statsDashIfEmpty(session.Model))
	fmt.Fprintf(w, "状态: %s\n", statsDashIfEmpty(session.Status))
	fmt.Fprintf(w, "开始: %s    最后活动: %s\n",
		statsFormatTime(session.StartTime), statsFormatTime(session.LastObservedAt))
	fmt.Fprintf(w, "请求: %d（成功 %d / 失败 %d）  令牌: %d  回合: %d（失败 %d）\n",
		session.TotalRequests, session.LLMSuccesses, session.LLMErrors,
		session.TotalTokens, session.TurnCount, session.FailedTurns)
	fmt.Fprintf(w, "工具调用: %d（失败 %d）  子代理: %d（失败 %d）  步骤记录: %d\n",
		session.ToolCallsObserved, session.ToolFailures,
		session.SubagentRuns, session.SubagentFailures, detail.StepCount)

	if len(detail.Turns) > 0 {
		fmt.Fprintf(w, "\n轮次 (%d):\n", len(detail.Turns))
		rows := make([][]string, 0, len(detail.Turns))
		for _, turn := range detail.Turns {
			rows = append(rows, []string{
				statsTruncate(turn.TurnID, 24),
				statsDashIfEmpty(turn.Outcome),
				strconv.Itoa(turn.LLMRequests),
				strconv.Itoa(turn.LLMErrors),
				strconv.Itoa(turn.ToolResultsObserved),
				strconv.Itoa(turn.ToolErrors),
				strconv.Itoa(turn.Usage.TotalTokens),
				statsFormatDuration(turn.DurationMs),
			})
		}
		statsWriteTable(w, []string{
			"TURN_ID", "OUTCOME", "LLM", "LLM_ERR", "TOOL", "TOOL_ERR", "TOKENS", "DURATION",
		}, rows)
	}
	if len(detail.Tools) > 0 {
		fmt.Fprintf(w, "\n工具 (%d):\n", len(detail.Tools))
		rows := make([][]string, 0, len(detail.Tools))
		for _, tool := range detail.Tools {
			rows = append(rows, []string{
				statsTruncate(tool.ToolName, 24),
				strconv.Itoa(tool.Calls),
				strconv.Itoa(tool.Failures),
				statsFormatRate(tool.FailureRate),
				statsFormatDuration(tool.AverageDuration),
				statsFormatDuration(tool.P95DurationMS),
				strconv.Itoa(tool.RetriedCalls),
			})
		}
		statsWriteTable(w, []string{
			"TOOL_NAME", "CALLS", "FAILURES", "FAILURE_RATE", "AVG", "P95", "RETRIED",
		}, rows)
	}
	if len(detail.Subagents) > 0 {
		fmt.Fprintf(w, "\n子代理 (%d):\n", len(detail.Subagents))
		rows := make([][]string, 0, len(detail.Subagents))
		for _, subagent := range detail.Subagents {
			rows = append(rows, []string{
				statsTruncate(subagent.SubagentID, 24),
				statsFormatSuccess(subagent.Success),
				statsDashIfEmpty(subagent.CompletionReason),
				statsDashIfEmpty(subagent.FailureCategory),
				fmt.Sprintf("%d/%d", statsMaxInt(subagent.Attempt, 1), statsMaxInt(subagent.MaxAttempts, 1)),
				statsFormatDuration(subagent.DurationMS),
			})
		}
		statsWriteTable(w, []string{
			"SUBAGENT_ID", "SUCCESS", "COMPLETION_REASON", "FAILURE_CATEGORY", "ATTEMPT", "DURATION",
		}, rows)
	}
	if len(detail.ErrorPatterns) > 0 {
		fmt.Fprintf(w, "\n失败模式 (%d):\n", len(detail.ErrorPatterns))
		rows := make([][]string, 0, len(detail.ErrorPatterns))
		for _, pattern := range detail.ErrorPatterns {
			rows = append(rows, []string{
				statsDashIfEmpty(pattern.Source),
				statsDashIfEmpty(pattern.ErrorCode),
				statsDashIfEmpty(pattern.FailureCategory),
				strconv.Itoa(pattern.Count),
			})
		}
		statsWriteTable(w, []string{"SOURCE", "ERROR_CODE", "FAILURE_CATEGORY", "COUNT"}, rows)
	}
	if len(detail.Diagnostics) > 0 {
		fmt.Fprintf(w, "\n诊断 (%d):\n", len(detail.Diagnostics))
		rows := make([][]string, 0, len(detail.Diagnostics))
		for _, diagnostic := range detail.Diagnostics {
			rows = append(rows, []string{
				statsDashIfEmpty(diagnostic.Code),
				statsDashIfEmpty(diagnostic.Severity),
				strconv.Itoa(diagnostic.Count),
				statsFormatRate(diagnostic.Rate),
			})
		}
		statsWriteTable(w, []string{"CODE", "SEVERITY", "COUNT", "RATE"}, rows)
	}
}

// ============================================================================
// stats doctor：采集健康自检
// ============================================================================

// statsDoctorReport 是 doctor 的 JSON 输出（字段名即 runbook 约定的诊断字段）。
type statsDoctorReport struct {
	GeneratedAt time.Time           `json:"generated_at"`
	AnalyticsDB statsDoctorDatabase `json:"analytics_db"`
	SessionDB   statsDoctorDatabase `json:"session_db"`
	Warnings    []string            `json:"warnings"`
}

// statsDoctorDatabase 是单个库的自检结果。
type statsDoctorDatabase struct {
	Path        string            `json:"path"`
	Exists      bool              `json:"exists"`
	Readable    bool              `json:"readable"`
	SizeBytes   int64             `json:"size_bytes"`
	Tables      []statsTableCount `json:"tables"`
	EventTypes  []statsEventCount `json:"event_types,omitempty"`
	LastEventAt string            `json:"last_event_at,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// statsTableCount 是一张候选表的行数；缺失表以 exists=false 显式表达。
type statsTableCount struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
	Count  int64  `json:"count"`
}

// statsEventCount 是会话事件类型的行数（等价基线脚本的类型分布）。
type statsEventCount struct {
	Type  string `json:"type"`
	Count int64  `json:"count"`
}

// statsDBInspectionSpec 描述一个库要检查的候选表与最近事件。
type statsDBInspectionSpec struct {
	tables        []string
	lastEventSQL  string // 单值 SELECT，可为空
	lastEventNano bool   // true=UnixNano 整数，false=文本时间
	eventTypeSQL  string // 可为空；返回 (type, count) 两列
}

var statsAnalyticsDBSpec = statsDBInspectionSpec{
	tables: []string{
		"usage_requests", "usage_sessions", "usage_tool_calls", "usage_subagents", "usage_turns",
		// 旧 TypeScript 时代死表（方案 §0.2/§0.4）：保留展示以便识别历史库。
		"analytics_llm_requests", "analytics_sessions", "analytics_turns",
	},
	lastEventSQL:  `SELECT MAX(started_at_unix_nano) FROM usage_requests`,
	lastEventNano: true,
}

var statsSessionDBSpec = statsDBInspectionSpec{
	tables:        []string{"session_events", "session_tool_receipts", "cache_requests"},
	lastEventSQL:  `SELECT MAX(created_at) FROM session_events`,
	lastEventNano: false,
	eventTypeSQL: `SELECT type, COUNT(*) FROM session_events
WHERE type LIKE 'tool.%' OR type = 'subagent.completed'
GROUP BY type ORDER BY COUNT(*) DESC, type ASC`,
}

type statsDoctorOptions struct {
	statsOutput
	dbPath    string
	sessionDB string
}

func runStatsDoctor(opts statsDoctorOptions) int {
	analyticsPath := strings.TrimSpace(opts.dbPath)
	if analyticsPath == "" {
		analyticsPath = usageanalytics.DefaultDBPath()
	}
	sessionPath := strings.TrimSpace(opts.sessionDB)
	if sessionPath == "" {
		sessionPath = statsDefaultSessionRuntimeDBPath()
	}

	report := statsDoctorReport{
		GeneratedAt: time.Now().UTC(),
		AnalyticsDB: statsInspectDatabase(analyticsPath, statsAnalyticsDBSpec),
		SessionDB:   statsInspectDatabase(sessionPath, statsSessionDBSpec),
		Warnings:    []string{},
	}
	report.Warnings = statsDoctorWarnings(report)

	if opts.json {
		code := statsWriteJSON(opts.statsOutput, report)
		if code != statsExitOK {
			return code
		}
	} else {
		statsRenderDoctor(opts.out, report)
	}
	// 文件存在但不可读 = 确定性错误（写 runbook 用退出码 2）。
	if statsDatabaseUnreadable(report.AnalyticsDB) || statsDatabaseUnreadable(report.SessionDB) {
		return statsExitDeterministic
	}
	return statsExitOK
}

// statsDefaultSessionRuntimeDBPath 解析 CLI-local 模式的会话库默认路径。
func statsDefaultSessionRuntimeDBPath() string {
	paths := sessionruntime.ResolvePaths(sessionruntime.ResolveOptions{
		Mode: sessionruntime.ModeCLILocal,
	})
	return paths.SessionRuntimeStorePath
}

func statsDatabaseUnreadable(db statsDoctorDatabase) bool {
	return db.Exists && !db.Readable
}

// statsInspectDatabase 只读检查一个 SQLite 库：候选表行数、事件类型分布与
// 最近事件时间。文件缺失是正常降级（exists=false），不算错误。
func statsInspectDatabase(path string, spec statsDBInspectionSpec) statsDoctorDatabase {
	out := statsDoctorDatabase{Path: path, Tables: []statsTableCount{}}
	info, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			out.Error = err.Error()
		}
		return out
	}
	out.Exists = true
	if info.IsDir() {
		out.Error = "路径是目录而不是 SQLite 文件"
		return out
	}
	out.SizeBytes = info.Size()

	db, err := statsOpenReadOnlySQLite(path)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer func() { _ = db.Close() }()
	out.Readable = true

	existing := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		out.Readable = false
		out.Error = err.Error()
		return out
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			out.Readable = false
			out.Error = err.Error()
			return out
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		out.Readable = false
		out.Error = err.Error()
		return out
	}
	rows.Close()

	for _, name := range spec.tables {
		entry := statsTableCount{Name: name}
		if existing[name] {
			entry.Exists = true
			if count, err := statsTableRowCount(db, name); err == nil {
				entry.Count = count
			} else {
				entry.Exists = false
				entry.Count = -1
			}
		}
		out.Tables = append(out.Tables, entry)
	}

	if spec.lastEventSQL != "" && statsSpecTablePresent(spec, existing) {
		if spec.lastEventNano {
			var nano sql.NullInt64
			if err := db.QueryRow(spec.lastEventSQL).Scan(&nano); err == nil && nano.Valid && nano.Int64 > 0 {
				out.LastEventAt = time.Unix(0, nano.Int64).UTC().Format(time.RFC3339)
			}
		} else {
			var text sql.NullString
			if err := db.QueryRow(spec.lastEventSQL).Scan(&text); err == nil && text.Valid {
				out.LastEventAt = strings.TrimSpace(text.String)
			}
		}
	}

	if spec.eventTypeSQL != "" {
		eventRows, err := db.Query(spec.eventTypeSQL)
		if err == nil {
			for eventRows.Next() {
				var (
					eventType string
					count     int64
				)
				if scanErr := eventRows.Scan(&eventType, &count); scanErr != nil {
					break
				}
				out.EventTypes = append(out.EventTypes, statsEventCount{Type: eventType, Count: count})
			}
			eventRows.Close()
		}
	}
	return out
}

// statsSpecTablePresent 判断 lastEventSQL 引用的表是否存在（按候选表名匹配）。
func statsSpecTablePresent(spec statsDBInspectionSpec, existing map[string]bool) bool {
	for _, name := range spec.tables {
		if !existing[name] {
			continue
		}
		if strings.Contains(spec.lastEventSQL, name) {
			return true
		}
	}
	return false
}

func statsTableRowCount(db *sql.DB, name string) (int64, error) {
	var count int64
	query := fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, strings.ReplaceAll(name, `"`, `""`))
	if err := db.QueryRow(query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// statsDoctorWarnings 依据方案 §0.3 已知断点给出提示（只提示，不改数据）。
func statsDoctorWarnings(report statsDoctorReport) []string {
	warnings := make([]string, 0, 6)
	analytics := report.AnalyticsDB
	switch {
	case !analytics.Exists:
		warnings = append(warnings, "分析库不存在：完成一次真实会话后由采集链路创建；持续不存在说明 attach 未生效（G1）")
	case !analytics.Readable:
		warnings = append(warnings, "分析库存在但不可读（可能损坏）：见 runbook「库损坏/路径异常」")
	default:
		if statsTableCountValue(analytics, "usage_requests") == 0 {
			warnings = append(warnings, "usage_requests 为 0 行：分析采集未生效，需要检查进程 attach（G1）")
		}
		if statsTableCountValue(analytics, "usage_sessions") == 0 {
			warnings = append(warnings, "usage_sessions 为 0 行：会话维度尚未采集（G1）")
		}
		if statsTableCountValue(analytics, "usage_tool_calls") == 0 {
			warnings = append(warnings, "usage_tool_calls 为 0 行：工具生命周期未被采集（G2）")
		}
		if statsTableCountValue(analytics, "usage_subagents") == 0 {
			warnings = append(warnings, "usage_subagents 为 0 行：子代理完成结果未被采集（G4 归一化后仍为空时检查生产者）")
		}
	}

	session := report.SessionDB
	switch {
	case !session.Exists:
		warnings = append(warnings, "会话库不存在：本机尚无落盘会话（或使用了非默认路径）")
	case !session.Readable:
		warnings = append(warnings, "会话库存在但不可读（可能被其它进程持有或损坏）")
	default:
		if statsTableCountValue(session, "session_events") == 0 {
			warnings = append(warnings, "session_events 为 0 行：会话事件未落库")
		}
		if statsEventTypeCount(session, "tool.requested") == 0 && statsEventTypeCount(session, "tool.completed") == 0 {
			warnings = append(warnings, "会话库中 tool.* 事件为 0 行：工具观测断点（G2）")
		}
		if statsTableCountValue(session, "session_tool_receipts") == 0 {
			warnings = append(warnings, "session_tool_receipts 为 0 行：工具回执未持久化（G3）")
		}
	}
	return warnings
}

func statsTableCountValue(db statsDoctorDatabase, name string) int64 {
	for _, table := range db.Tables {
		if table.Name == name {
			return table.Count
		}
	}
	return 0
}

func statsEventTypeCount(db statsDoctorDatabase, eventType string) int64 {
	for _, event := range db.EventTypes {
		if event.Type == eventType {
			return event.Count
		}
	}
	return 0
}

func statsRenderDoctor(w io.Writer, report statsDoctorReport) {
	fmt.Fprintf(w, "采集健康自检 %s\n", report.GeneratedAt.Format(time.RFC3339))
	statsRenderDoctorDatabase(w, "分析库", report.AnalyticsDB)
	statsRenderDoctorDatabase(w, "会话库", report.SessionDB)
	if len(report.Warnings) == 0 {
		fmt.Fprintln(w, "提示: 未发现明显异常")
		return
	}
	fmt.Fprintln(w, "提示:")
	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "  - %s\n", warning)
	}
}

func statsRenderDoctorDatabase(w io.Writer, label string, db statsDoctorDatabase) {
	fmt.Fprintf(w, "%s: %s\n", label, statsDashIfEmpty(db.Path))
	switch {
	case !db.Exists:
		fmt.Fprintln(w, "  状态: 暂无数据（文件不存在）")
		return
	case !db.Readable:
		fmt.Fprintf(w, "  状态: 存在但不可读（%d B）\n", db.SizeBytes)
		if db.Error != "" {
			fmt.Fprintf(w, "  错误: %s\n", db.Error)
		}
		return
	default:
		fmt.Fprintf(w, "  状态: 可读（%d B）\n", db.SizeBytes)
	}
	rows := make([][]string, 0, len(db.Tables))
	for _, table := range db.Tables {
		count := "缺失"
		if table.Exists {
			count = strconv.FormatInt(table.Count, 10)
		}
		rows = append(rows, []string{table.Name, count})
	}
	statsWriteTable(w, []string{"表", "行数"}, rows)
	if db.LastEventAt != "" {
		fmt.Fprintf(w, "  最近事件时间: %s\n", db.LastEventAt)
	}
	if len(db.EventTypes) > 0 {
		fmt.Fprintln(w, "  事件类型:")
		eventRows := make([][]string, 0, len(db.EventTypes))
		for _, event := range db.EventTypes {
			eventRows = append(eventRows, []string{event.Type, strconv.FormatInt(event.Count, 10)})
		}
		statsWriteTable(w, []string{"类型", "数量"}, eventRows)
	}
}

// ============================================================================
// 渲染工具（简单对齐输出：无 ANSI、确定性、便于管道与测试）
// ============================================================================

func statsWriteTable(w io.Writer, headers []string, rows [][]string) {
	if len(headers) == 0 {
		return
	}
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = statsDisplayWidth(header)
	}
	for _, row := range rows {
		for i := range headers {
			if i >= len(row) {
				continue
			}
			if width := statsDisplayWidth(row[i]); width > widths[i] {
				widths[i] = width
			}
		}
	}
	writeLine := func(cells []string) {
		parts := make([]string, len(headers))
		for i := range headers {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			padding := widths[i] - statsDisplayWidth(cell)
			if padding < 0 {
				padding = 0
			}
			parts[i] = cell + strings.Repeat(" ", padding)
		}
		fmt.Fprintln(w, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	writeLine(headers)
	separators := make([]string, len(headers))
	for i := range headers {
		separators[i] = strings.Repeat("-", widths[i])
	}
	writeLine(separators)
	for _, row := range rows {
		writeLine(row)
	}
}

func statsDisplayWidth(value string) int {
	return utf8.RuneCountInString(value)
}

func statsTruncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return string(runes[:1])
	}
	return string(runes[:limit-1]) + "…"
}

func statsDashIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "--"
	}
	return strings.TrimSpace(value)
}

func statsFormatTime(value time.Time) string {
	if value.IsZero() {
		return "--"
	}
	return value.UTC().Format(time.RFC3339)
}

func statsFormatRate(rate float64) string {
	return fmt.Sprintf("%.1f%%", rate*100)
}

func statsFormatDuration(milliseconds int64) string {
	if milliseconds <= 0 {
		return "--"
	}
	if milliseconds < 1000 {
		return fmt.Sprintf("%dms", milliseconds)
	}
	return fmt.Sprintf("%.1fs", float64(milliseconds)/1000)
}

func statsFormatSuccess(success *bool) string {
	if success == nil {
		return "unknown"
	}
	if *success {
		return "true"
	}
	return "false"
}

func statsFormatCountMap(counts map[string]int) string {
	if len(counts) == 0 {
		return "--"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

func statsMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
