package commands

import (
	"context"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// localWaitFeedbackTracker returns this host's in-memory wait-feedback cadence
// tracker (delta floor + single silence escalation). Losing it on restart only
// resets the cadence; the durable parked record stays the source of truth.
func (h *localChatRuntimeHost) localWaitFeedbackTracker() *supervision.WaitFeedbackTracker {
	if h == nil {
		return nil
	}
	h.waitFeedbackTrackerOnce.Do(func() {
		h.waitFeedbackTracker = supervision.NewWaitFeedbackTracker()
	})
	return h.waitFeedbackTracker
}

// maybeScheduleWaitFeedback is the §"等待期兜底反馈" sweep: when the parent is
// parked on pending obligations and no lifecycle edge is in flight, hand the
// model one bounded observation episode so a long silent wait never looks like
// a hang. Gates (confirmed design 2026-10-04):
//
//   - user first: pending queued input / an active input drain pauses feedback;
//   - active run or active wait window (wait_agent/wait_team block inside the
//     run): fully silent — the wait window itself is the feedback channel;
//   - an already pending (undelivered) wake wins: lifecycle/approval edges are
//     delivered first and must not race a duplicate feedback episode;
//   - cadence: delta immediate (>= floor) + at most one long-silence
//     escalation; no repeated no-change heartbeats;
//   - fail-quiet: an unreadable runtime state / wake index skips the sweep.
//
// The scheduled wake goes through the normal MaybeWakeParent drain, so
// runnable/budget gates and the durable single-flight semantics stay in one
// place.
func (h *localChatRuntimeHost) maybeScheduleWaitFeedback(ctx context.Context, parentSessionID string) {
	if h == nil || h.SubagentBatches == nil || h.Supervision == nil || h.Supervision.Wakes == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// User first: a queued interactive input (or its drain) means the next
	// episode belongs to the user, not to the fallback.
	if session := h.BaseSession; session != nil {
		if pending := lenQueuedInteractiveInput(session); pending > 0 || session.queuedInputDrainActive() {
			return
		}
	}
	// Active run / active wait window: the run is busy, so the model is either
	// working or inside an explicit wait — both are progress channels of their
	// own and the fallback must not race them.
	if h.RuntimeStore != nil {
		state, err := h.RuntimeStore.LoadState(ctx, parentSessionID)
		if err != nil || state == nil {
			return
		}
		switch state.Status {
		case runtimechat.SessionRunning, runtimechat.SessionWaitingApproval, runtimechat.SessionRewinding:
			return
		}
	}
	// Lifecycle/approval edges already in flight take priority; a feedback
	// episode on top of them would be a duplicate (they carry the ledger).
	pending, err := h.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{
		TargetParentSessionID: parentSessionID,
		UnclaimedOnly:         true,
		Limit:                 1,
	})
	if err != nil || len(pending) > 0 {
		return
	}
	rootScopeID := h.baseRuntimeSessionID()
	if rootScopeID == "" {
		rootScopeID = parentSessionID
	}
	_, _ = supervision.ScheduleWaitFeedbackWake(
		ctx,
		h.SubagentBatches,
		h.agentSessionObligationResolver(),
		h.teamObligationResolver(),
		h.Supervision.Wakes,
		h.localWaitFeedbackTracker(),
		parentSessionID,
		rootScopeID,
	)
}
