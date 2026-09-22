package usageanalytics

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// 诊断阈值（方案 §4 批次 1.3：常量集中定义便于调整）。
const (
	toolErrorRateWarningThreshold   = 0.10
	subagentFailureWarningThreshold = 0.20
	subagentTimeoutWarningMinCount  = 3
	maxToolStatsRows                = 200
	maxSubagentStatsRows            = 200
	maxErrorPatternRows             = 50
)

// ToolStatsQuery 工具维度查询过滤条件（会话/工具名/outcome/时间窗）。
type ToolStatsQuery struct {
	SessionID string
	ToolName  string
	Outcome   string
	From      time.Time
	To        time.Time
	Limit     int
}

// ToolStat 是按工具名聚合的一行。
type ToolStat struct {
	ToolName        string         `json:"tool_name"`
	Calls           int            `json:"calls"`
	Failures        int            `json:"failures"`
	FailureRate     float64        `json:"failure_rate"`
	EmptyResults    int            `json:"empty_results"`
	RetriedCalls    int            `json:"retried_calls"`
	AverageDuration int64          `json:"average_duration_ms"`
	MinDurationMS   int64          `json:"min_duration_ms"`
	MaxDurationMS   int64          `json:"max_duration_ms"`
	P50DurationMS   int64          `json:"p50_duration_ms"`
	P95DurationMS   int64          `json:"p95_duration_ms"`
	ErrorTop        []ErrorPattern `json:"error_top,omitempty"`
}

// ToolStatsResult 是工具维度查询响应。
type ToolStatsResult struct {
	SchemaVersion string     `json:"schema_version"`
	GeneratedAt   time.Time  `json:"generated_at"`
	Tools         []ToolStat `json:"tools"`
	Totals        ToolStat   `json:"totals"`
}

// SubagentStatsQuery 子代理维度查询过滤条件。
type SubagentStatsQuery struct {
	SessionID       string
	FailureCategory string
	FailedOnly      bool
	From            time.Time
	To              time.Time
	Limit           int
}

// SubagentStat 是单个子代理完成记录。
type SubagentStat struct {
	SubagentID       string    `json:"subagent_id"`
	ParentSessionID  string    `json:"parent_session_id"`
	ChildSessionID   string    `json:"child_session_id,omitempty"`
	Role             string    `json:"role,omitempty"`
	TaskType         string    `json:"task_type,omitempty"`
	TaskSubject      string    `json:"task_subject,omitempty"`
	Source           string    `json:"source,omitempty"`
	Success          *bool     `json:"success"`
	CompletionReason string    `json:"completion_reason"`
	FailureCategory  string    `json:"failure_category,omitempty"`
	ErrorCode        string    `json:"error_code,omitempty"`
	Attempt          int       `json:"attempt"`
	MaxAttempts      int       `json:"max_attempts"`
	RetryReason      string    `json:"retry_reason,omitempty"`
	DurationMS       int64     `json:"duration_ms"`
	UsageTotalTokens int64     `json:"usage_total_tokens"`
	ConflictCount    int       `json:"conflict_count"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`
}

// SubagentStatsSummary 是子代理聚合视图（失败率/分类分布/来源分布）。
type SubagentStatsSummary struct {
	Total             int            `json:"total"`
	Succeeded         int            `json:"succeeded"`
	Failed            int            `json:"failed"`
	Unknown           int            `json:"unknown"`
	FailureRate       float64        `json:"failure_rate"`
	Timeouts          int            `json:"timeouts"`
	Retried           int            `json:"retried"`
	FailureCategories map[string]int `json:"failure_categories"`
	Sources           map[string]int `json:"sources"`
}

// SubagentStatsResult 是子代理维度查询响应。
type SubagentStatsResult struct {
	SchemaVersion string               `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Summary       SubagentStatsSummary `json:"summary"`
	Subagents     []SubagentStat       `json:"subagents"`
}

// ErrorPatternsQuery 失败模式 Top-N 查询（来源：tools|subagents|requests|""）。
type ErrorPatternsQuery struct {
	SessionID string
	Source    string
	From      time.Time
	To        time.Time
	Top       int
}

// ErrorPattern 是一个错误码/失败分类的聚合行。
type ErrorPattern struct {
	ErrorCode       string `json:"error_code,omitempty"`
	FailureCategory string `json:"failure_category,omitempty"`
	Source          string `json:"source"`
	Count           int    `json:"count"`
}

// ErrorPatternsResult 是失败模式查询响应。
type ErrorPatternsResult struct {
	SchemaVersion string         `json:"schema_version"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Patterns      []ErrorPattern `json:"patterns"`
}

