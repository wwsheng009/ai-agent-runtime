package runtimeapi

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

const (
	// branchTitleSuffix is appended to the source title when the caller supplies
	// no title. Sidebar/e2e expectations rely on this literal.
	branchTitleSuffix = " (branch)"
	// branchTitleDedupLimit bounds the " (N)" scan for same-parent titles.
	branchTitleDedupLimit = 1000
)

// branchMu serializes branch session creation (title dedup + insert) so
// concurrent clients cannot mint two sessions with the same title under one
// fork parent. Branching is a rare, user-triggered operation, so a single
// process-wide lock is preferable to per-parent bookkeeping.
var branchMu sync.Mutex

// BranchSession creates a new session seeded with a history prefix of the
// source session. The source session is never modified.
// POST /api/runtime/sessions/{id}/branch
func (h *Handler) BranchSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	sourceID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sourceID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	// All fields are optional; an empty body is a whole-session branch.
	var req struct {
		AnchorMessageID string `json:"anchor_message_id,omitempty"`
		IncludeAnchor   *bool  `json:"include_anchor,omitempty"`
		Title           string `json:"title,omitempty"`
		UserID          string `json:"user_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !stderrors.Is(err, io.EOF) {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}

	ctx, cancel := sessionStoreQueryContext(r)
	defer cancel()

	source, err := h.sessionManager.Get(ctx, sourceID)
	if err != nil {
		if stderrors.Is(err, chat.ErrSessionNotFound) {
			h.writeError(w, http.StatusNotFound, err)
			return
		}
		writeSessionStoreError(w, err)
		return
	}

	// A session mid-turn has no stable history tail to anchor on.
	if h.branchSourceBusy(ctx, sourceID) {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrValidationFailed,
			"source session is generating; branch after the current turn finishes"))
		return
	}

	includeAnchor := true
	if req.IncludeAnchor != nil {
		includeAnchor = *req.IncludeAnchor
	}
	plan, planErr := chat.PlanBranch(source.GetMessages(), chat.BranchRequest{
		AnchorMessageID: req.AnchorMessageID,
		IncludeAnchor:   includeAnchor,
	})
	if planErr != nil {
		status := http.StatusBadRequest
		if stderrors.Is(planErr, chat.ErrBranchAnchorNotTurnTail) {
			// The anchor resolves to a real message but is not a completed turn
			// tail: a conflict with the requested cut point, not a bad selector.
			status = http.StatusConflict
		}
		h.writeError(w, status, errors.New(errors.ErrValidationFailed, planErr.Error()))
		return
	}

	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		userID = strings.TrimSpace(source.UserID)
	}
	userID = h.resolveServerSessionUserID(userID)

	// Dedup + create must be atomic with respect to other branch requests.
	branchMu.Lock()
	defer branchMu.Unlock()

	title, titleErr := h.branchSessionTitle(ctx, source, userID, req.Title)
	if titleErr != nil {
		writeSessionStoreError(w, titleErr)
		return
	}

	branch, err := h.sessionManager.CreateSession(ctx, userID)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}

	branch.ReplaceHistory(plan.Prefix)
	// Inherit the directory binding so the branch resumes in the same workspace.
	if workspacePath := sessionmeta.String(source.Metadata.Context, sessionmeta.WorkspacePath); workspacePath != "" {
		branch.SetContext(sessionmeta.WorkspacePath, workspacePath)
	}
	branch.ApplyForkLineage(source, plan.Anchor.SourceMessageID, time.Now().UTC())
	branch.UpdateTitle(title)

	if err := h.sessionManager.Update(ctx, branch); err != nil {
		// Never leave a shell session behind when the seed write fails.
		h.rollbackBranchSession(branch.ID)
		writeSessionStoreError(w, err)
		return
	}

	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"session": branch,
		"anchor":  plan.Anchor,
	})
}

// branchSourceBusy reports whether the source session owns an in-flight turn.
// Live actor state is checked first; the persisted runtime state is consulted as
// well because the session store may be shared with aicli CLI processes.
func (h *Handler) branchSourceBusy(ctx context.Context, sessionID string) bool {
	if hub := h.getSessionHub(); hub != nil {
		if actor, ok := hub.Get(sessionID); ok && actor != nil {
			if summary, ready := actor.StateSummary(); ready && summary.Busy() {
				return true
			}
		}
	}
	if store := h.getSessionRuntimeStore(); store != nil {
		if state, err := store.LoadState(ctx, sessionID); err == nil && state != nil && state.Summary().Busy() {
			return true
		}
	}
	return false
}

// branchSessionTitle resolves the branch title: the caller's title when given,
// otherwise "<source title> (branch)", with a " (2)", " (3)" … suffix when a
// session with the same title already exists under the same fork parent.
func (h *Handler) branchSessionTitle(ctx context.Context, source *chat.Session, userID, requested string) (string, error) {
	base := strings.TrimSpace(requested)
	if base == "" {
		base = branchSourceTitle(source) + branchTitleSuffix
	}
	taken, err := h.branchSiblingTitles(ctx, userID, source)
	if err != nil {
		return "", err
	}
	if !taken[base] {
		return base, nil
	}
	for suffix := 2; suffix <= branchTitleDedupLimit; suffix++ {
		candidate := base + " (" + strconv.Itoa(suffix) + ")"
		if !taken[candidate] {
			return candidate, nil
		}
	}
	// Exhausting the scan is practically impossible; keep the result unique
	// rather than silently duplicating a title.
	return base + " (" + strconv.FormatInt(time.Now().Unix(), 10) + ")", nil
}

// branchSourceTitle returns the source session's display title, falling back to
// its id (matching the frontend's buildForkSessionTitle fallback).
func branchSourceTitle(source *chat.Session) string {
	if source == nil {
		return ""
	}
	if preview := source.BuildPreview(); preview != nil {
		if title := strings.TrimSpace(preview.Title); title != "" {
			return title
		}
	}
	if id := strings.TrimSpace(source.ID); id != "" {
		return id
	}
	return "session"
}

// branchSiblingTitles lists the titles already used by sessions forked from the
// same parent session (same fork_parent_session_id) for the target user.
func (h *Handler) branchSiblingTitles(ctx context.Context, userID string, source *chat.Session) (map[string]bool, error) {
	taken := make(map[string]bool)
	if h.sessionManager == nil || source == nil {
		return taken, nil
	}
	parentID := strings.TrimSpace(source.ID)
	if parentID == "" {
		return taken, nil
	}
	siblings, err := h.sessionManager.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, sibling := range siblings {
		if sibling == nil || strings.TrimSpace(sibling.ID) == parentID {
			continue
		}
		if sessionmeta.String(sibling.Metadata.Context, chat.ContextForkParentSessionID) != parentID {
			continue
		}
		if title := strings.TrimSpace(sibling.Metadata.Title); title != "" {
			taken[title] = true
		}
	}
	return taken, nil
}

// rollbackBranchSession best-effort deletes a freshly created branch session
// whose history seed failed, so a failed branch never leaves a shell session.
func (h *Handler) rollbackBranchSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if h.sessionManager == nil || sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionStoreQueryTimeout)
	defer cancel()
	if err := h.sessionManager.Delete(ctx, sessionID); err != nil {
		logger.Warnf("branch session rollback failed (session %s): %s", sessionID, err)
	}
}
