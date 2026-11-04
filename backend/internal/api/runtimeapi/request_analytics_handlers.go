package runtimeapi

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// GetAnalyticsRequests returns cross-session LLM request facts. Unlike the
// session-level cache endpoints, filters run before LIMIT/OFFSET, so a
// "today + cache ratio < 50%" view is complete rather than page-local.
func (h *Handler) GetAnalyticsRequests(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query, err := parseAnalyticsRequestQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	service := h.ensureUsageAnalyticsService(w)
	if service == nil {
		return
	}
	result, err := service.ListRequests(query)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func parseAnalyticsRequestQuery(r *http.Request) (usageanalytics.RequestQuery, error) {
	query := usageanalytics.RequestQuery{}
	values := analyticsQueryValues(r)
	from, to, err := parseAnalyticsTimeWindow(values)
	if err != nil {
		return query, err
	}
	query.From = from
	query.To = to
	query.SessionID = firstAnalyticsValue(values, "session", "session_id")
	query.Provider = strings.TrimSpace(values.Get("provider"))
	query.Model = strings.TrimSpace(values.Get("model"))
	query.Directory = strings.TrimSpace(values.Get("directory"))
	query.Project = strings.TrimSpace(values.Get("project"))
	query.SessionStatus = strings.TrimSpace(values.Get("status"))
	query.Query = firstAnalyticsValue(values, "q", "query")
	query.Status = strings.TrimSpace(values.Get("request_status"))
	query.CacheStatus = strings.TrimSpace(values.Get("cache_status"))
	query.CacheReportedOnly = parseAnalyticsBool(values.Get("cache_reported"))
	query.Limit = parseAnalyticsLimit(values.Get("limit"))
	query.Offset = parseAnalyticsLimit(values.Get("offset"))

	if raw := strings.TrimSpace(values.Get("success")); raw != "" {
		parsed, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return query, errors.New(errors.ErrValidationFailed, "invalid success value")
		}
		query.Success = &parsed
	}
	if query.Success == nil {
		if raw := strings.TrimSpace(values.Get("outcome")); raw != "" {
			success := strings.EqualFold(raw, "success")
			if !success && !strings.EqualFold(raw, "failure") && !strings.EqualFold(raw, "failed") {
				return query, errors.New(errors.ErrValidationFailed, "invalid outcome value")
			}
			query.Success = &success
		}
	}

	boundaries := []struct {
		key  string
		dest **float64
	}{
		{"cache_hit_ratio_min", &query.CacheHitRatioMin},
		{"cache_hit_ratio_max", &query.CacheHitRatioMax},
		{"cache_hit_ratio_lt", &query.CacheHitRatioBelow},
		{"cache_hit_ratio_gte", &query.CacheHitRatioAtLeast},
	}
	for _, boundary := range boundaries {
		value, parseErr := parseAnalyticsRatio(values.Get(boundary.key), boundary.key)
		if parseErr != nil {
			return query, parseErr
		}
		*boundary.dest = value
	}
	if query.CacheHitRatioMin != nil && query.CacheHitRatioMax != nil &&
		*query.CacheHitRatioMin > *query.CacheHitRatioMax {
		return query, errors.New(errors.ErrValidationFailed, "cache hit ratio range is inverted")
	}
	return query, nil
}

func parseAnalyticsRatio(raw, field string) (*float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return nil, errors.New(errors.ErrValidationFailed, "invalid "+field+" value")
	}
	return &value, nil
}