// ToolStats 按工具名聚合 usage_tool_calls（空库返回空数组而非错误）。
func (s *Store) ToolStats(q ToolStatsQuery) (ToolStatsResult, error) {
	result := ToolStatsResult{SchemaVersion: SchemaVersion, GeneratedAt: time.Now().UTC(), Tools: []ToolStat{}}
	where, args := toolStatsWhere(q)
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT tool_name,
       COUNT(*) AS calls,
       SUM(CASE WHEN ok = 0 OR outcome = 'failed' THEN 1 ELSE 0 END) AS failures,
       SUM(CASE WHEN empty_result = 1 OR outcome = 'empty' THEN 1 ELSE 0 END) AS empty_results,
       SUM(CASE WHEN retryable = 1 OR outcome = 'partial' THEN 1 ELSE 0 END) AS retried,
  SUM((%s)) AS total_duration,
  SUM(CASE WHEN (%s) > 0 THEN 1 ELSE 0 END) AS duration_samples
FROM usage_tool_calls
WHERE %s
GROUP BY tool_name
ORDER BY calls DESC, tool_name ASC
LIMIT ?`, effectiveToolDurationSQL, effectiveToolDurationSQL, where), append(args, normalizeLimit(q.Limit, maxToolStatsRows, maxToolStatsRows))...)
	if err != nil {
		return result, fmt.Errorf("query tool stats: %w", err)
	}
	if !ok {
		return result, nil
	}
	defer rows.Close()
	totals := ToolStat{ToolName: "all"}
	stats := make([]ToolStat, 0, 16)
	var totalsDurationSum, totalsDurationSamples int64
	for rows.Next() {
		var (
			stat                           ToolStat
			totalDuration, durationSamples int64
		)
		if err := rows.Scan(&stat.ToolName, &stat.Calls, &stat.Failures, &stat.EmptyResults, &stat.RetriedCalls, &totalDuration, &durationSamples); err != nil {
			return result, fmt.Errorf("scan tool stats: %w", err)
		}
		stat.FailureRate = ratio(stat.Failures, stat.Calls)
		if durationSamples > 0 {
			stat.AverageDuration = totalDuration / durationSamples
		}
		totalsDurationSum += totalDuration
		totalsDurationSamples += durationSamples
		stats = append(stats, stat)
		totals.Calls += stat.Calls
		totals.Failures += stat.Failures
		totals.EmptyResults += stat.EmptyResults
		totals.RetriedCalls += stat.RetriedCalls
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("scan tool stats: %w", err)
	}
	// 必须在发起后续查询前关闭游标：分析库是单连接池（SetMaxOpenConns(1)），
	// 持着未读空的 Rows 再 Query 会永久阻塞（分页查询同类问题已有注释）。
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("close tool stats rows: %w", err)
	}
	// Phase 2 收尾：列表行补齐百分位与错误 Top。用 2 次批量查询覆盖全部工具，
	// 避免 N 个工具 × 2 次额外查询（单连接 SQLite 下的 N+2 阻塞问题）；
	// 单工具详情（ToolStatsDetail）保留，供按需刷新与深度视图。
	durationsByTool, err := s.toolDurationSamples(q)
	if err != nil {
		return result, fmt.Errorf("query tool duration samples: %w", err)
	}
	errorTopByTool, err := s.toolErrorTopByTool(q, toolErrorTopLimit)
	if err != nil {
		return result, fmt.Errorf("query tool error tops: %w", err)
	}
	// 耗时样本按工具分组且组内升序：首尾即最小/最大，无需再扫库。
	allDurationSamples := make([]int64, 0, 64)
	for index := range stats {
		if samples := durationsByTool[stats[index].ToolName]; len(samples) > 0 {
			stats[index].MinDurationMS = samples[0]
			stats[index].MaxDurationMS = samples[len(samples)-1]
			stats[index].P50DurationMS = percentile(samples, 0.50)
			stats[index].P95DurationMS = percentile(samples, 0.95)
			allDurationSamples = append(allDurationSamples, samples...)
		}
		if top := errorTopByTool[stats[index].ToolName]; len(top) > 0 {
			stats[index].ErrorTop = top
		}
	}
	result.Tools = stats
	totals.FailureRate = ratio(totals.Failures, totals.Calls)
	// totals 耗时口径与行内一致：平均用加权样本；min/max/百分位用合并样本。
	// 此前 totals 从不累计耗时，页头「P95 耗时」恒为 0——这里一并修正。
	if totalsDurationSamples > 0 {
		totals.AverageDuration = totalsDurationSum / totalsDurationSamples
	}
	if len(allDurationSamples) > 0 {
		sort.Slice(allDurationSamples, func(i, j int) bool { return allDurationSamples[i] < allDurationSamples[j] })
		totals.MinDurationMS = allDurationSamples[0]
		totals.MaxDurationMS = allDurationSamples[len(allDurationSamples)-1]
		totals.P50DurationMS = percentile(allDurationSamples, 0.50)
		totals.P95DurationMS = percentile(allDurationSamples, 0.95)
	}
	result.Totals = totals
	return result, nil
}

// toolErrorTopLimit 列表视图每个工具保留的错误码条数（与 ToolStatsDetail 的 Top-3 对齐）。
const toolErrorTopLimit = 3

// effectiveToolDurationSQL 是耗时样本的统一口径：优先工具自报的 duration_ms；
// 缺失（0）时回退到 tool.requested/tool.completed 事件时间差，与实时 bridge 的
// 墙钟口径一致。未自报耗时的工具（ls/view/grep…）与历史行因此也能参与
// avg/min/max/p50/p95，而不是被 duration_ms > 0 整行过滤掉。
const effectiveToolDurationSQL = `MAX(COALESCE(NULLIF(duration_ms, 0), (completed_at_unix_nano - started_at_unix_nano) / 1000000), 0)`

// toolDurationSamples 一次查询取回过滤窗口内全部工具的耗时样本：
// 按 tool_name 分组、组内 duration_ms 升序（percentile 要求有序输入）。
func (s *Store) toolDurationSamples(q ToolStatsQuery) (map[string][]int64, error) {
	where, args := toolStatsWhere(q)
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT tool_name, (%s) AS duration FROM usage_tool_calls
WHERE %s AND (%s) > 0
ORDER BY tool_name ASC, duration ASC`, effectiveToolDurationSQL, where, effectiveToolDurationSQL), args...)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	defer rows.Close()
	samples := map[string][]int64{}
	for rows.Next() {
		var toolName string
		var duration int64
		if err := rows.Scan(&toolName, &duration); err != nil {
			return nil, err
		}
		samples[toolName] = append(samples[toolName], duration)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

// toolErrorTopByTool 一次查询取回全部工具的错误码聚合，
// 在 Go 内按 (COUNT DESC, error_code ASC) 截断为每工具 Top-N。
func (s *Store) toolErrorTopByTool(q ToolStatsQuery, top int) (map[string][]ErrorPattern, error) {
	if top <= 0 {
		top = toolErrorTopLimit
	}
	where, args := toolStatsWhere(q)
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT tool_name, error_code, COUNT(*) FROM usage_tool_calls
WHERE %s AND (ok = 0 OR outcome = 'failed') AND error_code <> ''
GROUP BY tool_name, error_code
ORDER BY tool_name ASC, COUNT(*) DESC, error_code ASC`, where), args...)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	defer rows.Close()
	patterns := map[string][]ErrorPattern{}
	for rows.Next() {
		var toolName, code string
		var count int
		if err := rows.Scan(&toolName, &code, &count); err != nil {
			return nil, err
		}
		if len(patterns[toolName]) >= top {
			continue
		}
		patterns[toolName] = append(patterns[toolName], ErrorPattern{
			Source:          "tools",
			ErrorCode:       strings.TrimSpace(code),
			FailureCategory: llm.FailureCategoryFromErrorCode(code),
			Count:           count,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return patterns, nil
}

// ToolStatsDetail 返回单个工具的百分位耗时与错误码 Top-N。
// 仅当用户在详情视图中展开具体工具时调用，不参与首屏聚合。
func (s *Store) ToolStatsDetail(q ToolStatsQuery, toolName string) (ToolStat, error) {
	stat := ToolStat{ToolName: toolName}
	where, args := toolStatsWhere(q)
	args = append(args, toolName)
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT
  COUNT(*) AS calls,
  SUM(CASE WHEN ok = 0 OR outcome = 'failed' THEN 1 ELSE 0 END) AS failures,
  SUM(CASE WHEN empty_result = 1 OR outcome = 'empty' THEN 1 ELSE 0 END) AS empty_results,
  SUM(CASE WHEN retryable = 1 OR outcome = 'partial' THEN 1 ELSE 0 END) AS retried,
  SUM((%s)) AS total_duration,
  SUM(CASE WHEN (%s) > 0 THEN 1 ELSE 0 END) AS duration_samples,
  COALESCE(MIN(NULLIF((%s), 0)), 0) AS min_duration,
  COALESCE(MAX((%s)), 0) AS max_duration
FROM usage_tool_calls
WHERE %s AND tool_name = ?
GROUP BY tool_name`, effectiveToolDurationSQL, effectiveToolDurationSQL, effectiveToolDurationSQL, effectiveToolDurationSQL, where), args...)
	if err != nil {
		return stat, fmt.Errorf("query tool stats detail: %w", err)
	}
	if !ok {
		return stat, nil
	}
	defer rows.Close()
	if rows.Next() {
		var totalDuration, durationSamples int64
		if err := rows.Scan(&stat.Calls, &stat.Failures, &stat.EmptyResults, &stat.RetriedCalls, &totalDuration, &durationSamples, &stat.MinDurationMS, &stat.MaxDurationMS); err != nil {
			return stat, fmt.Errorf("scan tool stats detail: %w", err)
		}
		stat.FailureRate = ratio(stat.Failures, stat.Calls)
		if durationSamples > 0 {
			stat.AverageDuration = totalDuration / durationSamples
		}
	}
	if err := rows.Err(); err != nil {
		return stat, fmt.Errorf("scan tool stats detail: %w", err)
	}
	// 必须在后续查询（toolDurations/toolErrorTop）前关闭游标：写库是单连接池
	//（store.go SetMaxOpenConns(1)），持着未读空的 Rows 再 Query 会永久阻塞。
	if err := rows.Close(); err != nil {
		return stat, fmt.Errorf("close tool stats detail rows: %w", err)
	}
	if durations, ok, err := s.toolDurations(q, toolName); err == nil && ok && len(durations) > 0 {
		stat.P50DurationMS = percentile(durations, 0.50)
		stat.P95DurationMS = percentile(durations, 0.95)
	}
	if top, err := s.toolErrorTop(q, toolName, 3); err == nil {
		stat.ErrorTop = top
	}
	return stat, nil
}

