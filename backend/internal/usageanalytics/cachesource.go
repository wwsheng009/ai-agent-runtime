package usageanalytics

import (
	"database/sql"
	"strings"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

// ============================================================================
// cacheanalytics.Source 的数据库实现：/api/runtime/sessions/{id}/cache/*
// 与 /api/runtime/analytics/* 读同一个 usage_analytics.sqlite。
//
// 契约（cache.analytics.v1）保持不变：capabilities/overview/requests/
// requests/{id}/messages/{id}/trace 的 JSON 形状由 cacheanalytics.httpapi
// 统一渲染；这里只提供 Source 数据面。
// ============================================================================

// CacheSource 把分析库适配为 cacheanalytics.Source（只读查询）。
type CacheSource struct {
	store        *Store
	history      cacheanalytics.HistoryLookup
	maxRequests  int
	supportsSSE  bool
	capabilities cacheanalytics.Capabilities
}

// NewCacheSource 构建数据库缓存数据源；store 为 nil 时返回 nil。
func NewCacheSource(store *Store, history cacheanalytics.HistoryLookup, supportsSSE bool) cacheanalytics.Source {
	if store == nil {
		return nil
	}
	source := &CacheSource{
		store:       store,
		history:     history,
		supportsSSE: supportsSSE,
	}
	source.capabilities = cacheanalytics.Capabilities{
		SchemaVersion: cacheanalytics.SchemaVersion,
		// 能力发现的 data_source 语义保持 cache.analytics.v1 契约不变：分析库
		// 由 runtime EventBus 实时写入，仍是"在线投影"；持久化属性由 Persisted
		// 字段表达（跨进程可回放，MaxRequestsPerSession 不再是硬上限）。
		DataSource:            cacheanalytics.DataSourceLive,
		MaxRequestsPerSession: 0,
		SupportsSSE:           supportsSSE,
		Persisted:             true,
	}
	if supportsSSE {
		source.capabilities.SupportedEvents = []string{cacheanalytics.EventCacheRequestFinished}
	}
	return source
}

// Capabilities 报告数据源能力（持久化库：无每会话上限）。
func (s *CacheSource) Capabilities() cacheanalytics.Capabilities {
	if s == nil {
		return cacheanalytics.Capabilities{}
	}
	return s.capabilities
}

// Overview 会话总览（SQL 聚合；会话不存在返回 ErrSessionNotFound）。
func (s *CacheSource) Overview(sessionID string) (cacheanalytics.CacheOverview, error) {
	overview := cacheanalytics.CacheOverview{
		SchemaVersion: cacheanalytics.SchemaVersion,
		SessionID:     sessionID,
		GeneratedAt:   time.Now().UTC(),
	}
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return overview, cacheanalytics.ErrInvalidRequest
	}
	if missing, err := s.sessionMissing(sessionID); err != nil {
		return overview, err
	} else if missing {
		return overview, cacheanalytics.ErrSessionNotFound
	}

	query := `SELECT
  COUNT(*),
  COALESCE(SUM(usage_available), 0),
  COALESCE(SUM(CASE WHEN cache_status <> '' AND cache_status <> 'not_reported' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(prompt_tokens), 0),
  COALESCE(SUM(completion_tokens), 0),
  COALESCE(SUM(total_tokens), 0),
  COALESCE(SUM(cache_read_tokens), 0),
  COALESCE(SUM(cache_creation_tokens), 0),
  COALESCE(SUM(reasoning_tokens), 0),
  COALESCE(MIN(NULLIF(started_at_unix_nano, 0)), 0),
  COALESCE(MAX(started_at_unix_nano), 0),
  COALESCE(SUM(CASE WHEN cache_status = 'hit' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN cache_status = 'write' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN cache_status = 'reported_zero' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN cache_status = 'not_reported' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN cache_status = 'error' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(cache_read_tokens), 0) * 1.0 / NULLIF(SUM(CASE WHEN usage_available = 1 THEN prompt_tokens ELSE 0 END), 0)
FROM usage_requests WHERE session_id = ?`

	rows, ok, err := s.store.query(query, sessionID)
	if err != nil {
		return overview, cacheanalytics.ErrInternal
	}
	if !ok {
		return overview, nil
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return overview, cacheanalytics.ErrInternal
		}
		return overview, nil
	}
	var (
		total, withUsage, cacheReported               int
		prompt, completion, totalTokens               int64
		cacheRead, cacheCreation, reasoning           int64
		windowFrom, windowTo                          int64
		hit, write, reportedZero, notReported, errCnt int
		hitRatio                                      sql.NullFloat64
	)
	if err := rows.Scan(
		&total, &withUsage, &cacheReported,
		&prompt, &completion, &totalTokens,
		&cacheRead, &cacheCreation, &reasoning,
		&windowFrom, &windowTo,
		&hit, &write, &reportedZero, &notReported, &errCnt,
		&hitRatio,
	); err != nil {
		return overview, cacheanalytics.ErrInternal
	}
	if err := rows.Err(); err != nil {
		return overview, cacheanalytics.ErrInternal
	}

	overview.RequestsTotal = total
	overview.RequestsWithUsage = withUsage
	overview.RequestsCacheReported = cacheReported
	overview.Tokens = cacheanalytics.CacheOverviewTokens{
		PromptTokens:        prompt,
		CompletionTokens:    completion,
		TotalTokens:         totalTokens,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreation,
		ReasoningTokens:     reasoning,
	}
	if from := timeFromUnixNano(windowFrom); !from.IsZero() {
		overview.WindowFrom = &from
	}
	if to := timeFromUnixNano(windowTo); !to.IsZero() {
		overview.WindowTo = &to
	}
	if hitRatio.Valid {
		ratio := hitRatio.Float64
		overview.CacheHitRatio = &ratio
	}
	if prompt > 0 && cacheCreation > 0 {
		ratio := float64(cacheCreation) / float64(prompt)
		overview.CacheWriteRatio = &ratio
	}
	overview.CacheStatusDistribution = cacheanalytics.CacheStatusDistribution{
		Hit:          hit,
		Write:        write,
		ReportedZero: reportedZero,
		NotReported:  notReported,
		Error:        errCnt,
	}
	coverage := cacheanalytics.CoverageInfo{Partial: false, PartialReasons: []string{}}
	if total > 0 {
		usageRate := float64(withUsage) / float64(total)
		cacheRate := float64(cacheReported) / float64(total)
		coverage.UsageRequestRate = &usageRate
		coverage.CacheReportRate = &cacheRate
	}
	overview.Coverage = coverage
	return overview, nil
}

