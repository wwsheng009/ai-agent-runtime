package runtimeapi

import (
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

const maxTeamProductRequestBytes = 2 << 20

type createTeamDefinitionAPIRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Config      json.RawMessage `json:"config"`
}

type updateTeamDefinitionAPIRequest struct {
	ExpectedVersion int64           `json:"expected_version"`
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	Config          json.RawMessage `json:"config"`
}

type createTeamRunDraftAPIRequest struct {
	DefinitionID              string          `json:"definition_id,omitempty"`
	ExpectedDefinitionVersion int64           `json:"expected_definition_version,omitempty"`
	Goal                      string          `json:"goal"`
	Config                    json.RawMessage `json:"config,omitempty"`
}

type startTeamRunAPIRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

// ListTeamDefinitions lists only Definitions owned by the verified caller in
// the workspace asserted by that same trusted identity.
func (h *Handler) ListTeamDefinitions(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.requireTeamProductIdentity(w, r)
	if !ok {
		return
	}
	store, ok := h.teamProductStore(w, r)
	if !ok {
		return
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeTeamProductError(w, r, http.StatusBadRequest, "TEAM_PRODUCT_INVALID", "invalid limit", "validation", false)
		return
	}
	definitions, err := store.ListTeamDefinitions(r.Context(), identity.OwnerID, identity.WorkspaceID, limit)
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	writeTeamProductJSON(w, http.StatusOK, map[string]interface{}{
		"definitions":  definitions,
		"count":        len(definitions),
		"limit":        limit,
		"capabilities": teamDefinitionCapabilities(store),
		"request_id":   teamProductRequestID(w, r),
	})
}

// CreateTeamDefinition creates reusable configuration owned by the verified
// caller. Owner and workspace are deliberately derived from trusted claims or
// a configured API-key binding, never from request JSON.
func (h *Handler) CreateTeamDefinition(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.requireTeamProductIdentity(w, r)
	if !ok {
		return
	}
	store, ok := h.teamProductStore(w, r)
	if !ok {
		return
	}
	var request createTeamDefinitionAPIRequest
	if !decodeTeamProductJSON(w, r, &request) {
		return
	}
	definition, err := store.CreateTeamDefinition(r.Context(), team.TeamDefinitionCreateRequest{
		OwnerID:     identity.OwnerID,
		WorkspaceID: identity.WorkspaceID,
		Name:        request.Name,
		Description: request.Description,
		Config:      request.Config,
	})
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	writeTeamProductJSON(w, http.StatusCreated, map[string]interface{}{
		"definition":   definition,
		"capabilities": teamDefinitionCapabilities(store),
		"request_id":   teamProductRequestID(w, r),
	})
}

// GetTeamDefinition returns a Definition only when both the owner and
// workspace match the verified request identity. A mismatch is intentionally
// indistinguishable from a missing resource.
func (h *Handler) GetTeamDefinition(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.requireTeamProductIdentity(w, r)
	if !ok {
		return
	}
	store, ok := h.teamProductStore(w, r)
	if !ok {
		return
	}
	definition, err := store.GetTeamDefinition(r.Context(), identity.OwnerID, mux.Vars(r)["id"])
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	if definition == nil || definition.WorkspaceID != identity.WorkspaceID {
		writeTeamProductError(w, r, http.StatusNotFound, "TEAM_DEFINITION_NOT_FOUND", "Team Definition not found", "not_found", false)
		return
	}
	writeTeamProductJSON(w, http.StatusOK, map[string]interface{}{
		"definition":   definition,
		"capabilities": teamDefinitionCapabilities(store),
		"request_id":   teamProductRequestID(w, r),
	})
}

