package skills

import (
	"net/http"
	"strconv"
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// ListRuntimeEvents returns recent runtime events with optional filters.
func (h *Handler) ListRuntimeEvents(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if limit == 0 {
		limit = 50
	}

	filters := runtimeevents.QueryFilter{
		TraceID:             strings.TrimSpace(r.URL.Query().Get("trace_id")),
		SessionID:           strings.TrimSpace(r.URL.Query().Get("session_id")),
		AgentName:           strings.TrimSpace(r.URL.Query().Get("agent_name")),
		ToolName:            strings.TrimSpace(r.URL.Query().Get("tool_name")),
		EventType:           strings.TrimSpace(r.URL.Query().Get("event_type")),
		TeamID:              strings.TrimSpace(r.URL.Query().Get("team_id")),
		ProfileResourceKind: strings.TrimSpace(r.URL.Query().Get("profile_resource_kind")),
		Limit:               limit,
	}

	events := h.getRuntimeEventBus().Query(filters)
	payload := map[string]interface{}{
		"events":     events,
		"count":      len(events),
		"provenance": summarizeRuntimeEventProvenance(events),
		"filters": map[string]interface{}{
			"trace_id":              filters.TraceID,
			"session_id":            filters.SessionID,
			"agent_name":            filters.AgentName,
			"tool_name":             filters.ToolName,
			"event_type":            filters.EventType,
			"team_id":               filters.TeamID,
			"profile_resource_kind": filters.ProfileResourceKind,
			"limit":                 limit,
		},
	}

	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw == "" {
		w.Header().Set("X-AI-Gateway-Default-Limit", strconv.Itoa(limit))
	}

	h.writeJSON(w, http.StatusOK, payload)
}

