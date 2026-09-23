package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AutoWakePrompt is the fixed parent turn prompt used for auto-scheduled
// supervision wakes (doc 6.5 rule 5: the wake prompt references only the
// lifecycle digest; the digest itself is injected by the preflight step on
// the parent turn start path).
const AutoWakePrompt = "[supervision] 存在待处理的子 Agent / Team 关键生命周期事件，请检查生命周期摘要（lifecycle digest）并继续。"

// AutoWakePromptFor renders the wake prompt for one delivery. When a resume
// context was assembled from the obligation ledger (plan C2-1) the prompt
// carries the rollup digest so the resumed turn needs no history replay (H4);
// otherwise it stays byte-identical to AutoWakePrompt (legacy path).
func AutoWakePromptFor(rc *ResumeContext) string {
	if rc == nil || strings.TrimSpace(rc.Text) == "" {
		return AutoWakePrompt
	}
	return ResumePrompt(rc)
}

// WakeConsumer wires the durable wake scheduler into a host-provided parent
// turn delivery path. It is the concrete P2 closure for doc 6.5: there is no
// resident polling goroutine; the consumer is invoked at runnable state
// transition points (child completion projection, parent turn end) and, when
// the parent is runnable and the auto-turn budget allows it, delivers the
// aggregated lifecycle digest as one parent turn.
type WakeConsumer struct {
	// Wakes is the durable wake scheduler; required.
	Wakes *WakeScheduler
	// Runnable decides whether the parent may start another turn right now
	// (doc 6.5 rule 2: no concurrent second turn while running / waiting
	// approval / waiting input / rewinding). When nil the parent is always
	// considered runnable; hosts should provide a state-based check so a
	// busy parent keeps the wake pending instead of queueing a second turn.
	Runnable ParentRunnable
	// Deliver submits the aggregated digest as one parent turn. It receives
	// the wake's root scope plus the claimed wake ids so hosts can correlate
	// the delivery and can re-schedule the wake when the turn never started
	// (plan §6-F: a swallowed submission error would consume the parent's
	// only auto-wake). A nil Deliver still drains and resolves wakes
	// (notification stays durable in the inbox and preflight injects it on
	// the next natural turn).
	Deliver func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string) error
	// DeliverResume optionally receives the same-turn resume context next to
	// the digest (plan C2-1: wake 升级为 resume). Hosts that can inject into
	// the parked turn use it to resume with the same turn_id (I3); when nil,
	// delivery falls back to Deliver, which keeps the legacy "new turn"
	// semantics for unwired hosts.
	DeliverResume func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakeIDs []string, resume *ResumeContext) error
	// ResumeBuilder optionally overrides how the resume context is assembled.
	// nil uses the wake scheduler's wired ledger + progress projections
	// (WakeScheduler.BuildResumeContext).
	ResumeBuilder func(ctx context.Context, req ResumeContextRequest) *ResumeContext
	// ResumeCapacity is the host-owned "resume = 起 turn" gate (plan C2-5 /
	// A6). When it denies, the claimed wakes go back to the pending queue
	// (durable, FIFO position preserved) instead of starting a turn. nil keeps
	// the pre-A6 behavior: a runnable parent always resumes. A probe error
	// fails open (deliver): an unreadable limiter must never wedge supervision.
	ResumeCapacity ResumeCapacityProbe
	// ResumeQueue bounds the deferral FIFO (timeout + depth, A6 有界 FIFO).
	// The zero value uses the package defaults.
	ResumeQueue ResumeQueuePolicy
	// OnResumeDeferred optionally observes a deferred resume so the host can
	// surface the queue position in its digest/UI (A6: digest 显示排队位次).
	// A nil hook is a no-op and panics are contained.
	OnResumeDeferred func(ctx context.Context, state ResumeQueueState)
}

