package runtimeapi

import (
	"context"
	"strings"
	"time"
)

// apiSupervisedWakeDrainTimeout bounds one asynchronous scan-wake drain; the
// drain talks to the durable control plane and must never outlive its purpose
// (the CLI host carries the same 30s bound).
const apiSupervisedWakeDrainTimeout = 30 * time.Second

// requestSupervisedWakeDrain attempts one asynchronous parent drain after a
// scan projection scheduled a durable wake (ExecutionSupervisor.WakeReady).
//
// Wake delivery is otherwise edge-triggered: a progress_stalled /
// execution_timed_out wake produced by a standalone scan while the parent is
// idle has no state transition to carry it, so the scheduling edge itself must
// try a delivery (2026-10-04 field gap; the CLI host fixed the same hole and
// this is its API twin). Runnable / budget gates stay inside MaybeWakeParent
// and keep the wake durable when they deny.
//
// Like the CLI host the scan loop must not block on the drain, so the work
// happens on a detached goroutine with a bounded context.
func (h *Handler) requestSupervisedWakeDrain(rootScopeID, parentSessionID string) {
	if h == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return
	}
	rootScopeID = strings.TrimSpace(rootScopeID)
	if rootScopeID == "" {
		rootScopeID = parentSessionID
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiSupervisedWakeDrainTimeout)
	go func() {
		defer cancel()
		_ = h.drainSupervisedParentWake(ctx, rootScopeID, parentSessionID)
	}()
}