// SubagentStats 聚合 usage_subagents（失败率、分类分布、来源分布）。
func (s *Store) SubagentStats(q SubagentStatsQuery) (SubagentStatsResult, error) {
	result := SubagentStatsResult{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Summary: SubagentStatsSummary{
			FailureCategories: map[string]int{},
			Sources:           map[string]int{},
		},
		Subagents: []SubagentStat{},
	}
	where, args := subagentStatsWhere(q)
	// task_type / task_subject 是 v7 增量列：只读旧库缺列时表达式退化为空串常量
	// （与 usage_routes 的 routeColumnExpr 同策略），而不是让整个 stats 查询报错。
	taskTypeExpr := s.columnExpr("usage_subagents", "task_type")
	taskSubjectExpr := s.columnExpr("usage_subagents", "task_subject")
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT subagent_id, parent_session_id, child_session_id, role, %s, %s, source, success, completion_reason,
       failure_category, error_code, attempt, max_attempts, retry_reason, duration_ms,
       usage_total_tokens, conflict_count, completed_at_unix_nano
FROM usage_subagents
WHERE %s
ORDER BY completed_at_unix_nano DESC, subagent_id ASC
LIMIT ?`, taskTypeExpr, taskSubjectExpr, where), append(args, normalizeLimit(q.Limit, maxSubagentStatsRows, maxSubagentStatsRows))...)
	if err != nil {
		return result, fmt.Errorf("query subagent stats: %w", err)
	}
	if !ok {
		return result, nil
	}
	defer rows.Close()
	for rows.Next() {
		var (
			stat          SubagentStat
			successFlag   *int
			completedNano int64
		)
		if err := rows.Scan(
			&stat.SubagentID, &stat.ParentSessionID, &stat.ChildSessionID, &stat.Role, &stat.TaskType, &stat.TaskSubject, &stat.Source,
			&successFlag, &stat.CompletionReason, &stat.FailureCategory, &stat.ErrorCode,
			&stat.Attempt, &stat.MaxAttempts, &stat.RetryReason, &stat.DurationMS,
			&stat.UsageTotalTokens, &stat.ConflictCount, &completedNano,
		); err != nil {
			return result, fmt.Errorf("scan subagent stats: %w", err)
		}
		if successFlag != nil {
			flag := *successFlag == 1
			stat.Success = &flag
		}
		stat.CompletedAt = timeFromUnixNano(completedNano)
		result.Subagents = append(result.Subagents, stat)

		result.Summary.Total++
		switch {
		case stat.Success == nil:
			result.Summary.Unknown++
		case *stat.Success:
			result.Summary.Succeeded++
		default:
			result.Summary.Failed++
		}
		if category := strings.TrimSpace(stat.FailureCategory); category != "" {
			result.Summary.FailureCategories[category]++
			if category == llm.FailureCategoryTimeout {
				result.Summary.Timeouts++
			}
		}
		if source := strings.TrimSpace(stat.Source); source != "" {
			result.Summary.Sources[source]++
		}
		if stat.Attempt > 1 {
			result.Summary.Retried++
		}
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("scan subagent stats: %w", err)
	}
	result.Summary.FailureRate = ratio(result.Summary.Failed, result.Summary.Succeeded+result.Summary.Failed)
	return result, nil
}

// ErrorPatterns 返回 error_code / failure_category Top-N（跨工具、子代理、LLM 请求）。
func (s *Store) ErrorPatterns(q ErrorPatternsQuery) (ErrorPatternsResult, error) {
	result := ErrorPatternsResult{SchemaVersion: SchemaVersion, GeneratedAt: time.Now().UTC(), Patterns: []ErrorPattern{}}
	top := normalizeLimit(q.Top, 10, maxErrorPatternRows)
	counts := map[string]ErrorPattern{}
	add := func(pattern ErrorPattern) {
		if strings.TrimSpace(pattern.ErrorCode) == "" && strings.TrimSpace(pattern.FailureCategory) == "" {
			return
		}
		key := pattern.Source + "\x00" + pattern.ErrorCode + "\x00" + pattern.FailureCategory
		existing := counts[key]
		existing.Count += pattern.Count
		existing.Source = pattern.Source
		existing.ErrorCode = pattern.ErrorCode
		existing.FailureCategory = pattern.FailureCategory
		counts[key] = existing
	}

	source := strings.ToLower(strings.TrimSpace(q.Source))
	if source == "" || source == "tools" {
		where, args := errorPatternToolWhere(q)
		rows, ok, err := s.query(fmt.Sprintf(`
SELECT error_code, COUNT(*) FROM usage_tool_calls
WHERE %s AND (error_code <> '' OR outcome = 'failed')
GROUP BY error_code`, where), args...)
		if err != nil {
			return result, fmt.Errorf("query tool error patterns: %w", err)
		}
		if ok {
			for rows.Next() {
				var code string
				var count int
				if err := rows.Scan(&code, &count); err != nil {
					rows.Close()
					return result, fmt.Errorf("scan tool error patterns: %w", err)
				}
				add(ErrorPattern{Source: "tools", ErrorCode: strings.TrimSpace(code), FailureCategory: llm.FailureCategoryFromErrorCode(code), Count: count})
			}
			rows.Close()
		}
	}
	if source == "" || source == "subagents" {
		where, args := errorPatternSubagentWhere(q)
		rows, ok, err := s.query(fmt.Sprintf(`
SELECT error_code, failure_category, COUNT(*) FROM usage_subagents
WHERE %s AND (failure_category <> '' OR error_code <> '')
GROUP BY error_code, failure_category`, where), args...)
		if err != nil {
			return result, fmt.Errorf("query subagent error patterns: %w", err)
		}
		if ok {
			for rows.Next() {
				var code, category string
				var count int
				if err := rows.Scan(&code, &category, &count); err != nil {
					rows.Close()
					return result, fmt.Errorf("scan subagent error patterns: %w", err)
				}
				add(ErrorPattern{Source: "subagents", ErrorCode: strings.TrimSpace(code), FailureCategory: strings.TrimSpace(category), Count: count})
			}
			rows.Close()
		}
	}
	if source == "" || source == "requests" {
		where, args := errorPatternRequestWhere(q)
		rows, ok, err := s.query(fmt.Sprintf(`
SELECT error_category, COUNT(*) FROM usage_requests
WHERE %s AND error_category <> ''
GROUP BY error_category`, where), args...)
		if err != nil {
			return result, fmt.Errorf("query request error patterns: %w", err)
		}
		if ok {
			for rows.Next() {
				var category string
				var count int
				if err := rows.Scan(&category, &count); err != nil {
					rows.Close()
					return result, fmt.Errorf("scan request error patterns: %w", err)
				}
				add(ErrorPattern{Source: "requests", FailureCategory: strings.TrimSpace(category), Count: count})
			}
			rows.Close()
		}
	}

	patterns := make([]ErrorPattern, 0, len(counts))
	for _, pattern := range counts {
		patterns = append(patterns, pattern)
	}
	sort.Slice(patterns, func(i, j int) bool {
		if patterns[i].Count != patterns[j].Count {
			return patterns[i].Count > patterns[j].Count
		}
		if patterns[i].Source != patterns[j].Source {
			return patterns[i].Source < patterns[j].Source
		}
		return patterns[i].ErrorCode < patterns[j].ErrorCode
	})
	if len(patterns) > top {
		patterns = patterns[:top]
	}
	result.Patterns = patterns
	return result, nil
}

// enrichRollupsWithV2 为会话列表补齐工具/子代理/恢复维度（仅当存在新表时）。
// 采用按页批量查询，避免逐会话 N+1。
func (s *Store) enrichRollupsWithV2(rollups []SessionRollup) {
	if len(rollups) == 0 {
		return
	}
	ids := make([]string, 0, len(rollups))
	index := make(map[string]int, len(rollups))
	for i := range rollups {
		id := strings.TrimSpace(rollups[i].SessionID)
		if id == "" {
			continue
		}
		ids = append(ids, id)
		index[id] = i
	}
	if len(ids) == 0 {
		return
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}

	if rows, ok, err := s.query(fmt.Sprintf(`
SELECT session_id, COUNT(*), SUM(CASE WHEN ok = 0 OR outcome = 'failed' THEN 1 ELSE 0 END)
FROM usage_tool_calls WHERE session_id IN (%s) GROUP BY session_id`, placeholders), args...); err == nil && ok {
		for rows.Next() {
			var sessionID string
			var calls, failures int
			if err := rows.Scan(&sessionID, &calls, &failures); err != nil {
				break
			}
			if position, found := index[sessionID]; found {
				rollups[position].ToolCallsObserved = calls
				rollups[position].ToolFailures = failures
				rollups[position].ToolFailureRate = ratio(failures, calls)
			}
		}
		rows.Close()
	}

	if rows, ok, err := s.query(fmt.Sprintf(`
SELECT parent_session_id, COUNT(*),
       SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END),
       SUM(CASE WHEN failure_category = ? THEN 1 ELSE 0 END)
