package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// AutoWakePrompt is the fixed parent turn prompt used for auto-scheduled
// supervision wakes (doc 6.5 rule 5: the wake prompt references only the
// lifecycle digest; the digest itself is injected by the preflight step on
// the parent turn start path).
const AutoWakePrompt = types.SupervisionPromptTag +
	" 存在待处理的子 Agent / Team 关键生命周期事件，请检查生命周期摘要（lifecycle digest）并继续。"

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
	// Announce optionally publishes the §6.8 turn.resumed milestone after the
	// host actually delivered the resume (G3: 挂起 → 恢复的可观测闭环). It is
	// called only on the success path — a failed delivery keeps the wake durable
	// and must not announce a resume that never started. Like the other hooks it
	// is best-effort: a nil hook is a no-op and panics are contained.
	Announce func(ctx context.Context, announcement ResumeAnnouncement)
	// OnAcceptedDeliveryError repairs a failure that happened after the host
	// durably accepted the prompt. The hook must persist a marker-only retry
	// before returning; the original prompt is never made retryable.
	OnAcceptedDeliveryError func(ctx context.Context, parentSessionID, rootScopeID string, digest *Digest, wakes []WakePending, resume *ResumeContext, cause error) error
	// DeliverResultsConsumptionRetry handles the marker-only wake created by
	// OnAcceptedDeliveryError. It must not submit a model prompt.
	DeliverResultsConsumptionRetry func(ctx context.Context, parentSessionID, rootScopeID, turnID string) error
	// Admission is the shared resume-admission service. When set, only wakes
	// that resolve to ResumeSameParkedTurn (or the P0 legacy bridge) may start a
	// model turn; late/detached settlements are consumed as durable
	// notifications without spending wake budget.
	Admission *ResumeAdmissionService
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
	if c.Admission != nil {
		// Converge claims abandoned between `claimed` and `submitted` before a
		// new drain evaluates them. Recovery never retries the model turn.
		_, _ = c.Admission.RecoverStaleClaims(ctx)
	}
	admissionResults := map[string]ResumeAdmissionResult{}
	var claimed []WakePending
	var digest *Digest
	var err error
	if c.Admission != nil {
		claimed, digest, err = c.Wakes.DrainRunnableWithAdmission(ctx, parentSessionID, parentTeamID, rootScopeID, c.Runnable,
			func(admitCtx context.Context, w WakePending) (ResumeAdmissionDecision, error) {
				// A results-consumption retry is control-plane-only and must
				// remain eligible even when the parent turn has already ended:
				// it never starts a model turn.
				if IsResultsConsumptionRetryWake(w.WakeReason) {
					return ResumeLegacy, nil
				}
				result, admitErr := c.Admission.Evaluate(admitCtx, ResumeAdmissionInputFromWake(w))
				if admitErr != nil {
					// A read failure is retryable, never "idle means resume".
					return RetryAdmission, nil
				}
				admissionResults[w.WakeID] = result
				return result.Decision, nil
			})
	} else {
		claimed, digest, err = c.Wakes.DrainRunnable(ctx, parentSessionID, parentTeamID, rootScopeID, c.Runnable)
	}
	if err != nil {
		// ErrWakeParentBusy / ErrWakeRateLimited: keep the wake durable;
		// the next runnable point (or the next natural turn preflight)
		// delivers the digest.
		if hasControlPlaneWake(claimed) {
			ids := wakeIDsOf(claimed)
			if releaseErr := c.releaseForRetry(ctx, ids); releaseErr != nil {
				return joinDeliveryErrors(err, releaseErr)
			}
		}
		return err
	}
	if len(claimed) == 0 {
		return nil
	}
	// Marker-only repair must run before digest delivery checks. It does not
	// need lifecycle content and must never reach Deliver/DeliverResume.
	if hasControlPlaneWake(claimed) {
		return c.deliverResultsConsumptionRetry(ctx, parentSessionID, rootScopeID, claimed)
	}
	if digest == nil {
		return nil
	}
	// The underlying notification may have been acknowledged, actioned, or
	// resolved while the parent was busy. DrainRunnable still claims that
	// stale durable wake so it can be cleaned up, but a digest without
	// deliverable content must not launch a content-free supervision turn.
	// A progress wake (P2-D) is the one family whose digest content is the
	// P0-B rollup instead of lifecycle items; see digestDeliverable.
	if !digestDeliverable(claimed, digest) {
		c.releasePending(ctx, claimed)
		return nil
	}
	// Delivery-time idempotency (C2-4 #15 / B4): a wake whose notify key was
	// already delivered — a replayed terminal event, or a row persisted right
	// before a crash — must not start a second resume (AC-P1-4a). The dropped
	// rows are still released below so their coalescing slot frees up.
	deliverable := c.Wakes.FilterDeliveredWakes(ctx, claimed)
	if len(deliverable) == 0 {
		c.releasePending(ctx, claimed)
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
	// Exactly-once: claim the settlement before building/submitting a resume.
	// Multiple wake rows for the same episode share one claim and one model
	// turn. A lost claim means another process already owns the continuation;
	// the local row is consumed as notification-only.
	claims := map[string]ResumeClaim{}
	if c.Admission != nil {
		kept := make([]WakePending, 0, len(deliverable))
		for _, w := range deliverable {
			result, ok := admissionResults[w.WakeID]
			if !ok || result.Decision != ResumeSameParkedTurn {
				kept = append(kept, w)
				continue
			}
			claimKey := strings.TrimSpace(result.ClaimKey)
			if claimKey == "" {
				claimKey = DeriveResumeClaimKey(result.Input.ParentSessionID, result.Input.ParentTurnID, result.Input.ParentEpisodeID, result.Input.SettlementCohortID, result.Input.SettlementID)
			}
			if _, exists := claims[claimKey]; exists {
				kept = append(kept, w)
				continue
			}
			claim, won, claimErr := c.Admission.Claim(ctx, result)
			if claimErr != nil {
				for _, held := range claims {
					_ = c.Admission.Release(ctx, held)
				}
				c.releasePending(ctx, claimed)
				return claimErr
			}
			if !won {
				_ = c.Wakes.markConsumed(ctx, w, "notification_only", "settlement_already_claimed")
				continue
			}
			claims[claimKey] = claim
			kept = append(kept, w)
		}
		deliverable = kept
		if len(deliverable) == 0 {
			for _, held := range claims {
				_ = c.Admission.Release(ctx, held)
			}
			c.releasePending(ctx, claimed)
			return nil
		}
	}
	// Keep every claimed settlement warm while buildResume/Deliver can take
	// longer than ClaimStaleAfter. The stop function is deferred so every
	// success/failure/early-return path cancels the heartbeat.
	stopClaimRenewal := c.startClaimRenewal(claims)
	defer stopClaimRenewal()
	wakeIDs := make([]string, 0, len(deliverable))
	for _, w := range deliverable {
		wakeIDs = append(wakeIDs, w.WakeID)
	}
	// Assemble the resume context from the ledger (best-effort: a missing or
	// failing projection degrades to the legacy digest prompt, never drops the
	// wake). The claimed wake's TurnID is only the scheduling-time hint; the
	// authoritative identity is the ledger's ParentTurnID, which the builder
	// prefers when it is present (I3 保底).
	resume := c.buildResume(ctx, parentSessionID, rootScopeID, deliverable, digest)
	// Close the replay window before crossing the host's acceptance boundary.
	// A submitting row is intentionally ambiguous: recovery may repair it, but
	// no later drain may submit the original prompt a second time.
	if err := c.Wakes.store.MarkWakeDeliverySubmitting(ctx, wakeIDs, false); err != nil {
		return joinDeliveryErrors(err, c.releaseForRetry(ctx, wakeIDs))
	}
	delivered := false
	if c.DeliverResume != nil {
		if err := c.DeliverResume(ctx, parentSessionID, rootScopeID, digest, wakeIDs, resume); err != nil {
			return c.handleDeliveryError(ctx, parentSessionID, rootScopeID, digest, deliverable, resume, wakeIDs, claims, err)
		}
		delivered = true
	} else if c.Deliver != nil {
		if err := c.Deliver(ctx, parentSessionID, rootScopeID, digest, wakeIDs); err != nil {
			return c.handleDeliveryError(ctx, parentSessionID, rootScopeID, digest, deliverable, resume, wakeIDs, claims, err)
		}
		delivered = true
	}
	// §6.8 / 审计 G3：投递成功即"恢复已发生"。只在成功分支播报；失败分支的 wake
	// 仍留在 durable 队列里等下一次可运行窗口，播报会与事实相反。
	if delivered {
		c.announceResume(ctx, parentSessionID, rootScopeID, deliverable, resume)
	}
	var submittedErr error
	if delivered && c.Admission != nil {
		submittedErr = c.markClaimsSubmitted(ctx, claims, wakeIDs)
	}
	acceptedErr := c.Wakes.store.MarkWakeDeliveryAccepted(ctx, wakeIDs, false)
	// The delivery ledger is written only after a delivery actually happened
	// (AC-P1-4b): a failed delivery is never recorded, so the retry reuses the
	// same notify key and still reaches the parent.
	recordErr := c.Wakes.RecordWakeDelivery(ctx, deliverable)
	// The turn delivery consumed the wakes: a later event for the same
	// root + parent + reason must coalesce into a fresh row again.
	var resolveErr error
	if acceptedErr == nil && recordErr == nil {
		resolveErr = c.resolveWakeIDs(ctx, wakeIDs)
	} else {
		// Keep the accepted/submitting row observable for control-plane
		// recovery. Releasing the claim is safe because the fence prevents
		// normal prompt delivery.
		resolveErr = c.releaseForRetry(ctx, wakeIDs)
	}
	return joinDeliveryErrors(joinDeliveryErrors(acceptedErr, submittedErr), joinDeliveryErrors(recordErr, resolveErr))
}

