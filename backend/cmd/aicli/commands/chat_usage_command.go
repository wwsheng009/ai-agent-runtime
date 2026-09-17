package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// ============================================================================
// TUI /usage 命令（方案 §6.4）：进程内直调 CacheAnalyticsSource，不经 HTTP。
// 与 micro web client"缓存"页签共用同一 cacheanalytics.Service（§3.2）。
//
// 命令语义：
//   /usage                          → 当前会话缓存总览（默认视图）
//   /usage cache                    → 同上（显式子命令）
//   /usage cache requests [N]       → 最近 N 条请求明细（默认 20，上限 100）
//   /usage cache trace <message_id> → 按消息 id 追溯
//
// 渲染降级（§6.4）：not_reported 命中率显示 --；partial 首行标注窗口；
// history_inferred 的 message_id 标注（推断）；空会话/服务缺失有明确提示。
// ============================================================================

const (
	usageCacheRequestsDefaultLimit = 20
	usageCacheRequestsMaxLimit     = 100
	usageCacheUnavailableHint      = "缓存分析不可用（当前后端版本不支持）"

	// usageCommandUsage 是 /usage 的稳定用法文本（未知子命令与参数错误共用）。
	usageCommandUsage = "用法: /usage [cache [requests [N] | trace <message_id>]] | tools [N] | subagents [N] [--failed] | errors [top N]"
)

// handleUsageCommand /usage 命令入口（command.go 分发挂接，与 /status 同型）。
// 返回值对齐既有命令处理器（false = 不退出 REPL）。
func handleUsageCommand(session *ChatSession, command string) bool {
	req, errText := resolveUsageViewRequest(parseUsageCommandArgs(command))
	if errText != "" {
		printChatCommandOutput(session, errText)
		return false
	}
	src, sessionID, errLines, ok := usageCacheSourceOrLines(session)
	analytics := chatUsageAnalyticsSourceOrNil()
	if !ok {
		printChatCommandOutput(session,
			strings.Join(usageDegradationLines(analytics, req.Mode, errLines), "\n"))
		return false
	}
	printChatCommandOutput(session,
		strings.Join(usageDocumentLines(src, analytics, sessionID, req), "\n"))
	return false
}

// parseUsageCommandArgs 提取 /usage 之后的参数词列表。
func parseUsageCommandArgs(command string) []string {
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		return nil
	}
	return strings.Fields(arg)
}

// resolveUsageViewRequest 把 /usage 参数词解析为视图请求。错误文本为空表示成功；
// 两种投影（legacy stdout / 结构化 CommandResult）共用同一解析，避免参数语义漂移。
// 参数边界与既有 requests 契约一致：非法或 <=0 报错，超上限归一（clamp）。
func resolveUsageViewRequest(args []string) (UsageScreenRequest, string) {
	if len(args) == 0 {
		return UsageScreenRequest{Mode: usageScreenModeOverview}, ""
	}
	switch args[0] {
	case "cache":
		return resolveUsageCacheViewRequest(args[1:])
	case "tools":
		return resolveUsageToolsViewRequest(args[1:])
	case "subagents":
		return resolveUsageSubagentsViewRequest(args[1:])
	case "errors":
		return resolveUsageErrorsViewRequest(args[1:])
	default:
		return UsageScreenRequest{}, fmt.Sprintf("错误: 未知子命令 %q\n%s", args[0], usageCommandUsage)
	}
}