FROM usage_subagents WHERE parent_session_id IN (%s) GROUP BY parent_session_id`, placeholders),
		append([]interface{}{llm.FailureCategoryTimeout}, args...)...); err == nil && ok {
		for rows.Next() {
			var sessionID string
			var runs, failures, timeouts int
			if err := rows.Scan(&sessionID, &runs, &failures, &timeouts); err != nil {
				break
			}
			if position, found := index[sessionID]; found {
				rollups[position].SubagentRuns = runs
				rollups[position].SubagentFailures = failures
				rollups[position].SubagentTimeouts = timeouts
				rollups[position].SubagentFailureRate = ratio(failures, runs)
			}
		}
		rows.Close()
	}

	if rows, ok, err := s.query(fmt.Sprintf(`
SELECT session_id, SUM(recovered_tool_error_count)
FROM usage_turns WHERE session_id IN (%s) GROUP BY session_id`, placeholders), args...); err == nil && ok {
		for rows.Next() {
			var sessionID string
			var recovered int
			if err := rows.Scan(&sessionID, &recovered); err != nil {
				break
			}
			if position, found := index[sessionID]; found {
				rollups[position].RetryRecoveredTurns = recovered
			}
		}
		rows.Close()
	}
}

// enrichTurnsWithV2 用 usage_turns 的工具失败/恢复计数补齐回合行。
func (s *Store) enrichTurnsWithV2(sessionID string, turns []TurnUsage) {
	if len(turns) == 0 {
		return
	}
	index := make(map[string]int, len(turns))
	for i := range turns {
		index[strings.TrimSpace(turns[i].TurnID)] = i
	}
	rows, ok, err := s.query(`
