package usageanalytics

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	defaultListLimit    = 50
	maxListLimit        = 200
	defaultSummaryLimit = 100
	maxSummaryLimit     = 500
	// maxDimensionValues 单个维度 DISTINCT 取值上限（防止异常库把 UI 撑爆）。
	maxDimensionValues = 1000
	// maxSessionSteps 单会话明细上限；超出标记 partial 证据不足。
	maxSessionSteps = 20000
)

// notFoundError 会话在分析库中不存在（既无会话行也无可归属请求）。
type notFoundError struct {
	sessionID string
}

func (e notFoundError) Error() string {
	return "analytics session not found: " + e.sessionID
}

// ErrNotFound 会话不存在（IsNotFound 兼容旧调用点）。
var ErrNotFound = errors.New("usage_analytics_session_not_found")

// IsNotFound 报告错误是否为"会话不存在"。
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotFound) {
		return true
	}
	var nf notFoundError
	return errors.As(err, &nf)
}

func errSessionNotFound(sessionID string) error {
	return notFoundError{sessionID: strings.TrimSpace(sessionID)}
}

// ---------------------------------------------------------------------------
// WHERE 构造：会话级过滤（时间窗按会话开始时间；DB 查询不做全库扫描）。
// ---------------------------------------------------------------------------

// sessionStartExpr 会话开始时间：优先会话行，其次首条请求，最后 0。
const sessionStartExpr = "COALESCE(NULLIF(s.started_at_unix_nano, 0), NULLIF(req.first_started, 0), 0)"

// sessionLastExpr 会话最后活动时间：MAX(会话 updated/ended, 末条请求时间)。
const sessionLastExpr = "MAX(COALESCE(s.updated_at_unix_nano, 0), COALESCE(s.ended_at_unix_nano, 0), COALESCE(req.last_started, 0))"

// statsSessionStartExpr / statsSessionLastExpr 是 schema v3 预聚合列的等价表达式。
const (
	statsSessionStartExpr = "COALESCE(NULLIF(s.started_at_unix_nano, 0), NULLIF(s.c_first_started_at, 0), 0)"
	statsSessionLastExpr  = "MAX(COALESCE(s.updated_at_unix_nano, 0), COALESCE(s.ended_at_unix_nano, 0), COALESCE(s.c_last_started_at, 0))"
)

type whereClause struct {
	sql  string
	args []interface{}
}

