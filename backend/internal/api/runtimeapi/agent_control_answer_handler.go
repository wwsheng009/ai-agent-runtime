package runtimeapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

type sessionAgentAnswerRequest struct {
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

// AnswerSessionAgent is the HTTP twin of `/agents answer` and the connector
// answer path. It resolves the target through the same AgentControl controller
// and then calls the owning actor, so remote clients do not get a second
// question-resolution path.
func (h *Handler) AnswerSessionAgent(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		http.Error(w, "handler is not configured", http.StatusInternalServerError)
		return
	}
	rootSessionID := strings.TrimSpace(mux.Vars(r)["id"])
	target := strings.TrimSpace(mux.Vars(r)["agent_id"])
	if rootSessionID == "" || target == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id and agent id are required"))
		return
	}
	var req sessionAgentAnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	questionID := strings.TrimSpace(req.QuestionID)
	if questionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "question_id is required"))
		return
	}
	controller := h.getAgentSessionController()
	if controller == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "agent controller not configured"))
		return
	}
	if record, ok, err := controller.resolveAgentRecord(r.Context(), target, false); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	} else if ok && strings.TrimSpace(record.RootSessionID) != "" &&
		!strings.EqualFold(strings.TrimSpace(record.RootSessionID), rootSessionID) {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrAgentSessionNotFound, "agent does not belong to this session"))
		return
	}
	if err := controller.AnswerQuestion(r.Context(), target, questionID, req.Answer); err != nil {
		message := strings.ToLower(err.Error())
		switch {
		case strings.Contains(message, "required question"),
			strings.Contains(message, "not found"),
			strings.Contains(message, "is closed"),
			strings.Contains(message, "question id is required"):
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		default:
			h.writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":  rootSessionID,
		"agent_id":    target,
		"question_id": questionID,
		"answered":    true,
	})
}