// resolveUsageCacheViewRequest 解析既有缓存视图子命令（行为与 §6.4 完全一致）。
func resolveUsageCacheViewRequest(sub []string) (UsageScreenRequest, string) {
	switch {
	case len(sub) == 0:
		return UsageScreenRequest{Mode: usageScreenModeOverview}, ""
	case sub[0] == "requests":
		limit := usageCacheRequestsDefaultLimit
		if len(sub) > 1 {
			n, err := strconv.Atoi(sub[1])
			if err != nil || n <= 0 {
				return UsageScreenRequest{}, fmt.Sprintf("错误: requests 数量非法: %q（应为 1-%d）", sub[1], usageCacheRequestsMaxLimit)
			}
			limit = n
			if limit > usageCacheRequestsMaxLimit {
				limit = usageCacheRequestsMaxLimit
			}
		}
		return UsageScreenRequest{Mode: usageScreenModeRequests, Limit: limit}, ""
	case sub[0] == "trace":
		if len(sub) < 2 || strings.TrimSpace(sub[1]) == "" {
			return UsageScreenRequest{}, "错误: trace 需要 message_id\n用法: /usage cache trace <message_id>"
		}
		return UsageScreenRequest{Mode: usageScreenModeTrace, TraceID: strings.TrimSpace(sub[1])}, ""
	default:
		return UsageScreenRequest{}, fmt.Sprintf("错误: 未知 cache 子命令 %q\n用法: /usage cache [requests [N] | trace <message_id>]", sub[0])
	}
}

// resolveUsageToolsViewRequest 解析 /usage tools [N]。
func resolveUsageToolsViewRequest(rest []string) (UsageScreenRequest, string) {
	limit, errText := resolveUsageLimitArg("tools", rest, usageToolsDefaultLimit, usageToolsMaxLimit)
	if errText != "" {
		return UsageScreenRequest{}, errText
	}
	return UsageScreenRequest{Mode: usageScreenModeTools, Limit: limit}, ""
}

// resolveUsageSubagentsViewRequest 解析 /usage subagents [N] [--failed]；N 与
// --failed 顺序无关，重复 N 以最后一个为准。
func resolveUsageSubagentsViewRequest(rest []string) (UsageScreenRequest, string) {
	req := UsageScreenRequest{Mode: usageScreenModeSubagents, Limit: usageSubagentsDefaultLimit}
	for _, arg := range rest {
		if arg == "--failed" {
			req.FailedOnly = true
			continue
		}
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			return UsageScreenRequest{}, fmt.Sprintf("错误: subagents 参数非法: %q\n用法: /usage subagents [N] [--failed]", arg)
		}
		if n > usageSubagentsMaxLimit {
			n = usageSubagentsMaxLimit
		}
		req.Limit = n
	}
	return req, ""
}

// resolveUsageErrorsViewRequest 解析 /usage errors [top N]（裸 N 亦接受）。
func resolveUsageErrorsViewRequest(rest []string) (UsageScreenRequest, string) {
	top := usageErrorsDefaultTop
	tokens := rest
	if len(tokens) > 0 && tokens[0] == "top" {
		tokens = tokens[1:]
		if len(tokens) == 0 {
			return UsageScreenRequest{}, "错误: errors 需要 top 数量\n用法: /usage errors [top N]"
		}
	}
	if len(tokens) > 0 {
		n, err := strconv.Atoi(tokens[0])
		if err != nil || n <= 0 {
			return UsageScreenRequest{}, fmt.Sprintf("错误: errors 数量非法: %q（应为 1-%d）", tokens[0], usageErrorsMaxTop)
		}
		if n > usageErrorsMaxTop {
			n = usageErrorsMaxTop
		}
		top = n
	}
	return UsageScreenRequest{Mode: usageScreenModeErrors, Top: top}, ""
}

// resolveUsageLimitArg 解析「子命令 [N]」形式的数量参数：缺省取默认值，
// 非法/<=0 报错，超上限归一（clamp）。
func resolveUsageLimitArg(name string, rest []string, defaultLimit, maxLimit int) (int, string) {
	if len(rest) == 0 {
		return defaultLimit, ""
	}
	n, err := strconv.Atoi(rest[0])
	if err != nil || n <= 0 {
		return 0, fmt.Sprintf("错误: %s 数量非法: %q（应为 1-%d）", name, rest[0], maxLimit)
	}
	if n > maxLimit {
		n = maxLimit
	}
	return n, ""
}