func (c *WakeConsumer) handleDeliveryError(
	ctx context.Context,
	parentSessionID, rootScopeID string,
	digest *Digest,
	deliverable []WakePending,
	resume *ResumeContext,
	wakeIDs []string,
	claims map[string]ResumeClaim,
	deliveryErr error,
) error {
	accepted, wasAccepted := AcceptedDelivery(deliveryErr)
	if !wasAccepted {
		var claimErr error
		for _, held := range claims {
			if c.Admission != nil {
				claimErr = joinDeliveryErrors(claimErr, c.Admission.Release(ctx, held))
			}
		}
		rejectErr := c.Wakes.store.MarkWakeDeliveryRejected(ctx, wakeIDs)
		releaseErr := c.releaseForRetry(ctx, wakeIDs)
		return joinDeliveryErrors(deliveryErr, joinDeliveryErrors(claimErr, joinDeliveryErrors(rejectErr, releaseErr)))
	}

	acceptedFenceErr := c.Wakes.store.MarkWakeDeliveryAccepted(ctx, wakeIDs, accepted.RetryMarker)
	var repairErr error
	if accepted.RetryMarker {
		if c.OnAcceptedDeliveryError == nil {
			repairErr = fmt.Errorf("accepted wake requires marker repair but no post-acceptance repair hook is configured")
		} else {
			repairErr = c.OnAcceptedDeliveryError(ctx, parentSessionID, rootScopeID, digest, deliverable, resume, accepted)
		}
	}

	// The actor accepted the prompt; even a later model error must not put the
	// original prompt back on the queue. Preserve evidence before closing it.
	c.announceResume(ctx, parentSessionID, rootScopeID, deliverable, resume)
	submittedErr := c.markClaimsSubmitted(ctx, claims, wakeIDs)
	recordErr := c.Wakes.RecordWakeDelivery(ctx, deliverable)
	var resolveErr error
	if acceptedFenceErr == nil && repairErr == nil && recordErr == nil {
		resolveErr = c.resolveWakeIDs(ctx, wakeIDs)
	} else {
		resolveErr = c.releaseForRetry(ctx, wakeIDs)
	}
	return joinDeliveryErrors(accepted, joinDeliveryErrors(acceptedFenceErr, joinDeliveryErrors(repairErr, joinDeliveryErrors(submittedErr, joinDeliveryErrors(recordErr, resolveErr)))))
}

