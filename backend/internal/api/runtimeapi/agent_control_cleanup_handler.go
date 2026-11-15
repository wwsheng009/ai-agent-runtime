package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

type agentControlCleanupRequest struct {
	RootSessionID string `json:"root_session_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	DryRun        bool   `json:"dry_run,omitempty"`
	IdleMs        int64  `json:"idle_ms,omitempty"`
}

// AgentControlCleanup is the manual one-shot quota reclaim entry for remote
// hosts. It mirrors CLI `/agents cleanup`: materialize the projection, snapshot
// quota children, evaluate the shared ReclaimPolicy, then either preview or
// close the selected subtrees. The response carries the shared report text.
func (h *Handler) AgentControlCleanup(w http.ResponseWriter, r *http.Request) {
	req, err := parseAgentControlCleanupRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx := r.Context()
	rootSessionID, err := h.resolveAgentControlCleanupRoot(ctx, req)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	store := h.getAgentControlAgentStore()
	if store == nil {
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"reclaim":         "unavailable",
			"root_session_id": rootSessionID,
			"dry_run":         req.DryRun,
			"text":            "Agent Registry Cleanup:\nreclaim=unavailable（宿主未装配 durable agent registry，无需清理）",
		})
		return
	}
	reclaimStore, ok := store.(agentcontrol.AgentReclaimStore)
	if !ok || reclaimStore == nil {
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"reclaim":         "unavailable",
			"root_session_id": rootSessionID,
			"dry_run":         req.DryRun,
			"text":            "Agent Registry Cleanup:\nreclaim=unavailable（store 不支持按回收原因关闭）",
		})
		return
	}
	filter := agentcontrol.AgentFilter{RootSessionID: rootSessionID}
	if err := h.materializeAgentControlAgentProjections(ctx, store, filter); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	records, err := store.ListAgentControlAgents(ctx, filter)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	controller := &sessionAgentController{handler: h}
	children := agentcontrol.QuotaChildren(records)
	observations := controller.observeQuotaChildren(ctx, children)
	idle := time.Duration(req.IdleMs) * time.Millisecond
	now := time.Now().UTC()
	policy := agentcontrol.ReclaimPolicy{IdleTimeout: idle, Now: now}
	decisions := agentcontrol.SelectReclaimable(observations, policy)
	report := agentcontrol.ManualReclaimReport{
		RootSessionID: rootSessionID,
		QuotaChildren: len(children),
		Decisions:     decisions,
		DryRun:        req.DryRun,
		Idle:          idle,
	}
	response := map[string]interface{}{
		"reclaim":         "ok",
		"root_session_id": rootSessionID,
		"dry_run":         req.DryRun,
		"idle_ms":         req.IdleMs,
		"quota_children":  len(children),
		"reclaimable":     len(decisions),
		"decisions":       decisions,
	}
	if req.DryRun {
		response["text"] = report.Render()
		h.writeJSON(w, http.StatusOK, response)
		return
	}
	outcome, reclaimErr := agentcontrol.ReclaimAgentQuota(ctx, reclaimStore, rootSessionID, observations, policy)
	if reclaimErr != nil {
		h.writeError(w, http.StatusInternalServerError, reclaimErr)
		return
	}
	report.Outcome = outcome
	report.HasOutcome = true
	if outcome.Reclaimed() > 0 {
		controller.publishAgentReclaimEvent(rootSessionID, rootSessionID, agentcontrol.ReclaimSourceManualCleanup, outcome)
	}
	if remaining, listErr := store.ListAgentControlAgents(ctx, filter); listErr == nil {
		report.RemainingQuotaChildren = len(agentcontrol.QuotaChildren(remaining))
		response["remaining_quota_children"] = report.RemainingQuotaChildren
	}
	response["reclaimed"] = outcome.Reclaimed()
	response["failed"] = outcome.Failed
	response["summary"] = outcome.Summary()
	response["human_summary"] = outcome.HumanSummary()
	response["reasons"] = outcome.Reasons
	response["text"] = report.Render()
	h.writeJSON(w, http.StatusOK, response)
}

func parseAgentControlCleanupRequest(r *http.Request) (agentControlCleanupRequest, error) {
	req := agentControlCleanupRequest{}
	if r != nil && r.Body != nil {
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			return req, fmt.Errorf("invalid cleanup request: %w", err)
		}
	}
	if r != nil {
		q := r.URL.Query()
		req.RootSessionID = firstNonEmptyString(req.RootSessionID, q.Get("root_session_id"), q.Get("root"))
		req.SessionID = firstNonEmptyString(req.SessionID, q.Get("session_id"), q.Get("session"))
		if value := strings.TrimSpace(q.Get("dry_run")); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return req, fmt.Errorf("invalid dry_run value")
			}
			req.DryRun = parsed
		}
		if value := strings.TrimSpace(q.Get("idle_ms")); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 0 {
				return req, fmt.Errorf("invalid idle_ms value")
			}
			req.IdleMs = parsed
		}
		if value := strings.TrimSpace(q.Get("idle")); value != "" {
			parsed, err := time.ParseDuration(value)
			if err != nil || parsed < 0 {
				return req, fmt.Errorf("invalid idle duration")
			}
			req.IdleMs = parsed.Milliseconds()
		}
	}
	return req, nil
}

func (h *Handler) resolveAgentControlCleanupRoot(ctx context.Context, req agentControlCleanupRequest) (string, error) {
	if root := strings.TrimSpace(req.RootSessionID); root != "" {
		return chat.NormalizeSessionID(root), nil
	}
	sessionID := chat.NormalizeSessionID(req.SessionID)
	if sessionID == "" {
		return "", fmt.Errorf("root_session_id or session_id is required")
	}
	if h == nil || h.sessionManager == nil {
		return sessionID, nil
	}
	session, err := h.sessionManager.Get(ctx, sessionID)
	if err != nil || session == nil {
		return sessionID, nil
	}
	if root := strings.TrimSpace(apiAgentRootSessionID(session, sessionID)); root != "" {
		return root, nil
	}
	return sessionID, nil
}
