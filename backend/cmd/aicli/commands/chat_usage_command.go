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
)

// handleUsageCommand /usage 命令入口（command.go 分发挂接，与 /status 同型）。
// 返回值对齐既有命令处理器（false = 不退出 REPL）。
func handleUsageCommand(session *ChatSession, command string) bool {
	args := parseUsageCommandArgs(command)
	if len(args) > 0 && args[0] != "cache" {
		printfChatCommandOutput(session, "错误: 未知子命令 %q\n用法: /usage [cache [requests [N] | trace <message_id>]]", args[0])
		return false
	}
	sub := args
	if len(sub) > 0 && sub[0] == "cache" {
		sub = sub[1:]
	}

	switch {
	case len(sub) == 0:
		printUsageCacheOverview(session)
	case sub[0] == "requests":
		limit := usageCacheRequestsDefaultLimit
		if len(sub) > 1 {
			n, err := strconv.Atoi(sub[1])
			if err != nil || n <= 0 {
				printfChatCommandOutput(session, "错误: requests 数量非法: %q（应为 1-%d）", sub[1], usageCacheRequestsMaxLimit)
				return false
			}
			limit = n
			if limit > usageCacheRequestsMaxLimit {
				limit = usageCacheRequestsMaxLimit
			}
		}
		printUsageCacheRequests(session, limit)
	case sub[0] == "trace":
		if len(sub) < 2 || strings.TrimSpace(sub[1]) == "" {
			printChatCommandOutput(session, "错误: trace 需要 message_id\n用法: /usage cache trace <message_id>")
			return false
		}
		printUsageCacheTrace(session, strings.TrimSpace(sub[1]))
	default:
		printfChatCommandOutput(session, "错误: 未知 cache 子命令 %q\n用法: /usage cache [requests [N] | trace <message_id>]", sub[0])
	}
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

// executeStructuredUsageCommand renders /usage through the structured command
// channel. In the unified interactive projection the view variants request
// the lease-bound alternate-screen usage viewer (like /model and /debug
// display): no Scene cell is committed and dispatch opens the viewer instead.
// Plain, JSON and noninteractive projections keep the §6.4 document cell, and
// every branch — including argument and source degradation errors — stays
// inside a CommandResult, so the unified command gate can never observe a
// /usage fall-through.
func executeStructuredUsageCommand(session *ChatSession, command string) CommandResult {
	args := parseUsageCommandArgs(command)
	if len(args) > 0 && args[0] != "cache" {
		return commandTextResult(fmt.Sprintf("错误: 未知子命令 %q\n用法: /usage [cache [requests [N] | trace <message_id>]]", args[0]))
	}
	sub := args
	if len(sub) > 0 && sub[0] == "cache" {
		sub = sub[1:]
	}

	switch {
	case len(sub) == 0:
		return structuredUsageViewResult(session, UsageScreenRequest{Mode: usageScreenModeOverview})
	case sub[0] == "requests":
		limit := usageCacheRequestsDefaultLimit
		if len(sub) > 1 {
			n, err := strconv.Atoi(sub[1])
			if err != nil || n <= 0 {
				return commandTextResult(fmt.Sprintf("错误: requests 数量非法: %q（应为 1-%d）", sub[1], usageCacheRequestsMaxLimit))
			}
			limit = n
			if limit > usageCacheRequestsMaxLimit {
				limit = usageCacheRequestsMaxLimit
			}
		}
		return structuredUsageViewResult(session, UsageScreenRequest{Mode: usageScreenModeRequests, Limit: limit})
	case sub[0] == "trace":
		if len(sub) < 2 || strings.TrimSpace(sub[1]) == "" {
			return commandTextResult("错误: trace 需要 message_id\n用法: /usage cache trace <message_id>")
		}
		messageID := strings.TrimSpace(sub[1])
		return structuredUsageViewResult(session, UsageScreenRequest{Mode: usageScreenModeTrace, TraceID: messageID})
	default:
		return commandTextResult(fmt.Sprintf("错误: 未知 cache 子命令 %q\n用法: /usage cache [requests [N] | trace <message_id>]", sub[0]))
	}
}

// structuredUsageViewResult resolves the cache source, then either requests
// the alternate-screen usage viewer (unified interactive TTY) or renders the
// established §6.4 document cell. Source degradation errors become document
// lines with the same stable text in every projection.
func structuredUsageViewResult(session *ChatSession, req UsageScreenRequest) CommandResult {
	src, sessionID, errLines, ok := usageCacheSourceOrLines(session)
	if !ok {
		return commandTextResult(strings.Join(errLines, "\n"))
	}
	if unifiedDirectInteractiveOutput(session) {
		// The viewer captures its snapshot after the command result crosses
		// the dispatch boundary (like OpenDebugOverlay); the command carries
		// no Scene-cell document.
		return CommandResult{Action: CommandContinue, OpenUsageScreen: &req}
	}
	return commandTextResult(strings.Join(usageDocumentLines(src, sessionID, req), "\n"))
}

// usageDocumentLines keeps the §6.4 single-section document semantics for
// plain/JSON/noninteractive projections.
func usageDocumentLines(src cacheanalytics.Source, sessionID string, req UsageScreenRequest) []string {
	switch req.Mode {
	case usageScreenModeRequests:
		limit := req.Limit
		if limit <= 0 {
			limit = usageCacheRequestsDefaultLimit
		}
		return renderUsageCacheRequests(src, sessionID, limit)
	case usageScreenModeTrace:
		return renderUsageCacheTrace(src, sessionID, req.TraceID)
	default:
		return renderUsageCacheOverview(src, sessionID)
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

// usageCacheSource resolves the source and prints degradation lines for legacy
// stdout callers (plain/JSON projections). Structured callers must use
// usageCacheSourceOrLines so errors stay inside the command document.
func usageCacheSource(session *ChatSession) (cacheanalytics.Source, string, bool) {
	src, sessionID, errLines, ok := usageCacheSourceOrLines(session)
	if !ok {
		printChatCommandOutput(session, strings.Join(errLines, "\n"))
		return nil, "", false
	}
	return src, sessionID, true
}

func printUsageCacheOverview(session *ChatSession) {
	src, sessionID, ok := usageCacheSource(session)
	if !ok {
		return
	}
	printChatCommandOutput(session, strings.Join(renderUsageCacheOverview(src, sessionID), "\n"))
}

func printUsageCacheRequests(session *ChatSession, limit int) {
	src, sessionID, ok := usageCacheSource(session)
	if !ok {
		return
	}
	printChatCommandOutput(session, strings.Join(renderUsageCacheRequests(src, sessionID, limit), "\n"))
}

func printUsageCacheTrace(session *ChatSession, messageID string) {
	src, sessionID, ok := usageCacheSource(session)
	if !ok {
		return
	}
	printChatCommandOutput(session, strings.Join(renderUsageCacheTrace(src, sessionID, messageID), "\n"))
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
		tokens := "-"
		if r.Usage != nil {
			tokens = fmt.Sprintf("prompt=%s 读=%s 写=%s",
				formatTokenCount(r.Usage.PromptTokens),
				formatTokenCount(r.Usage.CacheReadTokens),
				formatTokenCount(r.Usage.CacheCreationTokens))
			if r.CacheHitRatio != nil {
				hit = usagePercent(r.CacheHitRatio)
			}
		}
		lines = append(lines, fmt.Sprintf("  #%d %s %s %s/%s step=%d %s %s %s",
			i+1,
			r.StartedAt.Format("15:04:05.000"),
			r.LLMRequestID,
			orDash(r.Provider), orDash(r.Model),
			r.Step,
			orDash(r.Status),
			usageCacheStatusLabel(r.CacheStatus),
			hit+" "+tokens))
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
