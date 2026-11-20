package runtimeapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

func (h *Handler) templateStore(w http.ResponseWriter) *schedule.SQLiteStore {
	store, ok := h.ScheduleStore().(*schedule.SQLiteStore)
	if !ok || store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "schedule template store not configured"))
		return nil
	}
	return store
}

func (h *Handler) ListScheduleTemplates(w http.ResponseWriter, _ *http.Request) {
	store := h.templateStore(w)
	if store == nil {
		return
	}
	templates, err := store.ListTemplates()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if templates == nil {
		templates = []schedule.Template{}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"templates": templates, "count": len(templates)})
}

func (h *Handler) CreateScheduleTemplate(w http.ResponseWriter, r *http.Request) {
	store := h.templateStore(w)
	if store == nil {
		return
	}
	var t schedule.Template
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&t); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid template JSON"))
		return
	}
	if t.ID == "" {
		t.ID = schedule.NewID()
	}
	if err := store.PutTemplate(t); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	created, _, err := store.GetTemplate(t.ID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]any{"template": created})
}

func (h *Handler) UpdateScheduleTemplate(w http.ResponseWriter, r *http.Request) {
	store := h.templateStore(w)
	if store == nil {
		return
	}
	var t schedule.Template
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&t); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid template JSON"))
		return
	}
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "id is required"))
		return
	}
	t.ID = id
	if _, ok, err := store.GetTemplate(id); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "template not found"))
		return
	}
	if err := store.UpdateTemplate(t); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	updated, _, err := store.GetTemplate(id)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"template": updated})
}

func (h *Handler) DeleteScheduleTemplate(w http.ResponseWriter, r *http.Request) {
	store := h.templateStore(w)
	if store == nil {
		return
	}
	id := strings.TrimSpace(mux.Vars(r)["id"])
	ok, err := store.DeleteTemplate(id)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "template not found"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}
