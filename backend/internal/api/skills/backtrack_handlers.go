package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// ListSessionTurns lists user-turn anchors for a session.
// GET /api/runtime/sessions/{id}/turns
func (h *Handler) ListSessionTurns(w http.ResponseWriter, r *http.Request) {
	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
		return
	}
	sessionID := runtimechat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		if h.writeSessionLeaseConflict(w, err) {
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	// turns 锚点落在共享 session_history.sqlite（win7 配置与 aicli 共享
	// 主库），读取带截止时间快速失败，避免前端 fetchRuntimeJson 10s 超时
	// 显示 "signal timed out"。
	queryCtx, queryCancel := sessionStoreQueryContext(r)
	defer queryCancel()
	turns, err := actor.ListTurns(queryCtx)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"turns":      turns,
		"count":      len(turns),
	})
}

// ListSessionBacktrackAudit lists durable backtrack tombstones for a session.
// GET /api/runtime/sessions/{id}/backtrack/audit
// Entries are oldest-first; physical history remains truncated.
func (h *Handler) ListSessionBacktrackAudit(w http.ResponseWriter, r *http.Request) {
	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
		return
	}
	sessionID := runtimechat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		if h.writeSessionLeaseConflict(w, err) {
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	// backtrack audit 落在共享 session_history.sqlite（win7 配置与 aicli
	// 共享主库），读取带截止时间快速失败，避免前端 fetchRuntimeJson 10s
	// 超时显示 "signal timed out"。
	queryCtx, queryCancel := sessionStoreQueryContext(r)
	defer queryCancel()
	entries, err := actor.ListBacktrackAudit(queryCtx)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}
	if entries == nil {
		entries = []runtimechat.BacktrackTombstone{}
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"entries":    entries,
		"count":      len(entries),
	})
}

// PreviewSessionBacktrack plans a user-turn backtrack without mutating state.
// POST /api/runtime/sessions/{id}/backtrack/preview
func (h *Handler) PreviewSessionBacktrack(w http.ResponseWriter, r *http.Request) {
	h.handleSessionBacktrack(w, r, true)
}

// ApplySessionBacktrack applies a user-turn backtrack.
// POST /api/runtime/sessions/{id}/backtrack
func (h *Handler) ApplySessionBacktrack(w http.ResponseWriter, r *http.Request) {
	h.handleSessionBacktrack(w, r, false)
}

func (h *Handler) handleSessionBacktrack(w http.ResponseWriter, r *http.Request, forcePreview bool) {
	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
		return
	}
	sessionID := runtimechat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	var req runtimechat.BacktrackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		// Allow empty body for query-only clients; otherwise require JSON.
		if r.ContentLength != 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid backtrack request body"))
			return
		}
	}
	if forcePreview {
		req.PreviewOnly = true
		req.AutoSubmit = false
	}

	// Accept selectors from query when body omits them.
	if req.UserTurnIndex == nil && req.MessageIndex == nil && strings.TrimSpace(req.MessageID) == "" {
		if raw := strings.TrimSpace(r.URL.Query().Get("user_turn_index")); raw != "" {
			if n, err := parseOptionalOffset(raw); err == nil {
				req.UserTurnIndex = runtimechat.IntPtr(n)
			}
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("message_index")); raw != "" {
			if n, err := parseOptionalOffset(raw); err == nil {
				req.MessageIndex = runtimechat.IntPtr(n)
			}
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("message_id")); raw != "" {
			req.MessageID = raw
		}
	}
	if mode := strings.TrimSpace(r.URL.Query().Get("mode")); mode != "" && strings.TrimSpace(req.Mode) == "" {
		req.Mode = mode
	}

	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		if h.writeSessionLeaseConflict(w, err) {
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Backtrack rewrites session history. The hub actor no longer holds the
	// session lease while idle (run-scoped leases), so take it explicitly for
	// the mutation and yield to any same-process web turn before it. Preview
	// is read-only and does not need the lease.
	var (
		result *runtimechat.BacktrackResult
	)
	if req.PreviewOnly || forcePreview {
		result, err = actor.PreviewBacktrack(r.Context(), req)
	} else {
		leaseHandle, leaseErr := h.acquireSessionLease(r.Context(), sessionID, sessionActorLeaseOwnerKind, "backtrack-apply")
		if leaseErr != nil {
			if h.writeSessionLeaseConflict(w, leaseErr) {
				return
			}
			h.writeError(w, http.StatusInternalServerError, leaseErr)
			return
		}
		if leaseHandle != nil {
			defer func() {
				_ = leaseHandle.Release(context.Background())
			}()
		}
		result, _, err = actor.Backtrack(r.Context(), req)
	}
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "busy"):
			h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed, msg))
		case strings.Contains(msg, "out of range"),
			strings.Contains(msg, "not found"),
			strings.Contains(msg, "required"),
			strings.Contains(msg, "unsupported"):
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, msg))
		default:
			h.writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"result": result,
	})
}
