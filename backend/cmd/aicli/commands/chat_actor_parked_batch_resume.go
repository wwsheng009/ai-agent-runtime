package commands

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
)

// startLocalParkedBatchResumer bridges a restarted host into the durable parked
// child-batch ledger. A parked batch has no live worker, so no heartbeat or wake
// can bring it back by itself.
//
//   - The base session owns the long-lived periodic scanner: a parked child task
//     becomes resumable as soon as its own required obligations settle.
//   - A non-base actor gets one bounded scan when it is materialized. Its child
//     wake normally materializes it precisely when the child-side obligation
//     became terminal; starting another long-lived ticker per child actor would
//     multiply full-ledger scans for no additional wake edge.
func (h *localChatRuntimeHost) startLocalParkedBatchResumer(apiAgent *agent.Agent, sessionID string, isBaseSession bool) {
	if h == nil || apiAgent == nil || h.lifecycleCtx == nil {
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
	if isBaseSession {
		coordinator.StartParkedBatchResumer(h.lifecycleCtx, 0, sessionID, 256)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(h.lifecycleCtx, 30*time.Second)
		defer cancel()
		_, _ = coordinator.ResumeParkedBatches(ctx, sessionID, 64)
	}()
}
