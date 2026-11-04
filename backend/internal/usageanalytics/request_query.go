package usageanalytics

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

const (
	defaultRequestListLimit = 50
	maxRequestListLimit     = 200
)

// RequestQuery filters cross-session LLM requests stored in usage_requests.
// From/To use request start time (unlike session-level analytics queries).
type RequestQuery struct {
	From time.Time
	To   time.Time

	SessionID     string
	Provider      string
	Model         string
	Directory     string
	Project       string
	SessionStatus string
	Query         string

	Status      string
	Success     *bool
	CacheStatus string

	CacheHitRatioMin     *float64
	CacheHitRatioMax     *float64
	CacheHitRatioBelow   *float64
	CacheHitRatioAtLeast *float64
	CacheReportedOnly    bool

	Limit  int
	Offset int
}

// RequestCacheStatusDistribution counts terminal cache classifications.
type RequestCacheStatusDistribution struct {
	Hit           int `json:"hit"`
	Write         int `json:"write"`
	ConfirmedMiss int `json:"confirmed_miss"`
	ReportedZero  int `json:"reported_zero"`
	NotReported   int `json:"not_reported"`
	Error         int `json:"error"`
}

// RequestSummary is aggregated over all matched rows, before pagination.
type RequestSummary struct {
	Requests                int                            `json:"requests"`
	Successes               int                            `json:"successes"`
	Failures                int                            `json:"failures"`
	InputTokens             int64                          `json:"input_tokens"`
	OutputTokens            int64                          `json:"output_tokens"`
	TotalTokens             int64                          `json:"total_tokens"`
	CacheReadTokens         int64                          `json:"cache_read_tokens"`
	CacheCreationTokens     int64                          `json:"cache_creation_tokens"`
	Sessions                int                            `json:"sessions"`
	Providers               int                            `json:"providers"`
	Models                  int                            `json:"models"`
	CacheReported           int                            `json:"cache_reported"`
	AverageCacheHitRatio    *float64                       `json:"average_cache_hit_ratio,omitempty"`
	CacheStatusDistribution RequestCacheStatusDistribution `json:"cache_status_distribution"`
}

// RequestListResult is the cross-session request list response.
type RequestListResult struct {
	SchemaVersion string                              `json:"schema_version"`
	GeneratedAt   time.Time                           `json:"generated_at"`
	Total         int                                 `json:"total"`
	Limit         int                                 `json:"limit"`
	Offset        int                                 `json:"offset"`
	Summary       RequestSummary                      `json:"summary"`
	Requests      []cacheanalytics.CacheRequestRecord `json:"requests"`
}

