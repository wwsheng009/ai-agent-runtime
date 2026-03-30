package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/gorilla/mux"
)

const (
	sessionRuntimeSubmitSyncWaitBudget = 100 * time.Millisecond
	sessionRuntimeSubmitPollInterval   = 10 * time.Millisecond
)

type sessionRuntimeSubmitOutcome struct {
	result *agent.Result
	err    error
}

type sessionRuntimeCommandRequest struct {
	Type         string          `json:"type"`
	Prompt       string          `json:"prompt,omitempty"`
	RunMeta      *team.RunMeta   `json:"run_meta,omitempty"`
	RequestID    string          `json:"request_id,omitempty"`
	Allow        *bool           `json:"allow,omitempty"`
	PatchedArgs  json.RawMessage `json:"patched_args,omitempty"`
	QuestionID   string          `json:"question_id,omitempty"`
	Answer       string          `json:"answer,omitempty"`
	CheckpointID string          `json:"checkpoint_id,omitempty"`
	Mode         string          `json:"mode,omitempty"`
}

func (h *Handler) SpawnSessionAgent(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	parentSessionID := strings.TrimSpace(mux.Vars(r)["id"])
	if parentSessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	var req toolbroker.SpawnAgentArgs
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	result, err := controller.Spawn(r.Context(), parentSessionID, req)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	statusCode := http.StatusCreated
	if result != nil && result.Queued {
		statusCode = http.StatusAccepted
	}
	h.writeJSON(w, statusCode, map[string]interface{}{
		"agent": result,
	})
}

func (h *Handler) GetSessionAgentStatus(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if agentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent id is required"))
		return
	}
	result, err := controller.snapshot(r.Context(), agentID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent": result,
	})
}

func (h *Handler) SendSessionAgentInput(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if agentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent id is required"))
		return
	}
	var req toolbroker.SendAgentInputArgs
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	req.ID = firstNonEmptyString(req.ID, agentID)
	result, err := controller.SendInput(r.Context(), req)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	h.writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"agent": result,
	})
}

func (h *Handler) WaitSessionAgents(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	var req toolbroker.WaitAgentArgs
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	result, err := controller.Wait(r.Context(), req)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
	})
}

func (h *Handler) ListSessionAgentEvents(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if agentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent id is required"))
		return
	}
	after := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after_seq value"))
			return
		}
		after = parsed
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	waitMs := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("wait_ms")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid wait_ms value"))
			return
		}
		waitMs = parsed
	}
	result, err := controller.ReadEvents(r.Context(), toolbroker.ReadAgentEventsArgs{
		ID:       agentID,
		AfterSeq: after,
		Limit:    limit,
		WaitMs:   waitMs,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
	})
}

func (h *Handler) CloseSessionAgent(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if agentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent id is required"))
		return
	}
	result, err := controller.Close(r.Context(), agentID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent": result,
	})
}

func (h *Handler) ResumeSessionAgent(w http.ResponseWriter, r *http.Request) {
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent session controller not configured"))
		return
	}
	agentID := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if agentID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent id is required"))
		return
	}
	result, err := controller.Resume(r.Context(), agentID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent": result,
	})
}

// GetSessionRuntimeState returns the session actor runtime state.
func (h *Handler) GetSessionRuntimeState(w http.ResponseWriter, r *http.Request) {
	store := h.getSessionRuntimeStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session runtime store not configured"))
		return
	}

	sessionID := strings.TrimSpace(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	state, err := store.LoadState(r.Context(), sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if state == nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "session runtime state not found"))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"state": state,
	})
}

// ListSessionRuntimeEvents returns session runtime events.
func (h *Handler) ListSessionRuntimeEvents(w http.ResponseWriter, r *http.Request) {
	store := h.getSessionEventStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session event store not configured"))
		return
	}

	sessionID := strings.TrimSpace(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	after := int64(0)
	rawAfter := strings.TrimSpace(r.URL.Query().Get("after"))
	if rawAfter == "" {
		rawAfter = strings.TrimSpace(r.URL.Query().Get("after_seq"))
	}
	if rawAfter != "" {
		parsed, err := strconv.ParseInt(rawAfter, 10, 64)
		if err != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after value"))
			return
		}
		after = parsed
	}

	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	events, err := store.ListEvents(r.Context(), sessionID, after, limit)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": buildSessionRuntimeEventViews(events),
		"count":  len(events),
	})
}

