package runtimeapi

import (
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

// planArtifactPreviewMaxRunes bounds the inline snapshot preview returned by the
// detail endpoint, mirroring the session plan-file preview limit.
const planArtifactPreviewMaxRunes = 200_000

type storedPlanRoundResponse struct {
	Version   int    `json:"version"`
	Decision  string `json:"decision,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Source    string `json:"source,omitempty"`
	Snapshot  string `json:"snapshot,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type storedPlanResponse struct {
	ID          string                    `json:"id"`
	SessionID   string                    `json:"session_id,omitempty"`
	ProjectSlug string                    `json:"project_slug,omitempty"`
	ProjectPath string                    `json:"project_path,omitempty"`
	PlanPath    string                    `json:"plan_path,omitempty"`
	Title       string                    `json:"title,omitempty"`
	Status      string                    `json:"status"`
	Version     int                       `json:"version"`
	Rounds      []storedPlanRoundResponse `json:"rounds,omitempty"`
	CreatedAt   string                    `json:"created_at,omitempty"`
	UpdatedAt   string                    `json:"updated_at,omitempty"`

	Content          string `json:"content,omitempty"`
	ContentAvailable bool   `json:"content_available,omitempty"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
	ContentError     string `json:"content_error,omitempty"`
}

type storedPlanListResponse struct {
	Plans []storedPlanResponse `json:"plans"`
	Count int                  `json:"count"`
}

// planArtifactStore returns the handler's plan artifact store, falling back to
// the process-wide default ($HOME/.aicli/plans, or AICLI_PLANS_DIR).
func (h *Handler) planArtifactStore() *planstore.Store {
	if h != nil && h.plansStore != nil {
		return h.plansStore
	}
	return planmode.DefaultPlanStore()
}

// ListStoredPlans returns every archived plan artifact (newest first), with an
// optional ?project=<slug> filter.
func (h *Handler) ListStoredPlans(w http.ResponseWriter, r *http.Request) {
	store := h.planArtifactStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "plan store not configured"))
		return
	}
	records, err := store.List()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid, err.Error()))
		return
	}
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	project = strings.ToLower(project)
	plans := make([]storedPlanResponse, 0, len(records))
	for _, record := range records {
		if project != "" && strings.ToLower(record.ProjectSlug) != project {
			continue
		}
		plans = append(plans, storedPlanResponseFromRecord(record))
	}
	h.writeJSON(w, http.StatusOK, storedPlanListResponse{Plans: plans, Count: len(plans)})
}

// GetStoredPlan returns one archived plan artifact plus the latest snapshot body.
func (h *Handler) GetStoredPlan(w http.ResponseWriter, r *http.Request) {
	store := h.planArtifactStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "plan store not configured"))
		return
	}
	// Path form: /plans/{id} where id may contain a slash; the query parameter
	// keeps the older id=? form working for scripted clients.
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		id = strings.Trim(strings.TrimSpace(mux.Vars(r)["id"]), "/")
	}
	if id == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "plan id is required"))
		return
	}
	record, ok, err := store.Get(id)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid, err.Error()))
		return
	}
	if !ok {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "plan not found: "+id))
		return
	}
	resp := storedPlanResponseFromRecord(record)
	if record.Version > 0 {
		if data, readErr := store.ReadLatest(record.ID); readErr != nil {
			resp.ContentError = readErr.Error()
		} else {
			content, truncated := truncatePlanArtifact(string(data))
			resp.Content = content
			resp.ContentAvailable = content != ""
			resp.ContentTruncated = truncated
		}
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// DeleteStoredPlan removes one archived plan (record + snapshot files).
//
// The route is idempotent: deleting an unknown id returns 200 with
// deleted=false so a retrying host never has to distinguish "already gone" from
// "never existed".
func (h *Handler) DeleteStoredPlan(w http.ResponseWriter, r *http.Request) {
	store := h.planArtifactStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "plan store not configured"))
		return
	}
	id := ""
	if r != nil {
		id = strings.Trim(strings.TrimSpace(mux.Vars(r)["id"]), "/")
	}
	if id == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "plan id is required"))
		return
	}
	_, existed, err := store.Get(id)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid, err.Error()))
		return
	}
	if err := store.Delete(id); err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrConfigInvalid, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":      id,
		"deleted": existed,
	})
}

type storedPlanReopenRequest struct {
	PlanID  string `json:"plan_id"`
	Version int    `json:"version"`
	// Force overwrites a workspace plan file that diverged from the snapshot.
	Force bool `json:"force"`
}

type storedPlanReopenResponse struct {
	Reopened    bool   `json:"reopened"`
	PlanID      string `json:"plan_id"`
	PlanPath    string `json:"plan_path,omitempty"`
	DisplayPath string `json:"display_path,omitempty"`
	Version     int    `json:"version"`
	Bytes       int    `json:"bytes"`
	Created     bool   `json:"created,omitempty"`
	Unchanged   bool   `json:"unchanged,omitempty"`
	Forced      bool   `json:"forced,omitempty"`
	// PlanMode is the session plan-mode state after the reopen, so a host can
	// render the review surface without a second request.
	PlanMode *sessionPlanModeResponse `json:"plan_mode,omitempty"`
}

// ReopenStoredPlan restores one archived round into the workspace plan file and
// enters plan mode on it (the HTTP twin of `/plans reopen`, report §4.5). The
// plan id travels in the body because archived ids contain "/" (for example
// "ai-agent-runtime/plan"), which cannot be a path segment before a suffix.
func (h *Handler) ReopenStoredPlan(w http.ResponseWriter, r *http.Request) {
	session, err := h.loadSessionForPlanMode(r)
	if err != nil {
		h.writePlanModeError(w, err)
		return
	}

	req, err := decodeStoredPlanReopenRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	if req.PlanID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "plan_id is required"))
		return
	}

	result, err := planmode.ReopenPlan(planmode.ReopenOptions{
		Store:     h.planArtifactStore(),
		Workspace: planModeWorkspacePath(session),
		RecordID:  req.PlanID,
		Version:   req.Version,
		Force:     req.Force,
	})
	if err != nil {
		h.writeStoredPlanReopenError(w, err)
		return
	}

	// Enter plan mode on the restored body. A live actor owns the transition
	// (session state + permission engine + events); otherwise it is applied to
	// the durable session directly, mirroring the enter action above.
	if actor, ok := h.sessionPlanModeActor(session.ID); ok && actor != nil {
		if _, err := actor.ReopenPlanMode(r.Context(), session.ID, chat.ReopenPlanModeArgs{
			RecordID: result.Record.ID,
			Version:  result.Version,
			PlanPath: result.DisplayPath,
		}); err != nil {
			h.writePlanModeError(w, err)
			return
		}
		reloaded, err := h.reloadSessionForPlanMode(r, session.ID)
		if err != nil {
			h.writePlanModeError(w, err)
			return
		}
		session = reloaded
	} else {
		if err := applyPlanModeToSession(session, "enter", "", result.DisplayPath, nil, ""); err != nil {
			h.writePlanModeError(w, err)
			return
		}
		state := planmode.MarkReopened(planmode.Load(session), result.Record.ID, result.Version)
		planmode.Save(session, state)
		ctx, cancel := sessionStoreQueryContext(r)
		defer cancel()
		if err := h.sessionManager.Update(ctx, session); err != nil {
			writeSessionStoreError(w, err)
			return
		}
		h.archiveSessionPlanArtifact(r.Context(), session, "enter", "", "")
	}

	h.writeJSON(w, http.StatusOK, storedPlanReopenResponse{
		Reopened:    true,
		PlanID:      result.Record.ID,
		PlanPath:    result.PlanPath,
		DisplayPath: result.DisplayPath,
		Version:     result.Version,
		Bytes:       result.Bytes,
		Created:     result.Created,
		Unchanged:   result.Unchanged,
		Forced:      req.Force,
		PlanMode:    planModeResponsePtr(h.buildSessionPlanModeResponse(session, "reopen")),
	})
}

// writeStoredPlanReopenError maps reopen failures onto host-actionable statuses:
// a diverged workspace file is a conflict the caller can force, a missing record
// is 404, and anything else (escape attempts, unreadable snapshots) is 400.
func (h *Handler) writeStoredPlanReopenError(w http.ResponseWriter, err error) {
	switch {
	case stderrors.Is(err, planmode.ErrReopenConflict):
		h.writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":    err.Error(),
			"conflict": true,
			"hint":     "工作区计划文件与归档快照不一致：确认覆盖后带 force=true 重试。",
		})
	case stderrors.Is(err, planstore.ErrNotFound):
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrAPINotFound, err.Error()))
	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
	}
}

func decodeStoredPlanReopenRequest(r *http.Request) (storedPlanReopenRequest, error) {
	req := storedPlanReopenRequest{}
	if r == nil || r.Body == nil {
		return req, nil
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		if stderrors.Is(err, io.EOF) {
			return storedPlanReopenRequest{}, nil
		}
		return storedPlanReopenRequest{}, err
	}
	req.PlanID = strings.Trim(strings.TrimSpace(req.PlanID), "/")
	if req.Version < 0 {
		req.Version = 0
	}
	return req, nil
}

// reloadSessionForPlanMode re-reads a session after an actor-owned transition so
// the response carries the state the actor just persisted.
func (h *Handler) reloadSessionForPlanMode(r *http.Request, sessionID string) (*chat.Session, error) {
	if h == nil || h.sessionManager == nil {
		return nil, errors.New(errors.ErrConfigInvalid, "session manager not configured")
	}
	ctx, cancel := sessionStoreQueryContext(r)
	defer cancel()
	return h.sessionManager.GetSession(ctx, strings.TrimSpace(sessionID))
}

func planModeResponsePtr(resp sessionPlanModeResponse) *sessionPlanModeResponse {
	return &resp
}

func storedPlanResponseFromRecord(record planstore.Record) storedPlanResponse {
	resp := storedPlanResponse{
		ID:          record.ID,
		SessionID:   record.SessionID,
		ProjectSlug: record.ProjectSlug,
		ProjectPath: record.ProjectPath,
		PlanPath:    record.PlanPath,
		Title:       record.Title,
		Status:      string(record.Status),
		Version:     record.Version,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
	}
	for _, round := range record.Rounds {
		resp.Rounds = append(resp.Rounds, storedPlanRoundResponse{
			Version:   round.Version,
			Decision:  round.Decision,
			Notes:     round.Notes,
			Source:    round.Source,
			Snapshot:  round.Snapshot,
			CreatedAt: round.CreatedAt,
		})
	}
	return resp
}

func truncatePlanArtifact(text string) (string, bool) {
	if utf8.RuneCountInString(text) <= planArtifactPreviewMaxRunes {
		return text, false
	}
	runes := []rune(text)
	return string(runes[:planArtifactPreviewMaxRunes]), true
}
