package runtimeapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// GetScheduleCatalog returns all active and inactive reminders with their
// original Session bindings (read-only; no Session activation).
func (h *Handler) GetScheduleCatalog(w http.ResponseWriter, r *http.Request) {
	service := h.getScheduleService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "schedule service not configured"))
		return
	}
	entries, err := service.Catalog()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"tasks": entries, "count": len(entries)})
}

// ListScheduleTasks returns the active reminders of one Session.
func (h *Handler) ListScheduleTasks(w http.ResponseWriter, r *http.Request) {
	service := h.getScheduleService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "schedule service not configured"))
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session_id is required"))
		return
	}
	records, err := service.List(sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"tasks": records, "count": len(records)})
}

type scheduleUpdateRequest struct {
	SessionID string                `json:"session_id"`
	ID        string                `json:"id"`
	Expected  schedule.Record       `json:"expected"`
	Title     *string               `json:"title,omitempty"`
	Prompt    *string               `json:"prompt,omitempty"`
	Change    *scheduleTimingChange `json:"change,omitempty"`
}

type scheduleTimingChange struct {
	Kind         schedule.Kind `json:"kind"`
	At           *string       `json:"at,omitempty"`
	EverySeconds *int64        `json:"every_seconds,omitempty"`
	Daily        *struct {
		Time     string `json:"time"`
		TimeZone string `json:"time_zone"`
	} `json:"daily,omitempty"`
	Weekly *struct {
		Time     string `json:"time"`
		TimeZone string `json:"time_zone"`
		Weekdays []int  `json:"weekdays"`
	} `json:"weekly,omitempty"`
}

// UpdateScheduleTask edits one reminder in place within its original Session binding.
func (h *Handler) UpdateScheduleTask(w http.ResponseWriter, r *http.Request) {
	service := h.getScheduleService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "schedule service not configured"))
		return
	}
	var request scheduleUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid JSON body"))
		return
	}
	if strings.TrimSpace(request.SessionID) == "" || strings.TrimSpace(request.ID) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session_id and id are required"))
		return
	}
	content := schedule.UpdateContent{Title: request.Title, Prompt: request.Prompt}
	result, err := service.Update(request.SessionID, request.ID, request.Expected, content, toTimingChange(request.Change))
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

type scheduleDeleteRequest struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
}

// DeleteScheduleTask hard-deletes one reminder within its original Session binding.
func (h *Handler) DeleteScheduleTask(w http.ResponseWriter, r *http.Request) {
	service := h.getScheduleService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "schedule service not configured"))
		return
	}
	var request scheduleDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid JSON body"))
		return
	}
	if strings.TrimSpace(request.SessionID) == "" || strings.TrimSpace(request.ID) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session_id and id are required"))
		return
	}
	result, err := service.Delete(request.SessionID, request.ID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func toTimingChange(change *scheduleTimingChange) *schedule.TimingChange {
	if change == nil {
		return nil
	}
	out := &schedule.TimingChange{Kind: change.Kind, At: change.At, EverySeconds: change.EverySeconds}
	if change.Daily != nil {
		out.Daily = &schedule.DailyInput{Time: change.Daily.Time, TimeZone: change.Daily.TimeZone}
	}
	if change.Weekly != nil {
		out.Weekly = &schedule.WeeklyInput{Time: change.Weekly.Time, TimeZone: change.Weekly.TimeZone, Weekdays: change.Weekly.Weekdays}
	}
	return out
}

// scheduleLimitOrDefault parses an optional positive integer query parameter.
func scheduleLimitOrDefault(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
