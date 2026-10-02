package baseline

import (
	"fmt"
	"sort"
	"strings"
)

// Rows 产出 §4.3 基线登记表：未采集项输出 n/a + 原因，阈值一律"待标定"。
func Rows(stats Stats) []Row {
	date := ""
	if idx := strings.Index(stats.GeneratedAt, "T"); idx > 0 {
		date = stats.GeneratedAt[:idx]
	}
	window := "n/a → n/a"
	if stats.FirstAt != "" || stats.LastAt != "" {
		window = stats.FirstAt + " → " + stats.LastAt
	}
	inline := stats.Triggers["inline"]

	coverage := ""
	switch {
	case stats.Sessions == 0:
		coverage = "n/a（窗口内无 LSP 活跃会话）"
	case stats.ActiveEdit > 0:
		coverage = ratioText(inline, stats.ActiveEdit, "")
	default:
		coverage = "n/a（LSP 活跃会话内无编辑类工具回执）"
	}

	diagHit := "n/a（窗口内无 injected 请求）"
	if stats.Injected > 0 {
		diagHit = ratioText(stats.DiagHit, stats.Injected, "")
	}
	fallback := "n/a（窗口内无 LSP 请求）"
	if attempted := stats.Requests - stats.NoServer; attempted > 0 {
		// 与运行时读数（internal/lsp/metrics.go）同口径：分母排除 no_server，
		// 否则"没有任何成员认领"会稀释 fallback（plan §3.3/§5.8）。
		fallback = ratioText(stats.Degraded, attempted, "")
	} else if stats.Requests > 0 {
		fallback = "n/a（attempted 为 0：全部请求均无 server 认领）"
	}
	p95 := "n/a"
	if stats.LatencyP95MS != nil {
		p95 = fmt.Sprintf("%d ms", *stats.LatencyP95MS)
	}
	appendRatio := ""
	switch {
	case stats.Sessions == 0:
		appendRatio = "n/a（窗口内无 LSP 活跃会话）"
	case stats.ActiveOutput > 0:
		appendRatio = ratioText(stats.AppendedBytes, stats.ActiveOutput, "")
	default:
		appendRatio = "n/a（LSP 活跃会话内编辑回执缺少 output_model_visible_bytes）"
	}

	p50Text := "n/a"
	if stats.LatencyP50MS != nil {
		p50Text = fmt.Sprintf("%d ms", *stats.LatencyP50MS)
	}
	editCallsText := fmt.Sprintf("%d", stats.ActiveEdit)
	if stats.ActiveEdit != stats.EditCalls {
		editCallsText = fmt.Sprintf("%d（全部 %d）", stats.ActiveEdit, stats.EditCalls)
	}
	outputText := fmt.Sprintf("%d", stats.ActiveOutput)
	if stats.ActiveOutput != stats.EditOutput {
		outputText = fmt.Sprintf("%d（全部 %d）", stats.ActiveOutput, stats.EditOutput)
	}
	appendSamples := fmt.Sprintf("追加 %d B / LSP 活跃会话内回执可见 %s B", stats.AppendedBytes, outputText)
	if stats.AppendedDiagBytes+stats.AppendedNoteBytes+stats.AppendedEmptyBytes > 0 {
		appendSamples += fmt.Sprintf("（新构建拆分：诊断 %d / 提示 %d / 空块 %d）",
			stats.AppendedDiagBytes, stats.AppendedNoteBytes, stats.AppendedEmptyBytes)
	}

	return []Row{
		{
			Metric:     "lsp_edit_coverage_ratio",
			Value:      coverage,
			Window:     window,
			Samples:    fmt.Sprintf("inline %d / LSP 活跃会话内 edit %s", inline, editCallsText),
			Conclusion: "待标定（需人工判读）",
			Date:       date,
		},
		{
			Metric:     "lsp_diag_hit_ratio",
			Value:      diagHit,
			Window:     window,
			Samples:    fmt.Sprintf("hit %d / injected %d", stats.DiagHit, stats.Injected),
			Conclusion: "待标定（需人工判读）",
			Date:       date,
		},
		{
			Metric:     "lsp_diag_new_ratio",
			Value:      diagNewText(stats),
			Window:     window,
			Samples:    diagNewSamples(stats),
			Conclusion: diagNewConclusion(stats),
			Date:       date,
		},
		{
			Metric:     "lsp_fallback_ratio",
			Value:      fallback,
			Window:     window,
			Samples:    fallbackSamples(stats),
			Conclusion: "待标定（需人工判读）",
			Date:       date,
		},
		{
			Metric:     "lsp_wait_latency_p95",
			Value:      p95,
			Window:     window,
			Samples:    fmt.Sprintf("n=%d（P50 %s）", len(stats.durations), p50Text),
			Conclusion: "待标定（需人工判读）",
			Date:       date,
		},
		{
			Metric:     "lsp_append_bytes_ratio",
			Value:      appendRatio,
			Window:     window,
			Samples:    appendSamples,
			Conclusion: "待标定（需人工判读）",
			Date:       date,
		},
		{
			Metric:     "lsp_closure_ratio",
			Value:      closureText(stats),
			Window:     window,
			Samples:    closureSamples(stats),
			Conclusion: closureConclusion(stats),
			Date:       date,
		},
		{
			Metric:     "lsp_cold_first_publish_p95",
			Value:      coldStartText(stats),
			Window:     window,
			Samples:    coldStartSamples(stats),
			Conclusion: "待标定（需人工判读）",
			Date:       date,
		},
		{
			Metric:     "lsp_cold_first_probe_ratio",
			Value:      coldProbeText(stats),
			Window:     window,
			Samples:    coldProbeSamples(stats),
			Conclusion: coldProbeConclusion(stats),
			Date:       date,
		},
	}
}