// buildWhere 构造过滤条件；sessionStart 为会话开始时间表达式
// （schema v3 与旧聚合两种来源的占位方式不同）。
func buildWhere(q Query, sessionStart string) whereClause {
	if strings.TrimSpace(sessionStart) == "" {
		sessionStart = sessionStartExpr
	}
	clause := whereClause{sql: "1=1"}
	if !q.From.IsZero() {
		clause.sql += " AND " + sessionStart + " >= ?"
		clause.args = append(clause.args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		clause.sql += " AND " + sessionStart + " < ?"
		clause.args = append(clause.args, q.To.UnixNano())
	}
	if provider := strings.TrimSpace(q.Provider); provider != "" {
		clause.sql += " AND s.provider <> '' AND LOWER(s.provider) = LOWER(?)"
		clause.args = append(clause.args, provider)
	}
	if model := strings.TrimSpace(q.Model); model != "" {
		clause.sql += " AND s.model <> '' AND LOWER(s.model) = LOWER(?)"
		clause.args = append(clause.args, model)
	}
	if status := strings.TrimSpace(q.Status); status != "" {
		clause.sql += " AND LOWER(s.status) = LOWER(?)"
		clause.args = append(clause.args, status)
	}
	if directory := strings.TrimSpace(q.Directory); directory != "" {
		clause.sql += " AND (s.working_directory = ? OR s.working_directory LIKE ? ESCAPE '\\')"
		clause.args = append(clause.args, directory, "%"+escapeLike(directory)+"%")
	}
	if project := strings.TrimSpace(q.Project); project != "" {
		clause.sql += " AND (s.project_path = ? OR s.project_path LIKE ? ESCAPE '\\' OR s.working_directory = ? OR s.working_directory LIKE ? ESCAPE '\\')"
		clause.args = append(clause.args, project, "%"+escapeLike(project)+"%", project, "%"+escapeLike(project)+"%")
	}
	if text := strings.TrimSpace(q.Query); text != "" {
		pattern := "%" + escapeLike(text) + "%"
		clause.sql += " AND (s.session_id LIKE ? ESCAPE '\\' OR s.title LIKE ? ESCAPE '\\' OR s.provider LIKE ? ESCAPE '\\'" +
			" OR s.model LIKE ? ESCAPE '\\' OR s.working_directory LIKE ? ESCAPE '\\' OR s.project_path LIKE ? ESCAPE '\\')"
		clause.args = append(clause.args, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	return clause
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}

// sessionSelect 是全部列表/汇总查询共用的会话聚合 SELECT（会话行 LEFT JOIN 请求聚合）。
const sessionSelect = `WITH req AS (
  SELECT session_id,
         COUNT(*) AS total_requests,
         SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) AS llm_successes,
         SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END) AS llm_errors,
         SUM(CASE WHEN usage_available = 1 THEN 1 ELSE 0 END) AS requests_with_usage,
         COALESCE(SUM(total_tokens), 0) AS total_tokens,
         COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
         COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
         COALESCE(SUM(cache_read_tokens), 0) AS cached_tokens,
         COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
         COALESCE(SUM(duration_ms), 0) AS total_duration_ms,
         COALESCE(CAST(AVG(NULLIF(duration_ms, 0)) AS INTEGER), 0) AS avg_duration_ms,
         MIN(NULLIF(started_at_unix_nano, 0)) AS first_started,
         MAX(started_at_unix_nano) AS last_started,
         COUNT(DISTINCT COALESCE(NULLIF(trace_id, ''), NULLIF(turn_id, ''), llm_request_id)) AS turn_count,
         COUNT(DISTINCT CASE WHEN success = 0 THEN COALESCE(NULLIF(trace_id, ''), NULLIF(turn_id, ''), llm_request_id) END) AS failed_turns
  FROM usage_requests
  GROUP BY session_id
)
SELECT
  s.session_id,
  s.title,
  s.project_path,
  s.working_directory,
  s.provider,
  s.model,
  s.protocol,
  s.status,
  COALESCE(s.started_at_unix_nano, 0),
  COALESCE(s.ended_at_unix_nano, 0),
  COALESCE(s.updated_at_unix_nano, 0),
  COALESCE(req.total_requests, 0) AS c_total_requests,
  COALESCE(req.llm_successes, 0) AS c_llm_successes,
  COALESCE(req.llm_errors, 0) AS c_llm_errors,
  COALESCE(req.requests_with_usage, 0) AS c_requests_with_usage,
  COALESCE(req.total_tokens, 0) AS c_total_tokens,
  COALESCE(req.prompt_tokens, 0) AS c_prompt_tokens,
  COALESCE(req.completion_tokens, 0) AS c_completion_tokens,
  COALESCE(req.cached_tokens, 0) AS c_cached_tokens,
  COALESCE(req.reasoning_tokens, 0) AS c_reasoning_tokens,
  COALESCE(req.total_duration_ms, 0) AS c_total_duration_ms,
  COALESCE(req.avg_duration_ms, 0) AS c_avg_duration_ms,
  COALESCE(req.turn_count, 0) AS c_turn_count,
  COALESCE(req.failed_turns, 0) AS c_failed_turns,
  ` + sessionStartExpr + ` AS session_start,
  ` + sessionLastExpr + ` AS session_last
FROM usage_sessions s
LEFT JOIN req ON req.session_id = s.session_id
WHERE `

// sessionStatsSelect 是 schema v3 的单表会话统计 SELECT（§5.4）：
// 不再聚合 usage_requests，列顺序与 sessionSelect 完全一致，scanSessionRows 共用。
const sessionStatsSelect = `SELECT
  s.session_id,
  s.title,
  s.project_path,
  s.working_directory,
  s.provider,
  s.model,
  s.protocol,
  s.status,
  COALESCE(s.started_at_unix_nano, 0),
  COALESCE(s.ended_at_unix_nano, 0),
  COALESCE(s.updated_at_unix_nano, 0),
  s.c_total_requests,
  s.c_llm_successes,
  s.c_llm_errors,
  s.c_requests_with_usage,
  s.c_total_tokens,
  s.c_prompt_tokens,
  s.c_completion_tokens,
  s.c_cached_tokens,
  s.c_reasoning_tokens,
  s.c_total_duration_ms,
  CASE WHEN s.c_duration_samples > 0 THEN s.c_total_duration_ms / s.c_duration_samples ELSE 0 END AS c_avg_duration_ms,
  s.c_turn_count,
  s.c_failed_turns,
  ` + statsSessionStartExpr + ` AS session_start,
  ` + statsSessionLastExpr + ` AS session_last
FROM usage_sessions s
WHERE `

// sessionQuerySource 是读路径选源结果：按 schema/逃生开关二选一（§5.4/§10）。
type sessionQuerySource struct {
	selectSQL string
	startExpr string
}

func (s *Store) sessionQuerySource() sessionQuerySource {
	if s.statsReady() {
		return sessionQuerySource{selectSQL: sessionStatsSelect, startExpr: statsSessionStartExpr}
	}
	return sessionQuerySource{selectSQL: sessionSelect, startExpr: sessionStartExpr}
}

// scanSessionRows 读取会话聚合行。
func scanSessionRows(rows *sql.Rows) ([]SessionRollup, error) {
	defer rows.Close()
	rollups := make([]SessionRollup, 0, 32)
	for rows.Next() {
		var (
			rollup                             SessionRollup
			startedNano, endedNano, updated    int64
			sessionStart, sessionLast          int64
			totalRequests, successes, failures int
			requestsWithUsage                  int
			totalTokens, promptTokens          int
			completionTokens, cached, reason   int
			totalDurationMs, avgDurationMs     int64
			turnCount, failedTurns             int
		)
		if err := rows.Scan(
			&rollup.SessionID, &rollup.Title, &rollup.Project, &rollup.Directory,
			&rollup.Provider, &rollup.Model, &rollup.Protocol, &rollup.Status,
			&startedNano, &endedNano, &updated,
			&totalRequests, &successes, &failures, &requestsWithUsage,
			&totalTokens, &promptTokens, &completionTokens, &cached, &reason,
			&totalDurationMs, &avgDurationMs, &turnCount, &failedTurns,
			&sessionStart, &sessionLast,
		); err != nil {
			return nil, err
		}
		rollup.TotalRequests = totalRequests
		rollup.TotalResponses = totalRequests
		rollup.LLMRequests = totalRequests
		rollup.LLMRequestsWithUsage = requestsWithUsage
		rollup.LLMSuccesses = successes
		rollup.LLMErrors = failures
		rollup.TurnCount = turnCount
		rollup.FailedTurns = failedTurns
		rollup.TotalTokens = totalTokens
		rollup.PromptTokens = promptTokens
		rollup.CompletionTokens = completionTokens
		rollup.CachedTokens = cached
		rollup.ReasoningTokens = reason
		rollup.TotalDurationMs = totalDurationMs
		rollup.AverageResponseTimeMs = avgDurationMs
		rollup.StartTime = timeFromUnixNano(sessionStart)
		if endedNano > 0 {
			rollup.EndTime = timeFromUnixNano(endedNano)
		}
		if updated > 0 {
			rollup.LastObservedAt = timeFromUnixNano(updated)
		} else if sessionLast > 0 {
			rollup.LastObservedAt = timeFromUnixNano(sessionLast)
		}
		if rollup.EndTime.IsZero() && sessionLast > 0 {
			rollup.EndTime = timeFromUnixNano(sessionLast)
		}
		rollup.Source = "live"
		rollup.UsageQuality = usageQualityFor(totalRequests, requestsWithUsage, totalTokens)
		rollup.UsageComplete = rollup.UsageQuality == "complete"
		rollup.UsageCoverage = coverageRatio(totalRequests, requestsWithUsage)
		rollup.Partial = false
		rollup.PartialReasons = []string{}
		rollup.ReconciliationStatus = "matched"
		rollup.DroppedMessages = 0
		rollup.ToolResultsObserved = 0
		rollup.ToolErrors = 0
		rollups = append(rollups, rollup)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rollups, nil
}

func usageQualityFor(totalRequests, requestsWithUsage, totalTokens int) string {
	switch {
	case totalRequests == 0:
		if totalTokens > 0 {
			return "summary_only"
		}
		return "missing"
	case requestsWithUsage == 0:
		if totalTokens > 0 {
			return "summary_only"
		}
		return "missing"
	case requestsWithUsage >= totalRequests:
		return "complete"
	default:
		return "partial"
	}
}

func coverageRatio(total, withUsage int) float64 {
	if total <= 0 {
		return 0
	}
	ratio := float64(withUsage) / float64(total)
	if ratio > 1 {
		return 1
	}
	if ratio < 0 {
		return 0
	}
	return ratio
}

func timeFromUnixNano(nano int64) time.Time {
	if nano <= 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

func normalizeLimit(limit, fallback, max int) int {
	if limit <= 0 {
		return fallback
	}
	if max > 0 && limit > max {
		return max
	}
	return limit
}

func normalizeOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

func normalizeGroupBy(groupBy string) string {
	switch strings.ToLower(strings.TrimSpace(groupBy)) {
	case "", "day":
		return "day"
	case "provider", "model", "directory", "project", "status":
		return strings.ToLower(strings.TrimSpace(groupBy))
	default:
		return "day"
	}
}

func emptyListResult(limit, offset int) ListResult {
	return ListResult{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    time.Now().UTC(),
		Sessions:       []SessionRollup{},
		Count:          0,
		Total:          0,
		Limit:          limit,
		Offset:         offset,
		Scanned:        0,
		Totals:         GlobalTotals{},
		Coverage:       Coverage{},
		Partial:        false,
		PartialReasons: []string{},
	}
}

func emptySummaryResult(groupBy string) SummaryResult {
	return SummaryResult{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    time.Now().UTC(),
		GroupBy:        groupBy,
		Groups:         []GroupBucket{},
		Partial:        false,
		PartialReasons: []string{},
	}
}

// jsonUnmarshalRecord 反序列化 record_json（缓存端点回放同一行）。
func jsonUnmarshalRecord(raw []byte, out interface{}) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return fmt.Errorf("empty record payload")
	}
	return json.Unmarshal([]byte(trimmed), out)
}

func sortedStrings(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// ListSessions：DB 分页（LIMIT/OFFSET + COUNT(*)）。
// ---------------------------------------------------------------------------

// ListSessions 返回过滤后的会话用量分页；total/scanned 均为命中会话数。
func (s *Store) ListSessions(q Query) (ListResult, error) {
	limit := normalizeLimit(q.Limit, defaultListLimit, maxListLimit)
	offset := normalizeOffset(q.Offset)
	result := emptyListResult(limit, offset)

	source := s.sessionQuerySource()
	where := buildWhere(q, source.startExpr)
	sessionQuery := source.selectSQL + where.sql

	rows, ok, err := s.query("SELECT COUNT(*) FROM ("+sessionQuery+")", where.args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("count analytics sessions: %w", err)
	}
	total := 0
	if ok {
		defer rows.Close()
		if rows.Next() {
			if err := rows.Scan(&total); err != nil {
				return ListResult{}, fmt.Errorf("scan analytics session count: %w", err)
			}
		}
		if err := rows.Err(); err != nil {
			return ListResult{}, fmt.Errorf("count analytics sessions: %w", err)
		}
		// 必须在下一个查询前显式关闭：上面只读了一行、没有读空结果集，
		// database/sql 不会自动归还连接；分析库是单连接池
		// （SetMaxOpenConns(1)），未关闭的 Rows 会占住唯一连接，使下面的
		// 分页查询永久阻塞（此前 4 连接池只是让问题更晚暴露）。
		if err := rows.Close(); err != nil {
			return ListResult{}, fmt.Errorf("close analytics session count: %w", err)
		}
	}
	result.Total = total
	result.Scanned = total

	pageArgs := append(append([]interface{}{}, where.args...), limit, offset)
	// 排序键必须限定表别名：req CTE 同样暴露 session_id，未限定会触发
	// "ambiguous column name"（见 usageanalytics 分页测试）。
	pageRows, pageOK, err := s.query(sessionQuery+" ORDER BY session_start DESC, s.session_id ASC LIMIT ? OFFSET ?", pageArgs...)
	if err != nil {
		return ListResult{}, fmt.Errorf("query analytics sessions: %w", err)
	}
	rollups := []SessionRollup{}
	if pageOK {
		rollups, err = scanSessionRows(pageRows)
		if err != nil {
			return ListResult{}, fmt.Errorf("scan analytics sessions: %w", err)
		}
	}
	result.Sessions = rollups
	result.Count = len(rollups)
	// schema v2：补齐工具/子代理/恢复维度（按页批量查询，避免 N+1）。
	s.enrichRollupsWithV2(result.Sessions)

	totals, coverage, window, err := s.aggregateTotals(sessionQuery, where.args)
	if err != nil {
		return ListResult{}, err
	}
	result.Totals = totals
	result.Coverage = coverage
	result.DataWindow = window
	result.Partial = false
	result.PartialReasons = []string{}
	return result, nil
}

// aggregateTotals 在 SQL 内汇总命中会话（不把明细载入内存）。
func (s *Store) aggregateTotals(sessionQuery string, args []interface{}) (GlobalTotals, Coverage, DataWindow, error) {
	var totals GlobalTotals
	var coverage Coverage
	var window DataWindow
	sqlText := `SELECT
  COUNT(*),
  COALESCE(SUM(c_total_requests), 0),
  COALESCE(SUM(c_llm_successes), 0),
  COALESCE(SUM(c_llm_errors), 0),
  COALESCE(SUM(c_turn_count), 0),
  COALESCE(SUM(c_failed_turns), 0),
  COALESCE(SUM(c_total_duration_ms), 0),
  COALESCE(SUM(c_avg_duration_ms), 0),
  COALESCE(SUM(c_total_tokens), 0),
  COALESCE(SUM(c_prompt_tokens), 0),
  COALESCE(SUM(c_completion_tokens), 0),
  COALESCE(SUM(c_cached_tokens), 0),
  COALESCE(SUM(c_reasoning_tokens), 0),
  COALESCE(SUM(c_requests_with_usage), 0),
  COALESCE(SUM(CASE WHEN c_requests_with_usage > 0 OR c_total_tokens > 0 THEN 1 ELSE 0 END), 0),
  COALESCE(MIN(CASE WHEN session_start > 0 THEN session_start END), 0),
  COALESCE(MAX(session_last), 0)
FROM (` + sessionQuery + `)`
	rows, ok, err := s.query(sqlText, args...)
	if err != nil {
		return totals, coverage, window, fmt.Errorf("aggregate analytics totals: %w", err)
	}
	if !ok {
		coverage.UsageSessionRate = 0
		coverage.UsageRequestRate = 0
		return totals, coverage, window, nil
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return totals, coverage, window, fmt.Errorf("aggregate analytics totals: %w", err)
		}
		return totals, coverage, window, nil
	}
	var (
		sessions, requests, successes, failures int
		turns, failedTurns                      int
		totalDuration, avgDuration              int64
		totalTokens, promptTokens, completion   int
		cachedTokens, reasoningTokens           int
		requestsWithUsage, sessionsWithUsage    int
		windowFrom, windowTo                    int64
	)
	if err := rows.Scan(
		&sessions, &requests, &successes, &failures, &turns, &failedTurns,
		&totalDuration, &avgDuration,
		&totalTokens, &promptTokens, &completion, &cachedTokens, &reasoningTokens,
		&requestsWithUsage, &sessionsWithUsage, &windowFrom, &windowTo,
	); err != nil {
		return totals, coverage, window, fmt.Errorf("scan analytics totals: %w", err)
	}
	if err := rows.Err(); err != nil {
		return totals, coverage, window, fmt.Errorf("aggregate analytics totals: %w", err)
	}

	totals.Sessions = sessions
	totals.TotalRequests = requests
	totals.TotalResponses = requests
	totals.LLMRequests = requests
	totals.LLMSuccesses = successes
	totals.LLMErrors = failures
	totals.Turns = turns
	totals.FailedTurns = failedTurns
	totals.TotalDurationMs = totalDuration
	totals.AverageResponseTimeMs = avgDuration
	totals.TotalTokens = totalTokens
	totals.PromptTokens = promptTokens
	totals.CompletionTokens = completion
	totals.CachedTokens = cachedTokens
	totals.ReasoningTokens = reasoningTokens

	coverage.Sessions = sessions
	coverage.SessionsWithUsage = sessionsWithUsage
	coverage.LLMRequests = requests
	coverage.LLMRequestsWithUsage = requestsWithUsage
	if sessions > 0 {
		coverage.UsageSessionRate = float64(sessionsWithUsage) / float64(sessions)
	}
	if requests > 0 {
		coverage.UsageRequestRate = coverageRatio(requests, requestsWithUsage)
	}
	window.From = timeFromUnixNano(windowFrom)
	window.To = timeFromUnixNano(windowTo)
	return totals, coverage, window, nil
}

// ---------------------------------------------------------------------------
// Summarize：GROUP BY 聚合（day/provider/model/directory/project/status）。
// ---------------------------------------------------------------------------

// Summarize 返回命中会话的分组统计与全局合计。
func (s *Store) Summarize(q Query) (SummaryResult, error) {
	groupBy := normalizeGroupBy(q.GroupBy)
	limit := normalizeLimit(q.Limit, defaultSummaryLimit, maxSummaryLimit)
	result := emptySummaryResult(groupBy)

	source := s.sessionQuerySource()
	where := buildWhere(q, source.startExpr)
	sessionQuery := source.selectSQL + where.sql

	totals, coverage, window, err := s.aggregateTotals(sessionQuery, where.args)
	if err != nil {
		return SummaryResult{}, err
	}
	result.Totals = totals
	result.Coverage = coverage
	result.DataWindow = window
	result.Scanned = totals.Sessions
	result.Matched = totals.Sessions

	groupExpr := groupExprFor(groupBy)
	groupArgs := append(append([]interface{}{}, where.args...), limit)
	groupSQL := `SELECT ` + groupExpr + ` AS bucket_key,
  COUNT(*) AS sessions,
  COALESCE(SUM(c_total_requests), 0),
  COALESCE(SUM(c_llm_successes), 0),
  COALESCE(SUM(c_llm_errors), 0),
  COALESCE(SUM(c_turn_count), 0),
  COALESCE(SUM(c_failed_turns), 0),
  COALESCE(SUM(c_total_duration_ms), 0),
  COALESCE(SUM(c_avg_duration_ms), 0),
  COALESCE(SUM(c_total_tokens), 0),
  COALESCE(SUM(c_prompt_tokens), 0),
  COALESCE(SUM(c_completion_tokens), 0),
  COALESCE(SUM(c_cached_tokens), 0),
  COALESCE(SUM(c_reasoning_tokens), 0)
FROM (` + sessionQuery + `)
GROUP BY bucket_key
ORDER BY bucket_key ASC
LIMIT ?`
	rows, ok, err := s.query(groupSQL, groupArgs...)
	if err != nil {
		return SummaryResult{}, fmt.Errorf("aggregate analytics groups: %w", err)
	}
	groups := []GroupBucket{}
	if ok {
		defer rows.Close()
		for rows.Next() {
			var (
				bucket                   GroupBucket
				sessions, requests       int
				successes, failures      int
				turns, failedTurns       int
				totalDuration, avgDur    int64
				totalTokens, promptTok   int
				completionTok, cachedTok int
				reasoningTok             int
			)
			if err := rows.Scan(
				&bucket.Key, &sessions, &requests, &successes, &failures,
				&turns, &failedTurns, &totalDuration, &avgDur,
				&totalTokens, &promptTok, &completionTok, &cachedTok, &reasoningTok,
			); err != nil {
				return SummaryResult{}, fmt.Errorf("scan analytics groups: %w", err)
			}
			bucket.Sessions = sessions
			bucket.TotalRequests = requests
			bucket.TotalResponses = requests
			bucket.LLMRequests = requests
			bucket.LLMSuccesses = successes
			bucket.LLMErrors = failures
			bucket.Turns = turns
			bucket.FailedTurns = failedTurns
			bucket.TotalDurationMs = totalDuration
			bucket.AverageResponseTimeMs = avgDur
			bucket.TotalTokens = totalTokens
			bucket.PromptTokens = promptTok
			bucket.CompletionTokens = completionTok
			bucket.CachedTokens = cachedTok
			bucket.ReasoningTokens = reasoningTok
			groups = append(groups, bucket)
		}
		if err := rows.Err(); err != nil {
			return SummaryResult{}, fmt.Errorf("scan analytics groups: %w", err)
		}
	}
	result.Groups = groups
	result.Partial = false
	result.PartialReasons = []string{}
	return result, nil
}

func groupExprFor(groupBy string) string {
	switch groupBy {
	case "provider":
		return "COALESCE(NULLIF(provider, ''), '(unknown)')"
	case "model":
		return "COALESCE(NULLIF(model, ''), '(unknown)')"
	case "directory":
		return "COALESCE(NULLIF(working_directory, ''), '(unknown)')"
	case "project":
		return "COALESCE(NULLIF(project_path, ''), '(unknown)')"
	case "status":
		return "COALESCE(NULLIF(status, ''), '(unknown)')"
	default:
		return "CASE WHEN session_start > 0 THEN date(session_start / 1000000000, 'unixepoch', 'localtime') ELSE '(unknown)' END"
	}
}

// ---------------------------------------------------------------------------
// Dimensions：SELECT DISTINCT 过滤控件取值。
// ---------------------------------------------------------------------------

// Dimensions 返回过滤条件下的去重维度值（SQL DISTINCT + LIMIT）。
func (s *Store) Dimensions(q Query) (DimensionsResult, error) {
	result := DimensionsResult{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Providers:     []string{},
		Models:        []string{},
		Directories:   []string{},
		Projects:      []string{},
		Statuses:      []string{},
	}
	source := s.sessionQuerySource()
	where := buildWhere(q, source.startExpr)
	sessionQuery := source.selectSQL + where.sql

	// 5 个维度合并为一次 base 扫描（§8.1）：每段独立 ORDER BY + LIMIT，
	// UNION ALL 后按维度标签分发，最后在 Go 内按值排序保证输出稳定。
	dimensions := []struct {
		key   string
		out   *[]string
		expr  string
	}{
		{"provider", &result.Providers, "provider"},
		{"model", &result.Models, "model"},
		{"directory", &result.Directories, "working_directory"},
		{"project", &result.Projects, "project_path"},
		{"status", &result.Statuses, "status"},
	}
	parts := make([]string, 0, len(dimensions))
	args := make([]interface{}, 0, len(dimensions)*(len(where.args)+1))
	for _, dim := range dimensions {
		parts = append(parts, "SELECT '"+dim.key+"' AS dim, value FROM (SELECT DISTINCT "+dim.expr+
			" AS value FROM ("+sessionQuery+") WHERE value IS NOT NULL AND TRIM(value) <> '' ORDER BY value ASC LIMIT ?)")
		args = append(args, where.args...)
		args = append(args, maxDimensionValues)
	}
	rows, ok, err := s.query(strings.Join(parts, " UNION ALL "), args...)
	if err != nil {
		return DimensionsResult{}, fmt.Errorf("query analytics dimensions: %w", err)
	}
	if ok {
		defer rows.Close()
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				return DimensionsResult{}, fmt.Errorf("scan analytics dimensions: %w", err)
			}
			for _, dim := range dimensions {
				if dim.key == key {
					*dim.out = append(*dim.out, value)
					break
				}
			}
		}
		if err := rows.Err(); err != nil {
			return DimensionsResult{}, fmt.Errorf("query analytics dimensions: %w", err)
		}
	}
	for _, dim := range dimensions {
		sort.Strings(*dim.out)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// SessionUsage：单会话 + 请求明细（turns 由 GROUP BY 语义在 Go 内派生）。
// ---------------------------------------------------------------------------

// SessionUsage 返回单会话用量明细；会话既无会话行也无请求行时返回 ErrNotFound。
func (s *Store) SessionUsage(sessionID string) (SessionUsageDetail, error) {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return SessionUsageDetail{}, errSessionNotFound(sessionID)
	}
	detail := SessionUsageDetail{
		SchemaVersion:   SchemaVersion,
		GeneratedAt:     time.Now().UTC(),
		Steps:           []StepUsage{},
		Turns:           []TurnUsage{},
		Diagnostics:     []Diagnostic{},
		ErrorCategories: map[string]int{},
		Partial:         false,
		PartialReasons:  []string{},
	}

	rollup, found, err := s.sessionRollup(trimmed)
	if err != nil {
		return SessionUsageDetail{}, err
	}

	rows, truncated, err := s.sessionSteps(trimmed)
	if err != nil {
		return SessionUsageDetail{}, err
	}
	if !found && len(rows) == 0 {
		return SessionUsageDetail{}, errSessionNotFound(trimmed)
	}
	steps := make([]StepUsage, 0, len(rows))
	for _, row := range rows {
		steps = append(steps, row.usage)
	}
	if !found {
		rollup = rollupFromSteps(trimmed, rows)
	}
	// schema v2 会话级聚合（工具失败率/子代理失败率/重试恢复回合）。
	enriched := []SessionRollup{rollup}
	s.enrichRollupsWithV2(enriched)
	rollup = enriched[0]

	detail.Session = rollup
	detail.Steps = steps
	detail.StepCount = len(steps)
	detail.Turns = buildTurns(rows)
	s.enrichTurnsWithV2(trimmed, detail.Turns)
	// schema v2：单会话工具/子代理明细与失败模式 Top-N。
	if toolStats, err := s.ToolStats(ToolStatsQuery{SessionID: trimmed, Limit: maxToolStatsRows}); err == nil {
		detail.Tools = toolStats.Tools
	}
	if subagentStats, err := s.SubagentStats(SubagentStatsQuery{SessionID: trimmed, Limit: maxSubagentStatsRows}); err == nil {
		detail.Subagents = subagentStats.Subagents
		if subagentStats.Summary.Total > 0 {
			rollup.SubagentRuns = subagentStats.Summary.Total
			rollup.SubagentFailures = subagentStats.Summary.Failed
			rollup.SubagentFailureRate = subagentStats.Summary.FailureRate
			rollup.SubagentTimeouts = subagentStats.Summary.Timeouts
		}
	}
	if patterns, err := s.ErrorPatterns(ErrorPatternsQuery{SessionID: trimmed, Top: 10}); err == nil {
		detail.ErrorPatterns = patterns.Patterns
	}
	detail.Session = rollup
	detail.Diagnostics = append(buildDiagnostics(rollup, detail.Turns), buildV2Diagnostics(rollup, rollup.ToolCallsObserved > 0 || rollup.SubagentRuns > 0 || rollup.RetryRecoveredTurns > 0)...)
	detail.ErrorCategories = errorCategoryCounts(steps)
	detail.Coverage = detailCoverage(rollup, steps)
	detail.Partial = false
	detail.PartialReasons = []string{}
	if truncated {
		detail.PartialReasons = append(detail.PartialReasons, "session_steps_truncated")
	}
	return detail, nil
}

// sessionRollup 读取单会话聚合行；found=false 表示 usage_sessions 无该行。
func (s *Store) sessionRollup(sessionID string) (SessionRollup, bool, error) {
	source := s.sessionQuerySource()
	rows, ok, err := s.query(source.selectSQL+"s.session_id = ? LIMIT 1", sessionID)
	if err != nil {
		return SessionRollup{}, false, fmt.Errorf("query analytics session: %w", err)
	}
	if !ok {
		return SessionRollup{}, false, nil
	}
	rollups, err := scanSessionRows(rows)
	if err != nil {
		return SessionRollup{}, false, fmt.Errorf("scan analytics session: %w", err)
	}
	if len(rollups) == 0 {
		return SessionRollup{}, false, nil
	}
	return rollups[0], true, nil
}

// sessionStep 是请求明细行 + turn 归属键（turn_id 不进入对外 JSON 契约）。
type sessionStep struct {
	usage   StepUsage
	turnID  string
	traceID string
}

// sessionSteps 读取会话请求明细（SQL 排序 + 上限保护）。
func (s *Store) sessionSteps(sessionID string) ([]sessionStep, bool, error) {
	steps := []sessionStep{}
	query := `SELECT llm_request_id, trace_id, turn_id, step, provider, model, status, cache_status,
       success, error_category, started_at_unix_nano, duration_ms,
       prompt_tokens, completion_tokens, cache_read_tokens, cache_creation_tokens,
       reasoning_tokens, total_tokens, usage_available, record_json
FROM usage_requests
WHERE session_id = ?
ORDER BY started_at_unix_nano ASC, step ASC, llm_request_id ASC
LIMIT ?`
	rows, ok, err := s.query(query, sessionID, maxSessionSteps+1)
	if err != nil {
		return nil, false, fmt.Errorf("query analytics session requests: %w", err)
	}
	if !ok {
		return steps, false, nil
	}
	defer rows.Close()
	for rows.Next() {
		var (
			row                                        sessionStep
			step                                       = &row.usage
			llmRequestID, provider, model              string
			status, cacheStatus, errorCategory         string
			success, usageAvailable                    int
			startedNano, durationMs                    int64
			prompt, completion, cacheRead, cacheCreate int
			reasoning, totalTokens                     int
			raw                                        []byte
		)
		if err := rows.Scan(
			&llmRequestID, &row.traceID, &row.turnID, &step.Step, &provider, &model,
			&status, &cacheStatus, &success, &errorCategory, &startedNano, &durationMs,
			&prompt, &completion, &cacheRead, &cacheCreate, &reasoning, &totalTokens,
			&usageAvailable, &raw,
		); err != nil {
			return nil, false, fmt.Errorf("scan analytics session requests: %w", err)
		}
		_, _, _ = llmRequestID, provider, model
		_ = status
		step.TraceID = row.traceID
		step.Success = success == 1
		step.UsageAvailable = usageAvailable == 1
		step.ErrorCategory = errorCategory
		step.CacheStatus = cacheStatus
		step.StartedAt = timeFromUnixNano(startedNano)
		step.Timestamp = step.StartedAt
		step.DurationMs = durationMs
		step.PromptTokens = prompt
		step.CompletionTokens = completion
		step.TotalTokens = totalTokens
		step.CachedTokens = cacheRead
		step.CacheReadTokens = cacheRead
		step.CacheReadReported = usageAvailable == 1
		step.ReasoningTokens = reasoning
		if ratio := cacheHitRatioFromRecord(raw); ratio != nil {
			step.CacheHitRatio = *ratio
			step.CacheReadReported = true
		}
		if source := usageSourceFromRecord(raw); source != "" {
			step.UsageSource = source
		}
		if facts, ok := contextFactsFromRecord(raw); ok {
			step.ContextPromptTokens = facts.promptTokens
			step.ContextWindowTokens = facts.windowTokens
			step.PromptBudget = facts.budget
		}
		steps = append(steps, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("query analytics session requests: %w", err)
	}
	truncated := len(steps) > maxSessionSteps
	if truncated {
		steps = steps[:maxSessionSteps]
	}
	return steps, truncated, nil
}

// rollupFromSteps 在缺少会话行时用请求明细兜底构造会话汇总。
func rollupFromSteps(sessionID string, rows []sessionStep) SessionRollup {
	rollup := SessionRollup{
		SessionID:            sessionID,
		Source:               "live",
		PartialReasons:       []string{},
		ReconciliationStatus: "matched",
	}
	for _, row := range rows {
		step := row.usage
		rollup.TotalRequests++
		rollup.LLMRequests++
		if step.Success {
			rollup.LLMSuccesses++
		} else {
			rollup.LLMErrors++
		}
		if step.UsageAvailable {
			rollup.LLMRequestsWithUsage++
		}
		rollup.TotalTokens += step.TotalTokens
		rollup.PromptTokens += step.PromptTokens
		rollup.CompletionTokens += step.CompletionTokens
		rollup.CachedTokens += step.CachedTokens
		rollup.ReasoningTokens += step.ReasoningTokens
		rollup.TotalDurationMs += step.DurationMs
		if rollup.StartTime.IsZero() || (!step.StartedAt.IsZero() && step.StartedAt.Before(rollup.StartTime)) {
			rollup.StartTime = step.StartedAt
		}
		if step.StartedAt.After(rollup.EndTime) {
			rollup.EndTime = step.StartedAt
		}
	}
	rollup.TotalResponses = rollup.TotalRequests
	if rollup.TotalRequests > 0 {
		rollup.AverageResponseTimeMs = rollup.TotalDurationMs / int64(rollup.TotalRequests)
	}
	rollup.TurnCount = len(buildTurns(rows))
	rollup.UsageQuality = usageQualityFor(rollup.TotalRequests, rollup.LLMRequestsWithUsage, rollup.TotalTokens)
	rollup.UsageComplete = rollup.UsageQuality == "complete"
	rollup.UsageCoverage = coverageRatio(rollup.TotalRequests, rollup.LLMRequestsWithUsage)
	rollup.LastObservedAt = rollup.EndTime
	return rollup
}

func detailCoverage(rollup SessionRollup, steps []StepUsage) Coverage {
	coverage := Coverage{
		Sessions:             1,
		LLMRequests:          len(steps),
		LLMRequestsWithUsage: rollup.LLMRequestsWithUsage,
	}
	if rollup.TotalTokens > 0 || rollup.LLMRequestsWithUsage > 0 {
		coverage.SessionsWithUsage = 1
	}
	if coverage.Sessions > 0 {
		coverage.UsageSessionRate = float64(coverage.SessionsWithUsage) / float64(coverage.Sessions)
	}
	if coverage.LLMRequests > 0 {
		coverage.UsageRequestRate = coverageRatio(coverage.LLMRequests, coverage.LLMRequestsWithUsage)
	}
	return coverage
}

// ---------------------------------------------------------------------------
// turn 派生：usage_requests 按 (session_id, trace_id, turn_id) 分组的 Go 侧实现。
// ---------------------------------------------------------------------------

// stepTurnKey turn 分组键：trace_id → turn_id → "unknown"（与旧解析器一致）。
func stepTurnKey(row sessionStep) string {
	if key := strings.TrimSpace(row.traceID); key != "" {
		return key
	}
	if key := strings.TrimSpace(row.turnID); key != "" {
		return key
	}
	return "unknown"
}

// stepTurnLabel 对外 turn_id：优先 turn_id，其次 trace_id。
func stepTurnLabel(row sessionStep) string {
	if label := strings.TrimSpace(row.turnID); label != "" {
		return label
	}
	if label := strings.TrimSpace(row.traceID); label != "" {
		return label
	}
	return "unknown"
}

func buildTurns(rows []sessionStep) []TurnUsage {
	index := make(map[string]int, 8)
	turns := make([]TurnUsage, 0, 8)
	withUsage := make([]int, 0, 8)
	for _, row := range rows {
		step := row.usage
		key := stepTurnKey(row)
		idx, ok := index[key]
		if !ok {
			idx = len(turns)
			index[key] = idx
			turns = append(turns, TurnUsage{
				TurnID:       stepTurnLabel(row),
				TraceID:      step.TraceID,
				Ordinal:      idx + 1,
				UsageQuality: "missing",
			})
			withUsage = append(withUsage, 0)
		}
		turn := &turns[idx]
		turn.LLMRequests++
		if step.UsageAvailable {
			withUsage[idx]++
		}
		if step.Success {
			turn.LLMSuccesses++
		} else {
			turn.LLMErrors++
			if turn.ErrorCategory == "" {
				turn.ErrorCategory = step.ErrorCategory
			}
		}
		if turn.StartedAt.IsZero() || (!step.StartedAt.IsZero() && step.StartedAt.Before(turn.StartedAt)) {
			turn.StartedAt = step.StartedAt
		}
		if step.StartedAt.After(turn.EndedAt) {
			turn.EndedAt = step.StartedAt
		}
		turn.Usage.TotalTokens += step.TotalTokens
		turn.Usage.PromptTokens += step.PromptTokens
		turn.Usage.CompletionTokens += step.CompletionTokens
		turn.Usage.CachedTokens += step.CachedTokens
		turn.Usage.ReasoningTokens += step.ReasoningTokens
	}
	for i := range turns {
		turn := &turns[i]
		if !turn.StartedAt.IsZero() && !turn.EndedAt.IsZero() {
			turn.DurationMs = turn.EndedAt.Sub(turn.StartedAt).Milliseconds()
		}
		switch {
		case turn.LLMRequests == 0:
			turn.Outcome = "unknown"
		case turn.LLMErrors == 0:
			turn.Outcome = "success"
		case turn.LLMSuccesses > 0:
			turn.Outcome = "partial"
		default:
			turn.Outcome = "failed"
		}
		turn.UsageQuality = usageQualityFor(turn.LLMRequests, withUsage[i], turn.Usage.TotalTokens)
		turn.UsageCoverage = coverageRatio(turn.LLMRequests, withUsage[i])
		turn.ToolResultsObserved = 0
		turn.ToolErrors = 0
	}
	return turns
}

func buildDiagnostics(rollup SessionRollup, turns []TurnUsage) []Diagnostic {
	diagnostics := make([]Diagnostic, 0, 4)
	if rollup.LLMErrors > 0 {
		rate := float64(rollup.LLMErrors) / float64(maxInt(rollup.LLMRequests, 1))
		severity := "info"
		if rate >= 0.05 {
			severity = "warning"
		}
		diagnostics = append(diagnostics, Diagnostic{Code: "llm_failures", Severity: severity, Count: rollup.LLMErrors, Rate: rate})
	}
	if rollup.FailedTurns > 0 {
		diagnostics = append(diagnostics, Diagnostic{Code: "failed_turns", Severity: "error", Count: rollup.FailedTurns, Rate: float64(rollup.FailedTurns) / float64(maxInt(rollup.TurnCount, 1))})
	}
	for _, turn := range turns {
		if turn.MaxContextUtilization >= 0.8 {
			diagnostics = append(diagnostics, Diagnostic{Code: "context_pressure", Severity: "warning", Count: 1, Rate: turn.MaxContextUtilization, TurnID: turn.TurnID})
		}
	}
	if rollup.Partial {
		diagnostics = append(diagnostics, Diagnostic{Code: "partial_diagnostic_evidence", Severity: "info", Count: len(rollup.PartialReasons)})
	}
	return diagnostics
}

func errorCategoryCounts(steps []StepUsage) map[string]int {
	counts := make(map[string]int)
	for _, step := range steps {
		if category := strings.TrimSpace(step.ErrorCategory); category != "" {
			counts[category]++
		}
	}
	return counts
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// cacheHitRatioFromRecord 从 record_json 回放缓存命中率（best-effort）。
func cacheHitRatioFromRecord(raw []byte) *float64 {
	var record struct {
		CacheHitRatio *float64 `json:"cache_hit_ratio"`
	}
	if err := jsonUnmarshalRecord(raw, &record); err != nil {
		return nil
	}
	return record.CacheHitRatio
}

// usageSourceFromRecord 从 record_json 回放用量来源（best-effort）。
func usageSourceFromRecord(raw []byte) string {
	var record struct {
		Usage struct {
			UsageSource string `json:"usage_source"`
		} `json:"usage"`
	}
	if err := jsonUnmarshalRecord(raw, &record); err != nil {
		return ""
	}
	return strings.TrimSpace(record.Usage.UsageSource)
}

// contextFacts 是从 record_json 回放出的上下文事实；0 表示该次请求未观测到。
type contextFacts struct {
	promptTokens int
	windowTokens int
	budget       int
}

// contextFactsFromRecord 从 record_json 回放上下文事实（出站消息 token / 窗口 / 预算）。
// 三项全部缺失时返回 ok=false，避免给历史记录写出零值字段（前端据字段是否存在
// 区分"未观测"与"观测为 0"）。
func contextFactsFromRecord(raw []byte) (contextFacts, bool) {
	var record struct {
		ContextPromptTokens int `json:"context_prompt_tokens"`
		ContextWindowTokens int `json:"context_window_tokens"`
		PromptBudget        int `json:"prompt_budget"`
	}
	if err := jsonUnmarshalRecord(raw, &record); err != nil {
		return contextFacts{}, false
	}
	facts := contextFacts{
		promptTokens: record.ContextPromptTokens,
		windowTokens: record.ContextWindowTokens,
		budget:       record.PromptBudget,
	}
	if facts.promptTokens <= 0 && facts.windowTokens <= 0 && facts.budget <= 0 {
		return contextFacts{}, false
	}
	return facts, true
}