// ListRequests returns filtered requests across all sessions. Cache-ratio
// filters are evaluated before LIMIT/OFFSET and only match reported statuses.
func (s *Store) ListRequests(q RequestQuery) (RequestListResult, error) {
	limit := normalizeLimit(q.Limit, defaultRequestListLimit, maxRequestListLimit)
	offset := normalizeOffset(q.Offset)
	result := RequestListResult{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Limit:         limit,
		Offset:        offset,
		Requests:      []cacheanalytics.CacheRequestRecord{},
	}
	if s == nil {
		return result, nil
	}

	where := s.buildRequestWhere(q)
	from := requestListFrom(q)
	summaryQuery := `SELECT
  COUNT(*),
  COALESCE(SUM(CASE WHEN r.success = 1 THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(r.prompt_tokens), 0),
  COALESCE(SUM(r.completion_tokens), 0),
  COALESCE(SUM(r.total_tokens), 0),
  COALESCE(SUM(r.cache_read_tokens), 0),
  COALESCE(SUM(r.cache_creation_tokens), 0),
  COUNT(DISTINCT NULLIF(r.session_id, '')),
  COUNT(DISTINCT NULLIF(LOWER(r.provider), '')),
  COUNT(DISTINCT NULLIF(LOWER(r.model), '')),
  COALESCE(SUM(CASE WHEN ` + reportedCacheStatusSQL("r.cache_status") + ` THEN 1 ELSE 0 END), 0),
  AVG((` + s.cacheHitRatioSQLExpr("r") + `)),
  COALESCE(SUM(CASE WHEN r.cache_status = 'hit' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN r.cache_status = 'write' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN r.cache_status = 'confirmed_miss' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN r.cache_status = 'reported_zero' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN r.cache_status = 'not_reported' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN r.cache_status = 'error' THEN 1 ELSE 0 END), 0)
` + from + " WHERE " + where.sql

	rows, ok, err := s.query(summaryQuery, where.args...)
	if err != nil {
		return result, fmt.Errorf("summarize analytics requests: %w", err)
	}
	if ok {
		defer rows.Close()
		if rows.Next() {
			var average sql.NullFloat64
			if err := rows.Scan(
				&result.Summary.Requests,
				&result.Summary.Successes,
				&result.Summary.InputTokens,
				&result.Summary.OutputTokens,
				&result.Summary.TotalTokens,
				&result.Summary.CacheReadTokens,
				&result.Summary.CacheCreationTokens,
				&result.Summary.Sessions,
				&result.Summary.Providers,
				&result.Summary.Models,
				&result.Summary.CacheReported,
				&average,
				&result.Summary.CacheStatusDistribution.Hit,
				&result.Summary.CacheStatusDistribution.Write,
				&result.Summary.CacheStatusDistribution.ConfirmedMiss,
				&result.Summary.CacheStatusDistribution.ReportedZero,
				&result.Summary.CacheStatusDistribution.NotReported,
				&result.Summary.CacheStatusDistribution.Error,
			); err != nil {
				return result, fmt.Errorf("scan analytics request summary: %w", err)
			}
			if average.Valid {
				value := average.Float64
				result.Summary.AverageCacheHitRatio = &value
			}
		}
		if err := rows.Err(); err != nil {
			return result, fmt.Errorf("read analytics request summary: %w", err)
		}
		// The analytics store has a single connection; close before the page query.
		if err := rows.Close(); err != nil {
			return result, fmt.Errorf("close analytics request summary: %w", err)
		}
	}
	result.Summary.Failures = result.Summary.Requests - result.Summary.Successes
	result.Total = result.Summary.Requests

	pageArgs := append(append([]interface{}{}, where.args...), limit, offset)
	rows, ok, err = s.query(
		"SELECT r.record_json "+from+" WHERE "+where.sql+
			" ORDER BY r.started_at_unix_nano DESC, r.step DESC, r.llm_request_id DESC LIMIT ? OFFSET ?",
		pageArgs...,
	)
	if err != nil {
		return result, fmt.Errorf("query analytics requests: %w", err)
	}
	if ok {
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return result, fmt.Errorf("scan analytics request row: %w", err)
			}
			var record cacheanalytics.CacheRequestRecord
			if err := jsonUnmarshalRecord(raw, &record); err != nil {
				continue
			}
			result.Requests = append(result.Requests, record)
		}
		if err := rows.Err(); err != nil {
			return result, fmt.Errorf("read analytics request rows: %w", err)
		}
	}
	return result, nil
}

// requestListFrom only joins session metadata when a filter actually reads it.
// The common time/provider/cache views avoid a 280k-row join and can stay on the
// request time index.
func requestListFrom(q RequestQuery) string {
	from := "FROM usage_requests r"
	if strings.TrimSpace(q.Directory) == "" &&
		strings.TrimSpace(q.Project) == "" &&
		strings.TrimSpace(q.SessionStatus) == "" &&
		strings.TrimSpace(q.Query) == "" {
		return from
	}
	return from + " LEFT JOIN usage_sessions s ON s.session_id = r.session_id"
}

