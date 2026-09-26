package runtimeapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ============================================================================
// 用量分析端点（runtime.analytics.v1）——DB-only。
//
// 数据源唯一：usageanalytics.Service（~/.aicli/sessions/runtime/usage_analytics.sqlite，
// 由 runtime EventBus 事件实时写入）。不再扫描 chat-logs 目录。
// 认证与错误码保持原行为（authorizeUsageAdmin，Bearer admin token）。
// ============================================================================

// ListAnalyticsSessions returns per-session usage rollups from the analytics DB.
func (h *Handler) ListAnalyticsSessions(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseAnalyticsQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.ListSessions(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// GetAnalyticsSummary returns multi-dimension usage totals.
func (h *Handler) GetAnalyticsSummary(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseAnalyticsQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.Summarize(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// GetAnalyticsOverview 返回首屏一次性引导载荷（方案 Phase 2）：sessions +
// summary + dimensions 在同一 handler 内顺序取齐，减少 HTTP 往返与重复
// WHERE 解析；顶层保留 matched 字段兼容旧断言，summary 保留为完整对象。
//
// 方案：docs/plan/usage-analytics-query-performance-optimization-plan-20260917.md §7。
func (h *Handler) GetAnalyticsOverview(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseAnalyticsQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	sessions, err := service.ListSessions(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	summary, err := service.Summarize(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	dimensions, err := service.Dimensions(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema_version": usageanalytics.SchemaVersion,
		"generated_at":   time.Now().UTC(),
		"sessions":       sessions,
		"summary":        summary,
		"dimensions":     dimensions,
		"matched":        sessions.Total,
	})
}

// GetAnalyticsDimensions returns distinct finite values for analytics filters.
func (h *Handler) GetAnalyticsDimensions(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseAnalyticsQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.Dimensions(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// GetAnalyticsSessionUsage returns one session's usage detail including LLM steps.
func (h *Handler) GetAnalyticsSessionUsage(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.SessionUsage(sessionID)
	if err != nil {
		if usageanalytics.IsNotFound(err) {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrAPINotFound, err.Error()))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// ListAnalyticsSessionTurns returns the bounded turn facts for one session.
func (h *Handler) ListAnalyticsSessionTurns(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.SessionUsage(sessionID)
	if err != nil {
		if usageanalytics.IsNotFound(err) {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrAPINotFound, err.Error()))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema_version":  result.SchemaVersion,
		"generated_at":    result.GeneratedAt,
		"session_id":      result.Session.SessionID,
		"turns":           result.Turns,
		"count":           len(result.Turns),
		"coverage":        result.Coverage,
		"partial":         result.Partial,
		"partial_reasons": result.PartialReasons,
	})
}

// ListAnalyticsToolStats returns per-tool rollups from the analytics DB
// （schema v2；空库返回空数组，不返回 500）。
func (h *Handler) ListAnalyticsToolStats(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseToolStatsQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.ToolStats(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// GetAnalyticsToolStatsDetail returns a single tool's percentile durations
// and error code Top-N (lazy-loaded, not part of the initial page load).
func (h *Handler) GetAnalyticsToolStatsDetail(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseToolStatsQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	vars := mux.Vars(r)
	toolName := vars["tool"]
	if toolName == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "tool name is required"))
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.ToolStatsDetail(query, toolName)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// ListAnalyticsSubagentStats returns per-subagent rollups（失败率/分类/来源；
// 空库返回空数组，不返回 500）。
func (h *Handler) ListAnalyticsSubagentStats(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseSubagentStatsQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.SubagentStats(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// ListAnalyticsErrorPatterns returns error_code / failure_category Top-N
// （来源 tools|subagents|requests；空库返回空数组，不返回 500）。
func (h *Handler) ListAnalyticsErrorPatterns(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseErrorPatternsQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.ErrorPatterns(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// parseToolStatsQuery 解析 /analytics/tools 参数
// （session/tool/outcome/from/to/limit）。
func parseToolStatsQuery(r *http.Request) (usageanalytics.ToolStatsQuery, error) {
	query := usageanalytics.ToolStatsQuery{}
	values := analyticsQueryValues(r)
	from, to, err := parseAnalyticsTimeWindow(values)
	if err != nil {
		return query, err
	}
	query.From = from
	query.To = to
	query.SessionID = firstAnalyticsValue(values, "session", "session_id")
	query.ToolName = firstAnalyticsValue(values, "tool", "tool_name")
	query.Outcome = strings.TrimSpace(values.Get("outcome"))
	query.Limit = parseAnalyticsLimit(values.Get("limit"))
	return query, nil
}

// parseSubagentStatsQuery 解析 /analytics/subagents 参数
// （session/failure_category/failed_only/from/to/limit）。
func parseSubagentStatsQuery(r *http.Request) (usageanalytics.SubagentStatsQuery, error) {
	query := usageanalytics.SubagentStatsQuery{}
	values := analyticsQueryValues(r)
	from, to, err := parseAnalyticsTimeWindow(values)
	if err != nil {
		return query, err
	}
	query.From = from
	query.To = to
	query.SessionID = firstAnalyticsValue(values, "session", "session_id")
	query.FailureCategory = strings.TrimSpace(values.Get("failure_category"))
	query.FailedOnly = parseAnalyticsBool(values.Get("failed_only"))
	query.Limit = parseAnalyticsLimit(values.Get("limit"))
	return query, nil
}

// parseErrorPatternsQuery 解析 /analytics/errors 参数
// （session/source/top/from/to）。
func parseErrorPatternsQuery(r *http.Request) (usageanalytics.ErrorPatternsQuery, error) {
	query := usageanalytics.ErrorPatternsQuery{}
	values := analyticsQueryValues(r)
	from, to, err := parseAnalyticsTimeWindow(values)
	if err != nil {
		return query, err
	}
	query.From = from
	query.To = to
	query.SessionID = firstAnalyticsValue(values, "session", "session_id")
	query.Source = strings.TrimSpace(values.Get("source"))
	query.Top = parseAnalyticsLimit(values.Get("top"))
	return query, nil
}

// analyticsQueryValues 返回查询参数（空请求返回空集合，保证解析器 nil 安全）。
func analyticsQueryValues(r *http.Request) url.Values {
	if r == nil || r.URL == nil {
		return url.Values{}
	}
	return r.URL.Query()
}

// firstAnalyticsValue 返回第一个非空参数值（兼容 alias，如 session/session_id）。
func firstAnalyticsValue(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

// parseAnalyticsLimit 解析 limit/top：非法值或负值归一为 0（由查询层取默认上限），
// 只读端点不因过滤参数拼写失败而 400。
func parseAnalyticsLimit(raw string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

// parseAnalyticsBool 解析布尔过滤参数：空值或非法值归一为 false。
func parseAnalyticsBool(raw string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && value
}

func parseAnalyticsQuery(r *http.Request) (usageanalytics.Query, error) {
	q := usageanalytics.Query{}
	if r == nil || r.URL == nil {
		return q, nil
	}
	values := r.URL.Query()

	from, to, err := parseAnalyticsTimeWindow(values)
	if err != nil {
		return q, err
	}
	q.From = from
	q.To = to
	q.Provider = strings.TrimSpace(values.Get("provider"))
	q.Model = strings.TrimSpace(values.Get("model"))
	q.Directory = strings.TrimSpace(values.Get("directory"))
	q.Project = strings.TrimSpace(values.Get("project"))
	q.Status = strings.TrimSpace(values.Get("status"))
	q.Query = strings.TrimSpace(values.Get("q"))
	if q.Query == "" {
		q.Query = strings.TrimSpace(values.Get("query"))
	}
	q.GroupBy = strings.TrimSpace(values.Get("group_by"))

	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 {
			return q, errors.New(errors.ErrValidationFailed, "invalid limit value")
		}
		q.Limit = parsed
	}
	if raw := strings.TrimSpace(values.Get("offset")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 {
			return q, errors.New(errors.ErrValidationFailed, "invalid offset value")
		}
		q.Offset = parsed
	}
	if raw := strings.TrimSpace(values.Get("max_scan")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 {
			return q, errors.New(errors.ErrValidationFailed, "invalid max_scan value")
		}
		q.MaxScan = parsed
	}
	return q, nil
}

// parseAnalyticsTimeWindow 解析 from/to 时间窗；to 为纯日期时按"包含整天"处理
// （与既有 /analytics/sessions 语义一致）。
func parseAnalyticsTimeWindow(values url.Values) (time.Time, time.Time, error) {
	from, err := parseOptionalAnalyticsTime(values.Get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, errors.New(errors.ErrValidationFailed, "invalid from value")
	}
	toRaw := strings.TrimSpace(values.Get("to"))
	to, err := parseOptionalAnalyticsTime(toRaw)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New(errors.ErrValidationFailed, "invalid to value")
	}
	if len(toRaw) == len("2006-01-02") && !to.IsZero() {
		to = to.AddDate(0, 0, 1)
	}
	return from, to, nil
}

func parseOptionalAnalyticsTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		return parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local); err == nil {
		return parsed, nil
	}
	return time.Time{}, strconv.ErrSyntax
}

// ============================================================================
// 采集健康快照（方案 §4 批次 3.2）——挂到 runtimeStatusSnapshot 的
// usage_analytics 键上，供 /status、TUI 与三端"采集健康"可视化消费。
// ============================================================================

// usageAnalyticsHealthSnapshot 返回采集健康块。
//
// attached=false 时字段仍然齐全且语义稳定：db_path 为解析后的目标路径
// （不是空串隐藏），计数为 0，last_ingest_at=null，degraded=true。
func (h *Handler) usageAnalyticsHealthSnapshot() map[string]interface{} {
	block := map[string]interface{}{
		"attached":       false,
		"db_path":        "",
		"ingested_total": int64(0),
		"conflict_total": int64(0),
		"last_ingest_at": nil,
		"table_counts": map[string]interface{}{
			"requests": int64(0), "sessions": int64(0), "tool_calls": int64(0),
			"subagents": int64(0), "turns": int64(0),
		},
		"degraded":    true,
		"stats_ready": false,
		"stats_drift": nil,
	}
	if h == nil {
		return block
	}
	block["db_path"] = h.usageAnalyticsStorePath()
	service := peekUsageAnalyticsService()
	if service == nil {
		return block
	}
	health := service.AnalyticsHealth()
	block["attached"] = true
	block["db_path"] = service.DBPath()
	block["ingested_total"] = health.IngestedTotal
	block["conflict_total"] = health.ConflictTotal
	block["last_ingest_at"] = health.LastIngestAt
	block["table_counts"] = map[string]interface{}{
		"requests":   health.TableCounts.Requests,
		"sessions":   health.TableCounts.Sessions,
		"tool_calls": health.TableCounts.ToolCalls,
		"subagents":  health.TableCounts.Subagents,
		"turns":      health.TableCounts.Turns,
	}
	block["degraded"] = health.Degraded
	block["stats_ready"] = health.StatsReady
	block["stats_drift"] = health.StatsDrift
	return block
}

// peekUsageAnalyticsService 返回已挂载的分析服务单例；不触发懒加载（无副作用），
// 复用 usage_analytics_handlers.go 的进程级互斥锁保证读安全。
func peekUsageAnalyticsService() *usageanalytics.Service {
	usageAnalyticsMu.Lock()
	defer usageAnalyticsMu.Unlock()
	return usageAnalyticsService
}