// UpdateTeamDefinition performs a compare-and-swap update after checking the
// immutable workspace binding. Lifecycle fields are not accepted here.
func (h *Handler) UpdateTeamDefinition(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.requireTeamProductIdentity(w, r)
	if !ok {
		return
	}
	store, ok := h.teamProductStore(w, r)
	if !ok {
		return
	}
	definitionID := strings.TrimSpace(mux.Vars(r)["id"])
	current, err := store.GetTeamDefinition(r.Context(), identity.OwnerID, definitionID)
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	if current == nil || current.WorkspaceID != identity.WorkspaceID {
		writeTeamProductError(w, r, http.StatusNotFound, "TEAM_DEFINITION_NOT_FOUND", "Team Definition not found", "not_found", false)
		return
	}
	var request updateTeamDefinitionAPIRequest
	if !decodeTeamProductJSON(w, r, &request) {
		return
	}
	definition, err := store.UpdateTeamDefinition(r.Context(), team.TeamDefinitionUpdateRequest{
		ID:              definitionID,
		OwnerID:         identity.OwnerID,
		ExpectedVersion: request.ExpectedVersion,
		Name:            request.Name,
		Description:     request.Description,
		Config:          request.Config,
	})
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	writeTeamProductJSON(w, http.StatusOK, map[string]interface{}{
		"definition":   definition,
		"capabilities": teamDefinitionCapabilities(store),
		"request_id":   teamProductRequestID(w, r),
	})
}

// ListTeamRuns returns only the verified caller's Runs in the trusted
// workspace. Legacy Team records remain a separate compatibility surface.
func (h *Handler) ListTeamRuns(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.requireTeamProductIdentity(w, r)
	if !ok {
		return
	}
	store, ok := h.teamProductStore(w, r)
	if !ok {
		return
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeTeamProductError(w, r, http.StatusBadRequest, "TEAM_PRODUCT_INVALID", "invalid limit", "validation", false)
		return
	}
	runs, err := store.ListTeamRuns(r.Context(), identity.OwnerID, identity.WorkspaceID, limit)
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	writeTeamProductJSON(w, http.StatusOK, map[string]interface{}{
		"runs":         runs,
		"count":        len(runs),
		"limit":        limit,
		"capabilities": teamRunCapabilities(store),
		"request_id":   teamProductRequestID(w, r),
	})
}

// CreateTeamRunDraft creates only an inert immutable-snapshot draft. No Team
// execution or lifecycle operation is implied by this endpoint.
func (h *Handler) CreateTeamRunDraft(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.requireTeamProductIdentity(w, r)
	if !ok {
		return
	}
	store, ok := h.teamProductStore(w, r)
	if !ok {
		return
	}
	var request createTeamRunDraftAPIRequest
	if !decodeTeamProductJSON(w, r, &request) {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeTeamProductError(w, r, http.StatusBadRequest, "TEAM_PRODUCT_INVALID", "Idempotency-Key header is required", "validation", false)
		return
	}
	requestID := teamProductRequestID(w, r)
	run, err := store.CreateTeamRunDraft(r.Context(), team.TeamRunCreateRequest{
		OwnerID:                   identity.OwnerID,
		WorkspaceID:               identity.WorkspaceID,
		DefinitionID:              request.DefinitionID,
		ExpectedDefinitionVersion: request.ExpectedDefinitionVersion,
		Goal:                      request.Goal,
		Config:                    request.Config,
		IdempotencyKey:            idempotencyKey,
		RequestID:                 requestID,
	})
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	writeTeamProductJSON(w, http.StatusCreated, map[string]interface{}{
		"run":          run,
		"capabilities": teamRunCapabilities(store),
		"request_id":   requestID,
	})
}