// Requests 明细分页（SQL LIMIT/OFFSET + COUNT(*)）。
func (s *CacheSource) Requests(sessionID string, q cacheanalytics.RequestQuery) (cacheanalytics.RequestListResponse, error) {
	response := cacheanalytics.RequestListResponse{
		SchemaVersion: cacheanalytics.SchemaVersion,
		SessionID:     sessionID,
		Requests:      []cacheanalytics.CacheRequestRecord{},
	}
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return response, cacheanalytics.ErrInvalidRequest
	}
	if q.Limit < 0 || q.Offset < 0 {
		return response, cacheanalytics.ErrInvalidRequest
	}
	if missing, err := s.sessionMissing(sessionID); err != nil {
		return response, err
	} else if missing {
		return response, cacheanalytics.ErrSessionNotFound
	}

	where := "session_id = ?"
	args := []interface{}{sessionID}
	if traceID := strings.TrimSpace(q.TraceID); traceID != "" {
		where += " AND trace_id = ?"
		args = append(args, traceID)
	}
	if turnID := strings.TrimSpace(q.TurnID); turnID != "" {
		where += " AND turn_id = ?"
		args = append(args, turnID)
	}
	if status := strings.TrimSpace(q.Status); status != "" {
		where += " AND LOWER(status) = LOWER(?)"
		args = append(args, status)
	}
	if cacheStatus := strings.TrimSpace(q.CacheStatus); cacheStatus != "" {
		where += " AND LOWER(cache_status) = LOWER(?)"
		args = append(args, cacheStatus)
	}
	if q.From != nil && !q.From.IsZero() {
		where += " AND started_at_unix_nano >= ?"
		args = append(args, q.From.UnixNano())
	}
	if q.To != nil && !q.To.IsZero() {
		where += " AND started_at_unix_nano < ?"
		args = append(args, q.To.UnixNano())
	}

	totalRows, ok, err := s.store.query("SELECT COUNT(*) FROM usage_requests WHERE "+where, args...)
	if err != nil {
		return response, cacheanalytics.ErrInternal
	}
	total := 0
	if ok {
		defer totalRows.Close()
		if totalRows.Next() {
			if err := totalRows.Scan(&total); err != nil {
				return response, cacheanalytics.ErrInternal
			}
		}
		if err := totalRows.Err(); err != nil {
			return response, cacheanalytics.ErrInternal
		}
		// 单连接池（SetMaxOpenConns(1)）：COUNT 只读一行、结果集未读空，
		// 不显式关闭就会占住唯一连接，下面分页查询会永久阻塞。
		if err := totalRows.Close(); err != nil {
			return response, cacheanalytics.ErrInternal
		}
	}
	response.Total = total
	response.Limit = q.Limit
	response.Offset = q.Offset

	pageArgs := append(append([]interface{}{}, args...), q.Limit, q.Offset)
	rows, ok, err := s.store.query(
		"SELECT record_json FROM usage_requests WHERE "+where+
			" ORDER BY started_at_unix_nano DESC, step DESC, llm_request_id DESC LIMIT ? OFFSET ?",
		pageArgs...)
	if err != nil {
		return response, cacheanalytics.ErrInternal
	}
	if ok {
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return response, cacheanalytics.ErrInternal
			}
			var record cacheanalytics.CacheRequestRecord
			if err := jsonUnmarshalRecord(raw, &record); err != nil {
				continue
			}
			response.Requests = append(response.Requests, record)
		}
		if err := rows.Err(); err != nil {
			return response, cacheanalytics.ErrInternal
		}
	}
	return response, nil
}