// diagNewText 输出注入诊断中"新增（编辑前不存在）"的占比：A6（scope 默认值）
// 的判据——占比低说明 scope=all 在反复重发既有问题，切 changed 收益大；
// 未采集输出 n/a + 原因。
func diagNewText(stats Stats) string {
	if stats.TotalDiagCount <= 0 {
		return "n/a（窗口内无带 total_diag_count 的诊断样本；新构建落盘后开始采集）"
	}
	return ratioText(stats.NewDiagCount, stats.TotalDiagCount, "")
}

func diagNewSamples(stats Stats) string {
	if stats.TotalDiagCount <= 0 {
		return "n=0（未采集，非缺失数据）"
	}
	return fmt.Sprintf("new %d / all %d（全量为 scope 过滤前条数）", stats.NewDiagCount, stats.TotalDiagCount)
}

func diagNewConclusion(stats Stats) string {
	if stats.TotalDiagCount <= 0 {
		return "待采集（需要 total_diag_count 事件字段）"
	}
	return "待标定（A6：新增占比低 → 考虑 scope=changed 默认）"
}

// coldProbeText 输出 no_fresh 中"首探针"的占比：未标记已知冷、按完整预算等待
// 的那部分（O4 的 cold_fast_fail=false）。占比高说明路径级快速失败尚未覆盖主要
// 成本，是下一轮是否引入 cold_probe 预算的判据；未采集输出 n/a + 原因。
func coldProbeText(stats Stats) string {
	total := stats.ColdFirstProbe + stats.ColdRepeat
	if total <= 0 {
		return "n/a（窗口内无带 cold_fast_fail 的 no_fresh 请求；新构建落盘后开始采集）"
	}
	return ratioText(stats.ColdFirstProbe, total, "")
}

func coldProbeSamples(stats Stats) string {
	if stats.ColdFirstProbe+stats.ColdRepeat <= 0 {
		return "n=0（未采集，非缺失数据）"
	}
	return fmt.Sprintf("first %d / repeat %d（repeat 已由路径级快速失败覆盖）",
		stats.ColdFirstProbe, stats.ColdRepeat)
}

func coldProbeConclusion(stats Stats) string {
	if stats.ColdFirstProbe+stats.ColdRepeat <= 0 {
		return "待采集（需要 cold_fast_fail 事件字段）"
	}
	return "待标定（需人工判读）"
}

// fallbackSamples 描述 fallback 的分母构成；attempted 为 0 时不再打印
// "degraded 0 / attempted 0"（值本身是 n/a，样本行应解释原因）。
func fallbackSamples(stats Stats) string {
	if attempted := stats.Requests - stats.NoServer; attempted > 0 {
		return fmt.Sprintf("degraded %d / attempted %d（no_server %d、clean %d）",
			stats.Degraded, attempted, stats.NoServer, stats.Clean)
	}
	return fmt.Sprintf("no attempted requests（no_server %d）", stats.NoServer)
}