// hasControlPlaneWake identifies rows that must never re-enter model delivery.
// The durable acceptance fence is authoritative even when the original wake
// reason was an ordinary lifecycle reason.
func hasControlPlaneWake(wakes []WakePending) bool {
	for _, wake := range wakes {
		if IsResultsConsumptionRetryWake(wake.WakeReason) ||
			strings.EqualFold(strings.TrimSpace(wake.DeliveryState), WakeDeliveryStateAccepted) {
			return true
		}
	}
	return false
}

func (c *WakeConsumer) markClaimsSubmitted(ctx context.Context, claims map[string]ResumeClaim, wakeIDs []string) error {
	if c == nil || c.Admission == nil {
		return nil
	}
	var firstErr error
	wakeID := strings.Join(wakeIDs, ",")
	for _, held := range claims {
		if err := c.Admission.MarkSubmitted(ctx, held, resumeClaimModelTurnID(held, wakeID)); err != nil {
			firstErr = joinDeliveryErrors(firstErr, err)
		}
	}
	return firstErr
}

func (c *WakeConsumer) resolveWakeIDs(ctx context.Context, wakeIDs []string) error {
	if c == nil || c.Wakes == nil {
		return nil
	}
	var firstErr error
	for _, id := range wakeIDs {
		if err := c.Wakes.ResolveWake(ctx, id); err != nil {
			firstErr = joinDeliveryErrors(firstErr, fmt.Errorf("resolve wake %s: %w", id, err))
		}
	}
	return firstErr
}