// Request 单请求详情（record_json 与运行时投影同源）。
func (s *CacheSource) Request(sessionID, llmRequestID string) (cacheanalytics.CacheRequestRecord, error) {
	if s == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(llmRequestID) == "" {
		return cacheanalytics.CacheRequestRecord{}, cacheanalytics.ErrInvalidRequest
	}
	rows, ok, err := s.store.query(
		"SELECT record_json FROM usage_requests WHERE session_id = ? AND llm_request_id = ? LIMIT 1",
		sessionID, llmRequestID)
	if err != nil {
		return cacheanalytics.CacheRequestRecord{}, cacheanalytics.ErrInternal
	}
	if !ok {
		return cacheanalytics.CacheRequestRecord{}, cacheanalytics.ErrNotFound
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return cacheanalytics.CacheRequestRecord{}, cacheanalytics.ErrInternal
		}
		return cacheanalytics.CacheRequestRecord{}, cacheanalytics.ErrNotFound
	}
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		return cacheanalytics.CacheRequestRecord{}, cacheanalytics.ErrInternal
	}
	var record cacheanalytics.CacheRequestRecord
	if err := jsonUnmarshalRecord(raw, &record); err != nil {
		return cacheanalytics.CacheRequestRecord{}, cacheanalytics.ErrInternal
	}
	return record, nil
}

// MessageTrace 按消息 id 追溯：历史存储给 role/turn，分析库给该 turn 的请求。
func (s *CacheSource) MessageTrace(sessionID, messageID string) (cacheanalytics.MessageTrace, error) {
	trace := cacheanalytics.MessageTrace{
		SchemaVersion: cacheanalytics.SchemaVersion,
		SessionID:     sessionID,
		MessageID:     messageID,
		ConsumedBy:    []cacheanalytics.ConsumedBy{},
	}
	if s == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" {
		return cacheanalytics.MessageTrace{}, cacheanalytics.ErrInvalidRequest
	}
	if missing, err := s.sessionMissing(sessionID); err != nil {
		return cacheanalytics.MessageTrace{}, err
	} else if missing {
		return cacheanalytics.MessageTrace{}, cacheanalytics.ErrSessionNotFound
	}

	role := ""
	turnID := ""
	if s.history != nil {
		if ctx, ok := s.history.MessageContext(sessionID, messageID); ok {
			trace.HistoryAvailable = true
			trace.CorrelationSource = cacheanalytics.CorrelationSourceHistory
			trace.MessageRole = ctx.Role
			trace.Neighbors = cacheanalytics.MessageNeighbors{
				PrevMessageID: ctx.PrevMessageID,
				NextMessageID: ctx.NextMessageID,
			}
			role = strings.ToLower(strings.TrimSpace(ctx.Role))
			turnID = strings.TrimSpace(ctx.TurnID)
			trace.TurnID = turnID
		}
	}

	records, err := s.turnRequests(sessionID, turnID, 200)
	if err != nil {
		return cacheanalytics.MessageTrace{}, err
	}
	if role == "assistant" {
		// 产出：turn 内最后一个成功请求。
		for i := len(records) - 1; i >= 0; i-- {
			if records[i].Status != cacheanalytics.RequestStatusSuccess {
				continue
			}
			trace.ProducedBy = producedByRecord(&records[i])
			break
		}
		if trace.ProducedBy != nil && trace.ProducedBy.StartedAt != nil {
			consumed, err := s.requestsAfter(sessionID, *trace.ProducedBy.StartedAt, 50)
			if err != nil {
				return cacheanalytics.MessageTrace{}, err
			}
			trace.ConsumedBy = consumedByRecords(consumed)
		}
	} else if role == "user" {
		trace.ConsumedBy = consumedByRecords(records)
	}

	if trace.ProducedBy == nil && len(trace.ConsumedBy) == 0 && !trace.HistoryAvailable {
		return cacheanalytics.MessageTrace{}, cacheanalytics.ErrNotFound
	}
	return trace, nil
}