// MaybeWakeParent is called at every parent runnable state-transition point:
// after a child completion projection and after the parent's own turn ends.
// It is safe to call when no wake is pending and when the parent is busy;
// both are no-ops that keep the durable wake row for a later runnable point.
func (c *WakeConsumer) MaybeWakeParent(ctx context.Context, parentSessionID, parentTeamID, rootScopeID string) error {
	if c == nil || c.Wakes == nil {
		return nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	rootScopeID = strings.TrimSpace(rootScopeID)
	if parentSessionID == "" || rootScopeID == "" {
		return nil
	}
	claimed, digest, err := c.Wakes.DrainRunnable(ctx, parentSessionID, parentTeamID, rootScopeID, c.Runnable)
	if err != nil {
		// ErrWakeParentBusy / ErrWakeRateLimited: keep the wake durable;
		// the next runnable point (or the next natural turn preflight)
		// delivers the digest.
		return err
	}
	if len(claimed) == 0 || digest == nil {
		return nil
	}
	wakeIDs := make([]string, 0, len(claimed))
	for _, w := range claimed {
		wakeIDs = append(wakeIDs, w.WakeID)
	}
	// The underlying notification may have been acknowledged, actioned, or
	// resolved while the parent was busy. DrainRunnable still claims that
	// stale durable wake so it can be cleaned up, but a digest without
	// deliverable content must not launch a content-free supervision turn.
	// A progress wake (P2-D) is the one family whose digest content is the
	// P0-B rollup instead of lifecycle items; see digestDeliverable.
	if !digestDeliverable(claimed, digest) {
		c.release(ctx, wakeIDs)
		return nil
	}
	// Delivery-time idempotency (C2-4 #15 / B4): a wake whose notify key was
	// already delivered — a replayed terminal event, or a row persisted right
	// before a crash — must not start a second resume (AC-P1-4a). The dropped
	// rows are still released below so their coalescing slot frees up.
	deliverable := c.Wakes.FilterDeliveredWakes(ctx, claimed)
	if len(deliverable) == 0 {
		c.release(ctx, wakeIDs)
		return nil
	}
	// A6 (C2-5): a resume is a turn start, so it must pass the same capacity
	// gates as any spawn (MaxConcurrent / MaxDepth / visibility). A denial
	// keeps the wake durable — release the claim, never resolve it — and the
	// bounded-FIFO policy decides whether the stall escalates.
	if c.ResumeCapacity != nil {
		verdict, gateErr := c.ResumeCapacity.CanResume(ctx, c.resumeCapacityRequest(ctx, parentSessionID, parentTeamID, rootScopeID, deliverable))
		if gateErr == nil {
			if !verdict.Allowed {
				return c.deferResume(ctx, parentSessionID, parentTeamID, rootScopeID, claimed, deliverable, verdict)
			}
			// A6 静态门控（深度 / 可见性）：不排队、不升级——静态策略等不到，
			// 排队就是 G12 的"永久排队变相死锁"。resume 照常投递，但 digest
			// 显式带上限制，模型不必先撞一次派发面的拒绝。
			if verdict.Restricted {
				digest.SetResumeGate(verdict.Reason, verdict.Detail)
			}
		}
	}
	// Assemble the resume context from the ledger (best-effort: a missing or
	// failing projection degrades to the legacy digest prompt, never drops the
	// wake). The claimed wake's TurnID is only the scheduling-time hint; the
	// authoritative identity is the ledger's ParentTurnID, which the builder
	// prefers when it is present (I3 保底).
	resume := c.buildResume(ctx, parentSessionID, rootScopeID, deliverable, digest)
	if c.DeliverResume != nil {
		if err := c.DeliverResume(ctx, parentSessionID, rootScopeID, digest, wakeIDs, resume); err != nil {
			// Delivery failed: release the claims anyway. The notification
			// stays durable in the inbox and the next natural parent turn
			// injects it via preflight; keeping the row claimed would block
			// coalescing of later events for the same dedup key.
			c.release(ctx, wakeIDs)
			return err
		}
	} else if c.Deliver != nil {
		if err := c.Deliver(ctx, parentSessionID, rootScopeID, digest, wakeIDs); err != nil {
			// Delivery failed: release the claims anyway. The notification
			// stays durable in the inbox and the next natural parent turn
			// injects it via preflight; keeping the row claimed would block
			// coalescing of later events for the same dedup key.
			c.release(ctx, wakeIDs)
			return err
		}
	}
	// The delivery ledger is written only after a delivery actually happened
	// (AC-P1-4b): a failed delivery is never recorded, so the retry reuses the
	// same notify key and still reaches the parent.
	_ = c.Wakes.RecordWakeDelivery(ctx, deliverable)
	// The turn delivery consumed the wakes: a later event for the same
	// root + parent + reason must coalesce into a fresh row again.
	c.release(ctx, wakeIDs)
	return nil
}

// resumeCapacityRequest describes the resume the claimed wakes would start, in
// the terms the host gate needs (A6). QueueDepth counts the wakes already
// waiting unclaimed for the same parent: those are the rows this resume would
// join when denied.
func (c *WakeConsumer) resumeCapacityRequest(ctx context.Context, parentSessionID, parentTeamID, rootScopeID string, claimed []WakePending) ResumeCapacityRequest {
	req := ResumeCapacityRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		TargetParentTeamID:    parentTeamID,
		TurnID:                claimedTurnID(claimed),
		QueuedAt:              earliestWakeCreatedAt(claimed),
	}
	for _, w := range claimed {
		req.WakeIDs = append(req.WakeIDs, w.WakeID)
		if reason := strings.TrimSpace(w.WakeReason); reason != "" {
			req.WakeReasons = append(req.WakeReasons, reason)
		}
	}
	if depth, err := c.Wakes.PendingDepth(ctx, rootScopeID, parentSessionID, parentTeamID); err == nil {
		req.QueueDepth = depth
	}
	return req
}