func (c *WakeConsumer) deliverResultsConsumptionRetry(ctx context.Context, parentSessionID, rootScopeID string, claimed []WakePending) error {
	var repairWakes, otherWakes []WakePending
	for _, wake := range claimed {
		if IsResultsConsumptionRetryWake(wake.WakeReason) ||
			strings.EqualFold(strings.TrimSpace(wake.DeliveryState), WakeDeliveryStateAccepted) {
			repairWakes = append(repairWakes, wake)
		} else {
			otherWakes = append(otherWakes, wake)
		}
	}
	releaseOtherErr := c.releaseForRetry(ctx, wakeIDsOf(otherWakes))
	if len(repairWakes) == 0 {
		return releaseOtherErr
	}
	groups := make(map[string][]WakePending)
	var turnIDs []string
	for _, wake := range repairWakes {
		turnID := firstNonEmptyString(strings.TrimSpace(wake.ParentTurnID), strings.TrimSpace(wake.TurnID))
		// Accepted rows whose marker was already recorded only need the
		// delivery ledger/replay fence and can be repaired without a turn
		// identity. Results-consumption retry wakes and accepted rows carrying
		// marker_repair_required still need the parent turn identity for the
		// marker-only host callback.
		if turnID == "" && (IsResultsConsumptionRetryWake(wake.WakeReason) || wake.MarkerRepairRequired) {
			releaseErr := c.releaseForRetry(ctx, wakeIDsOf(repairWakes))
			return joinDeliveryErrors(fmt.Errorf("results-consumption retry wake %s has no turn identity", wake.WakeID), joinDeliveryErrors(releaseOtherErr, releaseErr))
		}
		if turnID == "" {
			turnID = wake.WakeID
		}
		if _, exists := groups[turnID]; !exists {
			turnIDs = append(turnIDs, turnID)
		}
		groups[turnID] = append(groups[turnID], wake)
	}
	for groupIndex, turnID := range turnIDs {
		groupWakes := groups[turnID]
		needsMarkerRepair := false
		for _, wake := range groupWakes {
			if IsResultsConsumptionRetryWake(wake.WakeReason) || wake.MarkerRepairRequired {
				needsMarkerRepair = true
				break
			}
		}
		if needsMarkerRepair {
			if c.DeliverResultsConsumptionRetry == nil {
				releaseErr := c.releaseResultsConsumptionRetryGroups(ctx, turnIDs[groupIndex:], groups)
				return joinDeliveryErrors(fmt.Errorf("results-consumption retry handler is not configured"), joinDeliveryErrors(releaseOtherErr, releaseErr))
			}
			if err := c.DeliverResultsConsumptionRetry(ctx, parentSessionID, rootScopeID, turnID); err != nil {
				releaseErr := c.releaseResultsConsumptionRetryGroups(ctx, turnIDs[groupIndex:], groups)
				return joinDeliveryErrors(err, joinDeliveryErrors(releaseOtherErr, releaseErr))
			}
		}
		recordErr := c.Wakes.RecordWakeConsumption(ctx, groupWakes, "results_consumption_repair", "marker_repaired")
		if recordErr != nil {
			releaseErr := c.releaseResultsConsumptionRetryGroups(ctx, turnIDs[groupIndex:], groups)
			return joinDeliveryErrors(recordErr, joinDeliveryErrors(releaseOtherErr, releaseErr))
		}
		resolvedErr := c.resolveWakeIDs(ctx, wakeIDsOf(groupWakes))
		if resolvedErr != nil {
			// The marker write is idempotent; leave the wake retryable if its
			// own durable resolution failed. Earlier groups are already
			// committed and must not be reopened.
			releaseErr := c.releaseResultsConsumptionRetryGroups(ctx, turnIDs[groupIndex:], groups)
			return joinDeliveryErrors(resolvedErr, joinDeliveryErrors(releaseOtherErr, releaseErr))
		}
	}
	return releaseOtherErr
}