// ---------------------------------------------------------------------------
// 内部查询
// ---------------------------------------------------------------------------

// sessionMissing 会话在分析库与历史存储中都不存在。
func (s *CacheSource) sessionMissing(sessionID string) (bool, error) {
	rows, ok, err := s.store.query(
		"SELECT (SELECT COUNT(*) FROM usage_sessions WHERE session_id = ?) + (SELECT COUNT(*) FROM usage_requests WHERE session_id = ?)",
		sessionID, sessionID)
	if err != nil {
		return false, cacheanalytics.ErrInternal
	}
	count := 0
	if ok {
		defer rows.Close()
		if rows.Next() {
			if err := rows.Scan(&count); err != nil {
				return false, cacheanalytics.ErrInternal
			}
		}
		if err := rows.Err(); err != nil {
			return false, cacheanalytics.ErrInternal
		}
	}
	if count > 0 {
		return false, nil
	}
	if s.history != nil && s.history.SessionExists(sessionID) {
		return false, nil
	}
	return true, nil
}

// turnRequests 返回该 turn 的请求（turn_id 或 trace_id 命中，按时间升序）。
func (s *CacheSource) turnRequests(sessionID, turnID string, limit int) ([]cacheanalytics.CacheRequestRecord, error) {
	records := []cacheanalytics.CacheRequestRecord{}
	if strings.TrimSpace(turnID) == "" {
		return records, nil
	}
	rows, ok, err := s.store.query(
		`SELECT record_json FROM usage_requests
WHERE session_id = ? AND (turn_id = ? OR trace_id = ?)
ORDER BY started_at_unix_nano ASC, step ASC, llm_request_id ASC LIMIT ?`,
		sessionID, turnID, turnID, limit)
	if err != nil {
		return nil, cacheanalytics.ErrInternal
	}
	if !ok {
		return records, nil
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, cacheanalytics.ErrInternal
		}
		var record cacheanalytics.CacheRequestRecord
		if err := jsonUnmarshalRecord(raw, &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, cacheanalytics.ErrInternal
	}
	return records, nil
}

// requestsAfter 返回该时刻之后开始的请求（按时间升序）。
func (s *CacheSource) requestsAfter(sessionID string, after time.Time, limit int) ([]cacheanalytics.CacheRequestRecord, error) {
	records := []cacheanalytics.CacheRequestRecord{}
	rows, ok, err := s.store.query(
		`SELECT record_json FROM usage_requests
WHERE session_id = ? AND started_at_unix_nano > ?
ORDER BY started_at_unix_nano ASC, step ASC, llm_request_id ASC LIMIT ?`,
		sessionID, after.UnixNano(), limit)
	if err != nil {
		return nil, cacheanalytics.ErrInternal
	}
	if !ok {
		return records, nil
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, cacheanalytics.ErrInternal
		}
		var record cacheanalytics.CacheRequestRecord
		if err := jsonUnmarshalRecord(raw, &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, cacheanalytics.ErrInternal
	}
	return records, nil
}

func producedByRecord(record *cacheanalytics.CacheRequestRecord) *cacheanalytics.ProducedBy {
	if record == nil {
		return nil
	}
	produced := &cacheanalytics.ProducedBy{
		LLMRequestID:  record.LLMRequestID,
		Usage:         record.Usage,
		CacheHitRatio: record.CacheHitRatio,
		CacheStatus:   record.CacheStatus,
		TraceID:       record.TraceID,
		TurnID:        record.TurnID,
	}
	if !record.StartedAt.IsZero() {
		started := record.StartedAt
		produced.StartedAt = &started
	}
	return produced
}

func consumedByRecords(records []cacheanalytics.CacheRequestRecord) []cacheanalytics.ConsumedBy {
	consumed := make([]cacheanalytics.ConsumedBy, 0, len(records))
	for i := range records {
		record := &records[i]
		entry := cacheanalytics.ConsumedBy{
			LLMRequestID:  record.LLMRequestID,
			Step:          record.Step,
			TurnID:        record.TurnID,
			CacheHitRatio: record.CacheHitRatio,
			CacheStatus:   record.CacheStatus,
		}
		if !record.StartedAt.IsZero() {
			started := record.StartedAt
			entry.StartedAt = &started
		}
		consumed = append(consumed, entry)
	}
	return consumed
}
