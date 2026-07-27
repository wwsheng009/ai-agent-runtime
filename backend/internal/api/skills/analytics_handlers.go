package skills

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/chataloganalytics"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// SetChatLogsDir overrides the chat-log root used by usage analytics endpoints.
func (h *Handler) SetChatLogsDir(path string) {
	path = aiclipaths.ExpandUserPath(path)
	if path != "" {
		if absolutePath, err := filepath.Abs(path); err == nil {
			path = absolutePath
		}
	}
	h.chatLogsDir = path
}

func (h *Handler) resolveChatLogsDir() string {
	if h != nil {
		if path := strings.TrimSpace(h.chatLogsDir); path != "" {
			return path
		}
	}
	return aiclipaths.DefaultChatLogsDir()
}

// ListAnalyticsSessions returns per-session usage rollups from chat logs.
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

	result, err := chataloganalytics.ListSessions(h.resolveChatLogsDir(), query)
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

	result, err := chataloganalytics.Summarize(h.resolveChatLogsDir(), query)
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
	maxScan := 0
	if r != nil && r.URL != nil {
		raw := strings.TrimSpace(r.URL.Query().Get("max_scan"))
		if raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid max_scan value"))
				return
			}
			maxScan = parsed
		}
	}
	result, err := chataloganalytics.Dimensions(h.resolveChatLogsDir(), maxScan)
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

	sessionID := strings.TrimSpace(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	result, err := chataloganalytics.SessionUsage(h.resolveChatLogsDir(), sessionID)
	if err != nil {
		if chataloganalytics.IsNotFound(err) {
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
	sessionID := strings.TrimSpace(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	result, err := chataloganalytics.SessionUsage(h.resolveChatLogsDir(), sessionID)
	if err != nil {
		if chataloganalytics.IsNotFound(err) {
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

func parseAnalyticsQuery(r *http.Request) (chataloganalytics.Query, error) {
	q := chataloganalytics.Query{}
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
