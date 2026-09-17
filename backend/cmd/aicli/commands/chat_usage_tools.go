package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ============================================================================
// /usage 聚合视图（方案 §9.3 T2 / §9.4 数据点表）：
//
//	/usage tools [N]                  工具调用/失败/耗时表
//	/usage subagents [N] [--failed]   子代理完成率/失败分类/重试/耗时
//	/usage errors [top N]             失败模式 Top-N
//
// 数据源是进程内 usageanalytics 查询层（批次 1.3 产物），与 /usage cache 的
// CacheAnalyticsSource 同模式：**不 import httpapi、不发 HTTP**。渲染函数只依赖
// usageAnalyticsSource 窄接口（*usageanalytics.Service 实现），便于单测注入假实现。
//
// 渲染降级（§9.3，与 9.1/9.2 同文案）：not_reported/未知 → `--`；空会话 →
// 「当前会话暂无记录」；分析库未挂载/空库 → 「分析数据不可用（需批次 0/1 采集）」；
// 查询错误一律降级为稳定文本，绝不向 TUI 抛错。
// ============================================================================

const (
	usageToolsDefaultLimit     = 10
	usageToolsMaxLimit         = 50
	usageSubagentsDefaultLimit = 10
	usageSubagentsMaxLimit     = 50
	usageErrorsDefaultTop      = 10
	usageErrorsMaxTop          = 50
)

const (
	// usageAnalyticsUnavailableHint 是聚合视图的稳定降级文案（§9.3：分析库缺失/
	// 未启用）。与缓存视图的 usageCacheUnavailableHint 分开，避免把"分析库未挂载"
	// 误报成"后端版本不支持缓存分析"。
	usageAnalyticsUnavailableHint = "分析数据不可用（需批次 0/1 采集）"

	// usageHealthNoDataLine 是未挂载/只读降级时的采集健康首行：显示 `--` 与
	// 「暂无数据」，不报错（§9.4 降级语义）。
	usageHealthNoDataLine = "采集健康: --（暂无数据）"

	// usageSubagentFailedEmptyLine 是 /usage subagents --failed 的空结果文案。
	usageSubagentFailedEmptyLine = "当前会话暂无子代理失败记录（暂无数据）"
)

// usageAnalyticsSource 是 /usage 聚合视图需要的最小查询面。除 *usageanalytics.Service
// 外不引入实现：TUI 直连查询层，HTTP 契约不在进程内复用（§9.3 数据源）。
type usageAnalyticsSource interface {
	AnalyticsHealth() usageanalytics.AnalyticsHealth
	ToolStats(usageanalytics.ToolStatsQuery) (usageanalytics.ToolStatsResult, error)
	SubagentStats(usageanalytics.SubagentStatsQuery) (usageanalytics.SubagentStatsResult, error)
	ErrorPatterns(usageanalytics.ErrorPatternsQuery) (usageanalytics.ErrorPatternsResult, error)
}

// isUsageAnalyticsMode 报告 mode 是否属于批次 1.3 的聚合视图（tools/subagents/errors）。
// 缓存视图（overview/requests/trace）保持既有降级契约不变。
func isUsageAnalyticsMode(mode string) bool {
	switch mode {
	case usageScreenModeTools, usageScreenModeSubagents, usageScreenModeErrors:
		return true
	default:
		return false
	}
}

// chatUsageAnalyticsSourceOrNil 返回当前活动会话的分析服务；未挂载时返回 nil
// （接口零值，避免 typed-nil）。与 chatLocalCacheService 同源：同一个
// usageanalytics.Service 同时提供缓存 Source 与 v2 查询层。
func chatUsageAnalyticsSourceOrNil() usageAnalyticsSource {
	service := chatLocalUsageService()
	if service == nil {
		return nil
	}
	return service
}

// renderUsageAnalyticsHealthLine 渲染 /usage 首行的采集健康（§9.4）。
// attached 状态来自 AnalyticsHealth 快照；未挂载/只读降级/空库一律不报错：
// 显示 `--` 与「暂无数据」，空库额外保留 attached 事实便于定位"已挂载但没数据"。
func renderUsageAnalyticsHealthLine(src usageAnalyticsSource) string {
	if src == nil {
		return usageHealthNoDataLine
	}
	health := src.AnalyticsHealth()
	if health.Degraded {
		return usageHealthNoDataLine
	}
	last := "--"
	if health.LastIngestAt != nil {
		last = health.LastIngestAt.UTC().Format("2006-01-02 15:04:05") + " UTC"
	}
	if health.IngestedTotal <= 0 {
		return fmt.Sprintf("采集健康: attached（已入库 0 · 最近写入 %s · 暂无数据）", last)
	}
	return fmt.Sprintf("采集健康: attached（已入库 %s · 最近写入 %s）",
		formatTokenCount(health.IngestedTotal), last)
}

// usageAnalyticsUnavailableLines 查询层错误/未挂载的稳定降级行（不携带原始错误文本）。
func usageAnalyticsUnavailableLines() []string {
	return []string{usageAnalyticsUnavailableHint}
}

