package runtimeapi

import (
	"context"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

const memoryG4Actor = "admin:runtime-api"

type memoryPatchCreateRequest struct {
	WorkspacePath     string                 `json:"workspace_path"`
	Operation         string                 `json:"operation"`
	TargetIDs         []string               `json:"target_ids"`
	ProposedContent   string                 `json:"proposed_content"`
	ProposedMetadata  map[string]interface{} `json:"proposed_metadata"`
	Reason            string                 `json:"reason"`
	Evidence          []string               `json:"evidence"`
	RiskLevel         string                 `json:"risk_level"`
	ExpectedRevisions map[string]int64       `json:"expected_revisions"`
	ScopeKind         string                 `json:"scope_kind"`
	ScopeKey          string                 `json:"scope_key"`
}

type memoryPatchReviewRequest struct {
	WorkspacePath string `json:"workspace_path"`
	Note          string `json:"note"`
}

type memoryPatchRollbackRequest struct {
	WorkspacePath string `json:"workspace_path"`
	Reason        string `json:"reason"`
}

type memoryDreamRunRequest struct {
	WorkspacePath string `json:"workspace_path"`
	Limit         int    `json:"limit"`
	Shadow        bool   `json:"shadow"`
	Force         bool   `json:"force"`
}

func (h *Handler) ListMemoryEpisodes(w http.ResponseWriter, r *http.Request) {
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	canViewPrivate := h.authorizeMemoryAdmin(r) == nil
	episodes, err := store.ListEpisodes(h.memoryRequestContext(r, canViewPrivate), memorystore.EpisodeFilter{
		TaskID:    strings.TrimSpace(r.URL.Query().Get("task_id")),
		SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
		Outcome:   memorystore.EpisodeOutcome(strings.TrimSpace(r.URL.Query().Get("outcome"))),
		Limit:     parsePositiveIntQuery(r, "limit", 50),
	})
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_path": workspace, "items": episodes, "count": len(episodes),
	})
}

func (h *Handler) GetMemoryEpisode(w http.ResponseWriter, r *http.Request) {
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	canViewPrivate := h.authorizeMemoryAdmin(r) == nil
	episode, err := store.GetEpisode(h.memoryRequestContext(r, canViewPrivate), mux.Vars(r)["id"])
	if stderrors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory episode not found"))
		return
	}
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, episode)
}

func (h *Handler) PromoteMemoryEpisode(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	ctx := h.memoryRequestContext(r, true)
	episode, err := store.PromoteEpisode(ctx, mux.Vars(r)["id"])
	if stderrors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory episode not found"))
		return
	}
	if err != nil {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, episode)
}

func (h *Handler) ListMemoryPatches(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	patches, err := store.ListPatches(h.memoryRequestContext(r, true), memorystore.PatchFilter{
		Status: memorystore.PatchStatus(strings.TrimSpace(r.URL.Query().Get("status"))),
		Risk:   memorystore.PatchRisk(strings.TrimSpace(r.URL.Query().Get("risk"))),
		Limit:  parsePositiveIntQuery(r, "limit", 50),
	})
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_path": workspace, "items": patches, "count": len(patches),
	})
}

func (h *Handler) CreateMemoryPatch(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryPatchCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid patch request"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	patch, err := store.CreatePatch(h.memoryRequestContext(r, true), memorystore.PatchInput{
		Operation: memorystore.PatchOperation(req.Operation),
		TargetIDs: req.TargetIDs, ProposedContent: req.ProposedContent,
		ProposedMetadata: req.ProposedMetadata, Reason: req.Reason,
		Evidence: req.Evidence, RiskLevel: memorystore.PatchRisk(req.RiskLevel),
		ExpectedRevisions: req.ExpectedRevisions,
		ScopeKind:         memorystore.Scope(req.ScopeKind), ScopeKey: req.ScopeKey,
		CreatedBy: memoryG4Actor,
	})
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, patch)
}

func (h *Handler) GetMemoryPatch(w http.ResponseWriter, r *http.Request) {
	h.withMemoryPatch(w, r, func(store *memorystore.Store, ctx context.Context) {
		patch, err := store.GetPatch(ctx, mux.Vars(r)["id"])
		if stderrors.Is(err, sql.ErrNoRows) {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory patch not found"))
			return
		}
		if err != nil {
			h.writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		h.writeJSON(w, http.StatusOK, patch)
	})
}

func (h *Handler) ApproveMemoryPatch(w http.ResponseWriter, r *http.Request) {
	h.reviewMemoryPatch(w, r, false)
}

func (h *Handler) RejectMemoryPatch(w http.ResponseWriter, r *http.Request) {
	h.reviewMemoryPatch(w, r, true)
}

func (h *Handler) reviewMemoryPatch(w http.ResponseWriter, r *http.Request, reject bool) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryPatchReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid review request"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	ctx := h.memoryRequestContext(r, true)
	var patch memorystore.MemoryPatch
	if reject {
		patch, err = store.RejectPatch(ctx, mux.Vars(r)["id"], memoryG4Actor, req.Note)
	} else {
		patch, err = store.ApprovePatch(ctx, mux.Vars(r)["id"], memoryG4Actor, req.Note)
	}
	if stderrors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory patch not found"))
		return
	}
	if err != nil {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, patch)
}

func (h *Handler) ApplyMemoryPatch(w http.ResponseWriter, r *http.Request) {
	h.withMemoryPatch(w, r, func(store *memorystore.Store, ctx context.Context) {
		patch, err := store.ApplyPatch(ctx, mux.Vars(r)["id"], memorystore.PatchApplyOptions{
			AutoApproveLowRisk: true, Actor: memoryG4Actor,
		})
		if stderrors.Is(err, sql.ErrNoRows) {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory patch not found"))
			return
		}
		if err != nil {
			h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, err.Error()))
			return
		}
		h.writeJSON(w, http.StatusOK, patch)
	})
}

func (h *Handler) RollbackMemoryPatch(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryPatchRollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid rollback request"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	patch, err := store.RollbackPatch(h.memoryRequestContext(r, true), mux.Vars(r)["id"], memoryG4Actor, req.Reason)
	if stderrors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory patch not found"))
		return
	}
	if err != nil {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, patch)
}

func (h *Handler) ListMemoryDreams(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	runs, err := store.ListDreamRuns(h.memoryRequestContext(r, true), parsePositiveIntQuery(r, "limit", 50))
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_path": workspace, "items": runs, "count": len(runs),
	})
}

func (h *Handler) RunMemoryDreams(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryDreamRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid dream request"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, req.WorkspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	ctx := h.memoryRequestContext(r, true)
	options := memorystore.DreamRunOptions{
		Limit: req.Limit, CreatedBy: memoryG4Actor, Shadow: req.Shadow, Force: req.Force,
	}
	var run memorystore.DreamRun
	if runtime := h.MemoryRuntimeForWorkspace(workspace); runtime != nil {
		run, err = runtime.RunDreaming(ctx, options)
	} else {
		store, openErr := h.openMemoryStore(workspace)
		if openErr != nil {
			h.writeError(w, http.StatusInternalServerError, openErr)
			return
		}
		defer store.Close()
		run, err = store.RunDreamPipeline(ctx, options)
	}
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, run)
}

func (h *Handler) withMemoryPatch(w http.ResponseWriter, r *http.Request, fn func(*memorystore.Store, context.Context)) {
	if err := h.authorizeMemoryAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	workspace, err := h.resolveMemoryWorkspace(r, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	store, err := h.openMemoryStore(workspace)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer store.Close()
	fn(store, h.memoryRequestContext(r, true))
}
