package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// ListSessionTurns lists user-turn anchors for a session.
// GET /api/runtime/sessions/{id}/turns
func (h *Handler) ListSessionTurns(w http.ResponseWriter, r *http.Request) {
	sessionID := runtimechat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	// turns 锚点落在共享 session_history.sqlite（win7 配置与 aicli 共享
	// 主库），读取带截止时间快速失败，避免前端 fetchRuntimeJson 10s 超时
	// 显示 "signal timed out"。
	queryCtx, queryCancel := sessionStoreQueryContext(r)
	defer queryCancel()
	session, err := h.readSessionSnapshot(queryCtx, sessionID)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}
	// checkpoint 注解是 best-effort：读取失败只降级为无注解的 turn 锚点。
	checkpoints := h.readSessionCheckpoints(queryCtx, sessionID)
	turns := runtimechat.ListUserTurns(session.GetMessages(), checkpoints)
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
	sessionID := runtimechat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}
	// backtrack audit 落在共享 session_history.sqlite（win7 配置与 aicli
	// 共享主库），读取带截止时间快速失败，避免前端 fetchRuntimeJson 10s
	// 超时显示 "signal timed out"。
	queryCtx, queryCancel := sessionStoreQueryContext(r)
	defer queryCancel()
	session, err := h.readSessionSnapshot(queryCtx, sessionID)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}
	entries := runtimechat.ListBacktrackTombstones(session)
	if entries == nil {
		entries = []runtimechat.BacktrackTombstone{}
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"entries":    entries,
		"count":      len(entries),
	})
}

// readSessionSnapshot 以"免租约"方式读取会话快照，供只读端点使用。
//
// turns / backtrack-audit 都是纯读端点，不改变会话状态。此前它们通过
// hub.GetOrCreate 获取 session actor，于是只要会话正被另一个持有者
// （aicli CLI 或另一个 web 回合）租用，纯读请求也会拿到
// 409 SESSION_LEASE_CONFLICT，前端只能在控制台反复报错。租约的语义是
// 互斥写入而非互斥读取，因此读路径直接读会话存储：代价是可能看到略微
// 滞后的已持久化快照，但不会再因为别人持有写租约而失败。
func (h *Handler) readSessionSnapshot(ctx context.Context, sessionID string) (*runtimechat.Session, error) {
	if h == nil || h.sessionManager == nil {
		return nil, errors.New(errors.ErrConfigInvalid, "session manager not configured")
	}
	return h.sessionManager.Get(ctx, sessionID)
}

// readSessionCheckpoints 读取会话 checkpoint 列表，用于 turn 锚点注解。
// 与 actor 内部行为一致：失败只降级为 nil（无注解），不阻断读路径。
func (h *Handler) readSessionCheckpoints(ctx context.Context, sessionID string) []artifact.Checkpoint {
	reader, cleanup, err := h.openCheckpointReadService(sessionID)
	if err != nil || reader == nil {
		return nil
	}
	if cleanup != nil {
		defer cleanup()
	}
	checkpoints, err := reader.ListCheckpoints(ctx, 0, 0)
	if err != nil {
		return nil
	}
	return checkpoints
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