// usageDegradationLines 组装降级首行：缓存模式保持既有 errLines 原样（契约不变）；
// 聚合模式前置采集健康行，未挂载时再补稳定降级文案。
func usageDegradationLines(analytics usageAnalyticsSource, mode string, cacheErrLines []string) []string {
	if !isUsageAnalyticsMode(mode) {
		return cacheErrLines
	}
	health := renderUsageAnalyticsHealthLine(analytics)
	if analytics == nil {
		return []string{health, usageAnalyticsUnavailableHint}
	}
	return append([]string{health}, cacheErrLines...)
}

// ---------------------------------------------------------------------------
// 工具维度
// ---------------------------------------------------------------------------

// renderUsageAnalyticsToolStats 工具调用/失败/耗时表（§9.4：「/usage tools [N]」）。
func renderUsageAnalyticsToolStats(src usageAnalyticsSource, sessionID string, limit int) []string {
	if src == nil {
		return usageAnalyticsUnavailableLines()
	}
	if limit <= 0 {
		limit = usageToolsDefaultLimit
	}
	resp, err := src.ToolStats(usageanalytics.ToolStatsQuery{SessionID: sessionID, Limit: limit})
	if err != nil {
		return usageAnalyticsUnavailableLines()
	}
	if len(resp.Tools) == 0 {
		return []string{"当前会话暂无工具调用记录（暂无数据）"}
	}
	totals := resp.Totals
	lines := []string{fmt.Sprintf("工具调用统计（当前会话，共 %d 个工具；调用 %d，失败 %d，失败率 %s）",
		len(resp.Tools), totals.Calls, totals.Failures, usageFailureRateLabel(totals.Failures, totals.Calls))}
	for i, stat := range resp.Tools {
		parts := []string{
			fmt.Sprintf("#%d %s", i+1, orDash(stat.ToolName)),
			fmt.Sprintf("调用=%d", stat.Calls),
			fmt.Sprintf("失败=%d", stat.Failures),
			"失败率=" + usageFailureRateLabel(stat.Failures, stat.Calls),
			fmt.Sprintf("空结果=%d", stat.EmptyResults),
			fmt.Sprintf("重试=%d", stat.RetriedCalls),
			"p50=" + usageDurationLabel(stat.P50DurationMS),
			"p95=" + usageDurationLabel(stat.P95DurationMS),
		}
		lines = append(lines, "  "+strings.Join(parts, " "))
		if len(stat.ErrorTop) > 0 {
			lines = append(lines, "      错误: "+usageErrorPatternSummary(stat.ErrorTop))
		}
	}
	return lines
}

// ---------------------------------------------------------------------------
// 子代理维度
// ---------------------------------------------------------------------------

// renderUsageAnalyticsSubagentStats 子代理完成率/失败分类/重试/耗时（§9.4：
// 「/usage subagents [--failed]」）。FailedOnly 时查询层只返回失败记录。
func renderUsageAnalyticsSubagentStats(src usageAnalyticsSource, sessionID string, limit int, failedOnly bool) []string {
	if src == nil {
		return usageAnalyticsUnavailableLines()
	}
	if limit <= 0 {
		limit = usageSubagentsDefaultLimit
	}
	resp, err := src.SubagentStats(usageanalytics.SubagentStatsQuery{
		SessionID:  sessionID,
		FailedOnly: failedOnly,
		Limit:      limit,
	})
	if err != nil {
		return usageAnalyticsUnavailableLines()
	}
	if len(resp.Subagents) == 0 {
		if failedOnly {
			return []string{usageSubagentFailedEmptyLine}
		}
		return []string{"当前会话暂无子代理记录（暂无数据）"}
	}
	scope := fmt.Sprintf("明细 %d 条（上限 %d）", len(resp.Subagents), limit)
	if failedOnly {
		scope += "，--failed"
	}
	summary := resp.Summary
	decided := summary.Succeeded + summary.Failed
	lines := []string{
		fmt.Sprintf("子代理完成统计（当前会话，%s）", scope),
		fmt.Sprintf("  完成: %d   失败: %d   未知: %d   完成率: %s   失败率: %s   重试: %d   超时: %d",
			summary.Succeeded, summary.Failed, summary.Unknown,
			usageSuccessRateLabel(summary.Succeeded, decided),
			usageFailureRateLabel(summary.Failed, decided),
			summary.Retried, summary.Timeouts),
	}
	if summary.Failed > 0 || len(summary.FailureCategories) > 0 {
		lines = append(lines, "  失败分类: "+usageCountMapSummary(summary.FailureCategories))
	}
	if len(summary.Sources) > 0 {
		lines = append(lines, "  来源分布: "+usageCountMapSummary(summary.Sources))
	}
	for i, stat := range resp.Subagents {
		parts := []string{fmt.Sprintf("#%d %s", i+1, orDash(stat.SubagentID))}
		if role := strings.TrimSpace(stat.Role); role != "" {
			parts = append(parts, role)
		}
		if source := strings.TrimSpace(stat.Source); source != "" {
			parts = append(parts, "来源="+source)
		}
		parts = append(parts, usageSubagentOutcomeLabel(stat.Success))
		if category := strings.TrimSpace(stat.FailureCategory); category != "" {
			parts = append(parts, "分类="+category)
		}
		if code := strings.TrimSpace(stat.ErrorCode); code != "" {
			parts = append(parts, "code="+code)
		}
		parts = append(parts, "attempt="+usageAttemptLabel(stat.Attempt, stat.MaxAttempts))
		if stat.DurationMS > 0 {
			parts = append(parts, "耗时="+usageDurationLabel(stat.DurationMS))
		}
		if stat.UsageTotalTokens > 0 {
			parts = append(parts, "token="+formatTokenCount(stat.UsageTotalTokens))
		}
		if !stat.CompletedAt.IsZero() {
			parts = append(parts, stat.CompletedAt.UTC().Format("01-02 15:04:05"))
		}
		lines = append(lines, "  "+strings.Join(parts, " "))
	}
	return lines
}