func (c *WakeConsumer) releaseResultsConsumptionRetryGroups(ctx context.Context, turnIDs []string, groups map[string][]WakePending) error {
	var remaining []WakePending
	for _, turnID := range turnIDs {
		remaining = append(remaining, groups[turnID]...)
	}
	return c.releaseForRetry(ctx, wakeIDsOf(remaining))
}

func wakeIDsOf(wakes []WakePending) []string {
	ids := make([]string, 0, len(wakes))
	for _, wake := range wakes {
		if id := strings.TrimSpace(wake.WakeID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
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
		WakeReasons:     claimedWakeReasons(claimed),
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

// claimedWakeReasons lists the claimed wakes' reasons in claim order (trimmed,
// empties dropped). The resume builder only uses them to phrase the prompt
// trigger — the join verdict always comes from the ledger projection.
func claimedWakeReasons(claimed []WakePending) []string {
	reasons := make([]string, 0, len(claimed))
	for _, w := range claimed {
		if reason := strings.TrimSpace(w.WakeReason); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	return reasons
}

// announceResume forwards one bounded resume milestone to the host hook. The
// supervision package never knows how the host renders it; the trigger class
// and the ledger verdict are what make it useful (UI: "托管挂起 → 恢复";
// 审计: 这次恢复消费了哪些 wake、当时账本是否全终态).
func (c *WakeConsumer) announceResume(ctx context.Context, parentSessionID, rootScopeID string, claimed []WakePending, resume *ResumeContext) {
	if c == nil || c.Announce == nil {
		return
	}
	reasons := make([]string, 0, len(claimed))
	ann := ResumeAnnouncement{
		TurnID:          claimedTurnID(claimed),
		EpisodeID:       claimedEpisodeID(claimed),
		ParentSessionID: parentSessionID,
		RootScopeID:     rootScopeID,
		WakeIDs:         make([]string, 0, len(claimed)),
		PendingCount:    -1,
		Status:          "unknown",
	}
	for _, w := range claimed {
		ann.WakeIDs = append(ann.WakeIDs, w.WakeID)
		if reason := strings.TrimSpace(w.WakeReason); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	ann.WakeReasons = reasons
	ann.Trigger = ResumeTriggerForReasons(reasons)
	if resume != nil {
		if turnID := strings.TrimSpace(resume.TurnID); turnID != "" {
			ann.TurnID = turnID
		}
		ann.PendingCount = resume.PendingCount
		if status := strings.TrimSpace(resume.Status); status != "" {
			ann.Status = status
		}
		ann.Terminal = resume.Terminal
		ann.Summary = boundedResumeSummary(resume.DigestText)
	}
	func() {
		defer func() { _ = recover() }()
		c.Announce(ctx, ann)
	}()
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

// claimedEpisodeID returns the logical episode carried by the claimed wake
// rows. The durable settlement wake records the parked episode; a legacy row
// without one simply omits the field from the announcement.
func claimedEpisodeID(claimed []WakePending) string {
	for _, w := range claimed {
		if episodeID := strings.TrimSpace(w.ParentEpisodeID); episodeID != "" {
			return episodeID
		}
	}
	return ""
}

func (c *WakeConsumer) release(ctx context.Context, wakeIDs []string) {
	for _, id := range wakeIDs {
		_ = c.Wakes.ResolveWake(ctx, id)
	}
}

// releaseForRetry drops the current claim without resolving the durable wake.
// A failed delivery has no receipt, so a later runnable-point drain must be
// able to retry the same wake instead of depending on a new lifecycle event.
func (c *WakeConsumer) releaseForRetry(ctx context.Context, wakeIDs []string) error {
	var releaseErr error
	for _, id := range wakeIDs {
		if err := c.Wakes.ReleaseWake(ctx, id, WakeClaimOwner); err != nil {
			if releaseErr == nil {
				releaseErr = fmt.Errorf("wake %s: %w", id, err)
			} else {
				releaseErr = fmt.Errorf("%v; wake %s: %w", releaseErr, id, err)
			}
		}
	}
	return releaseErr
}

// releasePending resolves the claimed rows that admission/coalescing left out
// of the final delivery set. Notification evidence remains durable; only the
// pending wake row is closed.
func (c *WakeConsumer) releasePending(ctx context.Context, wakes []WakePending) {
	ids := make([]string, 0, len(wakes))
	for _, w := range wakes {
		ids = append(ids, w.WakeID)
	}
	c.release(ctx, ids)
}
