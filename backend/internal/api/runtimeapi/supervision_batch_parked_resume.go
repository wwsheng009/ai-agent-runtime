package runtimeapi

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
)

// startParkedBatchResumeAsync gives a freshly materialized API session agent
// one bounded chance to resume its durable parked child batches. The batch
// worker is detached from any request, so a restarted API host has no other
// edge that could revive it. Concurrent agent builds for the same session share
// one scan; the key is released when the scan finishes so a later build can
// retry after an obligation becomes terminal.
func (h *Handler) startParkedBatchResumeAsync(sessionID string, apiAgent *agent.Agent) {
	if h == nil || apiAgent == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	coordinator := apiAgent.PeekSubagentBatchCoordinator()
	if coordinator == nil {
		return
	}
	if store := coordinator.Store(); store == nil || !store.IsDurable() {
		return
	}
	if _, loaded := h.parkedBatchResumeSeen.LoadOrStore(sessionID, struct{}{}); loaded {
		return
	}
	go func() {
		defer h.parkedBatchResumeSeen.Delete(sessionID)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = coordinator.ResumeParkedBatches(ctx, sessionID, 128)
	}()
}
