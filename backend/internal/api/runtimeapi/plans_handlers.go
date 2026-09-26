package runtimeapi

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gorilla/mux"
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