func (s *Store) buildRequestWhere(q RequestQuery) whereClause {
	clause := whereClause{sql: "1=1"}
	if !q.From.IsZero() {
		clause.sql += " AND r.started_at_unix_nano >= ?"
		clause.args = append(clause.args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		clause.sql += " AND r.started_at_unix_nano < ?"
		clause.args = append(clause.args, q.To.UnixNano())
	}
	if sessionID := strings.TrimSpace(q.SessionID); sessionID != "" {
		clause.sql += " AND r.session_id = ?"
		clause.args = append(clause.args, sessionID)
	}
	if provider := strings.TrimSpace(q.Provider); provider != "" {
		clause.sql += " AND LOWER(r.provider) = LOWER(?)"
		clause.args = append(clause.args, provider)
	}
	if model := strings.TrimSpace(q.Model); model != "" {
		clause.sql += " AND LOWER(r.model) = LOWER(?)"
		clause.args = append(clause.args, model)
	}
	if directory := strings.TrimSpace(q.Directory); directory != "" {
		clause.sql += " AND (s.working_directory = ? OR s.working_directory LIKE ? ESCAPE '\\')"
		clause.args = append(clause.args, directory, "%"+escapeLike(directory)+"%")
	}
	if project := strings.TrimSpace(q.Project); project != "" {
		clause.sql += " AND (s.project_path = ? OR s.project_path LIKE ? ESCAPE '\\'" +
			" OR s.working_directory = ? OR s.working_directory LIKE ? ESCAPE '\\')"
		clause.args = append(clause.args,
			project, "%"+escapeLike(project)+"%",
			project, "%"+escapeLike(project)+"%")
	}
	if status := strings.TrimSpace(q.SessionStatus); status != "" {
		clause.sql += " AND LOWER(s.status) = LOWER(?)"
		clause.args = append(clause.args, status)
	}
	if status := strings.TrimSpace(q.Status); status != "" {
		clause.sql += " AND LOWER(r.status) = LOWER(?)"
		clause.args = append(clause.args, status)
	}
	if q.Success != nil {
		if *q.Success {
			clause.sql += " AND r.success = 1"
		} else {
			clause.sql += " AND r.success = 0"
		}
	}
	if cacheStatus := strings.TrimSpace(q.CacheStatus); cacheStatus != "" {
		clause.sql += " AND LOWER(r.cache_status) = LOWER(?)"
		clause.args = append(clause.args, cacheStatus)
	}
	if q.CacheReportedOnly {
		clause.sql += " AND " + reportedCacheStatusSQL("r.cache_status")
	}
	ratioExpr := s.cacheHitRatioSQLExpr("r")
	if q.CacheHitRatioMin != nil {
		clause.sql += " AND (" + ratioExpr + ") >= ?"
		clause.args = append(clause.args, *q.CacheHitRatioMin)
	}
	if q.CacheHitRatioMax != nil {
		clause.sql += " AND (" + ratioExpr + ") <= ?"
		clause.args = append(clause.args, *q.CacheHitRatioMax)
	}
	if q.CacheHitRatioBelow != nil {
		clause.sql += " AND (" + ratioExpr + ") < ?"
		clause.args = append(clause.args, *q.CacheHitRatioBelow)
	}
	if q.CacheHitRatioAtLeast != nil {
		clause.sql += " AND (" + ratioExpr + ") >= ?"
		clause.args = append(clause.args, *q.CacheHitRatioAtLeast)
	}
	if text := strings.TrimSpace(q.Query); text != "" {
		pattern := "%" + escapeLike(text) + "%"
		clause.sql += " AND (r.llm_request_id LIKE ? ESCAPE '\\' OR r.session_id LIKE ? ESCAPE '\\'" +
			" OR r.trace_id LIKE ? ESCAPE '\\' OR r.turn_id LIKE ? ESCAPE '\\'" +
			" OR r.provider LIKE ? ESCAPE '\\' OR r.model LIKE ? ESCAPE '\\'" +
			" OR s.title LIKE ? ESCAPE '\\' OR s.project_path LIKE ? ESCAPE '\\'" +
			" OR s.working_directory LIKE ? ESCAPE '\\')"
		clause.args = append(clause.args,
			pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	return clause
}

func reportedCacheStatusSQL(column string) string {
	return column + " IN ('hit','write','confirmed_miss','reported_zero')"
}
