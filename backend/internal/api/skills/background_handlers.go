package skills

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/gorilla/mux"
)

// ListBackgroundJobs lists background jobs with optional filters.
func (h *Handler) ListBackgroundJobs(w http.ResponseWriter, r *http.Request) {
	manager := h.getBackgroundManager(h.runtimeConfig)
	if manager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "background manager not configured"))
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	statuses, err := parseBackgroundStatusFilter(r.URL.Query().Get("status"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	offset, err := parseOptionalOffset(r.URL.Query().Get("offset"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	jobs, err := manager.ListJobs(r.Context(), background.JobFilter{
		SessionID: sessionID,
		Status:    statuses,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"jobs":  jobs,
		"count": len(jobs),
	})
}

// GetBackgroundJob returns a background job by id.
func (h *Handler) GetBackgroundJob(w http.ResponseWriter, r *http.Request) {
	manager := h.getBackgroundManager(h.runtimeConfig)
	if manager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "background manager not configured"))
		return
	}

	jobID := strings.TrimSpace(mux.Vars(r)["id"])
	if jobID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "job id is required"))
		return
	}
	job, err := manager.GetJob(r.Context(), jobID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "job not found"))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if job == nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "job not found"))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"job": job,
	})
}

// CancelBackgroundJob requests cancellation of a background job.
func (h *Handler) CancelBackgroundJob(w http.ResponseWriter, r *http.Request) {
	manager := h.getBackgroundManager(h.runtimeConfig)
	if manager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "background manager not configured"))
		return
	}

	jobID := strings.TrimSpace(mux.Vars(r)["id"])
	if jobID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "job id is required"))
		return
	}
	job, err := manager.CancelJob(r.Context(), jobID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "job not found"))
			return
		}
		if strings.Contains(err.Error(), "already finished") {
			h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, err.Error()))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if job == nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "job not found"))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"job": job,
	})
}

// ListBackgroundJobEvents lists background job events for a job.
func (h *Handler) ListBackgroundJobEvents(w http.ResponseWriter, r *http.Request) {
	manager := h.getBackgroundManager(h.runtimeConfig)
	if manager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "background manager not configured"))
		return
	}

	jobID := strings.TrimSpace(mux.Vars(r)["id"])
	if jobID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "job id is required"))
		return
	}

	afterSeq := int64(0)
	rawAfter := strings.TrimSpace(r.URL.Query().Get("after"))
	if rawAfter != "" {
		parsed, err := strconv.ParseInt(rawAfter, 10, 64)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after value"))
			return
		}
		afterSeq = parsed
	}

	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	events, err := manager.ListEvents(r.Context(), jobID, afterSeq, limit)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": events,
		"count":  len(events),
	})
}

// GetBackgroundJobOutput returns background job output by offset.
func (h *Handler) GetBackgroundJobOutput(w http.ResponseWriter, r *http.Request) {
	manager := h.getBackgroundManager(h.runtimeConfig)
	if manager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "background manager not configured"))
		return
	}

	jobID := strings.TrimSpace(mux.Vars(r)["id"])
	if jobID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "job id is required"))
		return
	}

	offset, err := parseOptionalOffset64(r.URL.Query().Get("offset"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	output, err := manager.ReadOutput(r.Context(), background.TaskOutputArgs{
		JobID:  jobID,
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "job not found"))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"output": output,
	})
}

func parseBackgroundStatusFilter(raw string) ([]background.JobStatus, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	statuses := make([]background.JobStatus, 0, len(parts))
	for _, part := range parts {
		value := strings.ToLower(strings.TrimSpace(part))
		if value == "" {
			continue
		}
		status := background.JobStatus(value)
		switch status {
		case background.StatusPending, background.StatusRunning, background.StatusCompleted, background.StatusFailed, background.StatusCancelled:
			statuses = append(statuses, status)
		default:
			return nil, errors.New(errors.ErrValidationFailed, "invalid status value")
		}
	}
	if len(statuses) == 0 {
		return nil, nil
	}
	return statuses, nil
}

func parseOptionalOffset(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, errors.New(errors.ErrValidationFailed, "invalid offset value")
	}
	return value, nil
}

func parseOptionalOffset64(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New(errors.ErrValidationFailed, "invalid offset value")
	}
	return value, nil
}