// StartTeamRun performs the only currently supported product lifecycle
// mutation. The store atomically validates the immutable snapshot, projects
// the legacy Team runtime, records a durable start event, and transitions the
// Run. The existing supervisor/orchestrator remains the execution authority.
func (h *Handler) StartTeamRun(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.requireTeamProductIdentity(w, r)
	if !ok {
		return
	}
	productStore, ok := h.teamProductStore(w, r)
	if !ok {
		return
	}
	starter, ok := productStore.(team.TeamRunStarter)
	if !ok || starter == nil {
		writeTeamProductError(w, r, http.StatusServiceUnavailable, "TEAM_RUN_START_UNAVAILABLE",
			"Team Run Start is unavailable for this runtime store", "unavailable", true)
		return
	}
	var request startTeamRunAPIRequest
	if !decodeTeamProductJSON(w, r, &request) {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeTeamProductError(w, r, http.StatusBadRequest, "TEAM_PRODUCT_INVALID",
			"Idempotency-Key header is required", "validation", false)
		return
	}
	runID := strings.TrimSpace(mux.Vars(r)["id"])
	current, err := productStore.GetTeamRun(r.Context(), identity.OwnerID, runID)
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	if current == nil || current.WorkspaceID != identity.WorkspaceID {
		writeTeamProductError(w, r, http.StatusNotFound, "TEAM_RUN_NOT_FOUND", "Team Run not found", "not_found", false)
		return
	}
	run, err := starter.StartTeamRun(r.Context(), team.TeamRunStartRequest{
		OwnerID:         identity.OwnerID,
		WorkspaceID:     identity.WorkspaceID,
		RunID:           runID,
		ExpectedVersion: request.ExpectedVersion,
		IdempotencyKey:  idempotencyKey,
		RequestID:       teamProductRequestID(w, r),
	})
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.SyncLoops()
	}
	writeTeamProductJSON(w, http.StatusOK, map[string]interface{}{
		"run":          run,
		"capabilities": teamRunCapabilities(productStore),
		"request_id":   teamProductRequestID(w, r),
	})
}

// GetTeamRun is owner/workspace scoped and deliberately returns a not-found
// response for resources belonging to another workspace.
func (h *Handler) GetTeamRun(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.requireTeamProductIdentity(w, r)
	if !ok {
		return
	}
	store, ok := h.teamProductStore(w, r)
	if !ok {
		return
	}
	run, err := store.GetTeamRun(r.Context(), identity.OwnerID, mux.Vars(r)["id"])
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	if run == nil || run.WorkspaceID != identity.WorkspaceID {
		writeTeamProductError(w, r, http.StatusNotFound, "TEAM_RUN_NOT_FOUND", "Team Run not found", "not_found", false)
		return
	}
	writeTeamProductJSON(w, http.StatusOK, map[string]interface{}{
		"run":          run,
		"capabilities": teamRunCapabilities(store),
		"request_id":   teamProductRequestID(w, r),
	})
}

// GetTeamRunPreflight evaluates the immutable Run snapshot without creating a
// legacy Team or changing any lifecycle state. The Start handler repeats the
// same checks inside its transaction, so this response is explanatory rather
// than a client-supplied readiness assertion.
func (h *Handler) GetTeamRunPreflight(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.requireTeamProductIdentity(w, r)
	if !ok {
		return
	}
	store, ok := h.teamProductStore(w, r)
	if !ok {
		return
	}
	preflighter, ok := store.(team.TeamRunPreflighter)
	if !ok || preflighter == nil {
		writeTeamProductError(w, r, http.StatusServiceUnavailable, "TEAM_RUN_PREFLIGHT_UNAVAILABLE",
			"Team Run preflight is unavailable for this runtime store", "unavailable", true)
		return
	}
	runID := strings.TrimSpace(mux.Vars(r)["id"])
	run, err := store.GetTeamRun(r.Context(), identity.OwnerID, runID)
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	if run == nil || run.WorkspaceID != identity.WorkspaceID {
		writeTeamProductError(w, r, http.StatusNotFound, "TEAM_RUN_NOT_FOUND", "Team Run not found", "not_found", false)
		return
	}
	report, err := preflighter.PreflightTeamRun(r.Context(), team.TeamRunPreflightRequest{
		OwnerID:     identity.OwnerID,
		WorkspaceID: identity.WorkspaceID,
		RunID:       runID,
	})
	if err != nil {
		writeTeamProductStoreError(w, r, err)
		return
	}
	writeTeamProductJSON(w, http.StatusOK, map[string]interface{}{
		"run":          run,
		"run_id":       runID,
		"preflight":    report,
		"readiness":    report != nil && report.Ready,
		"capabilities": teamRunCapabilities(store),
		"request_id":   teamProductRequestID(w, r),
	})
}

