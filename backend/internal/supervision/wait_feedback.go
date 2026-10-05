package supervision

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// WakeReasonWaitFeedback is the wake reason of the wait-period fallback
// feedback sweep: the parent turn is parked on pending obligations, no
// lifecycle edge fired, and the tracker judged it is time to hand the model a
// fresh look at the wait ledger. It contains no failure/approval keyword, so
// WakeBudgetClassOf lands it in the bounded "other" class and it can never
// starve a critical or approval wake.
const WakeReasonWaitFeedback = "wait_feedback"

// Wait-feedback cadence (confirmed with the user 2026-10-04, best-practice
// reading):
//
//   - a progress delta (a new terminal obligation since the last feedback) is
//     reported as soon as the delta floor allows — no need to wait for a
//     heartbeat interval;
//   - with no progress, the sweep sends at most ONE escalation after a long
//     silence, then stays quiet until the ledger actually moves. Repeated
//     no-change heartbeats are deliberately not sent: every injected episode
//     costs a full LLM call, and the parent already knows the cohort is
//     running (the park edge carried the first ledger).
const (
	// WaitFeedbackDeltaFloor is the minimum quiet time between two feedback
	// wakes (also applies to the very first delta after the park edge).
	WaitFeedbackDeltaFloor = 10 * time.Second
	// WaitFeedbackSilence is the no-progress interval before the single
	// escalation wake ("still waiting, no new progress").
	WaitFeedbackSilence = 4 * time.Minute
)

// WaitFeedbackTracker decides when the fallback feedback sweep may schedule a
// wake. It is deliberately in-memory and host-local: losing it on restart only
// means a fresh cadence, while the durable parked record remains the single
// source of truth for what is owed.
type WaitFeedbackTracker struct {
	mu      sync.Mutex
	entries map[string]*waitFeedbackEntry
}

type waitFeedbackEntry struct {
	lastAt       time.Time
	lastTerminal int
	// escalated is set after a no-change escalation; the tracker then stays
	// silent until the terminal count grows again.
	escalated bool
}

// NewWaitFeedbackTracker creates an empty tracker.
func NewWaitFeedbackTracker() *WaitFeedbackTracker {
	return &WaitFeedbackTracker{}
}

// Due reports whether a feedback wake is due for key.
//
//   - progress grew since the last feedback => due once the delta floor passed;
//   - no growth, no escalation yet => due after the silence window;
//   - no growth after an escalation => not due until progress grows again.
//
// parkedAt anchors the very first decision so a freshly parked turn gets a
// chance to settle through its own event edges before the fallback speaks.
func (t *WaitFeedbackTracker) Due(key string, progress int, parkedAt, now time.Time) bool {
	if t == nil {
		return false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	t.mu.Lock()
	entry := t.entries[key]
	t.mu.Unlock()
	if entry == nil {
		anchor := parkedAt
		if anchor.IsZero() {
			anchor = now
		}
		if progress > 0 {
			// First observed delta: report it, subject to the floor.
			return !now.Before(anchor.Add(WaitFeedbackDeltaFloor))
		}
		// Nothing finished yet: only the long-silence escalation.
		return !now.Before(anchor.Add(WaitFeedbackSilence))
	}
	if progress > entry.lastTerminal {
		return now.Sub(entry.lastAt) >= WaitFeedbackDeltaFloor
	}
	if entry.escalated {
		return false
	}
	return now.Sub(entry.lastAt) >= WaitFeedbackSilence
}

// Mark records that one feedback wake was scheduled for key at now.
func (t *WaitFeedbackTracker) Mark(key string, progress int, now time.Time) {
	if t == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = make(map[string]*waitFeedbackEntry)
	}
	entry := t.entries[key]
	if entry == nil {
		entry = &waitFeedbackEntry{}
		t.entries[key] = entry
	}
	if progress > entry.lastTerminal {
		entry.escalated = false
	} else {
		entry.escalated = true
	}
	entry.lastTerminal = progress
	entry.lastAt = now
}

// Reset clears the cadence for one key (turn settled, interrupted, or the
// session started a new turn).
func (t *WaitFeedbackTracker) Reset(key string) {
	if t == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// ScheduleWaitFeedbackWake schedules at most one best-effort feedback wake per
// sweep for a parked turn whose obligations are still pending. It skips:
//
//   - records whose obligations are all terminal (the settlement edge owns
//     that wake — duplicating it here would race the settle path);
//   - records the tracker is not due for (delta floor / single escalation);
//   - records without a turn id (no resume target).
//
// Best-effort: an unreadable record is skipped instead of failing the sweep;
// the caller drains scheduled wakes through the normal MaybeWakeParent path,
// which keeps runnable/budget gates and the durable wake as the single-flight
// mechanism.
func ScheduleWaitFeedbackWake(
	ctx context.Context,
	batches subagentbatch.BatchStore,
	resolver subagentbatch.AgentSessionObligationResolver,
	teamResolver subagentbatch.TeamObligationResolver,
	scheduler *WakeScheduler,
	tracker *WaitFeedbackTracker,
	parentSessionID, rootScopeID string,
) (bool, error) {
	if batches == nil || scheduler == nil {
		return false, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return false, nil
	}
	rootScopeID = strings.TrimSpace(rootScopeID)
	if rootScopeID == "" {
		rootScopeID = parentSessionID
	}
	if ctx == nil {
		ctx = context.Background()
	}
	records, err := batches.ListTurnSuspensions(ctx, parentSessionID)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	for _, record := range records {
		if record == nil || strings.TrimSpace(record.TurnID) == "" {
			continue
		}
		progress, err := subagentbatch.CountObligationProgress(ctx, batches, record, resolver, teamResolver)
		if err != nil {
			continue
		}
		if progress.Total == 0 || progress.AllTerminal() {
			continue
		}
		key := parentSessionID + "|" + strings.TrimSpace(record.TurnID)
		if tracker != nil && !tracker.Due(key, progress.Terminal, record.ParkedAt, now) {
			continue
		}
		if _, err := scheduler.ScheduleWake(ctx, WakeRequest{
			RootScopeID:           rootScopeID,
			TargetParentSessionID: parentSessionID,
			WakeReason:            WakeReasonWaitFeedback,
			TurnID:                record.TurnID,
			EventKind:             WakeEventLifecycle,
		}); err != nil {
			return false, err
		}
		if tracker != nil {
			tracker.Mark(key, progress.Terminal, now)
		}
		return true, nil
	}
	return false, nil
}