// executeStructuredUsageCommand renders /usage through the structured command
// channel. In the unified interactive projection the view variants request
// the lease-bound alternate-screen usage viewer (like /model and /debug
// display): no Scene cell is committed and dispatch opens the viewer instead.
// Plain, JSON and noninteractive projections keep the §6.4 document cell, and
// every branch — including argument and source degradation errors — stays
// inside a CommandResult, so the unified command gate can never observe a
// /usage fall-through.
func executeStructuredUsageCommand(session *ChatSession, command string) CommandResult {
	req, errText := resolveUsageViewRequest(parseUsageCommandArgs(command))
	if errText != "" {
		return commandTextResult(errText)
	}
	return structuredUsageViewResult(session, req)
}

// structuredUsageViewResult resolves the cache source, then either requests
// the alternate-screen usage viewer (unified interactive TTY) or renders the
// established §6.4 document cell. Source degradation errors become document
// lines with the same stable text in every projection.
func structuredUsageViewResult(session *ChatSession, req UsageScreenRequest) CommandResult {
	src, sessionID, errLines, ok := usageCacheSourceOrLines(session)
	analytics := chatUsageAnalyticsSourceOrNil()
	if !ok {
		return commandTextResult(strings.Join(usageDegradationLines(analytics, req.Mode, errLines), "\n"))
	}
	if unifiedDirectInteractiveOutput(session) {
		// The viewer captures its snapshot after the command result crosses
		// the dispatch boundary (like OpenDebugOverlay); the command carries
		// no Scene-cell document.
		return CommandResult{Action: CommandContinue, OpenUsageScreen: &req}
	}
	return commandTextResult(strings.Join(usageDocumentLines(src, analytics, sessionID, req), "\n"))
}

// usageDocumentLines keeps the §6.4 single-section document semantics for
// plain/JSON/noninteractive projections. The batch 1.3 aggregation modes share
// the same single-section shape; every mode carries the health line first.
func usageDocumentLines(src cacheanalytics.Source, analytics usageAnalyticsSource, sessionID string, req UsageScreenRequest) []string {
	switch req.Mode {
	case usageScreenModeTools:
		return usageHealthFirstLines(analytics, renderUsageAnalyticsToolStats(analytics, sessionID, req.Limit))
	case usageScreenModeSubagents:
		return usageHealthFirstLines(analytics, renderUsageAnalyticsSubagentStats(analytics, sessionID, req.Limit, req.FailedOnly))
	case usageScreenModeErrors:
		return usageHealthFirstLines(analytics, renderUsageAnalyticsErrorPatterns(analytics, sessionID, req.Top))
	case usageScreenModeRequests:
		limit := req.Limit
		if limit <= 0 {
			limit = usageCacheRequestsDefaultLimit
		}
		return usageHealthFirstLines(analytics, renderUsageCacheRequests(src, sessionID, limit))
	case usageScreenModeTrace:
		return usageHealthFirstLines(analytics, renderUsageCacheTrace(src, sessionID, req.TraceID))
	default:
		return usageHealthFirstLines(analytics, renderUsageCacheOverview(src, sessionID))
	}
}

// usageCacheSourceOrLines resolves the session's cache analytics source. On
// failure it returns the stable degradation lines instead of writing to the
// terminal, so the legacy stdout projection and the structured CommandResult
// projection share one error contract（§6.4 渲染降级）.
func usageCacheSourceOrLines(session *ChatSession) (cacheanalytics.Source, string, []string, bool) {
	if session == nil {
		return nil, "", []string{"错误: 当前没有活动会话"}, false
	}
	service := chatLocalCacheService()
	if service == nil {
		return nil, "", []string{usageCacheUnavailableHint}, false
	}
	src := service.Source()
	if src == nil {
		return nil, "", []string{usageCacheUnavailableHint}, false
	}
	sessionID := currentRuntimeSessionID(session)
	if sessionID == "" {
		return nil, "", []string{"错误: 当前会话没有 runtime session id"}, false
	}
	return src, sessionID, nil, true
}

