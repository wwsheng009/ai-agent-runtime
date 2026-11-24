package runtimeapi

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// GetTeamPreflight returns a read-only validation report for an existing
// legacy Team. It deliberately does not imply that the Team has an independent
// Definition/Run resource or a Team-level lifecycle action API.
func (h *Handler) GetTeamPreflight(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(
			errors.ErrConfigInvalid,
			"team store not configured",
		))
		return
	}

	teamID := strings.TrimSpace(mux.Vars(r)["id"])
	workspaceRoot := ""
	if h != nil {
		h.teamStoreMu.RLock()
		if h.runtimeConfig != nil {
			workspaceRoot = strings.TrimSpace(h.runtimeConfig.Workspace.Root)
		}
		h.teamStoreMu.RUnlock()
	}

	service := team.NewPreflightService(store, team.PreflightOptions{
		WorkspaceRoot: workspaceRoot,
		RouteResolver: h.newTeamTaskRouteResolver(),
	})
	report, err := service.Build(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(
			errors.ErrConfigInvalid,
			"failed to build Team preflight report",
			err,
		))
		return
	}

	traceID, requestID := runtimeTraceIDForRequest(r)
	w.Header().Set("X-Trace-ID", traceID)
	envelope := teamCompatibilityEnvelope(r.Context(), store, report.Team, teamID, requestID, traceID)
	response := map[string]interface{}{
		"preflight": report,
		"readiness": report.Ready,
		"team_id":   teamID,
	}
	for key, value := range envelope {
		response[key] = value
	}
	h.writeJSON(w, http.StatusOK, response)
}

// GetTeamReadiness is the lightweight, compatibility-preserving readiness
// facade. The full report remains available in the same response so callers do
// not have to make a second request to explain a false readiness result.
func (h *Handler) GetTeamReadiness(w http.ResponseWriter, r *http.Request) {
	h.GetTeamPreflight(w, r)
}