// ---------------------------------------------------------------------------
// 失败模式 Top-N
// ---------------------------------------------------------------------------

// renderUsageAnalyticsErrorPatterns 失败模式 Top-N（§9.4：「/usage errors [top N]」）。
// 与 /usage 其余视图一致按当前会话过滤；跨源（tools/subagents/requests）合并计数。
func renderUsageAnalyticsErrorPatterns(src usageAnalyticsSource, sessionID string, top int) []string {
	if src == nil {
		return usageAnalyticsUnavailableLines()
	}
	if top <= 0 {
		top = usageErrorsDefaultTop
	}
	resp, err := src.ErrorPatterns(usageanalytics.ErrorPatternsQuery{SessionID: sessionID, Top: top})
	if err != nil {
		return usageAnalyticsUnavailableLines()
	}
	if len(resp.Patterns) == 0 {
		return []string{"当前会话暂无失败记录（暂无数据）"}
	}
	lines := []string{fmt.Sprintf("失败模式 Top-%d（当前会话，共 %d 类）", top, len(resp.Patterns))}
	for i, pattern := range resp.Patterns {
		lines = append(lines, fmt.Sprintf("  #%d 来源=%s %s 次数=%d",
			i+1, orDash(strings.TrimSpace(pattern.Source)),
			usageErrorPatternLabel(pattern), pattern.Count))
	}
	return lines
}

// ---------------------------------------------------------------------------
// 格式化助手
// ---------------------------------------------------------------------------

// usageDurationLabel 毫秒耗时渲染；<=0（未上报）→ `--`（§9.3 not_reported 语义）。
func usageDurationLabel(ms int64) string {
	if ms <= 0 {
		return "--"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// usageFailureRateLabel 失败率渲染；没有样本时 `--`（不伪造 0%）。
func usageFailureRateLabel(failures, calls int) string {
	if calls <= 0 {
		return "--"
	}
	return fmt.Sprintf("%.1f%%", float64(failures)/float64(calls)*100)
}

// usageSuccessRateLabel 完成率渲染；没有已判定样本（完成+失败）时 `--`。
func usageSuccessRateLabel(succeeded, decided int) string {
	if decided <= 0 {
		return "--"
	}
	return fmt.Sprintf("%.1f%%", float64(succeeded)/float64(decided)*100)
}

// usageSubagentOutcomeLabel 归一 success 指针：nil 为未知（读侧兼容旧生产者）。
func usageSubagentOutcomeLabel(success *bool) string {
	switch {
	case success == nil:
		return "未知"
	case *success:
		return "完成"
	default:
		return "失败"
	}
}

// usageAttemptLabel 重试次数渲染（attempt/max_attempts）；未上报 → `--`。
func usageAttemptLabel(attempt, maxAttempts int) string {
	if attempt <= 0 && maxAttempts <= 0 {
		return "--"
	}
	if maxAttempts <= 0 {
		return fmt.Sprintf("%d", attempt)
	}
	return fmt.Sprintf("%d/%d", attempt, maxAttempts)
}

// usageErrorPatternLabel 单条失败模式的 code/category 标签；缺失一律 `--`。
func usageErrorPatternLabel(pattern usageanalytics.ErrorPattern) string {
	code := strings.TrimSpace(pattern.ErrorCode)
	category := strings.TrimSpace(pattern.FailureCategory)
	switch {
	case code == "" && category == "":
		return "--"
	case code == "":
		return category
	case category == "" || category == code:
		return code
	default:
		return fmt.Sprintf("%s(%s)", code, category)
	}
}

// usageErrorPatternSummary 把错误码列表压缩为一行「code×count · …」。
func usageErrorPatternSummary(patterns []usageanalytics.ErrorPattern) string {
	parts := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		parts = append(parts, fmt.Sprintf("%s×%d", usageErrorPatternLabel(pattern), pattern.Count))
	}
	if len(parts) == 0 {
		return "--"
	}
	return strings.Join(parts, " · ")
}

// usageCountMapSummary 把分类/来源分布渲染为「key count · …」，次数倒序、键升序；
// 空 map → `--`（数据缺失不显示 0）。
func usageCountMapSummary(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return "--"
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
	}
	return strings.Join(parts, " · ")
}