SELECT turn_id, tool_error_count, recovered_tool_error_count, unrecovered_tool_error_count
FROM usage_turns WHERE session_id = ?`, sessionID)
	if err != nil || !ok {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var turnID string
		var toolErrors, recovered, unrecovered int
		if err := rows.Scan(&turnID, &toolErrors, &recovered, &unrecovered); err != nil {
			return
		}
		if position, found := index[strings.TrimSpace(turnID)]; found {
			turns[position].ToolErrors = toolErrors
			turns[position].ToolResultsObserved = toolErrors
			turns[position].RecoveredToolErrors = recovered
			turns[position].UnrecoveredToolErrors = unrecovered
		}
	}
}

// buildV2Diagnostics 在既有诊断之上追加 schema v2 诊断码（阈值集中定义）。
func buildV2Diagnostics(rollup SessionRollup, hasV2Data bool) []Diagnostic {
	if !hasV2Data {
		return nil
	}
	diagnostics := make([]Diagnostic, 0, 6)
	if rollup.ToolFailures > 0 {
		severity := "info"
		if rollup.ToolFailureRate >= toolErrorRateWarningThreshold {
			severity = "warning"
		}
		diagnostics = append(diagnostics, Diagnostic{Code: "tool_failures", Severity: severity, Count: rollup.ToolFailures, Rate: rollup.ToolFailureRate})
	}
	if rollup.ToolCallsObserved > 0 && rollup.ToolFailureRate >= toolErrorRateWarningThreshold {
		diagnostics = append(diagnostics, Diagnostic{Code: "tool_error_rate_high", Severity: "warning", Count: rollup.ToolFailures, Rate: rollup.ToolFailureRate})
	}
	if rollup.SubagentFailures > 0 {
		severity := "info"
		if rollup.SubagentFailureRate >= subagentFailureWarningThreshold {
			severity = "warning"
		}
		diagnostics = append(diagnostics, Diagnostic{Code: "subagent_failures", Severity: severity, Count: rollup.SubagentFailures, Rate: rollup.SubagentFailureRate})
	}
	if rollup.SubagentRuns > 0 && rollup.SubagentFailureRate >= subagentFailureWarningThreshold {
		diagnostics = append(diagnostics, Diagnostic{Code: "subagent_failure_rate_high", Severity: "warning", Count: rollup.SubagentFailures, Rate: rollup.SubagentFailureRate})
	}
	if rollup.SubagentTimeouts >= subagentTimeoutWarningMinCount {
		diagnostics = append(diagnostics, Diagnostic{Code: "subagent_timeout", Severity: "warning", Count: rollup.SubagentTimeouts})
	}
	if rollup.RetryRecoveredTurns > 0 {
		diagnostics = append(diagnostics, Diagnostic{Code: "retry_recovered", Severity: "info", Count: rollup.RetryRecoveredTurns})
	}
	return diagnostics
}

// ---------------------------------------------------------------------------
// SQL 过滤构造与工具函数
// ---------------------------------------------------------------------------

func toolStatsWhere(q ToolStatsQuery) (string, []interface{}) {
	clauses := []string{"1=1"}
	args := []interface{}{}
	if sessionID := strings.TrimSpace(q.SessionID); sessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, sessionID)
	}
	if toolName := strings.TrimSpace(q.ToolName); toolName != "" {
		clauses = append(clauses, "tool_name = ?")
		args = append(args, toolName)
	}
	if outcome := strings.TrimSpace(q.Outcome); outcome != "" {
		clauses = append(clauses, "outcome = ?")
		args = append(args, outcome)
	}
	if !q.From.IsZero() {
		clauses = append(clauses, "started_at_unix_nano >= ?")
		args = append(args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "started_at_unix_nano <= ?")
		args = append(args, q.To.UnixNano())
	}
	return strings.Join(clauses, " AND "), args
}

func subagentStatsWhere(q SubagentStatsQuery) (string, []interface{}) {
	clauses := []string{"1=1"}
	args := []interface{}{}
	if sessionID := strings.TrimSpace(q.SessionID); sessionID != "" {
		clauses = append(clauses, "parent_session_id = ?")
		args = append(args, sessionID)
	}
	if category := llm.NormalizeFailureCategory(q.FailureCategory); category != "" && category != llm.FailureCategoryUnknown {
		clauses = append(clauses, "failure_category = ?")
		args = append(args, category)
	}
	if q.FailedOnly {
		clauses = append(clauses, "success = 0")
	}
	if !q.From.IsZero() {
		clauses = append(clauses, "completed_at_unix_nano >= ?")
		args = append(args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "completed_at_unix_nano <= ?")
		args = append(args, q.To.UnixNano())
	}
	return strings.Join(clauses, " AND "), args
}

func errorPatternToolWhere(q ErrorPatternsQuery) (string, []interface{}) {
	clauses := []string{"1=1"}
	args := []interface{}{}
	if sessionID := strings.TrimSpace(q.SessionID); sessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, sessionID)
	}
	if !q.From.IsZero() {
		clauses = append(clauses, "started_at_unix_nano >= ?")
		args = append(args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "started_at_unix_nano <= ?")
		args = append(args, q.To.UnixNano())
	}
	return strings.Join(clauses, " AND "), args
}

func errorPatternSubagentWhere(q ErrorPatternsQuery) (string, []interface{}) {
	clauses := []string{"1=1"}
	args := []interface{}{}
	if sessionID := strings.TrimSpace(q.SessionID); sessionID != "" {
		clauses = append(clauses, "parent_session_id = ?")
		args = append(args, sessionID)
	}
	if !q.From.IsZero() {
		clauses = append(clauses, "completed_at_unix_nano >= ?")
		args = append(args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "completed_at_unix_nano <= ?")
		args = append(args, q.To.UnixNano())
	}
	return strings.Join(clauses, " AND "), args
}

func errorPatternRequestWhere(q ErrorPatternsQuery) (string, []interface{}) {
	clauses := []string{"1=1"}
	args := []interface{}{}
	if sessionID := strings.TrimSpace(q.SessionID); sessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, sessionID)
	}
	if !q.From.IsZero() {
		clauses = append(clauses, "started_at_unix_nano >= ?")
		args = append(args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "started_at_unix_nano <= ?")
		args = append(args, q.To.UnixNano())
	}
	return strings.Join(clauses, " AND "), args
}

// toolDurations 读取某工具在过滤条件下的耗时样本（上限保护）。
func (s *Store) toolDurations(q ToolStatsQuery, toolName string) ([]int64, bool, error) {
	scoped := q
	scoped.ToolName = toolName
	where, args := toolStatsWhere(scoped)
	args = append(args, 2000)
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT duration_ms FROM usage_tool_calls
WHERE %s AND duration_ms > 0 ORDER BY duration_ms ASC LIMIT ?`, where), args...)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	defer rows.Close()
	durations := make([]int64, 0, 64)
	for rows.Next() {
		var duration int64
		if err := rows.Scan(&duration); err != nil {
			return nil, false, err
		}
		durations = append(durations, duration)
	}
	return durations, true, nil
}

// toolErrorTop 读取某工具的错误码 Top-N。
func (s *Store) toolErrorTop(q ToolStatsQuery, toolName string, top int) ([]ErrorPattern, error) {
	scoped := q
	scoped.ToolName = toolName
	where, args := toolStatsWhere(scoped)
	args = append(args, normalizeLimit(top, 3, 10))
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT error_code, COUNT(*) FROM usage_tool_calls
WHERE %s AND (ok = 0 OR outcome = 'failed') AND error_code <> ''
GROUP BY error_code ORDER BY COUNT(*) DESC, error_code ASC LIMIT ?`, where), args...)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	defer rows.Close()
	patterns := make([]ErrorPattern, 0, top)
	for rows.Next() {
		var code string
		var count int
		if err := rows.Scan(&code, &count); err != nil {
			return nil, err
		}
		patterns = append(patterns, ErrorPattern{
			Source:          "tools",
			ErrorCode:       strings.TrimSpace(code),
			FailureCategory: llm.FailureCategoryFromErrorCode(code),
			Count:           count,
		})
	}
	return patterns, nil
}

func ratio(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

// percentile 返回已排序样本的 p 分位（最近秩法，样本为空返回 0）。
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	index := int(float64(len(sorted)) * p)
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