// coldStartText 输出冷启动延迟 P95（服务启动→首个诊断发布）；未采集输出 n/a + 原因。
func coldStartText(stats Stats) string {
	if stats.ColdFirstPublishP95MS == nil {
		return "n/a（窗口内无带 first_publish_ms 的 server 状态事件；新构建落盘后开始采集）"
	}
	return fmt.Sprintf("%d ms", *stats.ColdFirstPublishP95MS)
}

func coldStartSamples(stats Stats) string {
	if len(stats.coldStarts) == 0 {
		return "n=0（未采集，非缺失数据）"
	}
	p50 := "n/a"
	if stats.ColdFirstPublishP50MS != nil {
		p50 = fmt.Sprintf("%d ms", *stats.ColdFirstPublishP50MS)
	}
	return fmt.Sprintf("n=%d（P50 %s；按 (session, server) 取首个发布）", len(stats.coldStarts), p50)
}

func closureText(stats Stats) string {
	if stats.ClosureEligible <= 0 {
		return "n/a（窗口内无带 fingerprint 的 injected 请求）"
	}
	return ratioText(stats.ClosureClosed, stats.ClosureEligible, "")
}

func closureSamples(stats Stats) string {
	if stats.ClosureEligible <= 0 {
		return "eligible 0（未采集，非缺失数据）"
	}
	return fmt.Sprintf("closed %d / eligible %d（下一次同文件编辑为 clean）",
		stats.ClosureClosed, stats.ClosureEligible)
}

func closureConclusion(stats Stats) string {
	if stats.ClosureEligible <= 0 {
		return "待采集（需要 path/diag fingerprint 事件）"
	}
	return "待标定（需人工判读）"
}

