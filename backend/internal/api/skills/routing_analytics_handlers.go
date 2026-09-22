package skills

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// ============================================================================
// 路由切换观测端点（主 Agent / 子 Agent）——DB-only，数据源同为 usageanalytics。
//
//   - GET /api/runtime/analytics/routing        总览（totals + 维度分布桶）
//   - GET /api/runtime/analytics/routing/events 明细（时间倒序分页）
//
// 认证与错误码与既有 /analytics/* 一致：authorizeUsageAdmin，时间参数非法 400，
// 空库返回空数组（不 500）。
// ============================================================================

// ListAnalyticsRoutingStats 返回主/子 Agent 路由切换总览。
func (h *Handler) ListAnalyticsRoutingStats(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseRouteQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.RouteStats(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// ListAnalyticsRoutingEvents 返回路由切换明细（时间倒序）。
func (h *Handler) ListAnalyticsRoutingEvents(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseRouteQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.RouteEvents(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// parseRouteQuery 解析 /analytics/routing 参数：
// from/to/scope/kind/source/provider/model/difficulty/session/warnings_only/limit/offset。
//
// scope 兼容简写（main|subagent|sub）；warnings_only=true 等价于 kind=warning
// （显式 kind 优先）。limit/offset 非法值归一为 0（查询层取默认值），不 400。
func parseRouteQuery(r *http.Request) (usageanalytics.RouteQuery, error) {
	query := usageanalytics.RouteQuery{}
	values := analyticsQueryValues(r)
	from, to, err := parseAnalyticsTimeWindow(values)
	if err != nil {
		return query, err
	}
	query.From = from
	query.To = to
	query.Scope = normalizeRouteScope(values.Get("scope"))
	query.Kind = strings.TrimSpace(values.Get("kind"))
	query.Source = strings.TrimSpace(values.Get("source"))
	query.Provider = strings.TrimSpace(values.Get("provider"))
	query.Model = strings.TrimSpace(values.Get("model"))
	query.Difficulty = strings.TrimSpace(values.Get("difficulty"))
	query.SessionID = firstAnalyticsValue(values, "session", "session_id")
	query.Limit = parseAnalyticsLimit(values.Get("limit"))
	query.Offset = parseRoutingOffset(values.Get("offset"))
	if query.Kind == "" && parseAnalyticsBool(values.Get("warnings_only")) {
		query.Kind = usageanalytics.RouteKindWarning
	}
	return query, nil
}

// normalizeRouteScope 归一 scope 简写到契约取值；未知值原样返回（由查询层过滤空结果）。
func normalizeRouteScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "main", "main_agent", "main-agent", "primary":
		return usageanalytics.RouteScopeMainAgent
	case "sub", "subagent", "sub_agent", "child":
		return usageanalytics.RouteScopeSubagent
	default:
		return strings.TrimSpace(raw)
	}
}

// parseRoutingOffset 解析分页偏移：非法值或负值归一为 0。
func parseRoutingOffset(raw string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}