func teamDefinitionCapabilities(store team.TeamDefinitionRunStore) map[string]string {
	runExecution := "unsupported"
	if _, ok := store.(team.TeamRunStarter); ok {
		runExecution = "supported"
	}
	runPreflight := "unsupported"
	if _, ok := store.(team.TeamRunPreflighter); ok {
		runPreflight = "supported"
	}
	return map[string]string{
		"definition_read":         "supported",
		"definition_update":       "supported",
		"run_draft_create":        "supported",
		"run_execution":           runExecution,
		"run_preflight":           runPreflight,
		"run_route_preview":       "unsupported",
		"run_history":             "unsupported",
		"run_pause":               "unsupported",
		"run_resume":              "unsupported",
		"run_cancel":              "unsupported",
		"worker_session_liveness": "unsupported",
		"budget_hard_stop":        "unsupported",
		"event_stream":            "unsupported",
		"artifact_download":       "unsupported",
		"artifact_retention":      "unsupported",
		"attempt_lineage":         "unsupported",
	}
}

func teamRunCapabilities(store team.TeamDefinitionRunStore) map[string]string {
	runStart := "unsupported"
	if _, ok := store.(team.TeamRunStarter); ok {
		runStart = "supported"
	}
	runPreflight := "unsupported"
	if _, ok := store.(team.TeamRunPreflighter); ok {
		runPreflight = "supported"
	}
	return map[string]string{
		"run_draft_read":          "supported",
		"run_draft_list":          "supported",
		"run_draft_create":        "supported",
		"run_preflight":           runPreflight,
		"run_route_preview":       "unsupported",
		"run_history":             "unsupported",
		"run_start":               runStart,
		"run_pause":               "unsupported",
		"run_resume":              "unsupported",
		"run_cancel":              "unsupported",
		"worker_session_liveness": "unsupported",
		"budget_hard_stop":        "unsupported",
		"event_stream":            "unsupported",
		"artifact_download":       "unsupported",
		"artifact_retention":      "unsupported",
		"attempt_lineage":         "unsupported",
	}
}

func (h *Handler) requireTeamProductIdentity(w http.ResponseWriter, r *http.Request) (teamProductIdentity, bool) {
	identity, ok := h.resolveTeamProductIdentity(r)
	if !ok {
		writeTeamProductError(w, r, http.StatusUnauthorized, "TEAM_PRODUCT_UNAUTHENTICATED",
			"verified user and workspace identity are required", "authorization", false)
		return teamProductIdentity{}, false
	}
	return identity, true
}

func (h *Handler) teamProductStore(w http.ResponseWriter, r *http.Request) (team.TeamDefinitionRunStore, bool) {
	store := h.getTeamStore()
	productStore, ok := store.(team.TeamDefinitionRunStore)
	if !ok || productStore == nil {
		writeTeamProductError(w, r, http.StatusServiceUnavailable, "TEAM_PRODUCT_RESOURCES_UNAVAILABLE",
			"Team Definition/Run resources are unavailable", "unavailable", true)
		return nil, false
	}
	return productStore, true
}

func decodeTeamProductJSON(w http.ResponseWriter, r *http.Request, target interface{}) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTeamProductRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeTeamProductError(w, r, http.StatusBadRequest, "TEAM_PRODUCT_INVALID",
			"invalid Team product request", "validation", false)
		return false
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeTeamProductError(w, r, http.StatusBadRequest, "TEAM_PRODUCT_INVALID",
			"request body must contain one JSON object", "validation", false)
		return false
	}
	return true
}

func writeTeamProductStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case stderrors.Is(err, team.ErrTeamDefinitionNotFound):
		writeTeamProductError(w, r, http.StatusNotFound, "TEAM_DEFINITION_NOT_FOUND", "Team Definition not found", "not_found", false)
	case stderrors.Is(err, team.ErrTeamDefinitionVersionConflict):
		writeTeamProductError(w, r, http.StatusConflict, "TEAM_DEFINITION_VERSION_CONFLICT", "Team Definition version conflict", "conflict", false)
	case stderrors.Is(err, team.ErrTeamDefinitionArchived):
		writeTeamProductError(w, r, http.StatusConflict, "TEAM_DEFINITION_ARCHIVED", "Team Definition is archived", "conflict", false)
	case stderrors.Is(err, team.ErrTeamRunIdempotencyConflict):
		writeTeamProductError(w, r, http.StatusConflict, "TEAM_RUN_IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used with a different request", "conflict", false)
	case stderrors.Is(err, team.ErrTeamRunNotFound):
		writeTeamProductError(w, r, http.StatusNotFound, "TEAM_RUN_NOT_FOUND", "Team Run not found", "not_found", false)
	case stderrors.Is(err, team.ErrTeamRunVersionConflict):
		writeTeamProductError(w, r, http.StatusConflict, "TEAM_RUN_VERSION_CONFLICT", "Team Run version conflict", "conflict", false)
	case stderrors.Is(err, team.ErrTeamRunInvalidTransition):
		writeTeamProductError(w, r, http.StatusConflict, "TEAM_RUN_INVALID_TRANSITION", "Team Run cannot be started in its current state", "conflict", false)
	case stderrors.Is(err, team.ErrTeamRunStartIdempotencyConflict):
		writeTeamProductError(w, r, http.StatusConflict, "TEAM_RUN_START_IDEMPOTENCY_CONFLICT", "Start Idempotency-Key was already used with a different request", "conflict", false)
	case stderrors.Is(err, team.ErrTeamRunSnapshotCorrupt):
		writeTeamProductError(w, r, http.StatusConflict, "TEAM_RUN_SNAPSHOT_CORRUPT", "Team Run snapshot integrity check failed", "integrity", false)
	case stderrors.Is(err, team.ErrTeamRunProjectionConflict):
		writeTeamProductError(w, r, http.StatusConflict, "TEAM_RUN_PROJECTION_CONFLICT", "Team Run legacy projection conflicts with an existing Team", "conflict", false)
	case stderrors.Is(err, team.ErrTeamRunPreflightBlocked):
		var blocked *team.TeamRunPreflightBlockedError
		if stderrors.As(err, &blocked) {
			writeTeamProductJSON(w, http.StatusConflict, map[string]interface{}{
				"error": map[string]interface{}{
					"code":       "TEAM_RUN_PREFLIGHT_BLOCKED",
					"message":    "Team Run preflight has blockers",
					"category":   "preflight",
					"retryable":  false,
					"request_id": teamProductRequestID(w, r),
					"findings":   blocked.Findings,
				},
				"code":       "TEAM_RUN_PREFLIGHT_BLOCKED",
				"request_id": teamProductRequestID(w, r),
				"findings":   blocked.Findings,
			})
		} else {
			writeTeamProductError(w, r, http.StatusConflict, "TEAM_RUN_PREFLIGHT_BLOCKED", "Team Run preflight has blockers", "preflight", false)
		}
	case stderrors.Is(err, team.ErrTeamResourceInvalid):
		writeTeamProductError(w, r, http.StatusBadRequest, "TEAM_PRODUCT_INVALID", err.Error(), "validation", false)
	default:
		writeTeamProductError(w, r, http.StatusInternalServerError, "TEAM_PRODUCT_INTERNAL", "Team product resource operation failed", "internal", false)
	}
}

func writeTeamProductError(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	code string,
	message string,
	category string,
	retryable bool,
) {
	requestID := teamProductRequestID(w, r)
	payload := map[string]interface{}{
		"error": map[string]interface{}{
			"code":       code,
			"message":    message,
			"category":   category,
			"retryable":  retryable,
			"request_id": requestID,
		},
		"code":       code,
		"request_id": requestID,
	}
	writeTeamProductJSON(w, status, payload)
}

func writeTeamProductJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func teamProductRequestID(w http.ResponseWriter, r *http.Request) string {
	if requestID := strings.TrimSpace(w.Header().Get("X-Request-ID")); requestID != "" {
		return requestID
	}
	requestID := strings.TrimSpace(requestIDFromRequest(r))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	w.Header().Set("X-Request-ID", requestID)
	return requestID
}