// RenderMarkdown 渲染完整报告（章节与离线脚本同构，供 /lsp baseline 与后续
// web 端点复用）。
func RenderMarkdown(stats Stats) string {
	var builder strings.Builder
	builder.WriteString("## LSP 基线报告（自动生成，阈值待人工固化）\n\n")
	// 扫描量只在**真的扫了日志**时才有意义。数据面迁到分析库后（baselinesql），
	// SQL 源不读任何日志，Scan 恒为 0 —— 那时若照旧渲染就会输出"扫描文件：0 个"，
	// 看起来像故障，实质是"这条路本来就不扫日志"。所以按实际来源条件渲染。
	if stats.Scan.Files > 0 {
		fmt.Fprintf(&builder, "- 扫描文件：%d 个 runtime-events.jsonl，%d 行（损坏 %d 行，按窗口跳过 %d 行）\n",
			stats.Scan.Files, stats.Scan.Lines, stats.Scan.Malformed, stats.Scan.SkippedOld)
	} else {
		builder.WriteString("- 数据源：分析库（usage_lsp_requests / usage_lsp_first_publish）；不扫描日志，无扫描量\n")
	}
	first, last := "n/a", "n/a"
	if stats.FirstAt != "" {
		first = stats.FirstAt
	}
	if stats.LastAt != "" {
		last = stats.LastAt
	}
	fmt.Fprintf(&builder, "- 窗口：%s → %s；会话 %d 个；请求 %d 次\n", first, last, stats.Sessions, stats.Requests)
	builder.WriteString("- 读数由 `lsp.request.finished`（A 通道落盘）复算；未采集项输出 n/a，不渲染为 0；本报告不做阈值告警（ADR-0003 D4）\n\n")

	builder.WriteString("### §4.3 基线登记表（待人工回填 `结论/阈值`）\n\n")
	builder.WriteString("| 指标 | 基线值 | 采样窗口 | 样本量 | 结论/阈值 | 日期 |\n")
	builder.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, row := range Rows(stats) {
		fmt.Fprintf(&builder, "| `%s` | %s | %s | %s | %s | %s |\n",
			row.Metric, row.Value, row.Window, row.Samples, row.Conclusion, row.Date)
	}
	builder.WriteString("\n### 事实明细（不构成阈值判断）\n\n")
	fmt.Fprintf(&builder, "- 触发：%s\n", compactJSON(stats.Triggers))
	fmt.Fprintf(&builder, "- 结果：%s\n", compactJSON(stats.Outcomes))
	if n := stats.Outcomes["degraded"]; n > 0 {
		fmt.Fprintf(&builder, "- 未分类降级：%d 条（旧构建无 `reason` 字段或其他未知原因；计入 fallback 分子，但无法按原因细分）\n", n)
	}
	if len(stats.DegradeReasons) > 0 {
		fmt.Fprintf(&builder, "- 降级原因分布（新构建，低敏枚举）：%s\n", compactJSON(stats.DegradeReasons))
	}
	p50, p95 := "n/a", "n/a"
	if stats.LatencyP50MS != nil {
		p50 = fmt.Sprintf("%d", *stats.LatencyP50MS)
	}
	if stats.LatencyP95MS != nil {
		p95 = fmt.Sprintf("%d", *stats.LatencyP95MS)
	}
	fmt.Fprintf(&builder, "- 等待：P50 = %s ms，P95 = %s ms（n=%d）\n", p50, p95, len(stats.durations))
	fmt.Fprintf(&builder, "- 诊断：命中 %d 次（injected %d 次），累计条数 %d\n", stats.DiagHit, stats.Injected, stats.DiagCount)
	if stats.TotalDiagCount > 0 {
		fmt.Fprintf(&builder, "- 诊断新旧构成（O9/A6）：新增 %d / 全量 %d（scope=all 时全量即注入量）\n",
			stats.NewDiagCount, stats.TotalDiagCount)
	}
	fmt.Fprintf(&builder, "- 追加：累计 %d 字节；截断请求 %d 次（省略 %d 条 / %d 字符）\n",
		stats.AppendedBytes, stats.Truncated, stats.OmittedItems, stats.OmittedByChars)
	if stats.AppendedDiagBytes+stats.AppendedNoteBytes+stats.AppendedEmptyBytes > 0 {
		fmt.Fprintf(&builder, "- 追加拆分（O2，新构建样本）：诊断 %d / 提示 %d / 空块 %d 字节\n",
			stats.AppendedDiagBytes, stats.AppendedNoteBytes, stats.AppendedEmptyBytes)
	}
	if stats.ColdFirstProbe+stats.ColdRepeat > 0 {
		fmt.Fprintf(&builder, "- 冷路径探针（O4）：首探针 %d / 重复 %d（no_fresh 请求）\n",
			stats.ColdFirstProbe, stats.ColdRepeat)
	}
	if stats.MultiMemberRequests > 0 {
		fmt.Fprintf(&builder, "- 多成员请求（O5）：%d 次（最多尝试 %d 个成员）\n",
			stats.MultiMemberRequests, stats.AttemptedMembersMax)
	}
	fmt.Fprintf(&builder, "- 服务分布：%s\n", compactJSON(stats.Servers))
	fmt.Fprintf(&builder, "- 编辑调用（tool.completed 中的编辑类工具）：%d 次；可读回执字节 %d（%d 次回执含字节）\n\n",
		stats.EditCalls, stats.EditOutput, stats.EditOutputEvs)

	builder.WriteString("### 按日明细\n\n")
	builder.WriteString("| 日期 | 请求 | 注入 | 降级 | P95(ms) |\n")
	builder.WriteString("| --- | --- | --- | --- | --- |\n")
	days := make([]string, 0, len(stats.ByDay))
	for day := range stats.ByDay {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		bucket := stats.ByDay[day]
		p95Text := "n/a"
		if bucket.P95MS != nil {
			p95Text = fmt.Sprintf("%d", *bucket.P95MS)
		}
		fmt.Fprintf(&builder, "| %s | %d | %d | %d | %s |\n", day, bucket.Requests, bucket.Injected, bucket.Degraded, p95Text)
	}
	builder.WriteString("\n### 口径与限制\n\n")
	builder.WriteString("- 覆盖率分母是「LSP 活跃会话」内的编辑类工具调用次数；一次调用可能覆盖多文件（多行 request 事件），该比值是近似口径。\n")
	builder.WriteString("- `lsp_closure_ratio` 基于 path/diag fingerprint 计算：被注入的诊断集合在下一次同文件编辑后消失（下一次请求为 clean）才算闭环；无 fingerprint 样本输出 n/a。\n")
	builder.WriteString("- `lsp_append_bytes_ratio` 依赖编辑回执的 `output_model_visible_bytes`；回执未携带时标记 n/a（不猜分母）。\n")
	return builder.String()
}

// compactJSON 输出稳定、无空格的 JSON（键排序），供报告内嵌。
func compactJSON(counters map[string]int) string {
	if len(counters) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(counters))
	for key := range counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("{")
	for index, key := range keys {
		if index > 0 {
			builder.WriteString(", ")
		}
		fmt.Fprintf(&builder, "%q: %d", key, counters[key])
	}
	builder.WriteString("}")
	return builder.String()
}