// ListSessionToolReceipts returns persisted tool receipts for a session.
func (h *Handler) ListSessionToolReceipts(w http.ResponseWriter, r *http.Request) {
	store := h.getSessionToolReceiptStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session tool receipt store not configured"))
		return
	}

	sessionID := strings.TrimSpace(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	toolCallID := strings.TrimSpace(r.URL.Query().Get("tool_call_id"))
	if toolCallID != "" {
		receipt, err := store.GetToolReceipt(r.Context(), sessionID, toolCallID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		receipts := make([]chat.ToolExecutionReceipt, 0, 1)
		if receipt != nil {
			receipts = append(receipts, *receipt)
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"receipts": receipts,
			"count":    len(receipts),
		})
		return
	}

	receipts, err := store.ListToolReceipts(r.Context(), sessionID, limit)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"receipts": receipts,
		"count":    len(receipts),
	})
}

// SubmitSessionRuntimeCommand submits a session actor command.
func (h *Handler) SubmitSessionRuntimeCommand(w http.ResponseWriter, r *http.Request) {
	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
		return
	}

	sessionID := strings.TrimSpace(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	var req sessionRuntimeCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}

	commandType := strings.ToLower(strings.TrimSpace(req.Type))
	if commandType == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "command type is required"))
		return
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	switch commandType {
	case "submit_prompt", "submit":
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "prompt is required"))
			return
		}
		result, state, err, completed := submitSessionPrompt(actor, r.Context(), prompt, req.RunMeta)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !completed {
			h.writeJSON(w, http.StatusAccepted, map[string]interface{}{
				"ok":      true,
				"pending": true,
				"state":   state,
			})
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"result": result,
		})
		return

	case "approve_tool", "approve":
		requestID := strings.TrimSpace(req.RequestID)
		if requestID == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "request_id is required"))
			return
		}
		if req.Allow == nil {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "allow is required"))
			return
		}
		if err := actor.ApproveToolWithArgs(r.Context(), requestID, *req.Allow, req.PatchedArgs); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}

	case "answer_question", "answer":
		questionID := strings.TrimSpace(req.QuestionID)
		if questionID == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "question_id is required"))
			return
		}
		if strings.TrimSpace(req.Answer) == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "answer is required"))
			return
		}
		if err := actor.AnswerQuestion(r.Context(), questionID, req.Answer); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}

	case "interrupt":
		if err := actor.Interrupt(r.Context()); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}

	case "rewind_to", "rewind":
		checkpointID := strings.TrimSpace(req.CheckpointID)
		if checkpointID == "" {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "checkpoint_id is required"))
			return
		}
		result, err := actor.Rewind(r.Context(), checkpointID, strings.TrimSpace(req.Mode))
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":     true,
			"result": result,
		})
		return

	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "unsupported command type"))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
	})
}

func submitSessionPrompt(actor *chat.SessionActor, requestCtx context.Context, prompt string, runMeta *team.RunMeta) (*agent.Result, *chat.RuntimeState, error, bool) {
	if actor == nil {
		return nil, nil, errors.New(errors.ErrConfigInvalid, "session actor not configured"), false
	}

	runCtx := context.WithoutCancel(requestCtx)
	resultCh := make(chan sessionRuntimeSubmitOutcome, 1)
	go func() {
		result, err := actor.SubmitPrompt(runCtx, prompt, runMeta)
		resultCh <- sessionRuntimeSubmitOutcome{result: result, err: err}
	}()

	timer := time.NewTimer(sessionRuntimeSubmitSyncWaitBudget)
	defer timer.Stop()
	ticker := time.NewTicker(sessionRuntimeSubmitPollInterval)
	defer ticker.Stop()

	for {
		select {
		case outcome := <-resultCh:
			return outcome.result, nil, outcome.err, true
		case <-ticker.C:
			state := actor.State()
			if state == nil {
				continue
			}
			switch state.Status {
			case chat.SessionWaitingApproval, chat.SessionWaitingInput:
				return nil, state, nil, false
			}
		case <-timer.C:
			state := actor.State()
			if state == nil {
				state = &chat.RuntimeState{
					Status: chat.SessionRunning,
				}
			} else if state.Status == chat.SessionIdle {
				state = state.Clone()
				state.Status = chat.SessionRunning
			}
			return nil, state, nil, false
		}
	}
}

