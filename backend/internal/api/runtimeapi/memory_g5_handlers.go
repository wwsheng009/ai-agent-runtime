package runtimeapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type memoryFeedbackRequest struct {
	WorkspacePath string `json:"workspace_path"`
	Kind          string `json:"kind"`
	Comment       string `json:"comment"`
	SessionID     string `json:"session_id"`
	TurnID        string `json:"turn_id"`
}

func (h *Handler) ListMemoryEntities(w http.ResponseWriter, r *http.Request) {
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
	entities, err := store.ListEntities(h.memoryRequestContext(r, canViewPrivate), parsePositiveIntQuery(r, "limit", 50))
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_path": workspace, "items": entities, "count": len(entities),
	})
}

func (h *Handler) ListMemoryRecordEntities(w http.ResponseWriter, r *http.Request) {
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
	entities, err := store.ListRecordEntities(h.memoryRequestContext(r, canViewPrivate), mux.Vars(r)["id"])
	if err != nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "memory record entities unavailable"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"memory_id": mux.Vars(r)["id"], "items": entities, "count": len(entities),
	})
}

func (h *Handler) RecordMemoryFeedback(w http.ResponseWriter, r *http.Request) {
	if !h.memoryV2Enabled() {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, "memory mode is off"))
		return
	}
	var req memoryFeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid feedback request"))
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
	usage := h.resolveUsageScope(r, "", "", "")
	actor := "user:" + strings.TrimSpace(usage.UserID)
	if strings.TrimSpace(usage.UserID) == "" {
		actor = "user"
	}
	feedback, err := store.RecordFeedback(h.memoryRequestContext(r, h.authorizeMemoryAdmin(r) == nil),
		memorystore.FeedbackInput{
			MemoryID: mux.Vars(r)["id"], SessionID: req.SessionID, TurnID: req.TurnID,
			Kind: memorystore.MemoryFeedbackKind(req.Kind), Comment: req.Comment, Actor: actor,
		})
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, feedback)
}

func (h *Handler) ListMemoryFeedback(w http.ResponseWriter, r *http.Request) {
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
	items, err := store.ListFeedback(h.memoryRequestContext(r, true),
		mux.Vars(r)["id"], parsePositiveIntQuery(r, "limit", 50))
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_path": workspace, "memory_id": mux.Vars(r)["id"],
		"items": items, "count": len(items),
	})
}
