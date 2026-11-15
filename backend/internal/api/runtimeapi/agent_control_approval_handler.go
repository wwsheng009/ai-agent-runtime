package runtimeapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

type sessionAgentApprovalRequest struct {
	RequestID   string          `json:"request_id"`
	Allow       bool            `json:"allow"`
	PatchedArgs json.RawMessage `json:"patched_args,omitempty"`
}

// ApproveSessionAgent is the HTTP twin of the local `/agents approve|deny`
// command and the `subagent_approve` tool. It resolves the target through the
// same AgentControl controller, so remote clients never need to know whether
// the target is a session id, agent id, path, or batch task id.
func (h *Handler) ApproveSessionAgent(w http.ResponseWriter, r *http.Request) {
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
	var req sessionAgentApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if strings.TrimSpace(req.RequestID) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "request_id is required"))
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
	result, err := controller.ResolveApproval(r.Context(), toolbroker.ResolveAgentApprovalArgs{
		ID:          target,
		SessionID:   rootSessionID,
		RequestID:   strings.TrimSpace(req.RequestID),
		Allow:       req.Allow,
		PatchedArgs: req.PatchedArgs,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}
