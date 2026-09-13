package skills

import (
	"net/http"
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

func parseAnalyticsQuery(r *http.Request) (usageanalytics.Query, error) {
	q := usageanalytics.Query{}
	if r == nil || r.URL == nil {
		return q, nil
	}
	values := r.URL.Query()

	from, err := parseOptionalAnalyticsTime(values.Get("from"))
	if err != nil {
		return q, errors.New(errors.ErrValidationFailed, "invalid from value")
	}
	toRaw := strings.TrimSpace(values.Get("to"))
	to, err := parseOptionalAnalyticsTime(toRaw)
	if err != nil {
		return q, errors.New(errors.ErrValidationFailed, "invalid to value")
	}
	if len(toRaw) == len("2006-01-02") && !to.IsZero() {
		to = to.AddDate(0, 0, 1)
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