// ---------------------------------------------------------------------------
// 纯渲染函数（返回行切片，便于单测断言；§6.4 渲染降级规范）
// ---------------------------------------------------------------------------

// formatTokenCount 千分位格式化（对齐 §6.4 示例 128,400）。
func formatTokenCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	joined := strings.Join(parts, ",")
	if neg {
		return "-" + joined
	}
	return joined
}

// usagePercent 比例渲染；nil → "--"（§6.4：not_reported 不污染命中率）。
func usagePercent(ratio *float64) string {
	if ratio == nil {
		return "--"
	}
	return fmt.Sprintf("%.1f%%", *ratio*100)
}

// formatUsageTokenSummary 单条请求的 token 用量摘要（明细列表与消息追溯
// 共用，保证两处口径一致）。Usage 缺失（provider 未上报）时返回 "-"。
func formatUsageTokenSummary(usage *cacheanalytics.CacheUsage) string {
	if usage == nil {
		return "-"
	}
	return fmt.Sprintf("prompt=%s 输出=%s 读=%s 写=%s",
		formatTokenCount(usage.PromptTokens),
		formatTokenCount(usage.CompletionTokens),
		formatTokenCount(usage.CacheReadTokens),
		formatTokenCount(usage.CacheCreationTokens))
}

func usageCacheStatusLabel(status string) string {
	switch status {
	case cacheanalytics.CacheStatusHit:
		return "命中"
	case cacheanalytics.CacheStatusWrite:
		return "写入"
	case cacheanalytics.CacheStatusReportedZero:
		return "未命中"
	case cacheanalytics.CacheStatusNotReported:
		return "未上报（未知）"
	case cacheanalytics.CacheStatusError:
		return "错误"
	default:
		return status
	}
}

// renderUsageCacheOverview 总览（§6.4 输出示例）。
func renderUsageCacheOverview(src cacheanalytics.Source, sessionID string) []string {
	overview, err := src.Overview(sessionID)
	if err != nil {
		return usageSourceErrorLines(err)
	}
	shortSession := sessionID
	if len(shortSession) > 16 {
		shortSession = shortSession[:16] + "…"
	}
	header := fmt.Sprintf("会话缓存统计（session: %s", shortSession)
	if overview.Coverage.Partial {
		header += fmt.Sprintf("，仅统计最近 %d 条请求", src.Capabilities().MaxRequestsPerSession)
	}
	header += "）"
	dist := overview.CacheStatusDistribution
	lines := []string{header}
	lines = append(lines, fmt.Sprintf("  请求总数: %d   命中: %d   写入: %d   未命中: %d   未上报（未知）: %d   错误: %d",
		overview.RequestsTotal, dist.Hit, dist.Write, dist.ReportedZero, dist.NotReported, dist.Error))
	lines = append(lines, fmt.Sprintf("  输入 token: %s（缓存读取 %s / 缓存写入 %s）",
		formatTokenCount(overview.Tokens.PromptTokens),
		formatTokenCount(overview.Tokens.CacheReadTokens),
		formatTokenCount(overview.Tokens.CacheCreationTokens)))
	lines = append(lines, fmt.Sprintf("  输出 token: %s", formatTokenCount(overview.Tokens.CompletionTokens)))
	lines = append(lines, fmt.Sprintf("  合计 token: %s   推理 token: %s",
		formatTokenCount(overview.Tokens.TotalTokens),
		formatTokenCount(overview.Tokens.ReasoningTokens)))
	lines = append(lines, fmt.Sprintf("  缓存读取率: %s   缓存写入率: %s",
		usagePercent(overview.CacheHitRatio), usagePercent(overview.CacheWriteRatio)))
	return lines
}

