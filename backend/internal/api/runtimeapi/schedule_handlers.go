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

// GetScheduleDeliveryHistory returns saved delivery records for one reminder,
// newest-first in append order with an exclusive message-id cursor.
func (h *Handler) GetScheduleDeliveryHistory(w http.ResponseWriter, r *http.Request) {
	service := h.getScheduleService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "schedule service not configured"))
		return
	}
	query := r.URL.Query()
	sessionID := strings.TrimSpace(query.Get("session_id"))
	id := strings.TrimSpace(query.Get("id"))
	if sessionID == "" || id == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session_id and id are required"))
		return
	}
	limit, err := strconv.Atoi(strings.TrimSpace(query.Get("limit")))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "limit must be an integer from 1 through 100"))
		return
	}
	result, err := service.History(schedule.HistoryRequest{
		SessionID: sessionID,
		ID:        id,
		Limit:     limit,
		Before:    strings.TrimSpace(query.Get("before")),
	})
	if err != nil {
		if input, ok := schedule.IsInputError(err); ok {
			status := http.StatusBadRequest
			if input.Code == schedule.CodeNotFound || input.Code == schedule.CodeCursorNotFound {
				status = http.StatusNotFound
			}
			h.writeError(w, status, errors.New(errors.ErrValidationFailed, input.Message))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// GetScheduleJobRuns returns the run history (J3) of one job, newest-first.
// `session_id` is optional when the caller addresses the job by id; when set it
// must match the job's bound session or its creator session.
func (h *Handler) GetScheduleJobRuns(w http.ResponseWriter, r *http.Request) {
	service := h.getScheduleService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "schedule service not configured"))
		return
	}
	query := r.URL.Query()
	id := strings.TrimSpace(query.Get("id"))
	if id == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "id is required"))
		return
	}
	limit := 20
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "limit must be an integer from 1 through 100"))
			return
		}
		limit = parsed
	}
	runs, err := service.JobRuns(strings.TrimSpace(query.Get("session_id")), id, limit)
	if err != nil {
		if input, ok := schedule.IsInputError(err); ok {
			status := http.StatusBadRequest
			if input.Code == schedule.CodeNotFound {
				status = http.StatusNotFound
			}
			h.writeError(w, status, errors.New(errors.ErrValidationFailed, input.Message))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "runs": runs, "count": len(runs)})
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
	Cron *struct {
		Expression string `json:"expression"`
		TimeZone   string `json:"time_zone"`
	} `json:"cron,omitempty"`
}

type scheduleCreateRequest struct {
	SessionID    string                `json:"session_id"`
	Title        string                `json:"title"`
	Prompt       string                `json:"prompt"`
	AfterSeconds *int64                `json:"after_seconds,omitempty"`
	At           *string               `json:"at,omitempty"`
	EverySeconds *int64                `json:"every_seconds,omitempty"`
	Daily        *schedule.DailyInput  `json:"daily,omitempty"`
	Weekly       *schedule.WeeklyInput `json:"weekly,omitempty"`
	Cron         *schedule.CronInput   `json:"cron,omitempty"`
	// J0：作业通用化字段（全部可选）。
	Goal            string   `json:"goal,omitempty"`
	Plan            []string `json:"plan,omitempty"`
	TargetKind      string   `json:"target_kind,omitempty"`
	TargetWorkspace string   `json:"target_workspace,omitempty"`
	TargetProfile   string   `json:"target_profile,omitempty"`
}

// CreateScheduleTask creates one reminder bound to a Session through the
// management surface (origin=manual). The service-side validation pipeline —
// single selector, title/prompt rules, DST handling, cron normalization — is
// shared verbatim with the model tool; this endpoint adds no extra rules.
func (h *Handler) CreateScheduleTask(w http.ResponseWriter, r *http.Request) {
	service := h.getScheduleService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "schedule service not configured"))
		return
	}
	var request scheduleCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid JSON body"))
		return
	}
	// J1：只有 session 目标要求 session_id；workspace/headless 目标不绑会话。
	if schedule.NormalizeTargetKind(request.TargetKind) == schedule.TargetSession && strings.TrimSpace(request.SessionID) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session_id is required for target_kind=session"))
		return
	}
	req := schedule.CreateRequest{
		Prompt:          request.Prompt,
		Title:           request.Title,
		AfterSeconds:    request.AfterSeconds,
		At:              request.At,
		EverySeconds:    request.EverySeconds,
		Daily:           request.Daily,
		Weekly:          request.Weekly,
		Cron:            request.Cron,
		Goal:            request.Goal,
		Plan:            request.Plan,
		TargetKind:      schedule.NormalizeTargetKind(request.TargetKind),
		TargetWorkspace: request.TargetWorkspace,
		TargetProfile:   request.TargetProfile,
	}
	record, err := service.Create(request.SessionID, req, schedule.OriginManual)
	if err != nil {
		if input, ok := schedule.IsInputError(err); ok {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, string(input.Code)+": "+input.Message))
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	// S3（§4.1）：回执携带在岗检查快照；非在岗/不兼容时 view.notice 给出
	// 可操作警告（任务照常创建——存储与控制面独立于调度面）。
	presence := h.schedulePresenceForCreate()
	view := schedule.BuildView(record, service.Now())
	view.Notice = presence.Notice()
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"record":   record,
		"origin":   schedule.OriginManual,
		"view":     view,
		"presence": presence,
	})
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
	if change.Cron != nil {
		out.Cron = &schedule.CronInput{Expression: change.Cron.Expression, TimeZone: change.Cron.TimeZone}
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