// deferResume is the A6 denial path: the resume may not start now, so the
// wakes stay durable and keep their FIFO slot instead of being dropped or
// marked delivered. Already-delivered rows (a replayed terminal event) are
// dropped exactly like the delivery path does — keeping them queued would only
// re-drain and re-drop them forever. When the bounded FIFO is exceeded the
// stall escalates as one durable critical + action_required notification.
func (c *WakeConsumer) deferResume(ctx context.Context, parentSessionID, parentTeamID, rootScopeID string, claimed, deliverable []WakePending, verdict ResumeCapacityVerdict) error {
	keep := make(map[string]bool, len(deliverable))
	for _, w := range deliverable {
		keep[w.WakeID] = true
	}
	for _, w := range claimed {
		if keep[w.WakeID] {
			continue
		}
		_ = c.Wakes.ResolveWake(ctx, w.WakeID)
	}
	for _, w := range deliverable {
		// Release (not resolve): the row stays pending for the next runnable
		// window, which is what makes the queue durable across parent turns.
		_ = c.Wakes.ReleaseWake(ctx, w.WakeID, WakeClaimOwner)
	}
	now := c.clock()
	state := ResumeQueueState{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		TargetParentTeamID:    parentTeamID,
		TurnID:                claimedTurnID(deliverable),
		QueuedAt:              earliestWakeCreatedAt(deliverable),
		Gate:                  strings.TrimSpace(verdict.Reason),
		GateDetail:            strings.TrimSpace(verdict.Detail),
	}
	if len(deliverable) > 0 {
		state.WakeID = deliverable[0].WakeID
		state.WakeReason = deliverable[0].WakeReason
		if pos, err := c.Wakes.QueuePosition(ctx, rootScopeID, parentSessionID, parentTeamID, state.WakeID); err == nil {
			state.Position = pos.Position
			state.Depth = pos.Total
		}
	}
	if !state.QueuedAt.IsZero() {
		state.Waited = now.Sub(state.QueuedAt)
	}
	if c.OnResumeDeferred != nil {
		func() {
			defer func() { _ = recover() }()
			c.OnResumeDeferred(ctx, state)
		}()
	}
	if cause, escalate := ResumeQueueEscalation(state, c.ResumeQueue, now); escalate {
		if _, err := c.Wakes.store.UpsertNotification(ctx, resumeQueueNotification(state, cause, now)); err != nil {
			return fmt.Errorf("escalate deferred resume: %w", err)
		}
	}
	return nil
}

// clock reads the scheduler's injectable clock, falling back to wall time so a
// hand-built consumer (tests, embedders) never panics on a nil clock.
func (c *WakeConsumer) clock() time.Time {
	if c != nil && c.Wakes != nil && c.Wakes.now != nil {
		return c.Wakes.now().UTC()
	}
	return time.Now().UTC()
}

// buildResume assembles the same-turn resume context for the claimed wakes.
// It is best-effort by construction: any missing projection yields nil (the
// caller then keeps the legacy digest prompt) rather than an error that would
// swallow the wake.
func (c *WakeConsumer) buildResume(ctx context.Context, parentSessionID, rootScopeID string, claimed []WakePending, digest *Digest) *ResumeContext {
	req := ResumeContextRequest{
		ParentSessionID: parentSessionID,
		RootScopeID:     rootScopeID,
		TurnID:          claimedTurnID(claimed),
		Digest:          digest,
	}
	builder := c.ResumeBuilder
	if builder == nil {
		if c.Wakes == nil {
			return nil
		}
		builder = c.Wakes.BuildResumeContext
	}
	var resume *ResumeContext
	func() {
		defer func() {
			if recover() != nil {
				resume = nil
			}
		}()
		resume = builder(ctx, req)
	}()
	if resume == nil || strings.TrimSpace(resume.Text) == "" {
		return nil
	}
	return resume
}

// claimedTurnID returns the scheduling-time turn hint of the claimed wakes.
// Coalesced wakes for the same parent share one turn in practice; the first
// non-empty hint wins and the ledger backfill remains authoritative.
func claimedTurnID(claimed []WakePending) string {
	for _, w := range claimed {
		if turnID := strings.TrimSpace(w.TurnID); turnID != "" {
			return turnID
		}
	}
	return ""
}

func (c *WakeConsumer) release(ctx context.Context, wakeIDs []string) {
	for _, id := range wakeIDs {
		_ = c.Wakes.ResolveWake(ctx, id)
	}
}