// renderUsageCacheRequests 明细列表（按 started_at 倒序，§4.4）。
func renderUsageCacheRequests(src cacheanalytics.Source, sessionID string, limit int) []string {
	resp, err := src.Requests(sessionID, cacheanalytics.RequestQuery{Limit: limit})
	if err != nil {
		return usageSourceErrorLines(err)
	}
	if resp.Total == 0 || len(resp.Requests) == 0 {
		return []string{"当前会话暂无 LLM 请求记录"}
	}
	lines := []string{fmt.Sprintf("最近 %d 条 LLM 请求（共 %d 条，新→旧）", len(resp.Requests), resp.Total)}
	for i, r := range resp.Requests {
		hit := "--"
		if r.Usage != nil && r.CacheHitRatio != nil {
			hit = usagePercent(r.CacheHitRatio)
		}
		lines = append(lines, fmt.Sprintf("  #%d %s %s %s/%s step=%d %s %s %s",
			i+1,
			r.StartedAt.Format("15:04:05.000"),
			r.LLMRequestID,
			orDash(r.Provider), orDash(r.Model),
			r.Step,
			orDash(r.Status),
			usageCacheStatusLabel(r.CacheStatus),
			hit+" "+formatUsageTokenSummary(r.Usage)))
	}
	return lines
}

// renderUsageCacheTrace 消息追溯（§4.3：produced_by/consumed_by + 上下文）。
func renderUsageCacheTrace(src cacheanalytics.Source, sessionID, messageID string) []string {
	trace, err := src.MessageTrace(sessionID, messageID)
	if err != nil {
		if errors.Is(err, cacheanalytics.ErrNotFound) {
			return []string{fmt.Sprintf("未找到消息: %s", messageID)}
		}
		return usageSourceErrorLines(err)
	}
	label := messageID
	if trace.CorrelationSource == cacheanalytics.CorrelationSourceHistory {
		label += "（推断）"
	}
	role := orDash(trace.MessageRole)
	turn := orDash(trace.TurnID)
	lines := []string{fmt.Sprintf("消息 %s（角色: %s，turn: %s）", label, role, turn)}
	if trace.ProducedBy != nil {
		produced := trace.ProducedBy
		lines = append(lines, fmt.Sprintf("  产出请求: %s（%s，命中率 %s）",
			produced.LLMRequestID,
			usageCacheStatusLabel(produced.CacheStatus),
			usagePercent(produced.CacheHitRatio)))
		// 产出用量与明细列表同口径（§6.4：trace 展示 produced_by 的 token 明细）。
		lines = append(lines, "  产出用量: "+formatUsageTokenSummary(produced.Usage))
	} else {
		lines = append(lines, "  产出请求: （无关联请求）")
	}
	for _, consumed := range trace.ConsumedBy {
		step := "-"
		if consumed.Step > 0 {
			step = strconv.Itoa(consumed.Step)
		}
		lines = append(lines, fmt.Sprintf("  消费请求: %s（step %s，%s，命中率 %s）",
			consumed.LLMRequestID, step,
			usageCacheStatusLabel(consumed.CacheStatus),
			usagePercent(consumed.CacheHitRatio)))
	}
	if trace.Neighbors.PrevMessageID != "" || trace.Neighbors.NextMessageID != "" {
		lines = append(lines, fmt.Sprintf("  相邻消息: %s ← → %s",
			orDash(trace.Neighbors.PrevMessageID), orDash(trace.Neighbors.NextMessageID)))
	}
	return lines
}

// usageSourceErrorLines Source 错误 → 稳定降级提示（§6.4 渲染降级规范）。
func usageSourceErrorLines(err error) []string {
	switch {
	case errors.Is(err, cacheanalytics.ErrSessionNotFound):
		return []string{"会话不存在或已归档，无法查询缓存统计"}
	case errors.Is(err, cacheanalytics.ErrNotFound):
		return []string{"未找到对应记录"}
	case errors.Is(err, cacheanalytics.ErrDisabled):
		return []string{usageCacheUnavailableHint}
	default:
		return []string{usageCacheUnavailableHint}
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
